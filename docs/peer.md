# Peer Transport

Stages 13 and 14 provide the transport-independent protocol and compose it into
the `edge` and `host` command runtimes. An edge maps captured traffic into a
bounded reconnecting sender. A host accepts authenticated notes and gives them
to the same coordinated MIDI scheduler used by optional local host capture.

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

An edge uses `instance.role: edge`, disables local MIDI, enables capture and
peer transport, and supplies the host URL. A host uses `instance.role: host`,
enables MIDI and peer transport, omits `peer.url`, and listens on an address
reachable by its edges. For example:

```yaml
# host
instance: {id: studio-host, role: host}
server: {listen_address: "0.0.0.0:8080"}
peer: {enabled: true, token: shared-secret-at-least-16-bytes}

# edge
instance: {id: office-edge, role: edge}
midi: {enabled: false}
peer:
  enabled: true
  url: ws://studio-host:8080/api/v1/peer
  token: shared-secret-at-least-16-bytes
```

Run each configuration with `musical-packets run --config <path>`. Use `wss://`
directly or through a trusted TLS reverse proxy outside a trusted private
network.

Before capture starts, an edge resolves its configured host and excludes that
address-and-port pair in both BPF and selector safety rules. A DNS address
change therefore requires a process restart. The host's listener port is
excluded by the existing local HTTP safety rules.

`peer.token` and `peer.url` are write-only in management configuration
responses. The operational peer snapshot contains a display-safe target with
userinfo, query, and fragment removed. It never contains tokens or authorization
headers.

## Management and UI

`GET /api/v1/peers` is local-only and returns a complete bounded snapshot with
the role and whether transport is enabled. An
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

## Readiness and shutdown

An edge is ready only while its host handshake is connected. A host does not
require an edge to be connected, but still requires its configured MIDI output
when MIDI is enabled. Shutdown first stops local capture, then stops the edge
sender or closes and awaits every host WebSocket, and only then resets and
closes MIDI. This prevents remote Note On writes from crossing scheduler
teardown.
