package midi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestOperationGateDoesNotAdmitCanceledContext(t *testing.T) {
	gate := newOperationGate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 100 {
		if err := gate.acquire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire() error = %v, want context.Canceled", err)
		}
	}
}

func TestRuntimeKeepsRetriggerAndTransitionOnOneOutput(t *testing.T) {
	oldOutput := newTransitionOutput()
	newOutput := newTransitionOutput()
	driver := &transitionDriver{
		devices: []Device{{Number: 1, Name: "old"}},
		outputs: map[int]*transitionOutput{1: oldOutput, 2: newOutput},
	}
	manager, err := NewManager(ManagerConfig{Driver: driver, PollInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	clock := newManualClock(time.Unix(800, 0))
	scheduler := testScheduler(t, manager, clock, 100, 4, 0)
	runtime, err := NewRuntime(manager, scheduler)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	if err := manager.reconcile(); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}
	if err := runtime.Write(context.Background(), schedulerNote(1, 60, time.Second)); err != nil {
		t.Fatalf("initial Write() error = %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	oldOutput.blockNext(0x80, entered, release)
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- runtime.Write(context.Background(), schedulerNote(1, 60, time.Second))
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("retrigger did not reach blocked Note Off")
	}

	driver.setDevices([]Device{{Number: 2, Name: "new"}})
	discoveryStarted := driver.signalNextDiscovery()
	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- manager.reconcile() }()
	select {
	case <-discoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("transition discovery did not begin")
	}
	select {
	case err := <-reconcileDone:
		t.Fatalf("reconcile completed across an in-flight retrigger: %v", err)
	default:
	}

	close(release)
	if err := <-writeDone; err != nil {
		t.Fatalf("retrigger Write() error = %v", err)
	}
	if err := <-reconcileDone; err != nil {
		t.Fatalf("reconcile() error = %v", err)
	}
	if current, connected := manager.Current(); !connected || current.Number != 2 {
		t.Fatalf("manager current = %#v, %v; want new output", current, connected)
	}

	oldMessages := oldOutput.snapshot()
	wantTail := [][]byte{{0x80, 60, 0}, {0x90, 60, 100}}
	if len(oldMessages) < 2 || string(oldMessages[17]) != string(wantTail[0]) || string(oldMessages[18]) != string(wantTail[1]) {
		t.Fatalf("old output retrigger sequence = % X, want Note Off then Note On", oldMessages)
	}
	for _, message := range newOutput.snapshot() {
		if message[0]&0xF0 == 0x90 {
			t.Fatalf("new output received crossed Note On: % X", message)
		}
	}
	before := len(oldOutput.snapshot()) + len(newOutput.snapshot())
	clock.Advance(time.Second)
	after := len(oldOutput.snapshot()) + len(newOutput.snapshot())
	if after != before {
		t.Fatalf("timer emitted after transition panic: messages %d -> %d", before, after)
	}
}

type transitionDriver struct {
	mu      sync.Mutex
	devices []Device
	outputs map[int]*transitionOutput
	signal  chan struct{}
}

func (driver *transitionDriver) setDevices(devices []Device) {
	driver.mu.Lock()
	driver.devices = append([]Device(nil), devices...)
	driver.mu.Unlock()
}

func (driver *transitionDriver) signalNextDiscovery() <-chan struct{} {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	driver.signal = make(chan struct{})
	return driver.signal
}

func (driver *transitionDriver) Devices() ([]Device, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if driver.signal != nil {
		close(driver.signal)
		driver.signal = nil
	}
	return append([]Device(nil), driver.devices...), nil
}

func (driver *transitionDriver) Open(number int) (Output, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return driver.outputs[number], nil
}

func (*transitionDriver) Close() error { return nil }

type transitionOutput struct {
	mu          sync.Mutex
	messages    [][]byte
	blockStatus byte
	entered     chan struct{}
	release     chan struct{}
	blocked     bool
	resetDone   chan struct{}
	resetOnce   sync.Once
}

func newTransitionOutput() *transitionOutput {
	return &transitionOutput{resetDone: make(chan struct{})}
}

func (output *transitionOutput) blockNext(status byte, entered, release chan struct{}) {
	output.mu.Lock()
	output.blockStatus = status
	output.entered = entered
	output.release = release
	output.blocked = false
	output.mu.Unlock()
}

func (output *transitionOutput) Send(message []byte) error {
	output.mu.Lock()
	copyMessage := append([]byte(nil), message...)
	output.messages = append(output.messages, copyMessage)
	shouldBlock := !output.blocked && output.entered != nil && message[0]&0xF0 == output.blockStatus
	if shouldBlock {
		output.blocked = true
		close(output.entered)
	}
	if len(output.messages) == 16 {
		output.resetOnce.Do(func() { close(output.resetDone) })
	}
	release := output.release
	output.mu.Unlock()
	if shouldBlock {
		<-release
	}
	return nil
}

func (*transitionOutput) Close() error { return nil }

func (output *transitionOutput) snapshot() [][]byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	result := make([][]byte, len(output.messages))
	for index, message := range output.messages {
		result[index] = append([]byte(nil), message...)
	}
	return result
}
