// Package config defines runtime configuration defaults and invariants.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Role selects which runtime components are composed.
type Role string

const (
	RoleStandalone Role = "standalone"
	RoleEdge       Role = "edge"
	RoleHost       Role = "host"
)

// FlowState is the default action for traffic without an explicit rule.
type FlowState string

const (
	FlowIgnore  FlowState = "ignore"
	FlowMonitor FlowState = "monitor"
	FlowPlay    FlowState = "play"
)

// Config is the strictly validated application configuration model.
type Config struct {
	Instance    InstanceConfig    `json:"instance" yaml:"instance"`
	Capture     CaptureConfig     `json:"capture" yaml:"capture"`
	Mapping     MappingConfig     `json:"mapping" yaml:"mapping"`
	Performance PerformanceConfig `json:"performance" yaml:"performance"`
	MIDI        MIDIConfig        `json:"midi" yaml:"midi"`
	Server      ServerConfig      `json:"server" yaml:"server"`
	Peer        PeerConfig        `json:"peer" yaml:"peer"`
	Metrics     MetricsConfig     `json:"metrics" yaml:"metrics"`
	Logging     LoggingConfig     `json:"logging" yaml:"logging"`
	Rules       RulesConfig       `json:"rules" yaml:"rules"`
}

type InstanceConfig struct {
	ID   string `json:"id" yaml:"id"`
	Role Role   `json:"role" yaml:"role"`
}

type CaptureConfig struct {
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	Interface      string `json:"interface" yaml:"interface"`
	BPF            string `json:"bpf" yaml:"bpf"`
	SnapshotLength int    `json:"snapshot_length" yaml:"snapshot_length"`
	Promiscuous    bool   `json:"promiscuous" yaml:"promiscuous"`
}

type MappingConfig struct {
	Version         string        `json:"version" yaml:"version"`
	Seed            string        `json:"seed" yaml:"seed"`
	DefaultState    FlowState     `json:"default_state" yaml:"default_state"`
	DefaultChannel  uint8         `json:"default_channel" yaml:"default_channel"`
	MinimumNote     uint8         `json:"minimum_note" yaml:"minimum_note"`
	MaximumNote     uint8         `json:"maximum_note" yaml:"maximum_note"`
	MinimumDuration time.Duration `json:"minimum_duration" yaml:"minimum_duration"`
	MaximumDuration time.Duration `json:"maximum_duration" yaml:"maximum_duration"`
}

type PerformanceConfig struct {
	PacketQueueCapacity      int           `json:"packet_queue_capacity" yaml:"packet_queue_capacity"`
	NoteQueueCapacity        int           `json:"note_queue_capacity" yaml:"note_queue_capacity"`
	UIQueueCapacity          int           `json:"ui_queue_capacity" yaml:"ui_queue_capacity"`
	FlowRegistryCapacity     int           `json:"flow_registry_capacity" yaml:"flow_registry_capacity"`
	FlowTTL                  time.Duration `json:"flow_ttl" yaml:"flow_ttl"`
	MaximumNotesPerSecond    int           `json:"maximum_notes_per_second" yaml:"maximum_notes_per_second"`
	MaximumPolyphony         int           `json:"maximum_polyphony" yaml:"maximum_polyphony"`
	MinimumRetriggerInterval time.Duration `json:"minimum_retrigger_interval" yaml:"minimum_retrigger_interval"`
}

type MIDIConfig struct {
	Enabled          bool          `json:"enabled" yaml:"enabled"`
	ExactDeviceName  string        `json:"exact_device_name" yaml:"exact_device_name"`
	DeviceNameRegexp string        `json:"device_name_regexp" yaml:"device_name_regexp"`
	PollInterval     time.Duration `json:"poll_interval" yaml:"poll_interval"`
}

type ServerConfig struct {
	ListenAddress string        `json:"listen_address" yaml:"listen_address"`
	ReadTimeout   time.Duration `json:"read_timeout" yaml:"read_timeout"`
	WriteTimeout  time.Duration `json:"write_timeout" yaml:"write_timeout"`
}

type PeerConfig struct {
	Enabled        bool          `json:"enabled" yaml:"enabled"`
	URL            string        `json:"url" yaml:"url"`
	ReconnectBase  time.Duration `json:"reconnect_base" yaml:"reconnect_base"`
	ReconnectLimit time.Duration `json:"reconnect_limit" yaml:"reconnect_limit"`
	StaleAfter     time.Duration `json:"stale_after" yaml:"stale_after"`
}

type MetricsConfig struct {
	Namespace string `json:"namespace" yaml:"namespace"`
}

// LogLevel controls the minimum severity emitted by the application logger.
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LogFormat controls the encoding used by the application logger.
type LogFormat string

const (
	LogFormatText LogFormat = "text"
	LogFormatJSON LogFormat = "json"
)

type LoggingConfig struct {
	Level  LogLevel  `json:"level" yaml:"level"`
	Format LogFormat `json:"format" yaml:"format"`
}

// Default returns a safe, quiet standalone configuration. Unmatched traffic is
// monitored but does not produce notes.
func Default() Config {
	return Config{
		Instance: InstanceConfig{ID: "musical-packets", Role: RoleStandalone},
		Capture: CaptureConfig{
			Enabled:        true,
			Interface:      "auto",
			SnapshotLength: 65535,
			Promiscuous:    true,
		},
		Mapping: MappingConfig{
			Version:         "flow-mode-v1",
			Seed:            "musical-packets",
			DefaultState:    FlowMonitor,
			DefaultChannel:  1,
			MinimumNote:     36,
			MaximumNote:     96,
			MinimumDuration: 50 * time.Millisecond,
			MaximumDuration: 2 * time.Second,
		},
		Performance: PerformanceConfig{
			PacketQueueCapacity:      4096,
			NoteQueueCapacity:        1024,
			UIQueueCapacity:          512,
			FlowRegistryCapacity:     10_000,
			FlowTTL:                  5 * time.Minute,
			MaximumNotesPerSecond:    100,
			MaximumPolyphony:         32,
			MinimumRetriggerInterval: 10 * time.Millisecond,
		},
		MIDI: MIDIConfig{
			Enabled:      true,
			PollInterval: 2 * time.Second,
		},
		Server: ServerConfig{
			ListenAddress: "127.0.0.1:8080",
			ReadTimeout:   10 * time.Second,
			WriteTimeout:  10 * time.Second,
		},
		Peer: PeerConfig{
			ReconnectBase:  500 * time.Millisecond,
			ReconnectLimit: 30 * time.Second,
			StaleAfter:     500 * time.Millisecond,
		},
		Metrics: MetricsConfig{Namespace: "musical_packets"},
		Logging: LoggingConfig{Level: LogLevelInfo, Format: LogFormatText},
	}
}

// Validate reports all known configuration problems in one error.
func (c Config) Validate() error {
	var problems []error

	if strings.TrimSpace(c.Instance.ID) == "" {
		problems = append(problems, errors.New("instance.id is required"))
	}
	switch c.Instance.Role {
	case RoleStandalone, RoleEdge, RoleHost:
	default:
		problems = append(problems, fmt.Errorf("instance.role %q is invalid", c.Instance.Role))
	}

	if c.Capture.Enabled {
		if strings.TrimSpace(c.Capture.Interface) == "" {
			problems = append(problems, errors.New("capture.interface is required when capture is enabled"))
		}
		if c.Capture.SnapshotLength < 64 || c.Capture.SnapshotLength > 65535 {
			problems = append(problems, errors.New("capture.snapshot_length must be between 64 and 65535"))
		}
	}

	switch c.Mapping.DefaultState {
	case FlowIgnore, FlowMonitor, FlowPlay:
	default:
		problems = append(problems, fmt.Errorf("mapping.default_state %q is invalid", c.Mapping.DefaultState))
	}
	if c.Mapping.Version != "flow-mode-v1" {
		problems = append(problems, fmt.Errorf("mapping.version %q is unsupported", c.Mapping.Version))
	}
	if c.Mapping.Seed == "" {
		problems = append(problems, errors.New("mapping.seed is required"))
	}
	if c.Mapping.DefaultChannel < 1 || c.Mapping.DefaultChannel > 16 {
		problems = append(problems, errors.New("mapping.default_channel must be between 1 and 16"))
	}
	if c.Mapping.MinimumNote > 127 || c.Mapping.MaximumNote > 127 || c.Mapping.MinimumNote > c.Mapping.MaximumNote {
		problems = append(problems, errors.New("mapping note range must be ordered within 0 through 127"))
	}
	if c.Mapping.MinimumDuration <= 0 || c.Mapping.MaximumDuration < c.Mapping.MinimumDuration {
		problems = append(problems, errors.New("mapping duration range must be positive and ordered"))
	}

	if c.Performance.PacketQueueCapacity <= 0 || c.Performance.NoteQueueCapacity <= 0 || c.Performance.UIQueueCapacity <= 0 {
		problems = append(problems, errors.New("performance queue capacities must be positive"))
	}
	if c.Performance.FlowRegistryCapacity <= 0 {
		problems = append(problems, errors.New("performance.flow_registry_capacity must be positive"))
	}
	if c.Performance.FlowTTL <= 0 {
		problems = append(problems, errors.New("performance.flow_ttl must be positive"))
	}
	if c.Performance.MaximumNotesPerSecond <= 0 {
		problems = append(problems, errors.New("performance.maximum_notes_per_second must be positive"))
	}
	if c.Performance.MaximumPolyphony < 1 || c.Performance.MaximumPolyphony > 128 {
		problems = append(problems, errors.New("performance.maximum_polyphony must be between 1 and 128"))
	}
	if c.Performance.MinimumRetriggerInterval < 0 {
		problems = append(problems, errors.New("performance.minimum_retrigger_interval must not be negative"))
	}

	if c.MIDI.Enabled {
		if c.MIDI.PollInterval <= 0 {
			problems = append(problems, errors.New("midi.poll_interval must be positive"))
		}
		if c.MIDI.DeviceNameRegexp != "" {
			if _, err := regexp.Compile(c.MIDI.DeviceNameRegexp); err != nil {
				problems = append(problems, fmt.Errorf("midi.device_name_regexp: %w", err))
			}
		}
	}

	if _, _, err := net.SplitHostPort(c.Server.ListenAddress); err != nil {
		problems = append(problems, fmt.Errorf("server.listen_address: %w", err))
	}
	if c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 {
		problems = append(problems, errors.New("server timeouts must be positive"))
	}

	if c.Instance.Role == RoleEdge && !c.Peer.Enabled {
		problems = append(problems, errors.New("peer must be enabled for edge role"))
	}
	if c.Peer.Enabled {
		parsed, err := url.Parse(c.Peer.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
			problems = append(problems, errors.New("peer.url must be an absolute ws:// or wss:// URL"))
		}
		if c.Peer.ReconnectBase <= 0 || c.Peer.ReconnectLimit < c.Peer.ReconnectBase {
			problems = append(problems, errors.New("peer reconnect durations must be positive and ordered"))
		}
		if c.Peer.StaleAfter <= 0 {
			problems = append(problems, errors.New("peer.stale_after must be positive"))
		}
	}

	if !validMetricName(c.Metrics.Namespace) {
		problems = append(problems, errors.New("metrics.namespace must match [a-zA-Z_:][a-zA-Z0-9_:]*"))
	}

	switch c.Logging.Level {
	case LogLevelDebug, LogLevelInfo, LogLevelWarn, LogLevelError:
	default:
		problems = append(problems, fmt.Errorf("logging.level %q is invalid", c.Logging.Level))
	}
	switch c.Logging.Format {
	case LogFormatText, LogFormatJSON:
	default:
		problems = append(problems, fmt.Errorf("logging.format %q is invalid", c.Logging.Format))
	}

	if _, err := c.FlowRules(); err != nil {
		problems = append(problems, err)
	}

	return errors.Join(problems...)
}

var metricNamePattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

func validMetricName(value string) bool {
	return metricNamePattern.MatchString(value)
}
