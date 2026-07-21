package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/capture"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

// Registry is the flow-state boundary used by Processor.
type Registry interface {
	Observe(packet.Event) (flow.ObserveResult, error)
	Expire(time.Time) []flow.Snapshot
	Len() int
}

// Selector is the routing-rule boundary used by Processor.
type Selector interface {
	Evaluate(packet.Event, flow.Overlay) (flow.Selection, error)
}

// Mapper is the musical conversion boundary used by Processor.
type Mapper interface {
	Map(music.MapInput) (music.NoteEvent, error)
}

// Sink accepts ordered mapped note triggers. A sink must honor context
// cancellation so pipeline shutdown cannot block indefinitely.
type Sink interface {
	Write(context.Context, music.NoteEvent) error
}

// Config composes a bounded processor.
type Config struct {
	Source              capture.Source
	Registry            Registry
	Selector            Selector
	Mapper              Mapper
	Sink                Sink
	Observer            Observer
	Overlay             func() flow.Overlay
	PacketQueueCapacity int
	NoteQueueCapacity   int
}

// Processor owns its source and runs at most once.
type Processor struct {
	source      capture.Source
	registry    Registry
	selector    Selector
	mapper      Mapper
	sink        Sink
	observer    Observer
	overlay     func() flow.Overlay
	packetQueue chan packet.Event
	noteQueue   chan music.NoteEvent
	running     atomic.Bool
}

// New validates dependencies and allocates fixed-capacity queues.
func New(config Config) (*Processor, error) {
	if config.Source == nil || config.Registry == nil || config.Selector == nil || config.Mapper == nil || config.Sink == nil {
		return nil, errors.New("pipeline source, registry, selector, mapper, and sink are required")
	}
	if config.PacketQueueCapacity <= 0 || config.NoteQueueCapacity <= 0 {
		return nil, errors.New("pipeline queue capacities must be positive")
	}
	if config.Observer == nil {
		config.Observer = noopObserver{}
	}
	if config.Overlay == nil {
		config.Overlay = func() flow.Overlay { return flow.Overlay{} }
	}
	processor := &Processor{
		source:      config.Source,
		registry:    config.Registry,
		selector:    config.Selector,
		mapper:      config.Mapper,
		sink:        config.Sink,
		observer:    config.Observer,
		overlay:     config.Overlay,
		packetQueue: make(chan packet.Event, config.PacketQueueCapacity),
		noteQueue:   make(chan music.NoteEvent, config.NoteQueueCapacity),
	}
	processor.observer.PacketQueue(0, cap(processor.packetQueue))
	processor.observer.NoteQueue(0, cap(processor.noteQueue))
	return processor, nil
}

// Run captures until EOF, cancellation, or a source error, then shuts down all
// pipeline stages and closes the owned source.
func (p *Processor) Run(ctx context.Context) error {
	if !p.running.CompareAndSwap(false, true) {
		return errors.New("pipeline processor may only run once")
	}
	packetDone := make(chan struct{})
	noteDone := make(chan struct{})
	go p.processNotes(ctx, noteDone)
	go p.processPackets(ctx, packetDone)

	captureErr := p.capture(ctx)
	close(p.packetQueue)
	<-packetDone
	<-noteDone
	closeErr := p.source.Close()

	if errors.Is(captureErr, io.EOF) {
		captureErr = nil
	}
	if captureErr != nil && !errors.Is(captureErr, context.Canceled) && !errors.Is(captureErr, context.DeadlineExceeded) {
		p.observer.CaptureError("read_error")
	}
	return errors.Join(captureErr, closeErr)
}

func (p *Processor) capture(ctx context.Context) error {
	for {
		event, err := p.source.Next(ctx)
		if err != nil {
			return err
		}
		p.observer.PacketCaptured(string(event.Protocol), event.WireLength)
		select {
		case <-ctx.Done():
			p.observer.Dropped("packet_queue", "shutdown")
			return ctx.Err()
		case p.packetQueue <- event:
			p.observer.PacketQueue(len(p.packetQueue), cap(p.packetQueue))
		default:
			p.observer.Dropped("packet_queue", "full")
		}
	}
}

func (p *Processor) processPackets(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	defer close(p.noteQueue)
	for {
		select {
		case <-ctx.Done():
			p.discardPackets("shutdown")
			return
		case event, ok := <-p.packetQueue:
			if !ok {
				return
			}
			p.observer.PacketQueue(len(p.packetQueue), cap(p.packetQueue))
			p.processPacket(ctx, event)
		}
	}
}

func (p *Processor) processPacket(ctx context.Context, event packet.Event) {
	started := time.Now()
	defer func() { p.observer.Processed(time.Since(started)) }()

	expired := p.registry.Expire(event.CapturedAt)
	if len(expired) > 0 {
		p.observer.FlowEvicted("ttl", len(expired))
	}
	observed, err := p.registry.Observe(event)
	if err != nil {
		p.observer.Dropped("flow_registry", "invalid_packet")
		return
	}
	if observed.Evicted != nil {
		p.observer.FlowEvicted("capacity", 1)
	}
	p.observer.FlowCount(p.registry.Len())

	selection, err := p.selector.Evaluate(event, p.overlay())
	if err != nil {
		p.observer.Dropped("flow_selector", "evaluation_error")
		return
	}
	p.observer.Selected(string(selection.State), selection.Tier)
	if selection.State != flow.StatePlay {
		return
	}

	var interArrival time.Duration
	if !observed.PreviousLastSeen.IsZero() && event.CapturedAt.After(observed.PreviousLastSeen) {
		interArrival = event.CapturedAt.Sub(observed.PreviousLastSeen)
	}
	mappingStarted := time.Now()
	note, err := p.mapper.Map(music.MapInput{
		Packet:       event,
		Sequence:     observed.Flow.Packets,
		InterArrival: interArrival,
		Channel:      selection.Channel,
	})
	if err != nil {
		p.observer.Mapped("unknown", "error", time.Since(mappingStarted), 0, 0)
		p.observer.Dropped("mapper", "conversion_error")
		return
	}
	p.observer.Mapped(note.Mode.String(), "success", time.Since(mappingStarted), note.Duration, note.Velocity)
	select {
	case <-ctx.Done():
		p.observer.Dropped("note_queue", "shutdown")
	case p.noteQueue <- note:
		p.observer.NoteQueue(len(p.noteQueue), cap(p.noteQueue))
	default:
		p.observer.Dropped("note_queue", "full")
	}
}

func (p *Processor) processNotes(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			p.discardNotes("shutdown")
			return
		case note, ok := <-p.noteQueue:
			if !ok {
				return
			}
			p.observer.NoteQueue(len(p.noteQueue), cap(p.noteQueue))
			if err := p.sink.Write(ctx, note); err != nil {
				p.observer.Dropped("note_sink", "write_error")
			}
		}
	}
}

func (p *Processor) discardPackets(reason string) {
	for range p.packetQueue {
		p.observer.Dropped("packet_queue", reason)
	}
	p.observer.PacketQueue(0, cap(p.packetQueue))
}

func (p *Processor) discardNotes(reason string) {
	for range p.noteQueue {
		p.observer.Dropped("note_queue", reason)
	}
	p.observer.NoteQueue(0, cap(p.noteQueue))
}

func (p *Processor) String() string {
	return fmt.Sprintf("pipeline(packet_queue=%d,note_queue=%d)", cap(p.packetQueue), cap(p.noteQueue))
}
