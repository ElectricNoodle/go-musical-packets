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
	select {
	case <-manager.Ready():
	default:
		t.Fatal("Ready remained open after startup was canceled")
	}
	if err := manager.Run(context.Background()); err == nil {
		t.Fatal("second Run() error = nil")
	}
}

func TestManagerReadyAfterInitialUnavailableDiscovery(t *testing.T) {
	driver := &fakeDriver{}
	manager, err := NewManager(ManagerConfig{Driver: driver, PollInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()

	select {
	case <-manager.Ready():
	case <-time.After(time.Second):
		t.Fatal("manager did not report initial discovery completion")
	}
	if _, connected := manager.Current(); connected {
		t.Fatal("manager connected without an available device")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestManagerSnapshotIsDetachedAndRetainsCacheAfterDiscoveryError(t *testing.T) {
	driver := &fakeDriver{devices: []Device{{Number: 2, Name: "USB Synth"}}}
	manager, err := NewManager(ManagerConfig{Driver: driver, PollInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.reconcile(); err != nil {
		t.Fatalf("initial reconcile() error = %v", err)
	}

	snapshot := manager.Snapshot()
	if !snapshot.DiscoveryOK || !snapshot.Connected || len(snapshot.Devices) != 1 {
		t.Fatalf("initial Snapshot() = %#v", snapshot)
	}
	snapshot.Devices[0].Name = "mutated"
	if got := manager.Snapshot().Devices[0].Name; got != "USB Synth" {
		t.Fatalf("mutating snapshot changed manager device name to %q", got)
	}

	driver.mu.Lock()
	driver.devicesErr = errors.New("discovery failed")
	driver.mu.Unlock()
	if err := manager.reconcile(); err == nil {
		t.Fatal("reconcile() discovery error = nil")
	}
	stale := manager.Snapshot()
	if stale.DiscoveryOK || !stale.Connected || len(stale.Devices) != 1 || stale.Devices[0].Name != "USB Synth" {
		t.Fatalf("Snapshot() after discovery error = %#v, want connected stale cache", stale)
	}
}

func TestManagerDoesNotPublishOutputThatFailsInitialReset(t *testing.T) {
	resetErr := errors.New("reset failed")
	driver := &fakeDriver{
		devices:       []Device{{Number: 3, Name: "broken synth"}},
		outputSendErr: resetErr,
	}
	manager, err := NewManager(ManagerConfig{Driver: driver, PollInterval: time.Hour})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.reconcile(); !errors.Is(err, resetErr) {
		t.Fatalf("reconcile() error = %v, want reset failure", err)
	}
	if _, connected := manager.Current(); connected {
		t.Fatal("manager published output whose initial reset failed")
	}
	if driver.lastOutput == nil || !driver.lastOutput.closed {
		t.Fatal("manager did not close output whose initial reset failed")
	}
}

type fakeDriver struct {
	mu            sync.Mutex
	devices       []Device
	devicesErr    error
	openErr       error
	outputSendErr error
	openCount     int
	lastOutput    *fakeOutput
	closed        bool
}

func (d *fakeDriver) Devices() ([]Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]Device(nil), d.devices...), d.devicesErr
}

func (d *fakeDriver) Open(int) (Output, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.openErr != nil {
		return nil, d.openErr
	}
	d.openCount++
	d.lastOutput = &fakeOutput{sendErr: d.outputSendErr}
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
