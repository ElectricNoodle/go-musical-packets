package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestDecodeOverlaysDefaultAndConvertsRules(t *testing.T) {
	config, err := Decode(strings.NewReader(`
instance:
  id: example
capture:
  enabled: false
performance:
  flow_registry_capacity: 250
  flow_ttl: 45s
logging:
  level: debug
  format: json
rules:
  - id: web
    name: Web traffic
    enabled: true
    match:
      protocol: tcp
      destination_ports:
        minimum: 443
        maximum: 443
      required_tcp_flags: [syn, ack]
    action:
      state: play
      channel: 4
      mode: dorian
      root: 2
`))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if config.Instance.ID != "example" || config.Capture.Enabled {
		t.Fatalf("Decode() overrides = %#v, %#v", config.Instance, config.Capture)
	}
	if config.Mapping.Seed != Default().Mapping.Seed || config.Performance.PacketQueueCapacity != Default().Performance.PacketQueueCapacity {
		t.Fatal("Decode() did not retain unspecified defaults")
	}
	if config.Performance.FlowRegistryCapacity != 250 || config.Performance.FlowTTL != 45*time.Second {
		t.Fatalf("Decode() registry settings = %#v", config.Performance)
	}
	if config.Logging.Level != LogLevelDebug || config.Logging.Format != LogFormatJSON {
		t.Fatalf("Decode() logging = %#v", config.Logging)
	}

	rules, err := config.FlowRules()
	if err != nil {
		t.Fatalf("FlowRules() error = %v", err)
	}
	if len(rules) != 1 || rules[0].ID != "web" || rules[0].Action != (flow.Action{State: flow.StatePlay, Channel: 4, Mode: "dorian", Root: 2}) {
		t.Fatalf("converted rules = %#v", rules)
	}
	wantFlags := packet.TCPFlagSYN | packet.TCPFlagACK
	if rules[0].Match.RequiredTCPFlags != wantFlags {
		t.Fatalf("required TCP flags = %b, want %b", rules[0].Match.RequiredTCPFlags, wantFlags)
	}
}

func TestDecodeAcceptsEmptyInputAsDefault(t *testing.T) {
	got, err := Decode(strings.NewReader("# use defaults\n"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	want := Default()
	if got.Instance != want.Instance || got.Performance != want.Performance || got.Logging != want.Logging {
		t.Fatalf("Decode() = %#v, want defaults", got)
	}
}

func TestDecodeRejectsNonStrictOrInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "unknown top-level field", yaml: "unknown: true\n", want: "field unknown not found"},
		{name: "unknown nested field", yaml: "logging:\n  colour: true\n", want: "field colour not found"},
		{name: "unknown rule field", yaml: "rules:\n  - id: one\n    enabled: true\n    match:\n      source_address: 192.0.2.1\n    action:\n      state: monitor\n", want: "field source_address not found"},
		{name: "duplicate top-level field", yaml: "metrics:\n  namespace: first\nmetrics:\n  namespace: second\n", want: "mapping key \"metrics\" already defined"},
		{name: "duplicate nested field", yaml: "logging:\n  level: info\n  level: debug\n", want: "mapping key \"level\" already defined"},
		{name: "trailing document", yaml: "logging:\n  level: info\n---\nlogging:\n  level: debug\n", want: "multiple YAML documents"},
		{name: "invalid value", yaml: "performance:\n  flow_ttl: 0s\n", want: "performance.flow_ttl"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Decode() error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestDecodeRejectsNilReader(t *testing.T) {
	if _, err := Decode(nil); err == nil || !strings.Contains(err.Error(), "reader is nil") {
		t.Fatalf("Decode(nil) error = %v", err)
	}
}

func TestEncodeReturnsValidatedDeterministicFullYAML(t *testing.T) {
	configuration := Default()
	configuration.Instance.ID = "encoded"
	configuration.Rules = RulesConfig{
		{ID: "first", Name: "First", Enabled: true, Action: RuleActionConfig{State: FlowPlay, Channel: 1}},
		{ID: "second", Name: "Second", Enabled: true, Action: RuleActionConfig{State: FlowMonitor, Channel: 2}},
	}

	first, err := Encode(configuration)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	second, err := Encode(configuration)
	if err != nil {
		t.Fatalf("second Encode() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("Encode() is not deterministic:\n%s\n%s", first, second)
	}
	if !bytes.HasSuffix(first, []byte("\n")) || bytes.Contains(first, []byte("---")) {
		t.Fatalf("Encode() output must be exactly one document with a trailing newline:\n%s", first)
	}
	if !bytes.Contains(first, []byte("instance:\n  id: encoded\n  role: standalone\n")) {
		t.Fatalf("Encode() output does not use the canonical two-space indentation:\n%s", first)
	}
	for _, duration := range []string{"minimum_duration: 50ms", "maximum_duration: 2s", "flow_ttl: 5m0s"} {
		if !bytes.Contains(first, []byte(duration)) {
			t.Errorf("Encode() output does not contain human-readable %q", duration)
		}
	}
	firstRule := bytes.Index(first, []byte("id: first"))
	secondRule := bytes.Index(first, []byte("id: second"))
	if firstRule < 0 || secondRule < 0 || firstRule >= secondRule {
		t.Fatalf("Encode() changed configured rule order:\n%s", first)
	}
	for _, field := range []string{"instance:", "capture:", "mapping:", "performance:", "midi:", "server:", "peer:", "metrics:", "logging:", "rules:"} {
		if !bytes.Contains(first, []byte(field)) {
			t.Errorf("Encode() output does not contain %q", field)
		}
	}
	decoded, err := Decode(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("Decode(Encode()) error = %v", err)
	}
	if decoded.Instance.ID != configuration.Instance.ID || decoded.Mapping != configuration.Mapping {
		t.Fatalf("Decode(Encode()) = %#v, want encoded configuration", decoded)
	}

	invalid := configuration
	invalid.Instance.ID = ""
	if _, err := Encode(invalid); err == nil || !strings.Contains(err.Error(), "instance.id") {
		t.Fatalf("Encode(invalid) error = %v, want validation error", err)
	}
}

func TestLoadReadsAndValidatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("instance:\n  id: from-file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	config, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.Instance.ID != "from-file" {
		t.Fatalf("Load().Instance.ID = %q", config.Instance.ID)
	}
}

func TestLoadReportsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Load() error = %v, want path", err)
	}
}
