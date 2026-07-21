package app

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

func TestManagementMIDIDisabledDocumentAndActions(t *testing.T) {
	configuration := managementTestConfig()
	control := &stubManagementMIDI{}
	backend := newManagementMIDITestBackend(t, configuration, control, context.Background())

	document, err := backend.MIDIDevices(context.Background())
	if err != nil {
		t.Fatalf("MIDIDevices() error = %v", err)
	}
	want := managementapi.MIDIDevicesDocument{
		Discovery: managementapi.MIDIDiscoveryDisabled,
		Devices:   []managementapi.MIDIDevice{},
	}
	if !reflect.DeepEqual(document, want) {
		t.Fatalf("MIDIDevices() = %#v, want %#v", document, want)
	}
	request := managementapi.MIDIAuditionRequest{Channel: 1, Note: 60, Velocity: 100, DurationMS: 100}
	assertManagementBackendError(t, backend.AuditionMIDI(context.Background(), request), managementapi.ErrorConflict, "midi_disabled")
	assertManagementBackendError(t, backend.PanicMIDI(context.Background()), managementapi.ErrorConflict, "midi_disabled")
	if control.writeCalls.Load() != 0 || control.panicCalls.Load() != 0 || control.snapshotCalls.Load() != 0 {
		t.Fatalf("disabled MIDI invoked runtime: writes=%d panic=%d snapshots=%d", control.writeCalls.Load(), control.panicCalls.Load(), control.snapshotCalls.Load())
	}
}

func TestManagementMIDIDevicesMapsDetachedCachedSnapshot(t *testing.T) {
	configuration := managementTestConfig()
	configuration.MIDI.Enabled = true
	control := &stubManagementMIDI{snapshot: midi.ManagerSnapshot{
		Devices:     []midi.Device{{Number: 2, Name: "USB Synth"}, {Number: 4, Name: "Loopback"}},
		Current:     midi.Device{Number: 2, Name: "USB Synth"},
		Connected:   true,
		DiscoveryOK: false,
	}}
	backend := newManagementMIDITestBackend(t, configuration, control, context.Background())

	document, err := backend.MIDIDevices(context.Background())
	if err != nil {
		t.Fatalf("MIDIDevices() error = %v", err)
	}
	if !document.Enabled || document.Discovery != managementapi.MIDIDiscoveryError || !document.Connected ||
		document.Current == nil || document.Current.Number != 2 || len(document.Devices) != 2 {
		t.Fatalf("MIDIDevices() = %#v, want connected stale discovery", document)
	}
	document.Devices[0].Name = "mutated"
	document.Current.Name = "mutated"
	if control.snapshot.Devices[0].Name != "USB Synth" || control.snapshot.Current.Name != "USB Synth" {
		t.Fatal("mutating MIDI document changed runtime snapshot")
	}

	control.snapshot = midi.ManagerSnapshot{Devices: []midi.Device{}, DiscoveryOK: true}
	document, err = backend.MIDIDevices(context.Background())
	if err != nil {
		t.Fatalf("MIDIDevices(empty) error = %v", err)
	}
	if document.Discovery != managementapi.MIDIDiscoveryOK || document.Connected || document.Current != nil || document.Devices == nil || len(document.Devices) != 0 {
		t.Fatalf("MIDIDevices(empty) = %#v", document)
	}

	control.snapshot = midi.ManagerSnapshot{Devices: []midi.Device{{Number: -1, Name: "invalid"}}, DiscoveryOK: true}
	_, err = backend.MIDIDevices(context.Background())
	assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "midi_state_unavailable")
}

func TestManagementMIDIAuditionBuildsEventAndMapsSafetyLimits(t *testing.T) {
	configuration := managementTestConfig()
	configuration.MIDI.Enabled = true
	control := &stubManagementMIDI{}
	backend := newManagementMIDITestBackend(t, configuration, control, context.Background())
	request := managementapi.MIDIAuditionRequest{Channel: 16, Note: 127, Velocity: 127, DurationMS: maximumMIDIAuditionDurationMS}

	if err := backend.AuditionMIDI(context.Background(), request); err != nil {
		t.Fatalf("AuditionMIDI() error = %v", err)
	}
	event := control.lastEvent
	if event.ID == "" || event.Origin != configuration.Instance.ID || event.FlowID == "" ||
		event.MappingVersion != music.FlowModeV1 || event.Mode != music.Ionian || event.Root != 7 ||
		event.Channel != 16 || event.Note != 127 || event.Velocity != 127 || event.Duration != 10*time.Second {
		t.Fatalf("audition event = %#v", event)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("audition event Validate() error = %v", err)
	}

	invalid := []managementapi.MIDIAuditionRequest{
		{Channel: 0, Note: 60, Velocity: 100, DurationMS: 100},
		{Channel: 1, Note: -1, Velocity: 100, DurationMS: 100},
		{Channel: 1, Note: 60, Velocity: 0, DurationMS: 100},
		{Channel: 1, Note: 60, Velocity: 100, DurationMS: 10_001},
	}
	for _, candidate := range invalid {
		before := control.writeCalls.Load()
		err := backend.AuditionMIDI(context.Background(), candidate)
		assertManagementBackendError(t, err, managementapi.ErrorInvalid, "invalid_audition")
		if control.writeCalls.Load() != before {
			t.Fatalf("invalid audition %#v reached runtime", candidate)
		}
	}

	for _, test := range []struct {
		err  error
		code string
	}{
		{err: midi.ErrRateLimited, code: "midi_rate_limited"},
		{err: midi.ErrPolyphonyLimited, code: "midi_polyphony_limited"},
		{err: midi.ErrRetriggerLimited, code: "midi_retrigger_limited"},
	} {
		control.writeErr = test.err
		err := backend.AuditionMIDI(context.Background(), request)
		assertManagementBackendError(t, err, managementapi.ErrorRateLimited, test.code)
	}
	control.writeErr = midi.ErrOutputUnavailable
	assertManagementBackendError(t, backend.AuditionMIDI(context.Background(), request), managementapi.ErrorUnavailable, "midi_unavailable")
}

func TestManagementMIDIPanicAndOperationsFollowRuntimeLifecycle(t *testing.T) {
	configuration := managementTestConfig()
	configuration.MIDI.Enabled = true
	lifecycle, stopRuntime := context.WithCancel(context.Background())
	control := &stubManagementMIDI{}
	backend := newManagementMIDITestBackend(t, configuration, control, lifecycle)

	if err := backend.PanicMIDI(context.Background()); err != nil {
		t.Fatalf("PanicMIDI() while disconnected error = %v", err)
	}
	control.panicErr = errors.New("physical reset failed")
	assertManagementBackendError(t, backend.PanicMIDI(context.Background()), managementapi.ErrorUnavailable, "midi_unavailable")
	control.panicErr = nil

	controllerSnapshot := backend.controller.store.current.Load()
	backend.controller.store.publish(snapshotWithStatus(controllerSnapshot, ControllerStateDegraded, "internal detail"))
	if _, err := backend.MIDIDevices(context.Background()); err != nil {
		t.Fatalf("MIDIDevices() while controller degraded error = %v", err)
	}
	if err := backend.PanicMIDI(context.Background()); err != nil {
		t.Fatalf("PanicMIDI() while controller degraded error = %v", err)
	}

	stopRuntime()
	request := managementapi.MIDIAuditionRequest{Channel: 1, Note: 60, Velocity: 100, DurationMS: 100}
	assertManagementBackendError(t, backend.AuditionMIDI(context.Background(), request), managementapi.ErrorUnavailable, "runtime_unavailable")
	assertManagementBackendError(t, backend.PanicMIDI(context.Background()), managementapi.ErrorUnavailable, "runtime_unavailable")
}

func TestManagementMIDIAuditionIsCanceledWithRuntime(t *testing.T) {
	configuration := managementTestConfig()
	configuration.MIDI.Enabled = true
	lifecycle, stopRuntime := context.WithCancel(context.Background())
	started := make(chan struct{})
	control := &stubManagementMIDI{writeFunc: func(ctx context.Context, _ music.NoteEvent) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	backend := newManagementMIDITestBackend(t, configuration, control, lifecycle)
	request := managementapi.MIDIAuditionRequest{Channel: 1, Note: 60, Velocity: 100, DurationMS: 100}
	done := make(chan error, 1)
	go func() { done <- backend.AuditionMIDI(context.Background(), request) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("AuditionMIDI() did not reach runtime")
	}
	stopRuntime()
	select {
	case err := <-done:
		assertManagementBackendError(t, err, managementapi.ErrorUnavailable, "runtime_unavailable")
	case <-time.After(time.Second):
		t.Fatal("AuditionMIDI() did not stop with runtime lifecycle")
	}
}

func newManagementMIDITestBackend(t *testing.T, configuration config.Config, control managementMIDI, lifecycle context.Context) *managementBackend {
	t.Helper()
	controller := mustController(t, configuration, nil, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, lifecycle)
	backend.midi = control
	return backend
}

type stubManagementMIDI struct {
	snapshot      midi.ManagerSnapshot
	lastEvent     music.NoteEvent
	writeErr      error
	panicErr      error
	writeFunc     func(context.Context, music.NoteEvent) error
	snapshotCalls atomic.Int32
	writeCalls    atomic.Int32
	panicCalls    atomic.Int32
}

func (control *stubManagementMIDI) Snapshot() midi.ManagerSnapshot {
	control.snapshotCalls.Add(1)
	return control.snapshot
}

func (control *stubManagementMIDI) Write(ctx context.Context, event music.NoteEvent) error {
	control.writeCalls.Add(1)
	control.lastEvent = event
	if control.writeFunc != nil {
		return control.writeFunc(ctx, event)
	}
	return control.writeErr
}

func (control *stubManagementMIDI) Panic(context.Context) error {
	control.panicCalls.Add(1)
	return control.panicErr
}
