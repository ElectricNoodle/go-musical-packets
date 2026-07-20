// Package capture provides live and replay packet sources.
package capture

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

var (
	// ErrUnsupportedPacket indicates a frame without supported IP metadata.
	ErrUnsupportedPacket = errors.New("unsupported packet")
	// ErrLiveCaptureUnavailable indicates that this build lacks a live backend.
	ErrLiveCaptureUnavailable = errors.New("live packet capture is unavailable in this build")
)

// Source yields normalized packets until closed or its context is canceled.
type Source interface {
	Next(context.Context) (packet.Event, error)
	Close() error
}

// LiveConfig configures a libpcap-backed source.
type LiveConfig struct {
	Device         string
	SnapshotLength int
	Promiscuous    bool
	Timeout        time.Duration
	BPF            string
}

// Interface describes a packet-capture device without exposing backend types.
type Interface struct {
	Name        string
	Description string
	Addresses   []netip.Prefix
	Up          bool
	Loopback    bool
}

// SelectInterface resolves an explicit device name or chooses a conservative
// automatic default: an up, non-loopback device with an address.
func SelectInterface(interfaces []Interface, requested string) (Interface, error) {
	if requested != "" && requested != "auto" {
		for _, candidate := range interfaces {
			if candidate.Name == requested {
				return candidate, nil
			}
		}
		return Interface{}, errors.New("requested capture interface was not found")
	}
	for _, candidate := range interfaces {
		if candidate.Up && !candidate.Loopback && len(candidate.Addresses) > 0 {
			return candidate, nil
		}
	}
	return Interface{}, errors.New("no up non-loopback capture interface with an address was found")
}
