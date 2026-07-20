//go:build cgo && (darwin || linux)

package capture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/pcap"

	packetmeta "github.com/ElectricNoodle/go-musical-packets/internal/packet"
)

const defaultReadTimeout = 250 * time.Millisecond

type liveSource struct {
	handle    *pcap.Handle
	linkType  gopacket.Decoder
	closeOnce sync.Once
}

// OpenLive opens a libpcap source and applies its BPF expression.
func OpenLive(config LiveConfig) (Source, error) {
	if strings.TrimSpace(config.Device) == "" {
		return nil, errors.New("capture device is required")
	}
	if config.SnapshotLength < 64 || config.SnapshotLength > 65535 {
		return nil, errors.New("snapshot length must be between 64 and 65535")
	}
	if config.Timeout <= 0 {
		config.Timeout = defaultReadTimeout
	}

	handle, err := pcap.OpenLive(config.Device, int32(config.SnapshotLength), config.Promiscuous, config.Timeout)
	if err != nil {
		return nil, fmt.Errorf("open capture device %q: %w", config.Device, err)
	}
	if strings.TrimSpace(config.BPF) != "" {
		if err := handle.SetBPFFilter(config.BPF); err != nil {
			handle.Close()
			return nil, fmt.Errorf("apply BPF filter: %w", err)
		}
	}
	return &liveSource{handle: handle, linkType: handle.LinkType()}, nil
}

func (s *liveSource) Next(ctx context.Context) (packetmeta.Event, error) {
	for {
		select {
		case <-ctx.Done():
			return packetmeta.Event{}, ctx.Err()
		default:
		}

		data, info, err := s.handle.ReadPacketData()
		if err != nil {
			if err == pcap.NextErrorTimeoutExpired {
				continue
			}
			return packetmeta.Event{}, fmt.Errorf("read captured packet: %w", err)
		}
		frame := gopacket.NewPacket(data, s.linkType, gopacket.DecodeOptions{Lazy: true, NoCopy: true})
		event, err := Normalize(frame, info)
		if errors.Is(err, ErrUnsupportedPacket) {
			continue
		}
		return event, err
	}
}

func (s *liveSource) Close() error {
	s.closeOnce.Do(func() { s.handle.Close() })
	return nil
}

// Interfaces enumerates libpcap devices in stable name order.
func Interfaces() ([]Interface, error) {
	devices, err := pcap.FindAllDevs()
	if err != nil {
		return nil, fmt.Errorf("list capture interfaces: %w", err)
	}
	result := make([]Interface, 0, len(devices))
	for _, device := range devices {
		info := Interface{Name: device.Name, Description: device.Description}
		if system, err := net.InterfaceByName(device.Name); err == nil {
			info.Up = system.Flags&net.FlagUp != 0
			info.Loopback = system.Flags&net.FlagLoopback != 0
		}
		for _, address := range device.Addresses {
			ip, ok := netip.AddrFromSlice(address.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			ones, bits := address.Netmask.Size()
			if ones < 0 || (bits != 32 && bits != 128) {
				continue
			}
			if ip.Is4() && bits == 128 {
				ones -= 96
				bits = 32
			}
			if ones >= 0 && ones <= bits {
				info.Addresses = append(info.Addresses, netip.PrefixFrom(ip, ones))
			}
		}
		sort.Slice(info.Addresses, func(i, j int) bool {
			return info.Addresses[i].String() < info.Addresses[j].String()
		})
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
