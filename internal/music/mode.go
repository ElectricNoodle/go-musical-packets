// Package music contains deterministic musical domain types and mapping rules.
package music

import "fmt"

// Mode is one of the seven diatonic modes.
type Mode uint8

const (
	Ionian Mode = iota
	Dorian
	Phrygian
	Lydian
	Mixolydian
	Aeolian
	Locrian
	modeCount
)

var modeNames = [...]string{
	"ionian",
	"dorian",
	"phrygian",
	"lydian",
	"mixolydian",
	"aeolian",
	"locrian",
}

var modeIntervals = [...][7]uint8{
	{0, 2, 4, 5, 7, 9, 11},
	{0, 2, 3, 5, 7, 9, 10},
	{0, 1, 3, 5, 7, 8, 10},
	{0, 2, 4, 6, 7, 9, 11},
	{0, 2, 4, 5, 7, 9, 10},
	{0, 2, 3, 5, 7, 8, 10},
	{0, 1, 3, 5, 6, 8, 10},
}

// String returns the lowercase mode name.
func (m Mode) String() string {
	if !m.Valid() {
		return fmt.Sprintf("mode(%d)", m)
	}
	return modeNames[m]
}

// Valid reports whether m is a supported mode.
func (m Mode) Valid() bool {
	return m < modeCount
}

// Interval returns the semitone interval at degree. Degrees wrap every seven
// steps and advance octaves.
func (m Mode) Interval(degree int) (int, error) {
	if !m.Valid() {
		return 0, fmt.Errorf("invalid mode %d", m)
	}
	if degree < 0 {
		return 0, fmt.Errorf("degree must not be negative")
	}
	octave, index := degree/7, degree%7
	return octave*12 + int(modeIntervals[m][index]), nil
}

// Modes returns all supported modes in their stable mapping order.
func Modes() []Mode {
	return []Mode{Ionian, Dorian, Phrygian, Lydian, Mixolydian, Aeolian, Locrian}
}
