package capture

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"

	packetmeta "github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

func TestNormalizeTCP(t *testing.T) {
	data := serializePacket(t,
		&layers.Ethernet{SrcMAC: []byte{0, 1, 2, 3, 4, 5}, DstMAC: []byte{6, 7, 8, 9, 10, 11}, EthernetType: layers.EthernetTypeIPv4},
		&layers.IPv4{Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolTCP, SrcIP: netip.MustParseAddr("192.0.2.1").AsSlice(), DstIP: netip.MustParseAddr("198.51.100.2").AsSlice()},
		&layers.TCP{SrcPort: 50000, DstPort: 443, SYN: true},
		gopacket.Payload("hello"),
	)
	frame := gopacket.NewPacket(data, layers.LayerTypeEthernet, gopacket.Default)
	event, err := Normalize(frame, gopacket.CaptureInfo{Timestamp: time.Unix(100, 0), CaptureLength: len(data), Length: len(data)})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if event.Protocol != packetmeta.ProtocolTCP || event.Source.Port != 50000 || event.Destination.Port != 443 {
		t.Fatalf("Normalize() endpoints/protocol = %#v", event)
	}
	if event.PayloadLength != 5 || event.TCPFlags&packetmeta.TCPFlagSYN == 0 {
		t.Fatalf("Normalize() payload/flags = %#v", event)
	}
}

func TestNormalizeIPv6UDP(t *testing.T) {
	data := serializePacket(t,
		&layers.Ethernet{SrcMAC: []byte{0, 1, 2, 3, 4, 5}, DstMAC: []byte{6, 7, 8, 9, 10, 11}, EthernetType: layers.EthernetTypeIPv6},
		&layers.IPv6{Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolUDP, SrcIP: netip.MustParseAddr("2001:db8::1").AsSlice(), DstIP: netip.MustParseAddr("2001:db8::2").AsSlice()},
		&layers.UDP{SrcPort: 5353, DstPort: 5353},
		gopacket.Payload("dns"),
	)
	frame := gopacket.NewPacket(data, layers.LayerTypeEthernet, gopacket.Default)
	event, err := Normalize(frame, gopacket.CaptureInfo{CaptureLength: len(data), Length: len(data)})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if event.Protocol != packetmeta.ProtocolUDP || event.Source.Addr.String() != "2001:db8::1" || event.PayloadLength != 3 {
		t.Fatalf("Normalize() = %#v", event)
	}
}

func TestNormalizeRejectsNonIP(t *testing.T) {
	data := serializePacket(t,
		&layers.Ethernet{SrcMAC: []byte{0, 1, 2, 3, 4, 5}, DstMAC: []byte{6, 7, 8, 9, 10, 11}, EthernetType: layers.EthernetTypeARP},
		&layers.ARP{},
	)
	frame := gopacket.NewPacket(data, layers.LayerTypeEthernet, gopacket.Default)
	_, err := Normalize(frame, gopacket.CaptureInfo{CaptureLength: len(data), Length: len(data)})
	if !errors.Is(err, ErrUnsupportedPacket) {
		t.Fatalf("Normalize() error = %v, want ErrUnsupportedPacket", err)
	}
}

func serializePacket(t *testing.T, layersToSerialize ...gopacket.SerializableLayer) []byte {
	t.Helper()
	var network gopacket.NetworkLayer
	for _, serializable := range layersToSerialize {
		switch layer := serializable.(type) {
		case *layers.IPv4:
			network = layer
		case *layers.IPv6:
			network = layer
		case *layers.TCP:
			if err := layer.SetNetworkLayerForChecksum(network); err != nil {
				t.Fatalf("TCP SetNetworkLayerForChecksum() error = %v", err)
			}
		case *layers.UDP:
			if err := layer.SetNetworkLayerForChecksum(network); err != nil {
				t.Fatalf("UDP SetNetworkLayerForChecksum() error = %v", err)
			}
		}
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, layersToSerialize...); err != nil {
		t.Fatalf("SerializeLayers() error = %v", err)
	}
	return buffer.Bytes()
}
