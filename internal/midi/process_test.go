package midi

import (
	"bytes"
	"strings"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	want := []byte{0x90, 60, 100}
	if err := writeFrame(&buffer, opSend, want); err != nil {
		t.Fatalf("writeFrame() error = %v", err)
	}
	kind, got, err := readFrame(&buffer)
	if err != nil {
		t.Fatalf("readFrame() error = %v", err)
	}
	if kind != opSend || !bytes.Equal(got, want) {
		t.Fatalf("readFrame() = %d, % X; want %d, % X", kind, got, opSend, want)
	}
}

func TestReadFrameRejectsOversize(t *testing.T) {
	header := []byte{opSend, 0, 0x10, 0, 1}
	if _, _, err := readFrame(bytes.NewReader(header)); err == nil {
		t.Fatal("readFrame() error = nil")
	}
}

func TestRunHelperRejectsUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunHelper([]string{"unknown"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "unknown") {
		t.Fatalf("RunHelper() = %d, stderr %q", code, stderr.String())
	}
}

func TestSummarizeStderrStopsBeforeRuntimeDump(t *testing.T) {
	got := summarizeStderr("native error\nsecond line\nSIGABRT: abort\ngoroutine 1")
	if got != "native error; second line" {
		t.Fatalf("summarizeStderr() = %q", got)
	}
}
