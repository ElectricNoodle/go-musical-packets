package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValidAndQuiet(t *testing.T) {
	config := Default()
	if err := config.Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
	if config.Mapping.DefaultState != FlowMonitor {
		t.Fatalf("default flow state = %q, want %q", config.Mapping.DefaultState, FlowMonitor)
	}
	if config.Performance.FlowRegistryCapacity <= 0 || config.Performance.FlowTTL <= 0 {
		t.Fatalf("default flow registry settings = %d, %v, want positive values", config.Performance.FlowRegistryCapacity, config.Performance.FlowTTL)
	}
	if config.Logging.Level != LogLevelInfo || config.Logging.Format != LogFormatText {
		t.Fatalf("default logging = %#v, want info text", config.Logging)
	}
}

func TestValidateReportsMultipleProblems(t *testing.T) {
	config := Default()
	config.Instance.ID = ""
	config.Mapping.DefaultChannel = 17
	config.Performance.NoteQueueCapacity = 0

	err := config.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, want := range []string{"instance.id", "default_channel", "queue capacities"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestEdgeRequiresValidPeer(t *testing.T) {
	config := Default()
	config.Instance.Role = RoleEdge

	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "peer must be enabled") {
		t.Fatalf("Validate() error = %v, want peer requirement", err)
	}

	config.Peer.Enabled = true
	config.Peer.URL = "wss://host.example/v1/notes"
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsBadDeviceRegexp(t *testing.T) {
	config := Default()
	config.MIDI.DeviceNameRegexp = "["
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "device_name_regexp") {
		t.Fatalf("Validate() error = %v, want regexp error", err)
	}
}

func TestValidateRejectsNegativeRetriggerInterval(t *testing.T) {
	config := Default()
	config.Performance.MinimumRetriggerInterval = -time.Millisecond
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "minimum_retrigger_interval") {
		t.Fatalf("Validate() error = %v, want retrigger interval error", err)
	}
}

func TestValidateRejectsInvalidFlowRegistrySettings(t *testing.T) {
	config := Default()
	config.Performance.FlowRegistryCapacity = 0
	config.Performance.FlowTTL = 0

	err := config.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, want := range []string{"flow_registry_capacity", "flow_ttl"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestValidateRejectsInvalidLogging(t *testing.T) {
	config := Default()
	config.Logging.Level = "verbose"
	config.Logging.Format = "xml"

	err := config.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, want := range []string{"logging.level", "logging.format"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %q, want it to contain %q", err, want)
		}
	}
}

func TestValidateDisabledCaptureAndMIDIIgnoreInactiveSettings(t *testing.T) {
	config := Default()
	config.Capture.Enabled = false
	config.Capture.Interface = ""
	config.Capture.SnapshotLength = 0
	config.MIDI.Enabled = false
	config.MIDI.PollInterval = 0
	config.MIDI.DeviceNameRegexp = "["

	if err := config.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want disabled component settings ignored", err)
	}
}
