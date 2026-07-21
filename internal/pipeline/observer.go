// Package pipeline composes capture, flow selection, mapping, and note delivery.
package pipeline

import "time"

// Observer receives bounded-enum operational events. Implementations must be
// safe for concurrent use and return promptly.
type Observer interface {
	PacketCaptured(protocol string, bytes int)
	CaptureError(reason string)
	Dropped(stage, reason string)
	PacketQueue(depth, capacity int)
	NoteQueue(depth, capacity int)
	FlowCount(active int)
	FlowEvicted(reason string, count int)
	Selected(state, tier string)
	Mapped(mode, result string, elapsed, noteDuration time.Duration, velocity uint8)
	Processed(elapsed time.Duration)
}

type noopObserver struct{}

func (noopObserver) PacketCaptured(string, int)                                 {}
func (noopObserver) CaptureError(string)                                        {}
func (noopObserver) Dropped(string, string)                                     {}
func (noopObserver) PacketQueue(int, int)                                       {}
func (noopObserver) NoteQueue(int, int)                                         {}
func (noopObserver) FlowCount(int)                                              {}
func (noopObserver) FlowEvicted(string, int)                                    {}
func (noopObserver) Selected(string, string)                                    {}
func (noopObserver) Mapped(string, string, time.Duration, time.Duration, uint8) {}
func (noopObserver) Processed(time.Duration)                                    {}
