package music

import (
	"net/netip"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestMapperIsDeterministicAndInScale(t *testing.T) {
	mapper := testMapper(t)
	input := MapInput{
		Packet: packet.Event{
			CapturedAt:     time.Unix(100, 0),
			Source:         packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.10"), Port: 52100},
			Destination:    packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.20"), Port: 443},
			Protocol:       packet.ProtocolTCP,
			WireLength:     512,
			CapturedLength: 512,
			PayloadLength:  472,
			TCPFlags:       packet.TCPFlagACK,
		},
		Sequence:     42,
		InterArrival: 123 * time.Millisecond,
		Channel:      7,
	}

	first, err := mapper.Map(input)
	if err != nil {
		t.Fatalf("Map() error = %v", err)
	}
	second, err := mapper.Map(input)
	if err != nil {
		t.Fatalf("Map() second error = %v", err)
	}
	if first != second {
		t.Fatalf("Map() is not deterministic: first=%#v second=%#v", first, second)
	}
	if first.Channel != 7 {
		t.Fatalf("Map() channel = %d, want 7", first.Channel)
	}
	if first.Note < 36 || first.Note > 96 {
		t.Fatalf("Map() note = %d, want 36..96", first.Note)
	}
	assertNoteInMode(t, first)
}

func TestMapperReversedFlowKeepsIdentity(t *testing.T) {
	mapper := testMapper(t)
	a := packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.10"), Port: 52100}
	b := packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.20"), Port: 443}
	base := packet.Event{Protocol: packet.ProtocolTCP, WireLength: 100, CapturedLength: 100}

	forward := base
	forward.Source, forward.Destination = a, b
	reverse := base
	reverse.Source, reverse.Destination = b, a

	first, err := mapper.Map(MapInput{Packet: forward, Sequence: 1, Channel: 1})
	if err != nil {
		t.Fatalf("Map(forward) error = %v", err)
	}
	second, err := mapper.Map(MapInput{Packet: reverse, Sequence: 2, Channel: 1})
	if err != nil {
		t.Fatalf("Map(reverse) error = %v", err)
	}
	if first.FlowID != second.FlowID || first.Mode != second.Mode || first.Root != second.Root {
		t.Fatalf("flow identity changed: forward=%#v reverse=%#v", first, second)
	}
}

func TestMapperRejectsInvalidChannel(t *testing.T) {
	mapper := testMapper(t)
	event := packet.Event{
		Source:         packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1")},
		Destination:    packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.2")},
		Protocol:       packet.ProtocolOther,
		WireLength:     64,
		CapturedLength: 64,
	}
	if _, err := mapper.Map(MapInput{Packet: event, Channel: 0}); err == nil {
		t.Fatal("Map() error = nil, want channel error")
	}
}

func TestMapperUsesFixedMusicalIdentity(t *testing.T) {
	mapper := testMapper(t)
	event := packet.Event{
		Source: packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.1")}, Destination: packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.2")},
		Protocol: packet.ProtocolUDP, WireLength: 512, CapturedLength: 512,
	}
	got, err := mapper.Map(MapInput{Packet: event, Channel: 4, Mode: "locrian", Root: 6})
	if err != nil {
		t.Fatalf("Map() error = %v", err)
	}
	if got.Mode != Locrian || got.Root != 6 {
		t.Fatalf("fixed identity = %s/%d, want locrian/6", got.Mode, got.Root)
	}
	assertNoteInMode(t, got)

	if _, err := mapper.Map(MapInput{Packet: event, Channel: 4, Mode: "unknown", Root: 6}); err == nil {
		t.Fatal("Map(invalid fixed mode) error = nil")
	}
	if _, err := mapper.Map(MapInput{Packet: event, Channel: 4, Mode: "dorian", Root: 12}); err == nil {
		t.Fatal("Map(invalid fixed root) error = nil")
	}
}

func testMapper(t *testing.T) *Mapper {
	t.Helper()
	mapper, err := NewMapper(MapperConfig{
		Seed:            "test-seed",
		Origin:          "test-node",
		MinimumNote:     36,
		MaximumNote:     96,
		MinimumDuration: 50 * time.Millisecond,
		MaximumDuration: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewMapper() error = %v", err)
	}
	return mapper
}

func assertNoteInMode(t *testing.T, event NoteEvent) {
	t.Helper()
	pitchClass := (int(event.Note) - int(event.Root)) % 12
	if pitchClass < 0 {
		pitchClass += 12
	}
	for _, interval := range modeIntervals[event.Mode] {
		if pitchClass == int(interval) {
			return
		}
	}
	t.Fatalf("note %d is not in %s rooted at %d", event.Note, event.Mode, event.Root)
}
