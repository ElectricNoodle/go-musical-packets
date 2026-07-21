package midi

import (
	"context"
	"errors"

	"github.com/ElectricNoodle/go-musical-packets/internal/music"
)

var ErrRuntimeClosed = errors.New("MIDI runtime is closed")

type operationGate struct{ token chan struct{} }

func newOperationGate() *operationGate {
	gate := &operationGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (gate *operationGate) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate.token:
		if err := ctx.Err(); err != nil {
			gate.release()
			return err
		}
		return nil
	}
}

func (gate *operationGate) release() { gate.token <- struct{}{} }

// Runtime is the single coordination boundary for scheduled notes, timer
// callbacks, automatic device transitions, and operator controls.
type Runtime struct {
	manager   *Manager
	scheduler *Scheduler
	closed    bool
}

// NewRuntime binds an unused manager and scheduler to one operation gate. It
// must be called before Manager.Run or any scheduler operation begins.
func NewRuntime(manager *Manager, scheduler *Scheduler) (*Runtime, error) {
	if manager == nil {
		return nil, errors.New("MIDI manager is required")
	}
	if scheduler == nil {
		return nil, errors.New("MIDI scheduler is required")
	}
	senderManager, ok := scheduler.sender.(*Manager)
	if !ok || senderManager != manager {
		return nil, errors.New("MIDI scheduler must send through the runtime manager")
	}
	if manager.running.Load() {
		return nil, errors.New("MIDI manager is already running")
	}
	coordination := newOperationGate()
	runtime := &Runtime{manager: manager, scheduler: scheduler}
	manager.coordination = coordination
	manager.transition = runtime.transitionCoordinated
	scheduler.coordination = coordination
	return runtime, nil
}

// Write implements pipeline.Sink through the coordinated scheduler.
func (runtime *Runtime) Write(ctx context.Context, note music.NoteEvent) error {
	return runtime.scheduler.Write(ctx, note)
}

// Panic stops every scheduled note and sends All Notes Off on all channels.
func (runtime *Runtime) Panic(ctx context.Context) error {
	if ctx == nil {
		return errors.New("MIDI panic context is required")
	}
	if err := runtime.manager.coordination.acquire(ctx); err != nil {
		return err
	}
	defer runtime.manager.coordination.release()
	if runtime.closed {
		return ErrRuntimeClosed
	}
	return runtime.scheduler.panicCoordinated()
}

// Snapshot returns the manager's detached device and connection state.
func (runtime *Runtime) Snapshot() ManagerSnapshot { return runtime.manager.Snapshot() }

// Close serializes a final scheduler reset against every in-flight note,
// timer, device transition, and operator control.
func (runtime *Runtime) Close() error {
	_ = runtime.manager.coordination.acquire(context.Background())
	defer runtime.manager.coordination.release()
	if runtime.closed {
		return nil
	}
	runtime.closed = true
	return runtime.scheduler.closeCoordinated()
}

func (runtime *Runtime) transitionCoordinated(output Output, device Device) error {
	if runtime.closed {
		if output != nil {
			_ = output.Close()
		}
		return ErrRuntimeClosed
	}
	var panicErr error
	if _, connected := runtime.manager.Current(); connected {
		panicErr = runtime.scheduler.panicCoordinated()
	}
	transitionErr := runtime.manager.replaceOutputCoordinated(output, device)
	return errors.Join(panicErr, transitionErr)
}
