package music

import (
	"net/netip"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestIdentityForFlowIDMatchesMapperIdentity(t *testing.T) {
	mapper := testMapper(t)
	input := MapInput{Packet: packet.Event{
		CapturedAt:     time.Unix(100, 0),
		Source:         packet.Endpoint{Addr: netip.MustParseAddr("192.0.2.10"), Port: 52100},
		Destination:    packet.Endpoint{Addr: netip.MustParseAddr("198.51.100.20"), Port: 443},
		Protocol:       packet.ProtocolTCP,
		WireLength:     128,
		CapturedLength: 128,
	}, Sequence: 1, Channel: 3}
	event, err := mapper.Map(input)
	if err != nil {
		t.Fatalf("Map() error = %v", err)
	}
	identity, err := IdentityForFlowID(event.FlowID)
	if err != nil {
		t.Fatalf("IdentityForFlowID() error = %v", err)
	}
	if identity.Mode != event.Mode || identity.Root != event.Root {
		t.Fatalf("identity = %#v, mapped mode/root = %s/%d", identity, event.Mode, event.Root)
	}
}

func TestIdentityForFlowIDRejectsInvalidIDs(t *testing.T) {
	for _, value := range []string{"", "0123", "0123456789ABCDEF01234567", "zzzzzzzzzzzzzzzzzzzzzzzz"} {
		if _, err := IdentityForFlowID(value); err == nil {
			t.Fatalf("IdentityForFlowID(%q) error = nil", value)
		}
	}
}
