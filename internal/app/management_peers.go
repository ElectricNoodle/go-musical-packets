package app

import (
	"context"
	"time"

	"github.com/ElectricNoodle/go-musical-packets/internal/managementapi"
)

func (backend *managementBackend) Peers(ctx context.Context) (managementapi.PeersDocument, error) {
	if err := ctx.Err(); err != nil {
		return managementapi.PeersDocument{}, err
	}
	snapshot := backend.peers.Snapshot()
	document := managementapi.PeersDocument{Role: snapshot.Role, Enabled: snapshot.Enabled, Nodes: make([]managementapi.ConnectedNode, 0, len(snapshot.Nodes))}
	if snapshot.Outbound != nil {
		outbound := snapshot.Outbound
		document.Outbound = &managementapi.OutboundPeer{
			Enabled: outbound.Enabled, Target: outbound.Target, RemoteInstance: outbound.RemoteInstance,
			State: outbound.State, ProtocolVersion: outbound.ProtocolVersion, MappingVersion: outbound.MappingVersion,
			Queue:     managementapi.PeerQueue{Depth: outbound.QueueDepth, Capacity: outbound.QueueCapacity},
			SentTotal: outbound.SentTotal, DroppedFull: outbound.DroppedFull, DroppedStale: outbound.DroppedStale,
			Reconnects: outbound.Reconnects, SendRate: outbound.SendRate,
			LastSentAt: optionalTime(outbound.LastSentAt), ConnectedAt: optionalTime(outbound.ConnectedAt),
			LastAttemptAt: optionalTime(outbound.LastAttemptAt), NextRetryAt: optionalTime(outbound.NextRetryAt),
			RTTMilliseconds: outbound.RTT.Milliseconds(), LastError: outbound.LastError,
			ActiveChannels: append([]uint8(nil), outbound.ActiveChannels...),
		}
		if document.Outbound.ActiveChannels == nil {
			document.Outbound.ActiveChannels = []uint8{}
		}
	}
	for _, node := range snapshot.Nodes {
		document.Nodes = append(document.Nodes, managementapi.ConnectedNode{
			InstanceID: node.InstanceID, RemoteAddress: node.RemoteAddress, State: node.State,
			Authenticated: node.Authenticated, ProtocolVersion: node.ProtocolVersion, MappingVersion: node.MappingVersion,
			ConnectedAt: node.ConnectedAt, DisconnectedAt: optionalTime(node.DisconnectedAt), LastSeenAt: node.LastSeenAt,
			NoteRate: node.NoteRate, ReceivedTotal: node.ReceivedTotal, AcceptedTotal: node.AcceptedTotal,
			RejectedTotal: node.RejectedTotal, DuplicateTotal: node.DuplicateTotal, StaleTotal: node.StaleTotal,
			ActiveChannels: append([]uint8(nil), node.ActiveChannels...),
		})
		if document.Nodes[len(document.Nodes)-1].ActiveChannels == nil {
			document.Nodes[len(document.Nodes)-1].ActiveChannels = []uint8{}
		}
	}
	return document, nil
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}
