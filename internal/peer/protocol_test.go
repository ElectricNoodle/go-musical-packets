package peer

import (
	"strings"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

func TestNoteMessageRoundTripPreservesOriginatingChannel(t *testing.T) {
	event := testNote(time.Now().UTC(), 13)
	note, err := NoteFromEvent(event)
	if err != nil {
		t.Fatalf("NoteFromEvent() error = %v", err)
	}
	encoded, err := Encode(Message{Type: TypeNote, Version: ProtocolVersion, Note: &note})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	message, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	got, err := message.Note.Event()
	if err != nil {
		t.Fatalf("Event() error = %v", err)
	}
	if got.Channel != 13 || got.Origin != event.Origin || got.ID != event.ID || got.Mode != event.Mode || got.Root != event.Root {
		t.Fatalf("round trip = %#v, want originating event %#v", got, event)
	}
}

func TestDecodeRejectsNonCanonicalFrames(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"type":"ping","version":"peer-v1","ping":{"nonce":1,"sent_at":"2026-07-22T10:00:00Z"},"extra":true}`},
		{name: "duplicate field", body: `{"type":"ping","type":"pong","version":"peer-v1","ping":{"nonce":1,"sent_at":"2026-07-22T10:00:00Z"}}`},
		{name: "multiple payloads", body: `{"type":"ping","version":"peer-v1","ping":{"nonce":1,"sent_at":"2026-07-22T10:00:00Z"},"pong":{"nonce":1,"sent_at":"2026-07-22T10:00:00Z"}}`},
		{name: "wrong version", body: `{"type":"ping","version":"peer-v2","ping":{"nonce":1,"sent_at":"2026-07-22T10:00:00Z"}}`},
		{name: "trailing JSON", body: `{"type":"ping","version":"peer-v1","ping":{"nonce":1,"sent_at":"2026-07-22T10:00:00Z"}} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Decode([]byte(test.body)); err == nil {
				t.Fatal("Decode() error = nil")
			}
		})
	}
	if _, err := Decode([]byte(strings.Repeat("x", MaximumFrame+1))); err == nil {
		t.Fatal("Decode(oversized) error = nil")
	}
}

func FuzzDecodeNeverPanics(f *testing.F) {
	f.Add([]byte(`{"type":"ping","version":"peer-v1","ping":{"nonce":1,"sent_at":"2026-07-22T10:00:00Z"}}`))
	f.Add([]byte(`{"type":"note"}`))
	f.Add([]byte{0xff, 0xfe})
	f.Fuzz(func(t *testing.T, encoded []byte) {
		_, _ = Decode(encoded)
	})
}

func testNote(created time.Time, channel uint8) music.NoteEvent {
	return music.NoteEvent{
		ID: "edge-1:0123456789abcdef01234567:1", Origin: "edge-1", Sequence: 1,
		MappingVersion: music.FlowModeV1, FlowID: "0123456789abcdef01234567",
		Mode: music.Dorian, Root: 2, Note: 62, Velocity: 96,
		Duration: 250 * time.Millisecond, Channel: channel, CreatedAt: created,
	}
}
