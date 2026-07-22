package music

import "testing"

func TestModeIntervals(t *testing.T) {
	want := map[Mode][]int{
		Ionian:     {0, 2, 4, 5, 7, 9, 11},
		Dorian:     {0, 2, 3, 5, 7, 9, 10},
		Phrygian:   {0, 1, 3, 5, 7, 8, 10},
		Lydian:     {0, 2, 4, 6, 7, 9, 11},
		Mixolydian: {0, 2, 4, 5, 7, 9, 10},
		Aeolian:    {0, 2, 3, 5, 7, 8, 10},
		Locrian:    {0, 1, 3, 5, 6, 8, 10},
	}

	for mode, intervals := range want {
		for degree, expected := range intervals {
			got, err := mode.Interval(degree)
			if err != nil {
				t.Fatalf("%s.Interval(%d) error = %v", mode, degree, err)
			}
			if got != expected {
				t.Errorf("%s.Interval(%d) = %d, want %d", mode, degree, got, expected)
			}
		}
	}
}

func TestModeIntervalWrapsOctave(t *testing.T) {
	got, err := Dorian.Interval(8)
	if err != nil {
		t.Fatalf("Interval() error = %v", err)
	}
	if got != 14 {
		t.Fatalf("Dorian.Interval(8) = %d, want 14", got)
	}
}

func TestParseModeUsesCanonicalNames(t *testing.T) {
	for _, mode := range Modes() {
		got, err := ParseMode(mode.String())
		if err != nil {
			t.Fatalf("ParseMode(%q) error = %v", mode, err)
		}
		if got != mode {
			t.Fatalf("ParseMode(%q) = %v, want %v", mode, got, mode)
		}
	}
	if _, err := ParseMode("Dorian"); err == nil {
		t.Fatal("ParseMode(non-canonical name) error = nil")
	}
}
