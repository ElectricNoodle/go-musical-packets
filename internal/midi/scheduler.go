package midi

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

var (
	ErrRateLimited      = errors.New("MIDI note rate limit reached")
	ErrPolyphonyLimited = errors.New("MIDI polyphony limit reached")
	ErrRetriggerLimited = errors.New("MIDI note retriggered too quickly")
	ErrSchedulerClosed  = errors.New("MIDI scheduler is closed")
)

// Sender is the immediate MIDI message boundary used by Scheduler.
type Sender interface {
	Send([]byte) error
}

// SchedulerObserver receives bounded-cardinality scheduler events.
type SchedulerObserver interface {
	Note(channel uint8, result string)
	Write(operation, result string, elapsed time.Duration)
	Active(channel uint8, count, total int)
}

// SchedulerConfig controls global MIDI safety limits.
type SchedulerConfig struct {
	Sender                   Sender
	MaximumNotesPerSecond    int
	MaximumPolyphony         int
	MinimumRetriggerInterval time.Duration
	Observer                 SchedulerObserver
	clock                    schedulerClock
}

// Scheduler emits Note On immediately and owns every corresponding Note Off.
type Scheduler struct {
	mu                       sync.Mutex
	sender                   Sender
	maximumNotesPerSecond    int
	maximumPolyphony         int
	minimumRetriggerInterval time.Duration
	observer                 SchedulerObserver
	clock                    schedulerClock
	active                   map[noteKey]activeNote
	recent                   []time.Time
	nextGeneration           uint64
	closed                   bool
	coordination             *operationGate
}

type noteKey struct{ channel, note uint8 }

type activeNote struct {
	started    time.Time
	generation uint64
	timer      schedulerTimer
}

type schedulerClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) schedulerTimer
}

type schedulerTimer interface{ Stop() bool }

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) AfterFunc(duration time.Duration, callback func()) schedulerTimer {
	return time.AfterFunc(duration, callback)
}

type noopSchedulerObserver struct{}

func (noopSchedulerObserver) Note(uint8, string)                  {}
func (noopSchedulerObserver) Write(string, string, time.Duration) {}
func (noopSchedulerObserver) Active(uint8, int, int)              {}

// NewScheduler validates safety controls and constructs an idle scheduler.
func NewScheduler(config SchedulerConfig) (*Scheduler, error) {
	if config.Sender == nil {
		return nil, errors.New("MIDI sender is required")
	}
	if config.MaximumNotesPerSecond <= 0 {
		return nil, errors.New("maximum MIDI notes per second must be positive")
	}
	if config.MaximumPolyphony <= 0 || config.MaximumPolyphony > 128 {
		return nil, errors.New("maximum MIDI polyphony must be between 1 and 128")
	}
	if config.MinimumRetriggerInterval < 0 {
		return nil, errors.New("minimum MIDI retrigger interval must not be negative")
	}
	if config.Observer == nil {
		config.Observer = noopSchedulerObserver{}
	}
	if config.clock == nil {
		config.clock = wallClock{}
	}
	return &Scheduler{
		sender:                   config.Sender,
		maximumNotesPerSecond:    config.MaximumNotesPerSecond,
		maximumPolyphony:         config.MaximumPolyphony,
		minimumRetriggerInterval: config.MinimumRetriggerInterval,
		observer:                 config.Observer,
		clock:                    config.clock,
		active:                   make(map[noteKey]activeNote),
		coordination:             newOperationGate(),
	}, nil
}

// Write implements pipeline.Sink.
func (s *Scheduler) Write(ctx context.Context, note music.NoteEvent) error {
	if ctx == nil {
		return errors.New("MIDI scheduler context is required")
	}
	if err := note.Validate(); err != nil {
		return fmt.Errorf("schedule MIDI note: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.coordination.acquire(ctx); err != nil {
		return err
	}
	defer s.coordination.release()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSchedulerClosed
	}
	now := s.clock.Now()
	key := noteKey{channel: note.Channel, note: note.Note}
	current, retrigger := s.active[key]
	if retrigger && now.Sub(current.started) < s.minimumRetriggerInterval {
		s.observer.Note(note.Channel, "retrigger_limited")
		return ErrRetriggerLimited
	}
	s.pruneRateWindow(now)
	if len(s.recent) >= s.maximumNotesPerSecond {
		s.observer.Note(note.Channel, "rate_limited")
		return ErrRateLimited
	}
	if !retrigger && len(s.active) >= s.maximumPolyphony {
		s.observer.Note(note.Channel, "polyphony_limited")
		return ErrPolyphonyLimited
	}
	if retrigger {
		current.timer.Stop()
		delete(s.active, key)
		if err := s.send("note_off", note.Channel, note.Note, 0); err != nil {
			message, _ := AllNotesOff(note.Channel)
			_ = s.sendMessage("all_notes_off", message)
			s.observer.Note(note.Channel, "error")
			s.observeActive(note.Channel)
			return err
		}
	}
	message, _ := NoteOn(note.Channel, note.Note, note.Velocity)
	if err := s.sendMessage("note_on", message); err != nil {
		s.observer.Note(note.Channel, "error")
		return err
	}
	s.recent = append(s.recent, now)
	s.nextGeneration++
	generation := s.nextGeneration
	timer := s.clock.AfterFunc(note.Duration, func() { s.stop(key, generation) })
	s.active[key] = activeNote{started: now, generation: generation, timer: timer}
	s.observer.Note(note.Channel, "played")
	s.observeActive(note.Channel)
	return nil
}

// Panic stops scheduled timers, sends All Notes Off on all channels, and keeps
// the scheduler available for future notes.
func (s *Scheduler) Panic() error {
	_ = s.coordination.acquire(context.Background())
	defer s.coordination.release()
	return s.panicCoordinated()
}

func (s *Scheduler) panicCoordinated() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panicLocked()
}

// Close performs a final panic and permanently rejects new notes.
func (s *Scheduler) Close() error {
	_ = s.coordination.acquire(context.Background())
	defer s.coordination.release()
	return s.closeCoordinated()
}

func (s *Scheduler) closeCoordinated() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.panicLocked()
}

func (s *Scheduler) stop(key noteKey, generation uint64) {
	_ = s.coordination.acquire(context.Background())
	defer s.coordination.release()
	s.mu.Lock()
	defer s.mu.Unlock()
	active, exists := s.active[key]
	if !exists || active.generation != generation {
		return
	}
	delete(s.active, key)
	if err := s.send("note_off", key.channel, key.note, 0); err != nil {
		message, _ := AllNotesOff(key.channel)
		_ = s.sendMessage("all_notes_off", message)
		s.observer.Note(key.channel, "note_off_error")
	} else {
		s.observer.Note(key.channel, "stopped")
	}
	s.observeActive(key.channel)
}

func (s *Scheduler) panicLocked() error {
	for _, active := range s.active {
		active.timer.Stop()
	}
	s.active = make(map[noteKey]activeNote)
	var failures []error
	for channel := uint8(1); channel <= 16; channel++ {
		message, _ := AllNotesOff(channel)
		if err := s.sendMessage("all_notes_off", message); err != nil && !errors.Is(err, ErrOutputUnavailable) {
			failures = append(failures, err)
		}
		s.observer.Active(channel, 0, 0)
	}
	return errors.Join(failures...)
}

func (s *Scheduler) send(operation string, channel, note, velocity uint8) error {
	var message []byte
	var err error
	if operation == "note_off" {
		message, err = NoteOff(channel, note)
	} else {
		message, err = NoteOn(channel, note, velocity)
	}
	if err != nil {
		return err
	}
	return s.sendMessage(operation, message)
}

func (s *Scheduler) sendMessage(operation string, message []byte) error {
	started := time.Now()
	err := s.sender.Send(message)
	result := "success"
	if err != nil {
		result = "error"
	}
	s.observer.Write(operation, result, time.Since(started))
	if err != nil {
		s.clearActiveLocked()
		return fmt.Errorf("send MIDI %s: %w", operation, err)
	}
	return nil
}

func (s *Scheduler) clearActiveLocked() {
	for _, active := range s.active {
		active.timer.Stop()
	}
	s.active = make(map[noteKey]activeNote)
	for channel := uint8(1); channel <= 16; channel++ {
		s.observer.Active(channel, 0, 0)
	}
}

func (s *Scheduler) pruneRateWindow(now time.Time) {
	cutoff := now.Add(-time.Second)
	first := 0
	for first < len(s.recent) && !s.recent[first].After(cutoff) {
		first++
	}
	s.recent = s.recent[first:]
}

func (s *Scheduler) observeActive(channel uint8) {
	channelCount := 0
	for key := range s.active {
		if key.channel == channel {
			channelCount++
		}
	}
	s.observer.Active(channel, channelCount, len(s.active))
}
