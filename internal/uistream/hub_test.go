package uistream

import (
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

func TestHubDropsOldestEventForSlowSubscriber(t *testing.T) {
	observer := &recordingObserver{}
	hub := New(2, observer)
	subscription := hub.Subscribe()
	defer subscription.Close()

	hub.Publish(testNote("one", 60))
	hub.Publish(testNote("two", 62))
	hub.Publish(testNote("three", 64))

	first := <-subscription.Events()
	second := <-subscription.Events()
	if first.ID != "two" || second.ID != "three" {
		t.Fatalf("queued IDs = %q, %q, want two, three", first.ID, second.ID)
	}
	if got := subscription.TakeDrops(); got != 1 {
		t.Fatalf("drops = %d, want 1", got)
	}
	if observer.dropped != 1 {
		t.Fatalf("observed drops = %d, want 1", observer.dropped)
	}
}

func TestHubSubscriptionsAreIndependent(t *testing.T) {
	hub := New(1, nil)
	first := hub.Subscribe()
	second := hub.Subscribe()
	defer first.Close()
	defer second.Close()

	hub.Publish(testNote("accepted", 60))
	if got := (<-first.Events()).ID; got != "accepted" {
		t.Fatalf("first subscription ID = %q", got)
	}
	if got := (<-second.Events()).ID; got != "accepted" {
		t.Fatalf("second subscription ID = %q", got)
	}
}

func testNote(id string, note uint8) music.NoteEvent {
	return music.NoteEvent{
		ID: id, Origin: "test", MappingVersion: music.FlowModeV1, FlowID: "flow",
		Mode: music.Ionian, Root: 0, Note: note, Velocity: 90,
		Duration: 250 * time.Millisecond, Channel: 1, CreatedAt: time.Unix(10, 0),
	}
}

type recordingObserver struct {
	clients int
	dropped int
	sent    int
}

func (observer *recordingObserver) Clients(count int) { observer.clients = count }
func (observer *recordingObserver) Events(result string, count int) {
	if result == "dropped" {
		observer.dropped += count
	}
	if result == "sent" {
		observer.sent += count
	}
}
