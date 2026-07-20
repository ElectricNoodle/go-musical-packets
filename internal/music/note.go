package music

import (
	"errors"
	"time"
)

// NoteEvent is the transport-independent trigger accepted by MIDI schedulers.
// Channel uses the user-facing MIDI range 1 through 16.
type NoteEvent struct {
	ID             string
	Origin         string
	Sequence       uint64
	MappingVersion string
	FlowID         string
	Mode           Mode
	Root           uint8
	Note           uint8
	Velocity       uint8
	Duration       time.Duration
	Channel        uint8
	CreatedAt      time.Time
}

// Validate checks protocol and MIDI-domain invariants.
func (e NoteEvent) Validate() error {
	if e.ID == "" || e.Origin == "" {
		return errors.New("event ID and origin are required")
	}
	if !e.Mode.Valid() {
		return errors.New("mode is invalid")
	}
	if e.Root > 11 {
		return errors.New("root must be in pitch-class range 0 through 11")
	}
	if e.Note > 127 || e.Velocity > 127 {
		return errors.New("note and velocity must be in MIDI range 0 through 127")
	}
	if e.Velocity == 0 {
		return errors.New("note trigger velocity must be greater than zero")
	}
	if e.Channel < 1 || e.Channel > 16 {
		return errors.New("channel must be in user-facing range 1 through 16")
	}
	if e.Duration <= 0 {
		return errors.New("duration must be positive")
	}
	return nil
}
