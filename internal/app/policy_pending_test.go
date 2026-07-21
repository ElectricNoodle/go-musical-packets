package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

func TestControllerStagesReplacesAndDiscardsPendingConfiguration(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	active := controller.Current()
	candidate := configuration.Clone()
	candidate.Capture.Interface = "pending0"

	pending, err := controller.StageRestartContext(context.Background(), active.Revision, candidate)
	if err != nil {
		t.Fatalf("StageRestartContext() error = %v", err)
	}
	if pending.Revision == active.Revision || pending.Config.Capture.Interface != "pending0" {
		t.Fatalf("pending = %#v, want distinct pending interface", pending)
	}
	current := controller.Current()
	if current.State != ControllerStateRestartPending || current.Revision != active.Revision || !reflect.DeepEqual(current.Config, active.Config) {
		t.Fatalf("active document = %#v, want unchanged restart-pending generation", current)
	}
	stored, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if stored.Revision != pending.Revision || !reflect.DeepEqual(stored.Config, candidate) {
		t.Fatalf("durable = %#v, want pending %#v", stored, candidate)
	}
	detached, ok := controller.Pending()
	if !ok || !reflect.DeepEqual(detached, pending) {
		t.Fatalf("Pending() = %#v, %t, want %#v", detached, ok, pending)
	}
	detached.Config.Capture.Interface = "caller-mutation"
	if next, _ := controller.Pending(); next.Config.Capture.Interface != "pending0" {
		t.Fatal("Pending() leaked mutable controller storage")
	}

	_, err = controller.Mutate(active.Revision, func(next *config.Config) error {
		next.Mapping.DefaultState = config.FlowPlay
		return nil
	})
	var restartPending *RestartPendingError
	if !errors.As(err, &restartPending) {
		t.Fatalf("Mutate() error = %v, want RestartPendingError", err)
	}
	mutedID := strings.Repeat("a", 24)
	if _, err := controller.ReplaceMute(map[string]struct{}{mutedID: {}}); err != nil {
		t.Fatalf("ReplaceMute() with pending restart error = %v", err)
	}
	if _, ok := controller.Overlay().Muted[mutedID]; !ok {
		t.Fatal("temporary overlay was not updated while restart was pending")
	}

	replacement := candidate.Clone()
	replacement.Capture.BPF = "tcp"
	replaced, err := controller.StageRestartContext(context.Background(), pending.Revision, replacement)
	if err != nil {
		t.Fatalf("StageRestartContext(replace) error = %v", err)
	}
	if replaced.Revision == pending.Revision || replaced.Config.Capture.BPF != "tcp" {
		t.Fatalf("replacement = %#v", replaced)
	}

	restored, err := controller.CancelRestartContext(context.Background(), replaced.Revision)
	if err != nil {
		t.Fatalf("CancelRestartContext() error = %v", err)
	}
	if restored.State != ControllerStateReady || restored.Revision != active.Revision || !reflect.DeepEqual(restored.Config, active.Config) {
		t.Fatalf("restored = %#v, want active %#v", restored, active)
	}
	if _, ok := controller.Pending(); ok {
		t.Fatal("Pending() exists after cancel")
	}
	stored, err = repository.Read()
	if err != nil {
		t.Fatalf("Read(after cancel) error = %v", err)
	}
	if stored.Revision != active.Revision || !reflect.DeepEqual(stored.Config, active.Config) {
		t.Fatalf("durable after cancel = %#v, want active %#v", stored, active)
	}
}

func TestControllerPendingConfigurationBecomesActiveOnRestart(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	candidate := configuration.Clone()
	candidate.Capture.Interface = "next0"
	pending, err := controller.StageRestartContext(context.Background(), controller.Current().Revision, candidate)
	if err != nil {
		t.Fatalf("StageRestartContext() error = %v", err)
	}

	restarted := mustController(t, configuration, repository, nil)
	current := restarted.Current()
	if current.State != ControllerStateReady || current.Revision != pending.Revision || !reflect.DeepEqual(current.Config, candidate) {
		t.Fatalf("restarted current = %#v, want pending %#v", current, pending)
	}
}

func TestControllerDiscardTracksRewrittenFileRevision(t *testing.T) {
	configuration := testConfig()
	repository, initial, _ := writePolicyRepository(t, configuration, "# original formatting\n")
	controller := mustController(t, configuration, repository, nil)
	candidate := configuration.Clone()
	candidate.Capture.Interface = "next0"
	pending, err := controller.StageRestartContext(context.Background(), initial.Revision, candidate)
	if err != nil {
		t.Fatalf("StageRestartContext() error = %v", err)
	}
	restored, err := controller.CancelRestartContext(context.Background(), pending.Revision)
	if err != nil {
		t.Fatalf("CancelRestartContext() error = %v", err)
	}
	durable, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if restored.Revision != durable.Revision || !reflect.DeepEqual(restored.Config, durable.Config) {
		t.Fatalf("restored = %#v, durable = %#v", restored, durable)
	}
	if restored.Revision == initial.Revision {
		t.Fatal("discard preserved the commented file revision after canonical rewrite")
	}
}

func TestControllerPendingConfigurationValidationAndPreconditions(t *testing.T) {
	configuration := testConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	active := controller.Current()

	hot := configuration.Clone()
	hot.Mapping.DefaultState = config.FlowPlay
	_, err := controller.StageRestartContext(context.Background(), active.Revision, hot)
	var notRequired *RestartNotRequiredError
	if !errors.As(err, &notRequired) {
		t.Fatalf("StageRestartContext(hot) error = %v, want RestartNotRequiredError", err)
	}

	restart := configuration.Clone()
	restart.Capture.Interface = "next0"
	_, err = controller.StageRestartContext(context.Background(), "stale", restart)
	var conflict *config.ConflictError
	if !errors.As(err, &conflict) || conflict.Actual != active.Revision {
		t.Fatalf("StageRestartContext(stale) error = %v, want active conflict", err)
	}

	readOnly := mustController(t, configuration, nil, nil)
	_, err = readOnly.StageRestartContext(context.Background(), readOnly.Current().Revision, restart)
	var readOnlyError *ReadOnlyError
	if !errors.As(err, &readOnlyError) {
		t.Fatalf("StageRestartContext(read-only) error = %v, want ReadOnlyError", err)
	}

	_, err = controller.CancelRestartContext(context.Background(), active.Revision)
	var missing *PendingConfigNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("CancelRestartContext(no pending) error = %v, want PendingConfigNotFoundError", err)
	}
}
