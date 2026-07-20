//go:build cgo && (darwin || linux)

package capture

import (
	"os"
	"testing"
	"time"
)

func TestLiveOpenIntegration(t *testing.T) {
	device := os.Getenv("MUSICAL_PACKETS_LIVE_INTERFACE")
	if device == "" {
		t.Skip("set MUSICAL_PACKETS_LIVE_INTERFACE to run the privileged live-open probe")
	}
	source, err := OpenLive(LiveConfig{
		Device:         device,
		SnapshotLength: 65535,
		Promiscuous:    false,
		Timeout:        250 * time.Millisecond,
		BPF:            "ip or ip6",
	})
	if err != nil {
		t.Fatalf("OpenLive(%q) error = %v", device, err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
