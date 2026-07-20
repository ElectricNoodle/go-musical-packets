package midi

import (
	"errors"
	"testing"
)

func TestSelectDevicePrecedence(t *testing.T) {
	devices := []Device{{Number: 1, Name: "Piano"}, {Number: 2, Name: "USB Strings"}}

	exact, err := SelectDevice(devices, "Piano", "Strings")
	if err != nil || exact.Number != 1 {
		t.Fatalf("SelectDevice(exact) = %#v, %v", exact, err)
	}
	pattern, err := SelectDevice(devices, "Missing", `USB .*`)
	if err != nil || pattern.Number != 2 {
		t.Fatalf("SelectDevice(pattern) = %#v, %v", pattern, err)
	}
	first, err := SelectDevice(devices, "Missing", "")
	if err != nil || first.Number != 1 {
		t.Fatalf("SelectDevice(first) = %#v, %v", first, err)
	}
}

func TestSelectDeviceNoDevices(t *testing.T) {
	_, err := SelectDevice(nil, "", "")
	if !errors.Is(err, ErrNoOutputDevices) {
		t.Fatalf("SelectDevice() error = %v", err)
	}
}

func TestMessagesUseHumanFacingChannels(t *testing.T) {
	noteOn, err := NoteOn(1, 60, 100)
	if err != nil {
		t.Fatalf("NoteOn() error = %v", err)
	}
	if noteOn[0] != 0x90 {
		t.Fatalf("NoteOn(1) status = %#x, want 0x90", noteOn[0])
	}
	noteOff, err := NoteOff(16, 60)
	if err != nil {
		t.Fatalf("NoteOff() error = %v", err)
	}
	if noteOff[0] != 0x8F {
		t.Fatalf("NoteOff(16) status = %#x, want 0x8F", noteOff[0])
	}
	panicMessage, err := AllNotesOff(3)
	if err != nil {
		t.Fatalf("AllNotesOff() error = %v", err)
	}
	if panicMessage[0] != 0xB2 || panicMessage[1] != 123 {
		t.Fatalf("AllNotesOff(3) = % X", panicMessage)
	}
}

func TestMessagesRejectInvalidChannel(t *testing.T) {
	if _, err := NoteOn(0, 60, 100); err == nil {
		t.Fatal("NoteOn(0) error = nil")
	}
	if _, err := NoteOff(17, 60); err == nil {
		t.Fatal("NoteOff(17) error = nil")
	}
}
