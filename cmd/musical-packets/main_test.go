package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"version"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if got := stdout.String(); !strings.Contains(got, "musical-packets") {
		t.Fatalf("run() output = %q, want version output", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("run() stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"unknown"}, &stdout, &stderr)

	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("run() stderr = %q, want error", stderr.String())
	}
}

func TestRunHelpListsDiscoveryCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	for _, command := range []string{"run", "replay", "validate-config", "interfaces", "devices"} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("help output = %q, want %q", stdout.String(), command)
		}
	}
}

func TestRunReplayAcceptsRecordingBeforeConfig(t *testing.T) {
	recordingPath := writeEmptyPCAP(t)
	configurationPath := writeConfiguration(t, "midi:\n  enabled: false\ncapture:\n  bpf: \"\"\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"replay", recordingPath, "--config", configurationPath}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
}

func TestReplayPathsAcceptsConfigBeforeRecording(t *testing.T) {
	var stdout, stderr bytes.Buffer
	recordingPath, configurationPath, code := replayPaths(
		[]string{"--config", "config.yaml", "recording.pcap"},
		&stdout,
		&stderr,
	)
	if code != -1 {
		t.Fatalf("replayPaths() code = %d, want -1; stderr = %q", code, stderr.String())
	}
	if recordingPath != "recording.pcap" || configurationPath != "config.yaml" {
		t.Fatalf("replayPaths() = (%q, %q), want recording and config paths", recordingPath, configurationPath)
	}
}

func TestReplayPathsAcceptsDashPrefixedRecordingAfterTerminator(t *testing.T) {
	var stdout, stderr bytes.Buffer
	recordingPath, configurationPath, code := replayPaths(
		[]string{"--config=config.yaml", "--", "-recording.pcap"},
		&stdout,
		&stderr,
	)
	if code != -1 || recordingPath != "-recording.pcap" || configurationPath != "config.yaml" {
		t.Fatalf("replayPaths() = (%q, %q, %d), want dash-prefixed recording and config; stderr = %q", recordingPath, configurationPath, code, stderr.String())
	}
}

func TestRunReplayRequiresOneRecordingAndConfig(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "recording", args: []string{"replay", "--config", "config.yaml"}, want: "recording path is required"},
		{name: "config", args: []string{"replay", "recording.pcap"}, want: "--config is required"},
		{name: "blank recording", args: []string{"replay", " ", "--config", "config.yaml"}, want: "recording path is required"},
		{name: "extra recording", args: []string{"replay", "one.pcap", "two.pcap", "--config", "config.yaml"}, want: "unexpected arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != 2 {
				t.Fatalf("run() code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("run() stderr = %q, want %q", stderr.String(), test.want)
			}
		})
	}
}

func TestRunReplayHelpUsesStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"replay", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "replay <recording.pcap> --config") {
		t.Fatalf("run() stdout = %q, want replay usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("run() stderr = %q, want empty", stderr.String())
	}
}

func TestRunReplayRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"replay", "recording.pcap", "--config", "config.yaml", "--fast"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown flag "--fast"`) {
		t.Fatalf("run() stderr = %q, want unknown-flag error", stderr.String())
	}
}

func TestRunReplayRejectsMalformedPCAP(t *testing.T) {
	directory := t.TempDir()
	recordingPath := filepath.Join(directory, "recording.pcap")
	if err := os.WriteFile(recordingPath, []byte("not a PCAP"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	configurationPath := writeConfiguration(t, "midi:\n  enabled: false\ncapture:\n  bpf: \"\"\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"replay", "--config", configurationPath, recordingPath}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "error") {
		t.Fatalf("run() stderr = %q, want replay error", stderr.String())
	}
}

func TestRunValidateConfig(t *testing.T) {
	path := writeConfiguration(t, "{}\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"validate-config", "--config", path}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "configuration is valid\n" {
		t.Fatalf("run() stdout = %q", got)
	}
}

func TestRunValidateConfigRejectsUnknownFields(t *testing.T) {
	path := writeConfiguration(t, "mystery: true\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"validate-config", "--config", path}, &stdout, &stderr)

	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "field mystery not found") {
		t.Fatalf("run() stderr = %q, want strict-decoding error", stderr.String())
	}
}

func TestRunRequiresConfigPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--config is required") {
		t.Fatalf("run() stderr = %q, want required flag error", stderr.String())
	}
}

func TestRunCommandHelpUsesStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "run --config") {
		t.Fatalf("run() stdout = %q, want command usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("run() stderr = %q, want empty", stderr.String())
	}
}

func TestRunRejectsFutureRuntimeRole(t *testing.T) {
	path := writeConfiguration(t, "instance:\n  role: host\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--config", path}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "instance role") || !strings.Contains(stderr.String(), "host") || !strings.Contains(stderr.String(), "unsupported") {
		t.Fatalf("run() stderr = %q, want unsupported-role error", stderr.String())
	}
}

func TestApplicationLoggerHonorsLevelAndFormat(t *testing.T) {
	var output bytes.Buffer
	logger := applicationLogger(config.LoggingConfig{Level: config.LogLevelWarn, Format: config.LogFormatJSON}, &output)
	logger.Info("hidden")
	logger.Error("visible")
	got := output.String()
	if strings.Contains(got, "hidden") || !strings.Contains(got, `"level":"ERROR"`) || !strings.Contains(got, `"msg":"visible"`) {
		t.Fatalf("logger output = %q, want JSON error only", got)
	}
}

func writeConfiguration(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func writeEmptyPCAP(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recording.pcap")
	contents := []byte{
		0xd4, 0xc3, 0xb2, 0xa1, // little-endian microsecond magic
		0x02, 0x00, 0x04, 0x00, // PCAP version 2.4
		0x00, 0x00, 0x00, 0x00, // timezone correction
		0x00, 0x00, 0x00, 0x00, // timestamp accuracy
		0xff, 0xff, 0x00, 0x00, // snapshot length
		0x01, 0x00, 0x00, 0x00, // Ethernet link type
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
