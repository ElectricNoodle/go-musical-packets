package pipeline

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestProcessorMapsSelectedPacketsInOrder(t *testing.T) {
	started := time.Unix(1_000, 0)
	events := []packet.Event{
		testEvent(started, 100),
		testEvent(started.Add(150*time.Millisecond), 200),
	}
	source := &sliceSource{events: events}
	sink := &collectingSink{}
	processor := testProcessor(t, source, sink, noopObserver{}, 8, 8)

	if err := processor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !source.closed {
		t.Fatal("Run() did not close its source")
	}
	got := sink.snapshot()
	if len(got) != 2 {
		t.Fatalf("sink notes = %d, want 2", len(got))
	}
	if got[0].Channel != 7 || got[1].Channel != 7 {
		t.Fatalf("channels = %d, %d; want 7", got[0].Channel, got[1].Channel)
	}
	if got[0].Sequence != 1 || got[1].Sequence != 2 {
		t.Fatalf("sequences = %d, %d; want 1, 2", got[0].Sequence, got[1].Sequence)
	}
	if got[0].Duration != 50*time.Millisecond || got[1].Duration != 150*time.Millisecond {
		t.Fatalf("durations = %s, %s; want 50ms, 150ms", got[0].Duration, got[1].Duration)
	}
	if got[0].FlowID != got[1].FlowID {
		t.Fatal("packets from one bidirectional flow produced different flow IDs")
	}
	if err := processor.Run(context.Background()); err == nil {
		t.Fatal("second Run() error = nil")
	}
}

func TestProcessorPassesFixedRuleIdentityToMapper(t *testing.T) {
	source := &sliceSource{events: []packet.Event{testEvent(time.Unix(1_500, 0), 256)}}
	sink := &collectingSink{}
	processor := testProcessor(t, source, sink, noopObserver{}, 2, 2)
	selector, err := flow.NewSelector(flow.SelectorConfig{
		Seed: "test-seed", Default: flow.Action{State: flow.StatePlay, Channel: 7, Mode: "phrygian", Root: 4},
	})
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	processor.selector = selector

	if err := processor.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	notes := sink.snapshot()
	if len(notes) != 1 || notes[0].Mode != music.Phrygian || notes[0].Root != 4 {
		t.Fatalf("notes = %#v, want one E phrygian note", notes)
	}
}

func TestProcessorDropsWhenNoteQueueIsFull(t *testing.T) {
	observer := &recordingObserver{}
	processor := testProcessor(t, &sliceSource{}, &collectingSink{}, observer, 1, 1)
	started := time.Unix(2_000, 0)

	processor.processPacket(context.Background(), testEvent(started, 100))
	processor.processPacket(context.Background(), testEvent(started.Add(time.Millisecond), 101))

	if got := observer.dropCount("note_queue", "full"); got != 1 {
		t.Fatalf("note queue full drops = %d, want 1", got)
	}
	if got := len(processor.noteQueue); got != 1 {
		t.Fatalf("note queue depth = %d, want 1", got)
	}
}

func TestProcessorCancellationClosesSource(t *testing.T) {
	source := &blockingSource{}
	processor := testProcessor(t, source, &collectingSink{}, noopObserver{}, 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := processor.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if !source.closed {
		t.Fatal("Run() did not close its source after cancellation")
	}
}

func testProcessor(t *testing.T, source interface {
	Next(context.Context) (packet.Event, error)
	Close() error
}, sink Sink, observer Observer, packetCapacity, noteCapacity int) *Processor {
	t.Helper()
	registry, err := flow.NewRegistry(flow.RegistryConfig{Seed: "test-seed", Capacity: 32, TTL: time.Minute})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	selector, err := flow.NewSelector(flow.SelectorConfig{
		Seed:    "test-seed",
		Default: flow.Action{State: flow.StatePlay, Channel: 7},
	})
	if err != nil {
		t.Fatalf("NewSelector() error = %v", err)
	}
	mapper, err := music.NewMapper(music.MapperConfig{
		Seed:            "test-seed",
		Origin:          "test-node",
		MinimumNote:     36,
		MaximumNote:     96,
		MinimumDuration: 50 * time.Millisecond,
		MaximumDuration: time.Second,
	})
	if err != nil {
		t.Fatalf("NewMapper() error = %v", err)
	}
	processor, err := New(Config{
		Source:              source,
		Registry:            registry,
		Selector:            selector,
		Mapper:              mapper,
		Sink:                sink,
		Observer:            observer,
		PacketQueueCapacity: packetCapacity,
		NoteQueueCapacity:   noteCapacity,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return processor
}

func testEvent(capturedAt time.Time, wireLength int) packet.Event {
	return packet.Event{
		CapturedAt:     capturedAt,
		Source:         packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.10"), Port: 50_000},
		Destination:    packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.20"), Port: 443},
		Protocol:       packet.ProtocolTCP,
		WireLength:     wireLength,
		CapturedLength: wireLength,
		PayloadLength:  wireLength - 40,
		TCPFlags:       packet.TCPFlagACK,
	}
}

type sliceSource struct {
	events []packet.Event
	index  int
	closed bool
}

func (s *sliceSource) Next(context.Context) (packet.Event, error) {
	if s.index == len(s.events) {
		return packet.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *sliceSource) Close() error {
	s.closed = true
	return nil
}

type blockingSource struct{ closed bool }

func (s *blockingSource) Next(ctx context.Context) (packet.Event, error) {
	<-ctx.Done()
	return packet.Event{}, ctx.Err()
}

func (s *blockingSource) Close() error {
	s.closed = true
	return nil
}

type collectingSink struct {
	mu    sync.Mutex
	notes []music.NoteEvent
}

func (s *collectingSink) Write(_ context.Context, note music.NoteEvent) error {
	s.mu.Lock()
	s.notes = append(s.notes, note)
	s.mu.Unlock()
	return nil
}

func (s *collectingSink) snapshot() []music.NoteEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]music.NoteEvent(nil), s.notes...)
}

type recordingObserver struct {
	mu    sync.Mutex
	drops map[string]int
}

func (o *recordingObserver) PacketCaptured(string, int) {}
func (o *recordingObserver) CaptureError(string)        {}
func (o *recordingObserver) PacketQueue(int, int)       {}
func (o *recordingObserver) NoteQueue(int, int)         {}
func (o *recordingObserver) FlowCount(int)              {}
func (o *recordingObserver) FlowEvicted(string, int)    {}
func (o *recordingObserver) Selected(string, string)    {}
func (o *recordingObserver) Mapped(string, string, time.Duration, time.Duration, uint8) {
}
func (o *recordingObserver) Processed(time.Duration) {}

func (o *recordingObserver) Dropped(stage, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.drops == nil {
		o.drops = make(map[string]int)
	}
	o.drops[stage+":"+reason]++
}

func (o *recordingObserver) dropCount(stage, reason string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.drops[stage+":"+reason]
}
