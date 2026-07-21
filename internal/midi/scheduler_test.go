package midi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

func TestSchedulerGuaranteesNoteOff(t *testing.T) {
	clock := newManualClock(time.Unix(100, 0))
	sender := &recordingSender{}
	scheduler := testScheduler(t, sender, clock, 10, 4, 10*time.Millisecond)

	if err := scheduler.Write(context.Background(), schedulerNote(1, 60, 100*time.Millisecond)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	assertMessages(t, sender.snapshot(), []byte{0x90, 60, 100})
	clock.Advance(99 * time.Millisecond)
	if got := len(sender.snapshot()); got != 1 {
		t.Fatalf("messages before duration = %d, want 1", got)
	}
	clock.Advance(time.Millisecond)
	assertMessages(t, sender.snapshot(), []byte{0x90, 60, 100}, []byte{0x80, 60, 0})
}

func TestSchedulerEnforcesSafetyLimits(t *testing.T) {
	t.Run("rate", func(t *testing.T) {
		clock := newManualClock(time.Unix(200, 0))
		scheduler := testScheduler(t, &recordingSender{}, clock, 1, 4, 0)
		if err := scheduler.Write(context.Background(), schedulerNote(1, 60, time.Second)); err != nil {
			t.Fatalf("first Write() error = %v", err)
		}
		if err := scheduler.Write(context.Background(), schedulerNote(1, 61, time.Second)); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("second Write() error = %v, want ErrRateLimited", err)
		}
		clock.Advance(time.Second)
		if err := scheduler.Write(context.Background(), schedulerNote(1, 61, time.Second)); err != nil {
			t.Fatalf("Write() after window error = %v", err)
		}
	})

	t.Run("polyphony", func(t *testing.T) {
		clock := newManualClock(time.Unix(300, 0))
		scheduler := testScheduler(t, &recordingSender{}, clock, 10, 1, 0)
		if err := scheduler.Write(context.Background(), schedulerNote(1, 60, time.Second)); err != nil {
			t.Fatalf("first Write() error = %v", err)
		}
		if err := scheduler.Write(context.Background(), schedulerNote(1, 61, time.Second)); !errors.Is(err, ErrPolyphonyLimited) {
			t.Fatalf("second Write() error = %v, want ErrPolyphonyLimited", err)
		}
	})

	t.Run("retrigger", func(t *testing.T) {
		clock := newManualClock(time.Unix(400, 0))
		sender := &recordingSender{}
		scheduler := testScheduler(t, sender, clock, 10, 4, 50*time.Millisecond)
		if err := scheduler.Write(context.Background(), schedulerNote(2, 64, time.Second)); err != nil {
			t.Fatalf("first Write() error = %v", err)
		}
		clock.Advance(49 * time.Millisecond)
		if err := scheduler.Write(context.Background(), schedulerNote(2, 64, time.Second)); !errors.Is(err, ErrRetriggerLimited) {
			t.Fatalf("early Write() error = %v, want ErrRetriggerLimited", err)
		}
		clock.Advance(time.Millisecond)
		if err := scheduler.Write(context.Background(), schedulerNote(2, 64, time.Second)); err != nil {
			t.Fatalf("allowed retrigger error = %v", err)
		}
		assertMessages(t, sender.snapshot(),
			[]byte{0x91, 64, 100},
			[]byte{0x81, 64, 0},
			[]byte{0x91, 64, 100},
		)
		clock.Advance(time.Second)
		assertMessages(t, sender.snapshot(),
			[]byte{0x91, 64, 100},
			[]byte{0x81, 64, 0},
			[]byte{0x91, 64, 100},
			[]byte{0x81, 64, 0},
		)
	})
}

func TestSchedulerPanicAndClose(t *testing.T) {
	clock := newManualClock(time.Unix(500, 0))
	sender := &recordingSender{}
	scheduler := testScheduler(t, sender, clock, 10, 4, 0)
	if err := scheduler.Write(context.Background(), schedulerNote(4, 67, time.Second)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := scheduler.Panic(); err != nil {
		t.Fatalf("Panic() error = %v", err)
	}
	if got := len(sender.snapshot()); got != 17 {
		t.Fatalf("messages after panic = %d, want note on plus 16 all-notes-off", got)
	}
	clock.Advance(time.Second)
	if got := len(sender.snapshot()); got != 17 {
		t.Fatalf("stopped timer emitted after panic; messages = %d", got)
	}
	if err := scheduler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := scheduler.Write(context.Background(), schedulerNote(4, 67, time.Second)); !errors.Is(err, ErrSchedulerClosed) {
		t.Fatalf("Write() after close error = %v, want ErrSchedulerClosed", err)
	}
}

func testScheduler(t *testing.T, sender Sender, clock schedulerClock, rate, polyphony int, retrigger time.Duration) *Scheduler {
	t.Helper()
	scheduler, err := NewScheduler(SchedulerConfig{
		Sender:                   sender,
		MaximumNotesPerSecond:    rate,
		MaximumPolyphony:         polyphony,
		MinimumRetriggerInterval: retrigger,
		clock:                    clock,
	})
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}
	return scheduler
}

func schedulerNote(channel, note uint8, duration time.Duration) music.NoteEvent {
	return music.NoteEvent{
		ID: "event", Origin: "test", MappingVersion: music.FlowModeV1,
		FlowID: "flow", Mode: music.Dorian, Root: 2, Note: note,
		Velocity: 100, Duration: duration, Channel: channel,
	}
}

type recordingSender struct {
	mu       sync.Mutex
	messages [][]byte
	err      error
}

func (s *recordingSender) Send(message []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.messages = append(s.messages, append([]byte(nil), message...))
	return nil
}

func (s *recordingSender) snapshot() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([][]byte, len(s.messages))
	for index, message := range s.messages {
		result[index] = append([]byte(nil), message...)
	}
	return result
}

func assertMessages(t *testing.T, got [][]byte, want ...[]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("messages = % X, want % X", got, want)
	}
	for index := range want {
		if string(got[index]) != string(want[index]) {
			t.Fatalf("message %d = % X, want % X", index, got[index], want[index])
		}
	}
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	clock    *manualClock
	due      time.Time
	callback func()
	stopped  bool
	fired    bool
}

func newManualClock(now time.Time) *manualClock { return &manualClock{now: now} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) AfterFunc(duration time.Duration, callback func()) schedulerTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{clock: c, due: c.now.Add(duration), callback: callback}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	var callbacks []func()
	for _, timer := range c.timers {
		if !timer.stopped && !timer.fired && !timer.due.After(c.now) {
			timer.fired = true
			callbacks = append(callbacks, timer.callback)
		}
	}
	c.mu.Unlock()
	for _, callback := range callbacks {
		callback()
	}
}

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	return true
}
