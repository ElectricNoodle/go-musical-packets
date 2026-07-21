package midi

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

var ErrOutputUnavailable = errors.New("MIDI output is unavailable")

var ErrManagerStopped = errors.New("MIDI manager has stopped")

// ManagerObserver receives device lifecycle events with bounded result values.
type ManagerObserver interface {
	DeviceState(state string)
	Reconnect(result string)
	DeviceError(operation string)
}

// ManagerConfig controls MIDI output selection and polling.
type ManagerConfig struct {
	Driver            Driver
	ExactDeviceName   string
	DeviceNamePattern string
	PollInterval      time.Duration
	Observer          ManagerObserver
}

// Manager discovers, selects, and reconnects one preferred MIDI output. It is
// safe to use as a Scheduler Sender while Run polls in another goroutine.
type Manager struct {
	reconcileMu       sync.Mutex
	mu                sync.Mutex
	driver            Driver
	exactDeviceName   string
	deviceNamePattern string
	pollInterval      time.Duration
	observer          ManagerObserver
	output            Output
	device            Device
	devices           []Device
	discoveryOK       bool
	coordination      *operationGate
	transition        func(Output, Device) error
	running           atomic.Bool
	stopped           atomic.Bool
	ready             chan struct{}
	readyOnce         sync.Once
}

// ManagerSnapshot is a detached view of the most recent device discovery and
// selected output state.
type ManagerSnapshot struct {
	Devices     []Device
	Current     Device
	Connected   bool
	DiscoveryOK bool
}

type noopManagerObserver struct{}

func (noopManagerObserver) DeviceState(string) {}
func (noopManagerObserver) Reconnect(string)   {}
func (noopManagerObserver) DeviceError(string) {}

// NewManager validates configuration and creates a disconnected manager.
func NewManager(config ManagerConfig) (*Manager, error) {
	if config.Driver == nil {
		return nil, errors.New("MIDI driver is required")
	}
	if config.PollInterval <= 0 {
		return nil, errors.New("MIDI poll interval must be positive")
	}
	if config.DeviceNamePattern != "" {
		if _, err := regexp.Compile(config.DeviceNamePattern); err != nil {
			return nil, fmt.Errorf("compile MIDI device pattern: %w", err)
		}
	}
	if config.Observer == nil {
		config.Observer = noopManagerObserver{}
	}
	manager := &Manager{
		driver:            config.Driver,
		exactDeviceName:   config.ExactDeviceName,
		deviceNamePattern: config.DeviceNamePattern,
		pollInterval:      config.PollInterval,
		observer:          config.Observer,
		coordination:      newOperationGate(),
		ready:             make(chan struct{}),
	}
	manager.observer.DeviceState("disconnected")
	return manager, nil
}

// Ready is closed after the first device-discovery attempt completes, or when
// Run terminates before an attempt can begin. A lack of devices or a discovery
// failure still completes startup; callers can use Current to distinguish
// connected and disconnected states.
func (m *Manager) Ready() <-chan struct{} { return m.ready }

// Run performs immediate discovery, polls until cancellation, then closes the
// selected output and driver. It may be called once.
func (m *Manager) Run(ctx context.Context) (runErr error) {
	if !m.running.CompareAndSwap(false, true) {
		return errors.New("MIDI manager may only run once")
	}
	defer func() {
		m.stopped.Store(true)
		m.readyOnce.Do(func() { close(m.ready) })
		m.reconcileMu.Lock()
		defer m.reconcileMu.Unlock()
		_ = m.coordination.acquire(context.Background())
		m.replaceOutputCoordinated(nil, Device{})
		m.coordination.release()
		runErr = errors.Join(runErr, m.driver.Close())
	}()

	if err := ctx.Err(); err != nil {
		return err
	}
	_ = m.reconcile()
	m.readyOnce.Do(func() { close(m.ready) })
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = m.reconcile()
		}
	}
}

// Send forwards a MIDI message or marks a failed output disconnected so the
// next poll can reopen it.
func (m *Manager) Send(message []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.output == nil {
		return ErrOutputUnavailable
	}
	if err := m.output.Send(message); err != nil {
		m.observer.DeviceError("send")
		m.disconnectLocked()
		return fmt.Errorf("send to MIDI device: %w", err)
	}
	return nil
}

// Current reports the selected device and whether its output is open.
func (m *Manager) Current() (Device, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.device, m.output != nil
}

// Snapshot returns the most recent discovered device list and connection
// state. Its device slice may be mutated by the caller.
func (m *Manager) Snapshot() ManagerSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	devices := append([]Device(nil), m.devices...)
	if devices == nil {
		devices = make([]Device, 0)
	}
	return ManagerSnapshot{
		Devices:     devices,
		Current:     m.device,
		Connected:   m.output != nil,
		DiscoveryOK: m.discoveryOK,
	}
}

func (m *Manager) reconcile() error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	if m.stopped.Load() {
		return ErrManagerStopped
	}
	return m.reconcileLocked()
}

func (m *Manager) reconcileLocked() error {
	devices, err := m.driver.Devices()
	if err != nil {
		m.mu.Lock()
		m.discoveryOK = false
		m.mu.Unlock()
		m.observer.DeviceError("discover")
		m.observer.Reconnect("discovery_error")
		return fmt.Errorf("discover MIDI devices: %w", err)
	}
	m.mu.Lock()
	m.devices = append(m.devices[:0], devices...)
	m.discoveryOK = true
	m.mu.Unlock()
	selected, err := SelectDevice(devices, m.exactDeviceName, m.deviceNamePattern)
	if err != nil {
		var transitionErr error
		if _, connected := m.Current(); connected {
			transitionErr = m.commitTransition(nil, Device{})
		}
		if !errors.Is(err, ErrNoOutputDevices) {
			m.observer.DeviceError("select")
		}
		m.observer.Reconnect("unavailable")
		return errors.Join(err, transitionErr)
	}

	m.mu.Lock()
	if m.output != nil && m.device == selected {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	output, err := m.driver.Open(selected.Number)
	if err != nil {
		m.observer.DeviceError("open")
		m.observer.Reconnect("open_error")
		return fmt.Errorf("open MIDI output: %w", err)
	}
	if err := resetOutput(output); err != nil {
		m.observer.DeviceError("reset")
		m.observer.Reconnect("reset_error")
		return errors.Join(fmt.Errorf("reset MIDI output: %w", err), output.Close())
	}
	if err := m.commitTransition(output, selected); err != nil {
		if _, connected := m.Current(); connected {
			m.observer.DeviceState("connected")
		}
		m.observer.Reconnect("transition_error")
		return err
	}
	m.observer.DeviceState("connected")
	m.observer.Reconnect("success")
	return nil
}

func (m *Manager) commitTransition(output Output, device Device) error {
	if err := m.coordination.acquire(context.Background()); err != nil {
		if output != nil {
			_ = output.Close()
		}
		return err
	}
	defer m.coordination.release()
	if m.transition != nil {
		return m.transition(output, device)
	}
	return m.replaceOutputCoordinated(output, device)
}

func (m *Manager) replaceOutputCoordinated(output Output, device Device) error {
	m.mu.Lock()
	previous := m.output
	m.output = output
	m.device = device
	m.mu.Unlock()
	if previous == nil {
		return nil
	}
	if err := previous.Close(); err != nil {
		m.observer.DeviceError("close")
	}
	if output == nil {
		m.observer.DeviceState("disconnected")
	}
	return nil
}

func resetOutput(output Output) error {
	for channel := uint8(1); channel <= 16; channel++ {
		message, _ := AllNotesOff(channel)
		if err := output.Send(message); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) disconnectLocked() {
	if m.output == nil {
		return
	}
	if err := m.output.Close(); err != nil {
		m.observer.DeviceError("close")
	}
	m.output = nil
	m.device = Device{}
	m.observer.DeviceState("disconnected")
}
