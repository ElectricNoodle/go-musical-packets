package app

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

const maximumMIDIAuditionDurationMS = 10_000

// MIDIDevices returns only the manager's bounded cached discovery state. It
// never invokes a native driver from an HTTP request goroutine.
func (backend *managementBackend) MIDIDevices(ctx context.Context) (managementapi.MIDIDevicesDocument, error) {
	if ctx == nil {
		return managementapi.MIDIDevicesDocument{}, managementMIDIInvalid(
			"invalid_midi_request",
			errors.New("management MIDI context is required"),
			nil,
		)
	}
	if err := backend.flowRuntimeAvailable(ctx, false); err != nil {
		return managementapi.MIDIDevicesDocument{}, err
	}
	if !backend.controller.Current().Config.MIDI.Enabled {
		return managementapi.MIDIDevicesDocument{
			Enabled:   false,
			Discovery: managementapi.MIDIDiscoveryDisabled,
			Devices:   make([]managementapi.MIDIDevice, 0),
		}, nil
	}
	if backend.midi == nil {
		return managementapi.MIDIDevicesDocument{}, managementMIDIUnavailable(
			"midi_unavailable",
			errors.New("enabled MIDI runtime is unavailable"),
		)
	}

	snapshot := backend.midi.Snapshot()
	discovery := managementapi.MIDIDiscoveryError
	if snapshot.DiscoveryOK {
		discovery = managementapi.MIDIDiscoveryOK
	}
	document := managementapi.MIDIDevicesDocument{
		Enabled:   true,
		Discovery: discovery,
		Connected: snapshot.Connected,
		Devices:   make([]managementapi.MIDIDevice, 0, len(snapshot.Devices)),
	}
	for _, device := range snapshot.Devices {
		converted, err := managementMIDIDevice(device)
		if err != nil {
			return managementapi.MIDIDevicesDocument{}, managementMIDIUnavailable("midi_state_unavailable", err)
		}
		document.Devices = append(document.Devices, converted)
	}
	if snapshot.Connected {
		current, err := managementMIDIDevice(snapshot.Current)
		if err != nil {
			return managementapi.MIDIDevicesDocument{}, managementMIDIUnavailable("midi_state_unavailable", err)
		}
		document.Current = &current
	}
	return document, nil
}

// AuditionMIDI sends one validated note through the same scheduler and global
// safety limits as packet-triggered music.
func (backend *managementBackend) AuditionMIDI(ctx context.Context, request managementapi.MIDIAuditionRequest) error {
	fields := invalidMIDIAuditionFields(request)
	if len(fields) != 0 {
		return managementMIDIInvalid(
			"invalid_audition",
			errors.New("MIDI audition fields are outside their accepted ranges"),
			fields,
		)
	}
	if err := backend.midiOperationAvailable(ctx); err != nil {
		return err
	}
	configuration := backend.controller.Current().Config
	if !configuration.MIDI.Enabled {
		return managementMIDIDisabled()
	}
	if backend.midi == nil {
		return managementMIDIUnavailable("midi_unavailable", errors.New("enabled MIDI runtime is unavailable"))
	}

	operationContext, cancelOperation := backend.flowMutationContext(ctx)
	defer cancelOperation()
	event := music.NoteEvent{
		ID:             "management-audition",
		Origin:         configuration.Instance.ID,
		MappingVersion: music.FlowModeV1,
		FlowID:         "management-audition",
		Mode:           music.Ionian,
		Root:           uint8(request.Note % 12),
		Note:           uint8(request.Note),
		Velocity:       uint8(request.Velocity),
		Duration:       time.Duration(request.DurationMS) * time.Millisecond,
		Channel:        uint8(request.Channel),
		CreatedAt:      time.Now(),
	}
	if err := backend.midi.Write(operationContext, event); err != nil {
		return managementMIDIOperationError(err)
	}
	return nil
}

// PanicMIDI clears local scheduler state and attempts All Notes Off on every
// channel through the coordinated runtime.
func (backend *managementBackend) PanicMIDI(ctx context.Context) error {
	if err := backend.midiOperationAvailable(ctx); err != nil {
		return err
	}
	if !backend.controller.Current().Config.MIDI.Enabled {
		return managementMIDIDisabled()
	}
	if backend.midi == nil {
		return managementMIDIUnavailable("midi_unavailable", errors.New("enabled MIDI runtime is unavailable"))
	}
	operationContext, cancelOperation := backend.flowMutationContext(ctx)
	defer cancelOperation()
	if err := backend.midi.Panic(operationContext); err != nil {
		return managementMIDIOperationError(err)
	}
	return nil
}

func (backend *managementBackend) midiOperationAvailable(ctx context.Context) error {
	if ctx == nil {
		return managementMIDIInvalid(
			"invalid_midi_request",
			errors.New("management MIDI context is required"),
			nil,
		)
	}
	return backend.flowRuntimeAvailable(ctx, false)
}

func managementMIDIDevice(device midi.Device) (managementapi.MIDIDevice, error) {
	if device.Number < 0 || !utf8.ValidString(device.Name) {
		return managementapi.MIDIDevice{}, errors.New("MIDI runtime returned invalid device state")
	}
	return managementapi.MIDIDevice{Number: device.Number, Name: device.Name}, nil
}

func invalidMIDIAuditionFields(request managementapi.MIDIAuditionRequest) []string {
	fields := make([]string, 0, 4)
	if request.Channel < 1 || request.Channel > 16 {
		fields = append(fields, "channel")
	}
	if request.Note < 0 || request.Note > 127 {
		fields = append(fields, "note")
	}
	if request.Velocity < 1 || request.Velocity > 127 {
		fields = append(fields, "velocity")
	}
	if request.DurationMS < 1 || request.DurationMS > maximumMIDIAuditionDurationMS {
		fields = append(fields, "duration_ms")
	}
	return fields
}

func managementMIDIDisabled() error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorConflict,
		Code:   "midi_disabled",
		Detail: "MIDI output is disabled",
	}
}

func managementMIDIOperationError(err error) error {
	code := "midi_unavailable"
	detail := "MIDI output is temporarily unavailable"
	if errors.Is(err, midi.ErrRateLimited) {
		return managementMIDIRateLimited("midi_rate_limited", "MIDI note rate limit reached", err)
	}
	if errors.Is(err, midi.ErrPolyphonyLimited) {
		return managementMIDIRateLimited("midi_polyphony_limited", "MIDI polyphony limit reached", err)
	}
	if errors.Is(err, midi.ErrRetriggerLimited) {
		return managementMIDIRateLimited("midi_retrigger_limited", "MIDI note retriggered too quickly", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, midi.ErrRuntimeClosed) || errors.Is(err, midi.ErrSchedulerClosed) {
		code = "runtime_unavailable"
		detail = "runtime is starting or stopping"
	}
	return managementMIDIUnavailableWithDetail(code, detail, err)
}

func managementMIDIRateLimited(code, detail string, err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorRateLimited,
		Code:   code,
		Detail: detail,
		Err:    err,
	}
}

func managementMIDIInvalid(code string, err error, fields []string) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorInvalid,
		Code:   code,
		Detail: "MIDI management request is invalid",
		Fields: append([]string(nil), fields...),
		Err:    err,
	}
}

func managementMIDIUnavailable(code string, err error) error {
	return managementMIDIUnavailableWithDetail(code, "MIDI output is temporarily unavailable", err)
}

func managementMIDIUnavailableWithDetail(code, detail string, err error) error {
	return &managementapi.BackendError{
		Kind:   managementapi.ErrorUnavailable,
		Code:   code,
		Detail: detail,
		Err:    err,
	}
}
