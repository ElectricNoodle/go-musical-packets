package app

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/peer"
)

type testPeerSnapshotter struct{ snapshot peer.Snapshot }

func (provider testPeerSnapshotter) Snapshot() peer.Snapshot { return provider.snapshot }

func TestManagementPeersConvertsDetachedRuntimeSnapshot(t *testing.T) {
	configuration := managementTestConfig()
	controller := mustController(t, configuration, nil, nil)
	var ready atomic.Bool
	ready.Store(true)
	backend := newTestManagementBackend(controller, &ready, context.Background())
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	backend.peers = testPeerSnapshotter{snapshot: peer.Snapshot{
		Role: "host", Enabled: true,
		Nodes: []peer.NodeSnapshot{{
			InstanceID: "edge-1", RemoteAddress: "192.0.2.4:53000", State: "connected", Authenticated: true,
			ProtocolVersion: peer.ProtocolVersion, MappingVersion: "flow-mode-v1", ConnectedAt: now, LastSeenAt: now,
			AcceptedTotal: 3, ActiveChannels: []uint8{2, 13},
		}},
	}}
	document, err := backend.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers() error = %v", err)
	}
	if document.Role != "host" || !document.Enabled || len(document.Nodes) != 1 || document.Nodes[0].AcceptedTotal != 3 || !reflect.DeepEqual(document.Nodes[0].ActiveChannels, []uint8{2, 13}) {
		t.Fatalf("Peers() = %#v", document)
	}
	document.Nodes[0].ActiveChannels[0] = 16
	if backend.peers.Snapshot().Nodes[0].ActiveChannels[0] != 2 {
		t.Fatal("Peers() returned an aliased channel collection")
	}
}
