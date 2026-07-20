package flow

import (
	"net/netip"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestCanonicalizeIsBidirectional(t *testing.T) {
	a := packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.10"), Port: 50000}
	b := packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.20"), Port: 443}

	forward, forwardDirection := Canonicalize(packet.Event{Source: a, Destination: b, Protocol: packet.ProtocolTCP})
	reverse, reverseDirection := Canonicalize(packet.Event{Source: b, Destination: a, Protocol: packet.ProtocolTCP})

	if forward != reverse {
		t.Fatalf("forward key = %#v, reverse key = %#v", forward, reverse)
	}
	if forwardDirection == reverseDirection {
		t.Fatalf("directions are both %v, want opposites", forwardDirection)
	}
	if forward.ID("seed") != reverse.ID("seed") {
		t.Fatal("reversed flow IDs differ")
	}
}

func TestKeyIDDependsOnSeed(t *testing.T) {
	e := packet.Event{
		Source:      packet.Endpoint{Addr: netip.MustParseAddr("2001:db8::1"), Port: 53},
		Destination: packet.Endpoint{Addr: netip.MustParseAddr("2001:db8::2"), Port: 53000},
		Protocol:    packet.ProtocolUDP,
	}
	key, _ := Canonicalize(e)
	if key.ID("one") == key.ID("two") {
		t.Fatal("IDs with different seeds are equal")
	}
}
