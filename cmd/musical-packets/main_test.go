package main

import (
	"bytes"
	"strings"
	"testing"
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
	for _, command := range []string{"interfaces", "devices"} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("help output = %q, want %q", stdout.String(), command)
		}
	}
}
