package app

import (
	"context"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/midi"
	"github.com/ElectricNoodle/go-musical-packets/internal/music"
	"github.com/ElectricNoodle/go-musical-packets/internal/pipeline"
	"github.com/ElectricNoodle/go-musical-packets/internal/uistream"
)

// viewerMIDI exposes only scheduler-accepted events: a failed or safety-limited
// write never reaches the browser, so the visual playhead reflects reality.
type viewerMIDI struct {
	runtime viewerRuntime
	stream  *uistream.Hub
}

type viewerRuntime interface {
	Write(context.Context, music.NoteEvent) error
	Snapshot() midi.ManagerSnapshot
	Panic(context.Context) error
}

func (output *viewerMIDI) Write(ctx context.Context, event music.NoteEvent) error {
	if err := output.runtime.Write(ctx, event); err != nil {
		return err
	}
	output.stream.Publish(event)
	return nil
}

func (output *viewerMIDI) Snapshot() midi.ManagerSnapshot  { return output.runtime.Snapshot() }
func (output *viewerMIDI) Panic(ctx context.Context) error { return output.runtime.Panic(ctx) }

type viewerPipelineObserver struct {
	metrics pipeline.Observer
	stream  *uistream.Hub
}

func (observer viewerPipelineObserver) PacketCaptured(protocol string, bytes int) {
	observer.metrics.PacketCaptured(protocol, bytes)
	observer.stream.RecordPacket()
}
func (observer viewerPipelineObserver) CaptureError(reason string) {
	observer.metrics.CaptureError(reason)
}
func (observer viewerPipelineObserver) Dropped(stage, reason string) {
	observer.metrics.Dropped(stage, reason)
}
func (observer viewerPipelineObserver) PacketQueue(depth, capacity int) {
	observer.metrics.PacketQueue(depth, capacity)
}
func (observer viewerPipelineObserver) NoteQueue(depth, capacity int) {
	observer.metrics.NoteQueue(depth, capacity)
}
func (observer viewerPipelineObserver) FlowCount(active int) { observer.metrics.FlowCount(active) }
func (observer viewerPipelineObserver) FlowEvicted(reason string, count int) {
	observer.metrics.FlowEvicted(reason, count)
}
func (observer viewerPipelineObserver) Selected(state, tier string) {
	observer.metrics.Selected(state, tier)
}
func (observer viewerPipelineObserver) Mapped(mode, result string, elapsed, duration time.Duration, velocity uint8) {
	observer.metrics.Mapped(mode, result, elapsed, duration, velocity)
}
func (observer viewerPipelineObserver) Processed(elapsed time.Duration) {
	observer.metrics.Processed(elapsed)
}
