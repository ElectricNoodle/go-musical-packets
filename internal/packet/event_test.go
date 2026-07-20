package packet

import (
	"net/netip"
	"testing"
)

func TestEventValidate(t *testing.T) {
	valid := Event{
		Source:         Endpoint{Addr: netip.MustParseAddr("192.0.2.1"), Port: 1234},
		Destination:    Endpoint{Addr: netip.MustParseAddr("198.51.100.2"), Port: 443},
		Protocol:       ProtocolTCP,
		WireLength:     100,
		CapturedLength: 100,
		PayloadLength:  60,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	invalid := valid
	invalid.PayloadLength = 101
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want payload length error")
	}
}
