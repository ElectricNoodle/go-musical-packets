// Package packet defines capture-backend-independent packet metadata.
package packet

import (
	"errors"
	"net/netip"
	"time"
)

// Protocol is a bounded transport/network protocol class.
type Protocol string

const (
	ProtocolTCP   Protocol = "tcp"
	ProtocolUDP   Protocol = "udp"
	ProtocolICMP  Protocol = "icmp"
	ProtocolICMP6 Protocol = "icmp6"
	ProtocolOther Protocol = "other"
)

// Endpoint identifies one side of an IP flow. Port is zero for protocols that
// do not have transport ports.
type Endpoint struct {
	Addr netip.Addr
	Port uint16
}

// TCPFlags contains the TCP control flags relevant to musical accents.
type TCPFlags uint8

const (
	TCPFlagFIN TCPFlags = 1 << iota
	TCPFlagSYN
	TCPFlagRST
	TCPFlagPSH
	TCPFlagACK
	TCPFlagURG
)

// Event is the normalized metadata retained for one captured packet. Payload
// bytes are deliberately absent.
type Event struct {
	CapturedAt     time.Time
	Source         Endpoint
	Destination    Endpoint
	Protocol       Protocol
	WireLength     int
	CapturedLength int
	PayloadLength  int
	TCPFlags       TCPFlags
}

// Validate checks invariants expected by flow and mapping code.
func (e Event) Validate() error {
	if !e.Source.Addr.IsValid() || !e.Destination.Addr.IsValid() {
		return errors.New("source and destination addresses must be valid")
	}
	if e.WireLength < 0 || e.CapturedLength < 0 || e.PayloadLength < 0 {
		return errors.New("packet lengths must not be negative")
	}
	if e.CapturedLength > e.WireLength {
		return errors.New("captured length must not exceed wire length")
	}
	if e.PayloadLength > e.CapturedLength {
		return errors.New("payload length must not exceed captured length")
	}
	switch e.Protocol {
	case ProtocolTCP, ProtocolUDP, ProtocolICMP, ProtocolICMP6, ProtocolOther:
		return nil
	default:
		return errors.New("unsupported normalized protocol")
	}
}
