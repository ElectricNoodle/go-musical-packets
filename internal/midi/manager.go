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
	mu                sync.Mutex
	driver            Driver
	exactDeviceName   string
	deviceNamePattern string
	pollInterval      time.Duration
	observer          ManagerObserver
	output            Output
	device            Device
	running           atomic.Bool
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
	}
	manager.observer.DeviceState("disconnected")
	return manager, nil
}

// Run performs immediate discovery, polls until cancellation, then closes the
// selected output and driver. It may be called once.
func (m *Manager) Run(ctx context.Context) (runErr error) {
	if !m.running.CompareAndSwap(false, true) {
		return errors.New("MIDI manager may only run once")
	}
	defer func() {
		m.mu.Lock()
		m.disconnectLocked()
		m.mu.Unlock()
		runErr = errors.Join(runErr, m.driver.Close())
	}()

	if err := ctx.Err(); err != nil {
		return err
	}
	m.reconcile()
	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.reconcile()
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

func (m *Manager) reconcile() {
	devices, err := m.driver.Devices()
	if err != nil {
		m.observer.DeviceError("discover")
		m.observer.Reconnect("discovery_error")
		return
	}
	selected, err := SelectDevice(devices, m.exactDeviceName, m.deviceNamePattern)
	if err != nil {
		m.mu.Lock()
		m.disconnectLocked()
		m.mu.Unlock()
		if !errors.Is(err, ErrNoOutputDevices) {
			m.observer.DeviceError("select")
		}
		m.observer.Reconnect("unavailable")
		return
	}

	m.mu.Lock()
	if m.output != nil && m.device == selected {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	output, err := m.driver.Open(selected.Number)
	if err != nil {
		m.observer.DeviceError("open")
		m.observer.Reconnect("open_error")
		return
	}

	m.mu.Lock()
	previous := m.output
	m.output = output
	m.device = selected
	m.mu.Unlock()
	if previous != nil {
		if err := previous.Close(); err != nil {
			m.observer.DeviceError("close")
		}
	}
	m.observer.DeviceState("connected")
	m.observer.Reconnect("success")
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
