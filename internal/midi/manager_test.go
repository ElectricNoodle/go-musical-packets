package midi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestManagerDiscoversDisconnectsAndReconnects(t *testing.T) {
	driver := &fakeDriver{devices: []Device{{Number: 2, Name: "USB Synth"}}}
	manager, err := NewManager(ManagerConfig{
		Driver: driver, ExactDeviceName: "USB Synth", PollInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	manager.reconcile()
	device, connected := manager.Current()
	if !connected || device.Name != "USB Synth" || driver.openCount != 1 {
		t.Fatalf("initial connection = %#v, %v, opens %d", device, connected, driver.openCount)
	}
	if err := manager.Send([]byte{0x90, 60, 100}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	driver.lastOutput.sendErr = errors.New("device removed")
	if err := manager.Send([]byte{0x80, 60, 0}); err == nil {
		t.Fatal("Send() after removal error = nil")
	}
	if _, connected := manager.Current(); connected {
		t.Fatal("manager remained connected after write failure")
	}

	driver.lastOutput.sendErr = nil
	manager.reconcile()
	if _, connected := manager.Current(); !connected || driver.openCount != 2 {
		t.Fatalf("reconnected = %v, opens = %d; want true, 2", connected, driver.openCount)
	}

	driver.devices = nil
	manager.reconcile()
	if _, connected := manager.Current(); connected {
		t.Fatal("manager remained connected after device disappeared")
	}
}

func TestManagerRunHonorsCancellationAndClosesDriver(t *testing.T) {
	driver := &fakeDriver{}
	manager, err := NewManager(ManagerConfig{Driver: driver, PollInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if !driver.closed {
		t.Fatal("Run() did not close driver")
	}
	if err := manager.Run(context.Background()); err == nil {
		t.Fatal("second Run() error = nil")
	}
}

type fakeDriver struct {
	mu         sync.Mutex
	devices    []Device
	openErr    error
	openCount  int
	lastOutput *fakeOutput
	closed     bool
}

func (d *fakeDriver) Devices() ([]Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Device(nil), d.devices...), nil
}

func (d *fakeDriver) Open(int) (Output, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.openErr != nil {
		return nil, d.openErr
	}
	d.openCount++
	d.lastOutput = &fakeOutput{}
	return d.lastOutput, nil
}

func (d *fakeDriver) Close() error {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	return nil
}

type fakeOutput struct {
	mu      sync.Mutex
	sendErr error
	closed  bool
}

func (o *fakeOutput) Send([]byte) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sendErr
}

func (o *fakeOutput) Close() error {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	return nil
}
