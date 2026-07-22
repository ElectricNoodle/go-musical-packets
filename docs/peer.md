# Peer Transport

Stage 13 provides a transport-independent, authenticated edge sender, host
receiver, bounded operational snapshots, metrics, management representation,
and role-aware browser workspace. Stage 14 composes these components into the
`edge` and `host` command runtimes; standalone execution still rejects active
peer transport until that composition is complete.

## Wire contract

The peer endpoint is `GET /api/v1/peer` over `ws://` or `wss://`. Native edge
clients send `Authorization: Bearer <token>` and no `Origin` header. The host
authenticates before upgrading, rejects browser-origin connections, disables
compression, and limits text frames to 16 KiB.

Every JSON message contains `type`, `version: "peer-v1"`, and exactly one
matching payload. Supported messages are `hello`, `note`, `ping`, `pong`, and
`error`; graceful shutdown uses the WebSocket close handshake. Decoding rejects
unknown fields, duplicate object names, excessive nesting, trailing JSON,
binary frames, invalid UTF-8, and unsupported versions.

Both endpoints exchange `hello` before notes. It contains the instance ID,
`edge` or `host` role, and `flow-mode-v1` mapping version. Notes carry the
complete `music.NoteEvent` identity and musical fields, including the
originating user-facing MIDI channel 1 through 16. The host never assigns a
replacement channel. It validates the authenticated origin, rejects future or
stale timestamps, suppresses duplicate event IDs in a bounded cache, and hands
accepted triggers to its scheduler boundary. The host owns Note Off timing.

## Bounded reconnect behavior

The edge pipeline sink never waits for the network. It writes into a fixed
`peer.queue_capacity`; a full queue rejects new work and increments a bounded
drop counter. The sender reconnects with capped exponential backoff and jitter.
Queued events older than `peer.stale_after` are discarded instead of replayed.
Application ping/pong messages measure liveness and RTT.

The host accepts at most `peer.maximum_connections` simultaneous instance
identities. A new authenticated connection for the same identity replaces the
old generation. Connected nodes and a bounded set of recent disconnects are
retained for `peer.recent_ttl`. Duplicate history is also capacity- and
time-bounded.

Peer configuration is restart-only:

```yaml
peer:
  enabled: true
  url: wss://host.example/api/v1/peer # required by an edge; omitted by a host
  token: replace-with-at-least-16-bytes
  queue_capacity: 1024
  maximum_connections: 64
  recent_ttl: 5m
  reconnect_base: 500ms
  reconnect_limit: 30s
  stale_after: 500ms
```

`peer.token` and `peer.url` are write-only in management configuration
responses. The operational peer snapshot contains a display-safe target with
userinfo, query, and fragment removed. It never contains tokens or authorization
headers.

## Management and UI

`GET /api/v1/peers` is local-only and returns a complete bounded snapshot. An
edge document contains its safe target, negotiated host, connection/backoff
state, queue depth/capacity, sends, drops, reconnects, send rate, timestamps,
RTT, safe last error, and observed channels. A host document contains connected
and recent nodes with identity, remote endpoint, connection/activity times,
protocol and mapping versions, note rate, accepted/rejected/duplicate/stale
totals, and observed channels.

The `/peers` workspace renders the edge destination and delivery pressure or a
searchable host node grid. Host node cards link to `/viewer?origin=<instance>`;
the viewer then filters accepted notes to that source. The workspace polls every
two seconds while visible and never affects peer delivery.

Prometheus uses bounded labels only:

```text
musical_packets_peer_connections{direction,state}
musical_packets_peer_events_total{direction,result}
musical_packets_peer_queue_depth
musical_packets_peer_queue_capacity
musical_packets_peer_round_trip_seconds
```

Instance IDs, addresses, URLs, flow IDs, and event IDs are never metric labels.
