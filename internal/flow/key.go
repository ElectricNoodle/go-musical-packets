// Package flow defines canonical flow identity and, later, flow selection.
package flow

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

// Direction describes packet direction relative to a canonical flow key.
type Direction uint8

const (
	DirectionAToB Direction = iota
	DirectionBToA
)

// Key identifies a bidirectional flow. A always sorts before or equal to B.
type Key struct {
	Protocol packet.Protocol
	A        packet.Endpoint
	B        packet.Endpoint
}

// Canonicalize returns the flow key and the event's direction within it.
func Canonicalize(e packet.Event) (Key, Direction) {
	if compareEndpoint(e.Source, e.Destination) <= 0 {
		return Key{Protocol: e.Protocol, A: e.Source, B: e.Destination}, DirectionAToB
	}
	return Key{Protocol: e.Protocol, A: e.Destination, B: e.Source}, DirectionBToA
}

// ID returns a deterministic, pseudonymous 96-bit identifier for a key and
// deployment seed. It is suitable for UI and protocol correlation, not access
// control.
func (k Key) ID(seed string) string {
	h := sha256.New()
	writeString(h, seed)
	writeString(h, string(k.Protocol))
	writeEndpoint(h, k.A)
	writeEndpoint(h, k.B)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

func compareEndpoint(a, b packet.Endpoint) int {
	if cmp := a.Addr.Compare(b.Addr); cmp != 0 {
		return cmp
	}
	switch {
	case a.Port < b.Port:
		return -1
	case a.Port > b.Port:
		return 1
	default:
		return 0
	}
}

func writeEndpoint(h hash.Hash, endpoint packet.Endpoint) {
	writeString(h, endpoint.Addr.String())
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], endpoint.Port)
	_, _ = h.Write(port[:])
}

func writeString(h hash.Hash, value string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(value))
}
