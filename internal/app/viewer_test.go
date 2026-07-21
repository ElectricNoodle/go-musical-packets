package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/ElectricNoodle/go-musical-packets/internal/uistream"
)

func TestViewerMIDIPublishesOnlySuccessfulSchedulerWrites(t *testing.T) {
	writeErr := errors.New("scheduler rejected note")
	stream := uistream.New(2, nil)
	subscription := stream.Subscribe()
	defer subscription.Close()
	output := &viewerMIDI{runtime: &stubViewerRuntime{writeErr: writeErr}, stream: stream}

	if err := output.Write(context.Background(), viewerNote("failed")); !errors.Is(err, writeErr) {
		t.Fatalf("Write() error = %v, want %v", err, writeErr)
	}
	select {
	case event := <-subscription.Events():
		t.Fatalf("failed write published event %#v", event)
	default:
	}

	output.runtime = &stubViewerRuntime{}
	if err := output.Write(context.Background(), viewerNote("accepted")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if got := (<-subscription.Events()).ID; got != "accepted" {
		t.Fatalf("published ID = %q, want accepted", got)
	}
}

type stubViewerRuntime struct{ writeErr error }

func (runtime *stubViewerRuntime) Write(context.Context, music.NoteEvent) error {
	return runtime.writeErr
}
func (*stubViewerRuntime) Snapshot() midi.ManagerSnapshot { return midi.ManagerSnapshot{} }
func (*stubViewerRuntime) Panic(context.Context) error    { return nil }

func viewerNote(id string) music.NoteEvent {
	return music.NoteEvent{
		ID: id, Origin: "test", MappingVersion: music.FlowModeV1, FlowID: "flow",
		Mode: music.Ionian, Root: 0, Note: 60, Velocity: 90,
		Duration: time.Second, Channel: 1, CreatedAt: time.Now(),
	}
}
