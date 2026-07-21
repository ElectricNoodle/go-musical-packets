package app

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestRunReplayUsesOrderedRulesAbsolutePacingAndNoLiveServer(t *testing.T) {
	configuration := testConfig()
	configuration.Mapping.DefaultState = config.FlowMonitor
	configuration.Rules = config.RulesConfig{
		{
			ID:      "first-match-ignore",
			Enabled: true,
			Match: config.RuleMatchConfig{SourcePorts: &config.PortRangeConfig{
				Minimum: 41000,
				Maximum: 41000,
			}},
			Action: config.RuleActionConfig{State: config.FlowIgnore},
		},
		{
			ID:      "play-tcp",
			Enabled: true,
			Match:   config.RuleMatchConfig{Protocol: packet.ProtocolTCP},
			Action:  config.RuleActionConfig{State: config.FlowPlay, Channel: 7},
		},
	}

	wallStart := time.Unix(1_000, 0)
	clockTimes := []time.Time{wallStart, wallStart.Add(20 * time.Millisecond), wallStart.Add(160 * time.Millisecond)}
	var clockMu sync.Mutex
	var clockCalls int
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		if clockCalls >= len(clockTimes) {
			return clockTimes[len(clockTimes)-1]
		}
		result := clockTimes[clockCalls]
		clockCalls++
		return result
	}
	var waitMu sync.Mutex
	var waits []time.Duration
	wait := func(_ context.Context, duration time.Duration) error {
		waitMu.Lock()
		waits = append(waits, duration)
		waitMu.Unlock()
		return nil
	}

	var log operationLog
	observer := newReplayObserver()
	source := &replayTestSource{
		events: []packet.Event{
			testPacket(41000, 443, time.Unix(100, 0)),
			testPacket(41001, 443, time.Unix(100, int64(100*time.Millisecond))),
			testPacket(41002, 443, time.Unix(100, int64(250*time.Millisecond))),
			testPacket(41003, 443, time.Unix(99, 0)),
		},
		log: &log,
	}
	driver := &fakeDriver{devices: []midi.Device{{Number: 5, Name: "synth"}}, log: &log}
	var replayPath string
	var forbiddenCalls atomic.Int32
	dependencies := Dependencies{
		Interfaces: func() ([]capture.Interface, error) {
			forbiddenCalls.Add(1)
			return nil, errors.New("unexpected live interface discovery")
		},
		OpenLive: func(capture.LiveConfig) (capture.Source, error) {
			forbiddenCalls.Add(1)
			return nil, errors.New("unexpected live capture")
		},
		Listen: func(string, string) (net.Listener, error) {
			forbiddenCalls.Add(1)
			return nil, errors.New("unexpected HTTP listener")
		},
		OpenReplayFile: func(path string) (capture.Source, error) {
			replayPath = path
			log.add("capture.open")
			return source, nil
		},
		NewMIDIDriver:  func() (midi.Driver, error) { return driver, nil },
		ReplayNow:      now,
		ReplayWait:     wait,
		ReplayObserver: observer,
	}

	if err := RunReplayWithDependencies(context.Background(), configuration, "fixture.pcap", dependencies); err != nil {
		t.Fatalf("RunReplayWithDependencies() error = %v, want nil", err)
	}
	if replayPath != "fixture.pcap" {
		t.Fatalf("replay path = %q, want fixture.pcap", replayPath)
	}
	if got := forbiddenCalls.Load(); got != 0 {
		t.Fatalf("live/server boundary calls = %d, want 0", got)
	}
	waitMu.Lock()
	gotWaits := append([]time.Duration(nil), waits...)
	waitMu.Unlock()
	wantWaits := []time.Duration{80 * time.Millisecond, 90 * time.Millisecond}
	if len(gotWaits) != len(wantWaits) || gotWaits[0] != wantWaits[0] || gotWaits[1] != wantWaits[1] {
		t.Fatalf("replay waits = %v, want %v", gotWaits, wantWaits)
	}
	clockMu.Lock()
	gotClockCalls := clockCalls
	clockMu.Unlock()
	if gotClockCalls != len(clockTimes) {
		t.Fatalf("replay clock calls = %d, want %d", gotClockCalls, len(clockTimes))
	}

	operations := log.snapshot()
	if got := observer.selectionCount("ignore", "user"); got != 1 {
		t.Fatalf("ordered first-rule ignore selections = %d, want 1", got)
	}
	if got := observer.selectionCount("play", "user"); got != 3 {
		t.Fatalf("configured play selections = %d, want 3", got)
	}
	if got := countPrefix(operations, "midi.send:96"); got == 0 {
		t.Fatalf("channel 7 Note On count = %d, want at least 1; operations = %v", got, operations)
	}
	if got := countPrefix(operations, "midi.send:90"); got != 0 {
		t.Fatalf("default-channel Note On count = %d, want 0; operations = %v", got, operations)
	}
	assertOrdered(t, operations,
		"capture.open", "midi.devices", "midi.open", "capture.next", "midi.send:96",
		"capture.close", "midi.send:b0", "midi.output.close", "midi.driver.close",
	)
}

func TestRunReplayMIDIUnavailableFailsBeforeReadingPCAPAndPreservesCleanupErrors(t *testing.T) {
	configuration := testConfig()
	sourceCloseErr := errors.New("PCAP close failed")
	driverCloseErr := errors.New("MIDI driver close failed")
	source := &replayTestSource{
		events:   []packet.Event{testPacket(41000, 443, time.Unix(100, 0))},
		closeErr: sourceCloseErr,
	}
	driver := &fakeDriver{closeErr: driverCloseErr}
	dependencies := Dependencies{
		OpenReplayFile: func(string) (capture.Source, error) { return source, nil },
		NewMIDIDriver:  func() (midi.Driver, error) { return driver, nil },
	}

	err := RunReplayWithDependencies(context.Background(), configuration, "fixture.pcap", dependencies)
	if !errors.Is(err, midi.ErrOutputUnavailable) {
		t.Fatalf("RunReplayWithDependencies() error = %v, want %v", err, midi.ErrOutputUnavailable)
	}
	if !errors.Is(err, sourceCloseErr) || !errors.Is(err, driverCloseErr) {
		t.Fatalf("RunReplayWithDependencies() error = %v, want both cleanup failures", err)
	}
	if got := source.nextCalls.Load(); got != 0 {
		t.Fatalf("PCAP Next calls = %d, want 0", got)
	}
	if !source.closed.Load() {
		t.Fatal("PCAP source was not closed")
	}
	if !driver.closed.Load() {
		t.Fatal("MIDI driver was not closed")
	}
}

func TestRunReplayRejectsNilMIDIDriverAndClosesPCAP(t *testing.T) {
	configuration := testConfig()
	source := &replayTestSource{}
	dependencies := Dependencies{
		OpenReplayFile: func(string) (capture.Source, error) { return source, nil },
		NewMIDIDriver:  func() (midi.Driver, error) { return nil, nil },
	}

	err := RunReplayWithDependencies(context.Background(), configuration, "fixture.pcap", dependencies)
	if !containsError(err, "MIDI driver: driver is unavailable") {
		t.Fatalf("RunReplayWithDependencies() error = %v, want unavailable-driver error", err)
	}
	if !source.closed.Load() {
		t.Fatal("PCAP source was not closed")
	}
}

func TestRunReplayMIDIDisabledDryRunReportsSelectedNotesAsDropped(t *testing.T) {
	configuration := testConfig()
	configuration.MIDI.Enabled = false
	configuration.Mapping.DefaultState = config.FlowPlay
	source := &replayTestSource{events: []packet.Event{
		testPacket(41000, 443, time.Unix(100, 0)),
	}}
	observer := newReplayObserver()
	var midiCalls atomic.Int32
	dependencies := Dependencies{
		OpenReplayFile: func(string) (capture.Source, error) { return source, nil },
		NewMIDIDriver: func() (midi.Driver, error) {
			midiCalls.Add(1)
			return nil, errors.New("unexpected MIDI initialization")
		},
		ReplayObserver: observer,
	}

	if err := RunReplayWithDependencies(context.Background(), configuration, "fixture.pcap", dependencies); err != nil {
		t.Fatalf("RunReplayWithDependencies() error = %v, want successful dry replay", err)
	}
	if got := midiCalls.Load(); got != 0 {
		t.Fatalf("MIDI factory calls = %d, want 0", got)
	}
	if got := observer.dropCount("note_sink", "write_error"); got != 1 {
		t.Fatalf("note sink write-error drops = %d, want 1", got)
	}
	if !source.closed.Load() {
		t.Fatal("PCAP source was not closed")
	}
}

func TestRunReplayCancellationInterruptsPacingAndIsSuccessful(t *testing.T) {
	configuration := testConfig()
	configuration.MIDI.Enabled = false
	source := &replayTestSource{events: []packet.Event{
		testPacket(41000, 443, time.Unix(100, 0)),
		testPacket(41001, 443, time.Unix(110, 0)),
	}}
	waiting := make(chan struct{})
	dependencies := Dependencies{
		OpenReplayFile: func(string) (capture.Source, error) { return source, nil },
		ReplayNow:      func() time.Time { return time.Unix(1_000, 0) },
		ReplayWait: func(ctx context.Context, _ time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunReplayWithDependencies(ctx, configuration, "fixture.pcap", dependencies) }()
	select {
	case <-waiting:
	case <-time.After(2 * time.Second):
		t.Fatal("replay did not begin pacing")
	}
	cancel()
	if err := await(t, done); err != nil {
		t.Fatalf("RunReplayWithDependencies() error = %v, want nil", err)
	}
	if !source.closed.Load() {
		t.Fatal("PCAP source was not closed")
	}
}

func TestRunReplayPreservesReadAndSourceCloseFailures(t *testing.T) {
	configuration := testConfig()
	configuration.MIDI.Enabled = false
	readErr := errors.New("truncated PCAP record")
	closeErr := errors.New("PCAP close failed")
	source := &replayTestSource{
		events:   []packet.Event{testPacket(41000, 443, time.Unix(100, 0))},
		nextErr:  readErr,
		closeErr: closeErr,
	}
	dependencies := Dependencies{
		OpenReplayFile: func(string) (capture.Source, error) { return source, nil },
	}

	err := RunReplayWithDependencies(context.Background(), configuration, "fixture.pcap", dependencies)
	if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
		t.Fatalf("RunReplayWithDependencies() error = %v, want read and close failures", err)
	}
	if !source.closed.Load() {
		t.Fatal("PCAP source was not closed")
	}
}

func TestRunReplayAlreadyCanceledOpensNoBoundaries(t *testing.T) {
	configuration := testConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var boundaryCalls atomic.Int32
	dependencies := Dependencies{
		OpenReplayFile: func(string) (capture.Source, error) {
			boundaryCalls.Add(1)
			return nil, errors.New("unexpected PCAP open")
		},
		NewMIDIDriver: func() (midi.Driver, error) {
			boundaryCalls.Add(1)
			return nil, errors.New("unexpected MIDI open")
		},
	}

	if err := RunReplayWithDependencies(ctx, configuration, "fixture.pcap", dependencies); err != nil {
		t.Fatalf("RunReplayWithDependencies() error = %v, want nil", err)
	}
	if got := boundaryCalls.Load(); got != 0 {
		t.Fatalf("boundary calls = %d, want 0", got)
	}
}

func TestRunReplayRejectsBPFBeforeOpeningBoundaries(t *testing.T) {
	configuration := testConfig()
	configuration.Capture.BPF = "tcp port 443"
	var boundaryCalls atomic.Int32
	dependencies := Dependencies{
		OpenReplayFile: func(string) (capture.Source, error) {
			boundaryCalls.Add(1)
			return nil, nil
		},
		NewMIDIDriver: func() (midi.Driver, error) {
			boundaryCalls.Add(1)
			return nil, nil
		},
	}

	err := RunReplayWithDependencies(context.Background(), configuration, "fixture.pcap", dependencies)
	if !containsError(err, "capture.bpf is unsupported") {
		t.Fatalf("RunReplayWithDependencies() error = %v, want unsupported capture.bpf", err)
	}
	if got := boundaryCalls.Load(); got != 0 {
		t.Fatalf("boundary calls = %d, want 0", got)
	}
}

func TestRunReplayRequiresEnabledStandaloneCapture(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*config.Config)
		errorContains string
	}{
		{
			name:          "capture disabled",
			configure:     func(configuration *config.Config) { configuration.Capture.Enabled = false },
			errorContains: "capture is disabled",
		},
		{
			name:          "host role",
			configure:     func(configuration *config.Config) { configuration.Instance.Role = config.RoleHost },
			errorContains: "instance role",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := testConfig()
			test.configure(&configuration)
			err := RunReplayWithDependencies(context.Background(), configuration, "fixture.pcap", Dependencies{})
			if !containsError(err, test.errorContains) {
				t.Fatalf("RunReplayWithDependencies() error = %v, want containing %q", err, test.errorContains)
			}
		})
	}
}

type replayTestSource struct {
	mu        sync.Mutex
	events    []packet.Event
	next      int
	nextCalls atomic.Int32
	closed    atomic.Bool
	nextErr   error
	closeErr  error
	log       *operationLog
}

func (source *replayTestSource) Next(ctx context.Context) (packet.Event, error) {
	if err := ctx.Err(); err != nil {
		return packet.Event{}, err
	}
	source.nextCalls.Add(1)
	if source.log != nil {
		source.log.add("capture.next")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.next == len(source.events) {
		if source.nextErr != nil {
			return packet.Event{}, source.nextErr
		}
		return packet.Event{}, io.EOF
	}
	event := source.events[source.next]
	source.next++
	return event, nil
}

func (source *replayTestSource) Close() error {
	if source.log != nil {
		source.log.add("capture.close")
	}
	source.closed.Store(true)
	return source.closeErr
}

type replayObserver struct {
	mu         sync.Mutex
	drops      map[[2]string]int
	selections map[[2]string]int
}

func newReplayObserver() *replayObserver {
	return &replayObserver{
		drops:      make(map[[2]string]int),
		selections: make(map[[2]string]int),
	}
}

func (observer *replayObserver) PacketCaptured(string, int) {}
func (observer *replayObserver) CaptureError(string)        {}
func (observer *replayObserver) Dropped(stage, reason string) {
	observer.mu.Lock()
	observer.drops[[2]string{stage, reason}]++
	observer.mu.Unlock()
}
func (observer *replayObserver) PacketQueue(int, int)    {}
func (observer *replayObserver) NoteQueue(int, int)      {}
func (observer *replayObserver) FlowCount(int)           {}
func (observer *replayObserver) FlowEvicted(string, int) {}
func (observer *replayObserver) Selected(state, tier string) {
	observer.mu.Lock()
	observer.selections[[2]string{state, tier}]++
	observer.mu.Unlock()
}
func (observer *replayObserver) Mapped(string, string, time.Duration, time.Duration, uint8) {}
func (observer *replayObserver) Processed(time.Duration)                                    {}

func (observer *replayObserver) dropCount(stage, reason string) int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.drops[[2]string{stage, reason}]
}

func (observer *replayObserver) selectionCount(state, tier string) int {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.selections[[2]string{state, tier}]
}

func containsError(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}
