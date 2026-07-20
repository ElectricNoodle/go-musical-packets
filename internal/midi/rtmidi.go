//go:build cgo && (darwin || linux)

package midi

import (
	"errors"
	"fmt"
	"sync"

	"gitlab.com/gomidi/midi/v2/drivers"
	"gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

type rtDriver struct {
	mu     sync.Mutex
	driver *rtmididrv.Driver
	closed bool
}

type rtOutput struct {
	parent *rtDriver
	port   drivers.Out
	closed bool
}

// newNativeDriver constructs the crash-prone native boundary. It is called only
// inside the isolated MIDI helper process.
func newNativeDriver() (Driver, error) {
	driver, err := rtmididrv.New()
	if err != nil {
		return nil, fmt.Errorf("create RtMidi driver: %w", err)
	}
	return &rtDriver{driver: driver}, nil
}

func (d *rtDriver) Devices() ([]Device, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errors.New("MIDI driver is closed")
	}
	ports, err := d.driver.Outs()
	if err != nil {
		return nil, fmt.Errorf("list MIDI outputs: %w", err)
	}
	devices := make([]Device, 0, len(ports))
	for _, port := range ports {
		devices = append(devices, Device{Number: port.Number(), Name: port.String()})
	}
	return devices, nil
}

func (d *rtDriver) Open(number int) (Output, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, errors.New("MIDI driver is closed")
	}
	ports, err := d.driver.Outs()
	if err != nil {
		return nil, fmt.Errorf("list MIDI outputs: %w", err)
	}
	for _, port := range ports {
		if port.Number() != number {
			continue
		}
		if err := port.Open(); err != nil {
			return nil, fmt.Errorf("open MIDI output %d (%s): %w", number, port, err)
		}
		return &rtOutput{parent: d, port: port}, nil
	}
	return nil, fmt.Errorf("MIDI output number %d was not found", number)
}

func (d *rtDriver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	if err := d.driver.Close(); err != nil {
		return fmt.Errorf("close RtMidi driver: %w", err)
	}
	return nil
}

func (o *rtOutput) Send(message []byte) error {
	o.parent.mu.Lock()
	defer o.parent.mu.Unlock()
	if o.closed || o.parent.closed {
		return errors.New("MIDI output is closed")
	}
	if err := o.port.Send(message); err != nil {
		return fmt.Errorf("send MIDI message: %w", err)
	}
	return nil
}

func (o *rtOutput) Close() error {
	o.parent.mu.Lock()
	defer o.parent.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	if err := o.port.Close(); err != nil {
		return fmt.Errorf("close MIDI output: %w", err)
	}
	return nil
}
