package music

import (
	"testing"
	"time"
)

func TestNoteEventValidate(t *testing.T) {
	valid := NoteEvent{
		ID:       "event-1",
		Origin:   "edge-1",
		Mode:     Dorian,
		Root:     2,
		Note:     62,
		Velocity: 100,
		Duration: 250 * time.Millisecond,
		Channel:  3,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for _, channel := range []uint8{0, 17} {
		invalid := valid
		invalid.Channel = channel
		if err := invalid.Validate(); err == nil {
			t.Errorf("Validate() with channel %d returned nil", channel)
		}
	}
}
