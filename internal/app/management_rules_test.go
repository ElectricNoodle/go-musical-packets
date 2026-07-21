package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
)

func TestManagementRulesCRUDPreservesOrderSecretsAndPublicRevision(t *testing.T) {
	configuration := managementTestConfig()
	configuration.Rules = config.RulesConfig{
		managementTestRule("first", config.FlowMonitor, 0),
		managementTestRule("second", config.FlowPlay, 2),
	}
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())

	document, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules() error = %v", err)
	}
	assertManagementRuleIDs(t, document.Rules, "first", "second")
	if !document.Writable {
		t.Fatal("Rules() writable = false, want true")
	}
	if document.Revision == "" || document.Revision.String() == controller.Current().Revision.String() {
		t.Fatalf("Rules() revision = %q, want opaque public token", document.Revision)
	}
	configDocument, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	if document.Revision != configDocument.Revision {
		t.Fatalf("Rules() revision = %q, Config() revision = %q", document.Revision, configDocument.Revision)
	}

	created, err := backend.CreateRule(
		context.Background(),
		document.Revision,
		managementTestRule("third", config.FlowIgnore, 0),
	)
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	assertManagementRuleIDs(t, created.Rules, "first", "second", "third")
	if created.Revision == document.Revision {
		t.Fatal("CreateRule() preserved revision")
	}

	replacement := managementTestRule("second", config.FlowPlay, 9)
	replacement.Name = "replacement"
	replaced, err := backend.ReplaceRule(context.Background(), created.Revision, "second", replacement)
	if err != nil {
		t.Fatalf("ReplaceRule() error = %v", err)
	}
	assertManagementRuleIDs(t, replaced.Rules, "first", "second", "third")
	if replaced.Rules[1].Name != "replacement" || replaced.Rules[1].Action.Channel != 9 {
		t.Fatalf("ReplaceRule() rule = %#v", replaced.Rules[1])
	}

	reordered, err := backend.ReorderRules(
		context.Background(),
		replaced.Revision,
		[]string{"third", "first", "second"},
	)
	if err != nil {
		t.Fatalf("ReorderRules() error = %v", err)
	}
	assertManagementRuleIDs(t, reordered.Rules, "third", "first", "second")

	deleted, err := backend.DeleteRule(context.Background(), reordered.Revision, "first")
	if err != nil {
		t.Fatalf("DeleteRule() error = %v", err)
	}
	assertManagementRuleIDs(t, deleted.Rules, "third", "second")
	if deleted.Rules == nil {
		t.Fatal("DeleteRule() returned nil rules")
	}

	// Every response is detached from both the active and durable documents.
	deleted.Rules[0].Name = "caller mutation"
	current, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules(after caller mutation) error = %v", err)
	}
	if current.Rules[0].Name == "caller mutation" {
		t.Fatal("RulesDocument leaked active rule storage")
	}
	durable, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	assertManagementRuleIDs(t, durable.Config.Rules, "third", "second")
	if durable.Config.Mapping.Seed != configuration.Mapping.Seed || durable.Config.Peer.URL != configuration.Peer.URL {
		t.Fatalf("rule CRUD changed secrets: seed %q peer %q", durable.Config.Mapping.Seed, durable.Config.Peer.URL)
	}
}

func TestManagementRulesStaleRevisionPrecedesResourceErrors(t *testing.T) {
	configuration := managementTestConfig()
	configuration.Rules = config.RulesConfig{managementTestRule("existing", config.FlowMonitor, 0)}
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())
	stale, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules() error = %v", err)
	}
	current, err := backend.CreateRule(
		context.Background(),
		stale.Revision,
		managementTestRule("new", config.FlowPlay, 3),
	)
	if err != nil {
		t.Fatalf("CreateRule(new) error = %v", err)
	}

	operations := []struct {
		name string
		run  func() error
	}{
		{
			name: "duplicate create",
			run: func() error {
				_, err := backend.CreateRule(context.Background(), stale.Revision, managementTestRule("existing", config.FlowMonitor, 0))
				return err
			},
		},
		{
			name: "missing replace",
			run: func() error {
				_, err := backend.ReplaceRule(context.Background(), stale.Revision, "missing", managementTestRule("missing", config.FlowMonitor, 0))
				return err
			},
		},
		{
			name: "body ID mismatch",
			run: func() error {
				_, err := backend.ReplaceRule(context.Background(), stale.Revision, "existing", managementTestRule("different", config.FlowMonitor, 0))
				return err
			},
		},
		{
			name: "missing delete",
			run: func() error {
				_, err := backend.DeleteRule(context.Background(), stale.Revision, "missing")
				return err
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			backendError := assertManagementBackendError(
				t,
				operation.run(),
				managementapi.ErrorPreconditionFailed,
				"revision_conflict",
			)
			if backendError.ActualRevision != current.Revision {
				t.Fatalf("actual revision = %q, want %q", backendError.ActualRevision, current.Revision)
			}
		})
	}
	assertManagementRuleIDs(t, controller.Current().Config.Rules, "existing", "new")
}

func TestManagementRulesMapResourceAndValidationErrors(t *testing.T) {
	configuration := managementTestConfig()
	configuration.Rules = config.RulesConfig{
		managementTestRule("first", config.FlowMonitor, 0),
		managementTestRule("second", config.FlowPlay, 2),
	}
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())
	document, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules() error = %v", err)
	}

	exists := assertManagementBackendError(t, createManagementRuleError(backend, document.Revision, managementTestRule("first", config.FlowMonitor, 0)), managementapi.ErrorConflict, "rule_exists")
	if exists.Detail != "rule ID already exists" {
		t.Fatalf("rule_exists detail = %q", exists.Detail)
	}
	mismatch := assertManagementBackendError(t, replaceManagementRuleError(backend, document.Revision, "first", managementTestRule("different", config.FlowMonitor, 0)), managementapi.ErrorConflict, "rule_id_mismatch")
	if mismatch.Detail != "rule body ID must match path ID" {
		t.Fatalf("rule_id_mismatch detail = %q", mismatch.Detail)
	}
	notFound := assertManagementBackendError(t, replaceManagementRuleError(backend, document.Revision, "missing", managementTestRule("missing", config.FlowMonitor, 0)), managementapi.ErrorNotFound, "rule_not_found")
	if notFound.Detail != "rule was not found" {
		t.Fatalf("rule_not_found detail = %q", notFound.Detail)
	}
	assertManagementBackendError(t, deleteManagementRuleError(backend, document.Revision, "missing"), managementapi.ErrorNotFound, "rule_not_found")

	invalid := managementTestRule("invalid", config.FlowState("invalid"), 0)
	invalidError := assertManagementBackendError(t, createManagementRuleError(backend, document.Revision, invalid), managementapi.ErrorInvalid, "invalid_rule")
	if invalidError.Detail != "rule is invalid" {
		t.Fatalf("invalid_rule detail = %q", invalidError.Detail)
	}

	orders := []struct {
		name  string
		order []string
	}{
		{name: "nil", order: nil},
		{name: "incomplete", order: []string{"first"}},
		{name: "duplicate", order: []string{"first", "first"}},
		{name: "unknown", order: []string{"first", "missing"}},
	}
	for _, test := range orders {
		t.Run("order "+test.name, func(t *testing.T) {
			_, err := backend.ReorderRules(context.Background(), document.Revision, test.order)
			assertManagementBackendError(t, err, managementapi.ErrorInvalid, "invalid_rule_order")
		})
	}
	hostileID := "hostile\n" + strings.Repeat("x", 4096)
	hostile := assertManagementBackendError(
		t,
		deleteManagementRuleError(backend, document.Revision, hostileID),
		managementapi.ErrorNotFound,
		"rule_not_found",
	)
	if hostile.Detail != "rule was not found" || strings.Contains(hostile.Detail, hostileID) {
		t.Fatalf("hostile rule detail = %q", hostile.Detail)
	}
	current := controller.Current()
	if current.Revision.String() != backend.revisions.resolve(document.Revision, current.Revision).String() {
		t.Fatalf("failed mutations changed revision: current %q public %q", current.Revision, document.Revision)
	}
	assertManagementRuleIDs(t, current.Config.Rules, "first", "second")
}

func TestManagementRulesReadOnlyAndRuntimeAvailability(t *testing.T) {
	configuration := managementTestConfig()
	readOnlyController := mustController(t, configuration, nil, nil)
	readOnlyBackend := readyManagementRuleBackend(readOnlyController, context.Background())
	document, err := readOnlyBackend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules(read-only) error = %v", err)
	}
	if document.Writable || document.Rules == nil {
		t.Fatalf("Rules(read-only) = %#v, want nonnil rules and writable false", document)
	}
	_, err = readOnlyBackend.CreateRule(
		context.Background(),
		document.Revision,
		managementTestRule("new", config.FlowMonitor, 0),
	)
	assertManagementBackendError(t, err, managementapi.ErrorConflict, "read_only")

	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	backend := newTestManagementBackend(controller, &ready, context.Background())
	expected := backend.revisions.issue(controller.Current().Revision)
	_, err = backend.CreateRule(context.Background(), expected, managementTestRule("new", config.FlowMonitor, 0))
	assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "runtime_unavailable")

	ready.Store(true)
	canceledLifecycle, cancelLifecycle := context.WithCancel(context.Background())
	cancelLifecycle()
	backend.lifecycle = canceledLifecycle
	_, err = backend.CreateRule(context.Background(), expected, managementTestRule("new", config.FlowMonitor, 0))
	assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "runtime_unavailable")
	if len(controller.Current().Config.Rules) != 0 {
		t.Fatal("unavailable rule mutation changed controller")
	}
}

func TestManagementRulesRejectOversizeCandidateWithoutSecretOracle(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())
	document, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules() error = %v", err)
	}
	oversize := managementTestRule("oversize", config.FlowMonitor, 0)
	oversize.Name = strings.Repeat("x", config.MaximumBytes)

	_, err = backend.CreateRule(context.Background(), document.Revision, oversize)
	backendError := assertManagementBackendError(t, err, managementapi.ErrorInvalid, "invalid_rule")
	const generic = "rule is invalid"
	if backendError.Detail != generic {
		t.Fatalf("oversize detail = %q, want %q", backendError.Detail, generic)
	}
	for _, hidden := range []string{configuration.Mapping.Seed, configuration.Peer.URL} {
		if strings.Contains(backendError.Detail, hidden) {
			t.Fatalf("oversize detail exposed secret %q", hidden)
		}
	}
	if got := controller.Current(); got.Revision.String() == "" || len(got.Config.Rules) != 0 {
		t.Fatalf("oversize mutation changed controller: %#v", got)
	}
}

func TestManagementRuleMutationStopsWithRuntimeLifecycle(t *testing.T) {
	configuration := managementTestConfig()
	repository := &cancelAwareConfigRepository{
		snapshot: memorySnapshot(configuration),
		started:  make(chan struct{}),
	}
	controller := mustController(t, configuration, repository, nil)
	var ready atomic.Bool
	ready.Store(true)
	lifecycle, stopRuntime := context.WithCancel(context.Background())
	backend := newTestManagementBackend(controller, &ready, lifecycle)
	expected := backend.revisions.issue(controller.Current().Revision)
	done := make(chan error, 1)
	go func() {
		_, err := backend.CreateRule(
			context.Background(),
			expected,
			managementTestRule("new", config.FlowMonitor, 0),
		)
		done <- err
	}()

	select {
	case <-repository.started:
	case <-time.After(time.Second):
		t.Fatal("CreateRule() did not reach repository mutation")
	}
	ready.Store(false)
	stopRuntime()
	select {
	case err := <-done:
		assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "update_unavailable")
	case <-time.After(time.Second):
		t.Fatal("CreateRule() did not stop after runtime cancellation")
	}
	if len(controller.Current().Config.Rules) != 0 || len(repository.snapshot.Config.Rules) != 0 {
		t.Fatal("canceled rule mutation changed active or durable rules")
	}
}

func TestManagementRulesReconcileExternalHotEditsAndPreserveOverlay(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	mutedID := strings.Repeat("a", 24)
	if _, err := controller.ReplaceMute(map[string]struct{}{mutedID: {}}); err != nil {
		t.Fatalf("ReplaceMute() error = %v", err)
	}
	backend := readyManagementRuleBackend(controller, context.Background())
	active, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules() error = %v", err)
	}
	external := configuration.Clone()
	external.Mapping.DefaultChannel = 4
	external.Mapping.DefaultState = config.FlowPlay
	externalRevision := repository.externalReplace(external)

	_, err = backend.CreateRule(
		context.Background(),
		active.Revision,
		managementTestRule("requested", config.FlowMonitor, 0),
	)
	conflict := assertManagementBackendError(t, err, managementapi.ErrorPreconditionFailed, "revision_conflict")
	if conflict.ActualRevision == "" || conflict.ActualRevision.String() == externalRevision.String() {
		t.Fatalf("actual revision = %q, want opaque durable token", conflict.ActualRevision)
	}

	reconciled, err := backend.CreateRule(
		context.Background(),
		conflict.ActualRevision,
		managementTestRule("requested", config.FlowMonitor, 0),
	)
	if err != nil {
		t.Fatalf("CreateRule(reconcile) error = %v", err)
	}
	if reconciled.Revision == conflict.ActualRevision {
		t.Fatal("CreateRule(reconcile) preserved durable revision")
	}
	current := controller.Current()
	if current.State != ControllerStateReady || current.Config.Mapping.DefaultChannel != 4 || current.Config.Mapping.DefaultState != config.FlowPlay {
		t.Fatalf("reconciled controller = %#v", current)
	}
	assertManagementRuleIDs(t, current.Config.Rules, "requested")
	if _, found := controller.Overlay().Muted[mutedID]; !found {
		t.Fatal("external-drift reconciliation lost overlay")
	}
	durable, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if durable.Config.Mapping.Seed != configuration.Mapping.Seed || durable.Config.Peer.URL != configuration.Peer.URL {
		t.Fatal("external-drift reconciliation changed secrets")
	}
	if !reflect.DeepEqual(durable.Config, current.Config) {
		t.Fatalf("durable configuration = %#v, want active %#v", durable.Config, current.Config)
	}
}

func TestManagementRulesRejectRestartRequiredExternalDrift(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())
	active, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules() error = %v", err)
	}
	external := configuration.Clone()
	external.Server.ReadTimeout += time.Second
	externalRevision := repository.externalReplace(external)

	_, err = backend.CreateRule(
		context.Background(),
		active.Revision,
		managementTestRule("requested", config.FlowMonitor, 0),
	)
	conflict := assertManagementBackendError(t, err, managementapi.ErrorPreconditionFailed, "revision_conflict")
	_, err = backend.CreateRule(
		context.Background(),
		conflict.ActualRevision,
		managementTestRule("requested", config.FlowMonitor, 0),
	)
	restart := assertManagementBackendError(t, err, managementapi.ErrorConflict, "restart_required")
	if !containsString(restart.Fields, "server.read_timeout") {
		t.Fatalf("restart fields = %v, want server.read_timeout", restart.Fields)
	}
	if current := controller.Current(); current.State != ControllerStateOutOfSync || len(current.Config.Rules) != 0 {
		t.Fatalf("controller after restart-required drift = %#v", current)
	}
	durable, err := repository.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if durable.Revision != externalRevision || !reflect.DeepEqual(durable.Config, external) {
		t.Fatalf("durable configuration = %#v, want untouched external %#v", durable, external)
	}
}

func TestManagementCreatedRuleAffectsSelectionImmediately(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())
	document, err := backend.Rules(context.Background())
	if err != nil {
		t.Fatalf("Rules() error = %v", err)
	}
	event := testPacket(41000, 443, time.Unix(100, 0))
	key, _ := flow.Canonicalize(event)
	rule := managementTestRule("immediate", config.FlowPlay, 7)
	rule.Match.ExactFlowID = key.ID(configuration.Mapping.Seed)

	if _, err := backend.CreateRule(context.Background(), document.Revision, rule); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	selection := mustSelect(t, controller, event)
	if selection.Tier != "pinned" || selection.RuleID != "immediate" || selection.State != flow.StatePlay || selection.Channel != 7 {
		t.Fatalf("Evaluate() = %#v, want immediately active rule", selection)
	}
}

func TestManagementConcurrentConfigAndRuleMutationHaveOneWinner(t *testing.T) {
	configuration := managementTestConfig()
	repository := newMemoryConfigRepository(configuration)
	controller := mustController(t, configuration, repository, nil)
	backend := readyManagementRuleBackend(controller, context.Background())
	configDocument, err := backend.Config(context.Background())
	if err != nil {
		t.Fatalf("Config() error = %v", err)
	}
	candidate := configDocument.Config.Clone()
	candidate.Mapping.DefaultChannel = 8
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := backend.UpdateConfig(context.Background(), configDocument.Revision, candidate)
		results <- err
	}()
	go func() {
		<-start
		_, err := backend.CreateRule(
			context.Background(),
			configDocument.Revision,
			managementTestRule("concurrent", config.FlowPlay, 6),
		)
		results <- err
	}()
	close(start)

	successes := 0
	conflicts := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		var backendError *managementapi.BackendError
		if errors.As(err, &backendError) && backendError.Kind == managementapi.ErrorPreconditionFailed && backendError.Code == "revision_conflict" {
			conflicts++
			continue
		}
		t.Fatalf("concurrent mutation error = %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("outcomes = %d successes, %d conflicts; want one each", successes, conflicts)
	}
	current := controller.Current()
	configWon := current.Config.Mapping.DefaultChannel == 8
	ruleWon := len(current.Config.Rules) == 1 && current.Config.Rules[0].ID == "concurrent"
	if configWon == ruleWon {
		t.Fatalf("final configuration = %#v, want exactly one mutation", current.Config)
	}
}

func readyManagementRuleBackend(controller *Controller, lifecycle context.Context) *managementBackend {
	var ready atomic.Bool
	ready.Store(true)
	return newTestManagementBackend(controller, &ready, lifecycle)
}

func managementTestRule(id string, state config.FlowState, channel uint8) config.RuleConfig {
	return config.RuleConfig{
		ID:      id,
		Name:    id + " rule",
		Enabled: true,
		Action:  config.RuleActionConfig{State: state, Channel: channel},
	}
}

func assertManagementRuleIDs(t *testing.T, rules config.RulesConfig, expected ...string) {
	t.Helper()
	if len(rules) != len(expected) {
		t.Fatalf("rule count = %d, want %d: %#v", len(rules), len(expected), rules)
	}
	for index, id := range expected {
		if rules[index].ID != id {
			t.Fatalf("rules[%d].id = %q, want %q: %#v", index, rules[index].ID, id, rules)
		}
	}
}

func createManagementRuleError(backend *managementBackend, revision managementapi.Revision, rule config.RuleConfig) error {
	_, err := backend.CreateRule(context.Background(), revision, rule)
	return err
}

func replaceManagementRuleError(backend *managementBackend, revision managementapi.Revision, id string, rule config.RuleConfig) error {
	_, err := backend.ReplaceRule(context.Background(), revision, id, rule)
	return err
}

func deleteManagementRuleError(backend *managementBackend, revision managementapi.Revision, id string) error {
	_, err := backend.DeleteRule(context.Background(), revision, id)
	return err
}
