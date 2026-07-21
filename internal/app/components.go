package app

import (
	"errors"
	"fmt"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/metrics"
	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/ElectricNoodle/go-musical-packets/internal/pipeline"
)

type processingComponents struct {
	registry *flow.Registry
	selector *flow.Selector
	mapper   *music.Mapper
}

// newProcessingComponents is shared by live capture and replay so both paths
// interpret registry, ordered selector-rule, and mapping configuration alike.
// Replay passes no safety rules because it opens no application listener whose
// traffic could feed back into capture.
func newProcessingComponents(configuration config.Config, safetyRules func([]flow.Rule) []flow.Rule) (processingComponents, error) {
	userRules, err := configuration.FlowRules()
	if err != nil {
		return processingComponents{}, fmt.Errorf("build flow rules: %w", err)
	}

	registry, err := flow.NewRegistry(flow.RegistryConfig{
		Seed:     configuration.Mapping.Seed,
		Capacity: configuration.Performance.FlowRegistryCapacity,
		TTL:      configuration.Performance.FlowTTL,
	})
	if err != nil {
		return processingComponents{}, fmt.Errorf("initialize flow registry: %w", err)
	}

	var configuredSafetyRules []flow.Rule
	if safetyRules != nil {
		configuredSafetyRules = safetyRules(userRules)
	}
	selector, err := flow.NewSelector(flow.SelectorConfig{
		Seed:        configuration.Mapping.Seed,
		Default:     flow.Action{State: flow.State(configuration.Mapping.DefaultState), Channel: configuration.Mapping.DefaultChannel},
		SafetyRules: configuredSafetyRules,
		UserRules:   userRules,
	})
	if err != nil {
		return processingComponents{}, fmt.Errorf("initialize flow selector: %w", err)
	}

	mapper, err := music.NewMapper(music.MapperConfig{
		Seed:            configuration.Mapping.Seed,
		Origin:          configuration.Instance.ID,
		MinimumNote:     configuration.Mapping.MinimumNote,
		MaximumNote:     configuration.Mapping.MaximumNote,
		MinimumDuration: configuration.Mapping.MinimumDuration,
		MaximumDuration: configuration.Mapping.MaximumDuration,
	})
	if err != nil {
		return processingComponents{}, fmt.Errorf("initialize music mapper: %w", err)
	}

	return processingComponents{registry: registry, selector: selector, mapper: mapper}, nil
}

func newProcessor(
	configuration config.Config,
	components processingComponents,
	source capture.Source,
	sink pipeline.Sink,
	observer pipeline.Observer,
) (*pipeline.Processor, error) {
	processor, err := pipeline.New(pipeline.Config{
		Source:              source,
		Registry:            components.registry,
		Selector:            components.selector,
		Mapper:              components.mapper,
		Sink:                sink,
		Observer:            observer,
		PacketQueueCapacity: configuration.Performance.PacketQueueCapacity,
		NoteQueueCapacity:   configuration.Performance.NoteQueueCapacity,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize packet pipeline: %w", err)
	}
	return processor, nil
}

type midiComponents struct {
	manager   *midi.Manager
	scheduler *midi.Scheduler
}

// newMIDIComponents transfers driver ownership to the returned manager. If
// construction fails before the manager starts, it closes the driver itself.
func newMIDIComponents(
	configuration config.Config,
	bundle *metrics.Bundle,
	newDriver func() (midi.Driver, error),
) (midiComponents, error) {
	driver, err := newDriver()
	if err != nil {
		return midiComponents{}, fmt.Errorf("initialize MIDI driver: %w", err)
	}
	if driver == nil {
		return midiComponents{}, errors.New("initialize MIDI driver: driver is unavailable")
	}
	manager, err := midi.NewManager(midi.ManagerConfig{
		Driver:            driver,
		ExactDeviceName:   configuration.MIDI.ExactDeviceName,
		DeviceNamePattern: configuration.MIDI.DeviceNameRegexp,
		PollInterval:      configuration.MIDI.PollInterval,
		Observer:          bundle.MIDI,
	})
	if err != nil {
		return midiComponents{}, errors.Join(fmt.Errorf("initialize MIDI manager: %w", err), driver.Close())
	}
	scheduler, err := midi.NewScheduler(midi.SchedulerConfig{
		Sender:                   manager,
		MaximumNotesPerSecond:    configuration.Performance.MaximumNotesPerSecond,
		MaximumPolyphony:         configuration.Performance.MaximumPolyphony,
		MinimumRetriggerInterval: configuration.Performance.MinimumRetriggerInterval,
		Observer:                 bundle.MIDI,
	})
	if err != nil {
		return midiComponents{}, errors.Join(fmt.Errorf("initialize MIDI scheduler: %w", err), driver.Close())
	}
	return midiComponents{manager: manager, scheduler: scheduler}, nil
}
