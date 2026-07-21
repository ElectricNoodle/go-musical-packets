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
	for _, command := range []string{"run", "validate-config", "interfaces", "devices"} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("help output = %q, want %q", stdout.String(), command)
		}
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
