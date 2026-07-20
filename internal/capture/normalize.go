package capture

import (
	"fmt"
	"net/netip"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"

	packetmeta "github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

// Normalize converts a decoded frame into privacy-conscious packet metadata.
func Normalize(frame gopacket.Packet, info gopacket.CaptureInfo) (packetmeta.Event, error) {
	var source, destination netip.Addr
	switch network := frame.NetworkLayer().(type) {
	case *layers.IPv4:
		source = addrFromBytes(network.SrcIP)
		destination = addrFromBytes(network.DstIP)
	case *layers.IPv6:
		source = addrFromBytes(network.SrcIP)
		destination = addrFromBytes(network.DstIP)
	default:
		return packetmeta.Event{}, ErrUnsupportedPacket
	}
	if !source.IsValid() || !destination.IsValid() {
		return packetmeta.Event{}, fmt.Errorf("%w: invalid IP address", ErrUnsupportedPacket)
	}

	event := packetmeta.Event{
		CapturedAt:     info.Timestamp,
		Source:         packetmeta.Endpoint{Addr: source},
		Destination:    packetmeta.Endpoint{Addr: destination},
		Protocol:       packetmeta.ProtocolOther,
		WireLength:     info.Length,
		CapturedLength: info.CaptureLength,
	}
	if event.CapturedLength == 0 {
		event.CapturedLength = len(frame.Data())
	}
	if event.WireLength < event.CapturedLength {
		event.WireLength = event.CapturedLength
	}

	switch transport := frame.TransportLayer().(type) {
	case *layers.TCP:
		event.Protocol = packetmeta.ProtocolTCP
		event.Source.Port = uint16(transport.SrcPort)
		event.Destination.Port = uint16(transport.DstPort)
		event.PayloadLength = len(transport.Payload)
		event.TCPFlags = tcpFlags(transport)
	case *layers.UDP:
		event.Protocol = packetmeta.ProtocolUDP
		event.Source.Port = uint16(transport.SrcPort)
		event.Destination.Port = uint16(transport.DstPort)
		event.PayloadLength = len(transport.Payload)
	default:
		switch {
		case frame.Layer(layers.LayerTypeICMPv4) != nil:
			event.Protocol = packetmeta.ProtocolICMP
			event.PayloadLength = len(frame.Layer(layers.LayerTypeICMPv4).LayerPayload())
		case frame.Layer(layers.LayerTypeICMPv6) != nil:
			event.Protocol = packetmeta.ProtocolICMP6
			event.PayloadLength = len(frame.Layer(layers.LayerTypeICMPv6).LayerPayload())
		}
	}

	if event.PayloadLength > event.CapturedLength {
		event.PayloadLength = event.CapturedLength
	}
	if err := event.Validate(); err != nil {
		return packetmeta.Event{}, fmt.Errorf("normalize packet: %w", err)
	}
	return event, nil
}

func addrFromBytes(value []byte) netip.Addr {
	address, ok := netip.AddrFromSlice(value)
	if !ok {
		return netip.Addr{}
	}
	return address.Unmap()
}

func tcpFlags(layer *layers.TCP) packetmeta.TCPFlags {
	var flags packetmeta.TCPFlags
	if layer.FIN {
		flags |= packetmeta.TCPFlagFIN
	}
	if layer.SYN {
		flags |= packetmeta.TCPFlagSYN
	}
	if layer.RST {
		flags |= packetmeta.TCPFlagRST
	}
	if layer.PSH {
		flags |= packetmeta.TCPFlagPSH
	}
	if layer.ACK {
		flags |= packetmeta.TCPFlagACK
	}
	if layer.URG {
		flags |= packetmeta.TCPFlagURG
	}
	return flags
}
