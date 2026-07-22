package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestResolvePeerCaptureEndpointUsesBoundedDeterministicAddresses(t *testing.T) {
	endpoint, err := resolvePeerCaptureEndpoint(context.Background(), "wss://host.example/api/v1/peer", func(_ context.Context, host string) ([]net.IP, error) {
		if host != "host.example" {
			t.Fatalf("lookup host = %q", host)
		}
		return []net.IP{
			net.ParseIP("2001:db8::5"), net.ParseIP("192.0.2.5"), net.ParseIP("192.0.2.5"),
		}, nil
	})
	if err != nil {
		t.Fatalf("resolvePeerCaptureEndpoint() error = %v", err)
	}
	if endpoint.port != 443 || len(endpoint.addresses) != 2 || endpoint.addresses[0].String() != "192.0.2.5" || endpoint.addresses[1].String() != "2001:db8::5" {
		t.Fatalf("endpoint = %#v", endpoint)
	}

	if _, err := resolvePeerCaptureEndpoint(context.Background(), "ws://missing.example/peer", func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("DNS unavailable")
	}); err == nil || !strings.Contains(err.Error(), "DNS unavailable") {
		t.Fatalf("resolve error = %v", err)
	}
}

func TestPeerCaptureExclusionsAreAddressAndPortScoped(t *testing.T) {
	endpoint, err := resolvePeerCaptureEndpoint(context.Background(), "ws://192.0.2.8:9090/api/v1/peer", nil)
	if err != nil {
		t.Fatalf("resolvePeerCaptureEndpoint() error = %v", err)
	}
	bpf := captureBPFWithPeer("ip or ip6", 8080, &endpoint)
	for _, want := range []string{"tcp src port 8080", "tcp dst port 8080", "tcp port 9090", "host 192.0.2.8"} {
		if !strings.Contains(bpf, want) {
			t.Fatalf("BPF = %q, want %q", bpf, want)
		}
	}

	rules := peerSafetyRules(endpoint, nil)
	if len(rules) != 2 || rules[0].Match.Protocol != packet.ProtocolTCP ||
		rules[0].Match.SourcePrefix == nil || rules[0].Match.SourcePorts == nil ||
		rules[1].Match.DestinationPrefix == nil || rules[1].Match.DestinationPorts == nil {
		t.Fatalf("peer safety rules = %#v", rules)
	}
	if rules[0].Match.SourcePorts.Minimum != 9090 || rules[1].Match.DestinationPorts.Minimum != 9090 {
		t.Fatalf("peer safety ports = %#v", rules)
	}
}
