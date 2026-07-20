// Package midi defines MIDI discovery and output boundaries.
package midi

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrNoOutputDevices   = errors.New("no MIDI output devices found")
	ErrDriverUnavailable = errors.New("native MIDI driver is unavailable in this build")
)

// Device identifies a MIDI output port.
type Device struct {
	Number int
	Name   string
}

// Driver discovers and opens MIDI output ports.
type Driver interface {
	Devices() ([]Device, error)
	Open(number int) (Output, error)
	Close() error
}

// Output immediately writes complete MIDI messages.
type Output interface {
	Send(message []byte) error
	Close() error
}

// SelectDevice applies exact name, regular expression, then first-device
// precedence. A missing exact name intentionally falls through.
func SelectDevice(devices []Device, exactName, namePattern string) (Device, error) {
	if len(devices) == 0 {
		return Device{}, ErrNoOutputDevices
	}
	if exactName != "" {
		for _, device := range devices {
			if device.Name == exactName {
				return device, nil
			}
		}
	}
	if namePattern != "" {
		pattern, err := regexp.Compile(namePattern)
		if err != nil {
			return Device{}, fmt.Errorf("compile MIDI device pattern: %w", err)
		}
		for _, device := range devices {
			if pattern.MatchString(device.Name) {
				return device, nil
			}
		}
	}
	return devices[0], nil
}

// NoteOn builds a MIDI 1.0 Note On message from a user-facing channel.
func NoteOn(channel, note, velocity uint8) ([]byte, error) {
	if err := validateChannel(channel); err != nil {
		return nil, err
	}
	if note > 127 || velocity == 0 || velocity > 127 {
		return nil, errors.New("note must be 0..127 and velocity must be 1..127")
	}
	return []byte{0x90 | (channel - 1), note, velocity}, nil
}

// NoteOff builds a MIDI 1.0 Note Off message from a user-facing channel.
func NoteOff(channel, note uint8) ([]byte, error) {
	if err := validateChannel(channel); err != nil {
		return nil, err
	}
	if note > 127 {
		return nil, errors.New("note must be between 0 and 127")
	}
	return []byte{0x80 | (channel - 1), note, 0}, nil
}

// AllNotesOff builds MIDI controller 123 for a user-facing channel.
func AllNotesOff(channel uint8) ([]byte, error) {
	if err := validateChannel(channel); err != nil {
		return nil, err
	}
	return []byte{0xB0 | (channel - 1), 123, 0}, nil
}

func validateChannel(channel uint8) error {
	if channel < 1 || channel > 16 {
		return errors.New("MIDI channel must be between 1 and 16")
	}
	return nil
}
