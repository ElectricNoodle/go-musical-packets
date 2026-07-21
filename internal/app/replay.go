package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/metrics"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
	"github.com/ElectricNoodle/go-musical-packets/internal/pipeline"
)

// RunReplay processes one classic PCAP file with the configured standalone
// rules, mapper, and optional MIDI output. Reaching the end of the file and
// context cancellation are successful completion conditions.
//
// Replay deliberately rejects a configured capture BPF. OpenReplayFile cannot
// apply libpcap filters, so accepting the value would misleadingly replay
// packets that live capture excludes.
func RunReplay(ctx context.Context, configuration config.Config, pcapPath string) error {
	return RunReplayWithDependencies(ctx, configuration, pcapPath, Dependencies{})
}

// RunReplayWithDependencies is RunReplay with injectable file, MIDI, pacing,
// and observer boundaries. It never discovers or opens a live interface and
// never binds the configured HTTP listener.
func RunReplayWithDependencies(ctx context.Context, configuration config.Config, pcapPath string, dependencies Dependencies) error {
	if ctx == nil {
		return errors.New("replay context is required")
	}
	if err := configuration.Validate(); err != nil {
		return fmt.Errorf("validate configuration: %w", err)
	}
	if configuration.Instance.Role != config.RoleStandalone {
		return fmt.Errorf("run replay: instance role %q is unsupported", configuration.Instance.Role)
	}
	if configuration.Peer.Enabled {
		return errors.New("run replay: peer transport is unsupported")
	}
	if !configuration.Capture.Enabled {
		return errors.New("run replay: capture is disabled")
	}
	if strings.TrimSpace(configuration.Capture.BPF) != "" {
		return errors.New("run replay: capture.bpf is unsupported for offline PCAP replay")
	}
	if strings.TrimSpace(pcapPath) == "" {
		return errors.New("run replay: PCAP path is required")
	}
	if ctx.Err() != nil {
		return nil
	}
	dependencies = dependencies.withDefaults()

	bundle, err := metrics.New(configuration.Metrics.Namespace)
	if err != nil {
		return fmt.Errorf("initialize metrics: %w", err)
	}
	components, err := newProcessingComponents(configuration, nil)
	if err != nil {
		return err
	}

	openedSource, err := dependencies.OpenReplayFile(pcapPath)
	if err != nil {
		return fmt.Errorf("open PCAP replay: %w", err)
	}
	if openedSource == nil {
		return errors.New("open PCAP replay: source is unavailable")
	}
	source := &pacedReplaySource{
		source: openedSource,
		now:    dependencies.ReplayNow,
		wait:   dependencies.ReplayWait,
	}

	observer := dependencies.ReplayObserver
	if observer == nil {
		observer = bundle.Pipeline
	}
	sink := pipeline.Sink(discardSink{})

	var manager *midi.Manager
	var midiRuntime *midi.Runtime
	var managerCancel context.CancelFunc
	var managerDone chan error
	if configuration.MIDI.Enabled {
		midiComponents, midiErr := newMIDIComponents(configuration, bundle, dependencies.NewMIDIDriver)
		if midiErr != nil {
			return errors.Join(midiErr, source.Close())
		}
		manager = midiComponents.manager
		midiRuntime = midiComponents.runtime
		sink = midiRuntime

		managerContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
		managerCancel = cancel
		managerDone = make(chan error, 1)
		go func() { managerDone <- manager.Run(managerContext) }()

		select {
		case <-manager.Ready():
			if _, connected := manager.Current(); !connected {
				return shutdownReplayStartup(midi.ErrOutputUnavailable, source, midiRuntime, managerCancel, managerDone)
			}
		case managerErr := <-managerDone:
			return shutdownReplayStartup(componentStopped("MIDI manager", managerErr), source, midiRuntime, nil, nil)
		case <-ctx.Done():
			return shutdownReplayStartup(nil, source, midiRuntime, managerCancel, managerDone)
		}
	}

	processor, err := newProcessor(configuration, components, source, sink, observer)
	if err != nil {
		return shutdownReplayStartup(err, source, midiRuntime, managerCancel, managerDone)
	}
	return superviseReplay(ctx, processor, midiRuntime, managerCancel, managerDone)
}

// pacedReplaySource targets wallStart+(packetTimestamp-firstTimestamp). Using
// an absolute target prevents packet processing and MIDI-write overhead from
// accumulating as replay timing drift. First and non-positive-offset packets
// are emitted immediately.
type pacedReplaySource struct {
	source        capture.Source
	now           func() time.Time
	wait          func(context.Context, time.Duration) error
	started       bool
	firstCaptured time.Time
	wallStart     time.Time
}

func (source *pacedReplaySource) Next(ctx context.Context) (packet.Event, error) {
	event, err := source.source.Next(ctx)
	if err != nil {
		return packet.Event{}, err
	}
	if !source.started {
		source.started = true
		source.firstCaptured = event.CapturedAt
		source.wallStart = source.now()
		return event, nil
	}

	offset := event.CapturedAt.Sub(source.firstCaptured)
	if offset <= 0 {
		return event, nil
	}
	delay := source.wallStart.Add(offset).Sub(source.now())
	if delay <= 0 {
		return event, nil
	}
	if err := source.wait(ctx, delay); err != nil {
		return packet.Event{}, fmt.Errorf("pace PCAP replay: %w", err)
	}
	return event, nil
}

func (source *pacedReplaySource) Close() error { return source.source.Close() }

func waitReplayDuration(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func shutdownReplayStartup(
	startupErr error,
	source capture.Source,
	midiRuntime *midi.Runtime,
	managerCancel context.CancelFunc,
	managerDone <-chan error,
) error {
	sourceErr := source.Close()
	schedulerErr := closeMIDIRuntime(midiRuntime)
	var managerErr error
	if managerCancel != nil {
		managerCancel()
	}
	if managerDone != nil {
		managerErr = <-managerDone
	}
	return errors.Join(startupErr, sourceErr, schedulerErr, normalizeComponentError(managerErr))
}

func superviseReplay(
	ctx context.Context,
	processor *pipeline.Processor,
	midiRuntime *midi.Runtime,
	managerCancel context.CancelFunc,
	managerDone <-chan error,
) error {
	processorContext, cancelProcessor := context.WithCancel(context.WithoutCancel(ctx))
	processorDone := make(chan error, 1)
	go func() { processorDone <- processor.Run(processorContext) }()

	var result error
	processorFinished := false
	managerFinished := managerDone == nil
	var managerErr error

	if managerDone == nil {
		select {
		case <-ctx.Done():
		case processorErr := <-processorDone:
			processorFinished = true
			result = normalizeComponentError(processorErr)
		}
	} else {
		select {
		case <-ctx.Done():
		case processorErr := <-processorDone:
			processorFinished = true
			result = normalizeComponentError(processorErr)
		case managerErr = <-managerDone:
			managerFinished = true
			result = componentStopped("MIDI manager", managerErr)
		}
	}

	cancelProcessor()
	if !processorFinished {
		result = errors.Join(result, normalizeComponentError(<-processorDone))
	}
	// The processor owns and has now closed the PCAP source. Keep shutdown
	// sequential so the scheduler can reset notes while MIDI remains connected.
	result = errors.Join(result, closeMIDIRuntime(midiRuntime))
	if managerCancel != nil {
		managerCancel()
	}
	if !managerFinished {
		managerErr = <-managerDone
		result = errors.Join(result, normalizeComponentError(managerErr))
	}
	return result
}
