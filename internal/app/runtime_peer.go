package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ElectricNoodle/go-musical-packets/internal/config"
	"github.com/ElectricNoodle/go-musical-packets/internal/flow"
	"github.com/ElectricNoodle/go-musical-packets/internal/metrics"
	"github.com/ElectricNoodle/go-musical-packets/internal/peer"
	"github.com/ElectricNoodle/go-musical-packets/internal/pipeline"
)

const hostDuplicateCapacity = 65_536

type runtimePeers struct {
	edge        *peer.Edge
	host        *peer.Host
	snapshotter peerSnapshotter
}

func newRuntimePeers(configuration config.Config, bundle *metrics.Bundle, hostSink pipeline.Sink) (runtimePeers, error) {
	switch configuration.Instance.Role {
	case config.RoleStandalone:
		return runtimePeers{snapshotter: staticPeerSnapshot{role: string(config.RoleStandalone)}}, nil
	case config.RoleEdge:
		edge, err := peer.NewEdge(peer.EdgeConfig{
			URL: configuration.Peer.URL, Token: configuration.Peer.Token,
			InstanceID: configuration.Instance.ID, MappingVersion: configuration.Mapping.Version,
			QueueCapacity: configuration.Peer.QueueCapacity, StaleAfter: configuration.Peer.StaleAfter,
			ReconnectBase: configuration.Peer.ReconnectBase, ReconnectLimit: configuration.Peer.ReconnectLimit,
			Observer: bundle.Peer,
		})
		if err != nil {
			return runtimePeers{}, fmt.Errorf("initialize edge peer: %w", err)
		}
		return runtimePeers{edge: edge, snapshotter: edge}, nil
	case config.RoleHost:
		if !configuration.Peer.Enabled {
			return runtimePeers{snapshotter: staticPeerSnapshot{role: string(config.RoleHost)}}, nil
		}
		if hostSink == nil {
			return runtimePeers{}, errors.New("initialize host peer: note sink is unavailable")
		}
		host, err := peer.NewHost(peer.HostConfig{
			Token: configuration.Peer.Token, InstanceID: configuration.Instance.ID,
			MappingVersion:     configuration.Mapping.Version,
			MaximumConnections: configuration.Peer.MaximumConnections,
			RecentCapacity:     configuration.Peer.MaximumConnections * 2,
			RecentTTL:          configuration.Peer.RecentTTL, StaleAfter: configuration.Peer.StaleAfter,
			DuplicateCapacity: hostDuplicateCapacity, Sink: hostSink, Observer: bundle.Peer,
		})
		if err != nil {
			return runtimePeers{}, fmt.Errorf("initialize host peer: %w", err)
		}
		return runtimePeers{host: host, snapshotter: host}, nil
	default:
		return runtimePeers{}, fmt.Errorf("initialize peers: instance role %q is unsupported", configuration.Instance.Role)
	}
}

type peerCaptureEndpoint struct {
	addresses []netip.Addr
	port      uint16
}

func resolvePeerCaptureEndpoint(ctx context.Context, rawURL string, lookup func(context.Context, string) ([]net.IP, error)) (peerCaptureEndpoint, error) {
	if ctx == nil {
		return peerCaptureEndpoint{}, errors.New("peer resolution context is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return peerCaptureEndpoint{}, err
	}
	portText := parsed.Port()
	if portText == "" {
		switch parsed.Scheme {
		case "ws":
			portText = "80"
		case "wss":
			portText = "443"
		default:
			return peerCaptureEndpoint{}, fmt.Errorf("unsupported peer URL scheme %q", parsed.Scheme)
		}
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return peerCaptureEndpoint{}, fmt.Errorf("peer URL port %q is invalid", portText)
	}
	host := parsed.Hostname()
	addresses := make([]netip.Addr, 0, 2)
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		if address.Zone() != "" {
			return peerCaptureEndpoint{}, errors.New("zoned peer IP addresses are unsupported")
		}
		addresses = append(addresses, address.Unmap())
	} else {
		if lookup == nil {
			return peerCaptureEndpoint{}, errors.New("peer hostname resolver is unavailable")
		}
		resolved, lookupErr := lookup(ctx, host)
		if lookupErr != nil {
			return peerCaptureEndpoint{}, fmt.Errorf("resolve peer host %q: %w", host, lookupErr)
		}
		seen := make(map[netip.Addr]struct{}, len(resolved))
		for _, candidate := range resolved {
			address, ok := netip.AddrFromSlice(candidate)
			if !ok {
				continue
			}
			address = address.Unmap()
			if _, exists := seen[address]; exists {
				continue
			}
			seen[address] = struct{}{}
			addresses = append(addresses, address)
		}
	}
	if len(addresses) == 0 {
		return peerCaptureEndpoint{}, fmt.Errorf("peer host %q resolved without usable addresses", host)
	}
	sort.Slice(addresses, func(left, right int) bool { return addresses[left].Less(addresses[right]) })
	return peerCaptureEndpoint{addresses: addresses, port: uint16(port)}, nil
}

func peerSafetyRules(endpoint peerCaptureEndpoint, existing []flow.Rule) []flow.Rule {
	rules := make([]flow.Rule, 0, len(endpoint.addresses)*2)
	used := append([]flow.Rule(nil), existing...)
	for index, address := range endpoint.addresses {
		bits := 128
		if address.Is4() {
			bits = 32
		}
		prefix := netip.PrefixFrom(address, bits)
		port := &flow.PortRange{Minimum: endpoint.port, Maximum: endpoint.port}
		sourceID := uniqueRuleID(fmt.Sprintf("__musical_packets_peer_source_%d", index), append(used, rules...))
		rules = append(rules, flow.Rule{
			ID: sourceID, Name: "peer transport source traffic", Enabled: true,
			Match:  flow.Match{Protocol: "tcp", SourcePrefix: &prefix, SourcePorts: port},
			Action: flow.Action{State: flow.StateIgnore},
		})
		destinationID := uniqueRuleID(fmt.Sprintf("__musical_packets_peer_destination_%d", index), append(used, rules...))
		rules = append(rules, flow.Rule{
			ID: destinationID, Name: "peer transport destination traffic", Enabled: true,
			Match:  flow.Match{Protocol: "tcp", DestinationPrefix: &prefix, DestinationPorts: port},
			Action: flow.Action{State: flow.StateIgnore},
		})
	}
	return rules
}

func captureBPFWithPeer(configured string, localPort uint16, endpoint *peerCaptureEndpoint) string {
	exclusions := []string{fmt.Sprintf("tcp src port %d or tcp dst port %d", localPort, localPort)}
	if endpoint != nil {
		hosts := make([]string, 0, len(endpoint.addresses))
		for _, address := range endpoint.addresses {
			hosts = append(hosts, "host "+address.String())
		}
		exclusions = append(exclusions, fmt.Sprintf("tcp port %d and (%s)", endpoint.port, strings.Join(hosts, " or ")))
	}
	exclusion := "not (" + strings.Join(exclusions, " or ") + ")"
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return exclusion
	}
	return fmt.Sprintf("(%s) and (%s)", configured, exclusion)
}
