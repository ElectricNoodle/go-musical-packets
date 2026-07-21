# Musical Packets: Architecture and Implementation Plan

Status: accepted for initial implementation on 2026-07-20.

This document is the canonical plan for Musical Packets. It records the agreed
product behaviour, architecture, delivery stages, and acceptance criteria. It
should be updated through deliberate design changes as implementation teaches us
more.

## Product goals

Musical Packets is a Go application that:

- Captures packets from a selected network interface on macOS and Debian Linux.
- Maps packet flows deterministically into musical modes and MIDI notes.
- Lets users monitor, mute, solo, and persist selectors for specific traffic.
- Assigns individual flows or classes of flows to MIDI channels 1 through 16.
- Finds and reconnects to USB MIDI output devices automatically.
- Runs as a standalone node, an edge node, or a MIDI-connected host.
- Streams versioned note events from edge nodes to hosts over WebSocket.
- Exposes bounded-cardinality Prometheus metrics at every processing boundary.
- Includes an embedded browser UI for configuration, flow isolation, routing,
  operational status, and real-time musical visualization.

The application must remain memory-bounded under overload. Slow browsers,
network peers, or MIDI devices must never apply unbounded backpressure to packet
capture.

## Runtime roles

- `standalone`: capture, map, and emit to local MIDI.
- `edge`: capture and map locally, then stream note events to a configured host.
- `host`: accept remote note events and emit them to local MIDI. It may also
  capture local traffic when configured.

Peers use explicit URLs in the first release. Peer discovery and multi-hop
relaying are out of scope.

## Processing pipeline

```text
Network interface / PCAP replay
             |
             v
     Broad BPF capture filter
             |
             v
      Packet normalizer
             |
             v
     Bounded flow registry ----------> Browser flow explorer
             |
             v
     Ordered flow selector
       ignore / monitor / play
             |
             v
      flow-mode-v1 mapper
  mode, root, note, velocity,
  duration, and MIDI channel
             |
             v
      Bounded note event bus
          |             |
          v             v
      Local MIDI     WebSocket peer
          ^             |
          +-------------+ host receiver
```

All queues are bounded. Each stage has an explicit drop policy and metrics.
Packet payloads are neither retained nor transmitted.

## Musical mapping: flow-mode-v1

A canonical bidirectional flow consists of protocol, addresses, and ports in a
direction-independent order. A stable, seeded hash assigns each flow:

- One of Ionian, Dorian, Phrygian, Lydian, Mixolydian, Aeolian, or Locrian.
- A root pitch class from C through B.
- A stable musical identity for the life of the flow.

Packets walk through that flow's scale:

- Packet length selects the scale degree.
- Direction selects lower or upper register.
- Transport protocol contributes an interval offset.
- TCP flags provide accents.
- Packet size maps logarithmically to MIDI velocity.
- Inter-arrival time selects a quantized duration.
- A configurable seed makes PCAP replay deterministic.

Output is clamped to a configured MIDI range, initially C2 through C7. Safety
controls include maximum note rate, maximum polyphony, minimum retrigger time,
duration bounds, guaranteed Note Off scheduling, and All Notes Off on shutdown,
device loss, or panic.

The mapper is replaceable behind a small interface. PCAP replay is a first-class
command and the golden reference for deterministic tests.

## Traffic selection and routing

### Broad capture filter

A BPF expression reduces traffic before decoding. The UI provides common filter
controls and an advanced raw-BPF field. Application control, metrics, and peer
traffic are excluded by default to prevent musical feedback loops.

### Flow registry

The bounded registry:

- Canonicalizes both directions into one flow.
- Assigns stable pseudonymous flow IDs.
- Aggregates packet and byte rates for the UI.
- Expires inactive flows after a configurable TTL.
- Evicts according to a documented bounded policy.
- Preserves pinned rules after an observed flow expires.
- Aggregates UI updates rather than forwarding every packet.

Addresses and flow IDs are never Prometheus labels.

### Ordered selector rules

Rules may match an exact flow, address, CIDR, protocol, port or range, direction,
packet-size range, TCP flags, source instance, or combinations thereof.

Actions are `ignore`, `monitor`, or `play`. A playing rule can assign channel,
automatic or fixed mode/root, register, velocity multiplier, rate limit,
polyphony, name, and color.

Precedence is:

1. Safety exclusions.
2. Temporary UI solo/mute state.
3. Exact pinned-flow rules.
4. Ordered user rules; first match wins.
5. Global default.

The default is `monitor`, so first launch is not an uncontrolled wall of sound.

## MIDI

The MIDI layer is isolated behind an output interface and scheduler. Device
selection prefers a configured exact name, then a configured regular expression,
then the first usable physical output. The application starts successfully
without a device, polls for attachment, and reconnects to the preferred device.

Channels are configured as user-facing values 1 through 16 and converted to
wire values 0 through 15 only inside the MIDI adapter.

The originating instance assigns a channel to each note event. A remote host
validates and honors that channel so several flows or edge nodes can address
different host instruments. A host may optionally allowlist or remap channels,
but transparent preservation is the default.

## Peer transport

The initial peer protocol is versioned JSON over WebSocket. Messages include
`hello`, `note`, `ping`, `pong`, `error`, and graceful close. A note event carries:

- Event ID, source instance, and source sequence.
- Mapping version and pseudonymous flow ID.
- Mode, root, note, velocity, duration, and MIDI channel (1 through 16).
- Creation timestamp for diagnosis, not synchronized playback.

The receiving host plays accepted triggers immediately and owns Note Off
scheduling. Events include origin and ID for duplicate/loop prevention. Clients
use exponential reconnect with jitter and retain only a short, bounded queue;
stale music is dropped rather than replayed.

The protocol enforces message limits, strict decoding, version negotiation,
bearer authentication, restrictive origins, and TLS directly or through a
trusted reverse proxy.

## HTTP and management API

The HTTP listener provides `/metrics`, `/healthz`, `/readyz`, the peer note
WebSocket, the management API, and the embedded frontend.

Proposed management routes:

```text
GET    /api/v1/status
GET    /api/v1/config
POST   /api/v1/config/validate
PUT    /api/v1/config
GET    /api/v1/interfaces
GET    /api/v1/midi/devices
POST   /api/v1/midi/audition
POST   /api/v1/midi/panic
GET    /api/v1/flows
GET    /api/v1/rules
POST   /api/v1/rules
PUT    /api/v1/rules/{id}
DELETE /api/v1/rules/{id}
POST   /api/v1/flows/solo
POST   /api/v1/flows/mute
WS     /api/v1/events
WS     /v1/notes
```

Configuration updates use strict decoding, optimistic revisions, validation,
atomic persistence, and rollback. Secrets are redacted/write-only. The
management listener binds to loopback by default; remote management requires
authentication and TLS, with CSRF and WebSocket-origin protection.

## Frontend

The frontend is a React/TypeScript single-page application built with Vite and
embedded into the Go binary with `go:embed`. Apache ECharts using Canvas renders
bounded real-time visualizations. There is no separate production frontend
service or server-side rendering layer.

### Screens

The setup assistant covers interface and permission checks, BPF construction,
role selection, MIDI discovery/test, default channel, peers, authentication,
rate/polyphony limits, validation, and apply.

The live dashboard shows capture/MIDI/peer health, packet and note rates, queue
utilization, drops, active flows, connected device, global pause, and MIDI panic.

The flow explorer is primarily a sortable, searchable table. It shows endpoints,
protocol, ports, rates, last activity, controlling rule, mode/root, channel, and
play state. Users can select, solo, mute, pin, generalize into a rule, assign
channel/mode/root/color/limits, and inspect why a rule matched.

The rule editor supports ordering, keyboard operation, validation, shadowing
warnings, match-count previews, enable/disable, duplicate, import/export, and
unsaved-change protection. The Go backend remains authoritative for evaluation.

MIDI and peer screens provide device selection/reconnect state, user-facing
channel labels, audition, active notes, panic, peer identity, events, drops,
round-trip time, channel policy, and authentication state.

### Musical viewer

The primary visualization is a live piano roll:

- Time on the horizontal axis and MIDI pitch on the vertical axis.
- Width represents duration; opacity/brightness represents velocity.
- Color represents channel by default, with mode/source/flow alternatives.
- A moving playhead represents host playback time.

Supporting views include an illuminated keyboard, root/mode scale display,
per-channel activity lanes, packet-rate versus note-rate strip, selected-flow
mapping explanation, and bounded accessible event log.

The viewer consumes actual accepted scheduler events. Server updates are
aggregated to approximately 10 Hz, histories use ring buffers, and UI slowness
or disconnection cannot affect capture or MIDI. Reduced motion, keyboard
navigation, non-color cues, and accessible labels are required.

## Configuration and CLI

YAML is the persistent format, with common CLI overrides and secrets accepted
from environment variables or files. Unknown fields and invalid combinations
fail before capture starts.

```text
musical-packets run --config config.yaml
musical-packets interfaces
musical-packets devices
musical-packets validate-config --config config.yaml
musical-packets replay recording.pcap --config config.yaml
musical-packets version
```

Configuration sections are `instance`, `capture`, `mapping`, `performance`,
`midi`, `server`, `peer`, `metrics`, `logging`, and `rules`.

## Metrics

Metrics use the `musical_packets` namespace, an injected Prometheus registry,
and only bounded labels.

Capture and flow metrics cover packets/bytes by bounded protocol, errors, drops,
processing latency, queue depth/capacity, capture state, active/selected flows,
rule evaluations, registry eviction, filtering, and UI-update drops.

Mapping metrics cover events by mode/result, duration, velocity, note duration,
active flows, and eviction.

MIDI metrics cover device state/reconnects, notes by source/channel/result,
errors, write latency, queue depth, active notes by channel, and drops.

Peer metrics cover connections, reconnects, peers, events and bytes by direction,
channel and result, write latency, receive-to-MIDI latency, ping round trip,
queue depth, drops, and protocol errors.

Management metrics cover UI clients/connections/events/drops, normalized API
route latency/counts, and configuration update results. Standard Go/process
collectors and build information are included.

## Source layout

```text
cmd/musical-packets/
internal/app/
internal/capture/
internal/config/
internal/flow/
internal/httpserver/
internal/managementapi/
internal/metrics/
internal/midi/
internal/music/
internal/packet/
internal/transport/
internal/uistream/
internal/webui/
internal/testutil/
web/src/
web/e2e/
docs/
testdata/pcap/
```

Clocks, capture sources, mapper, MIDI output, and peer transport are interfaces.
Pure domain packages do not depend directly on Prometheus.

## Delivery stages

Stages 1 through 10 are implemented. The current implementation frontier is
stage 11, frontend foundations, the setup assistant, and the flow explorer.

1. Architecture record and exact behavioral specification.
2. Go foundations, config, logging, lifecycle, build metadata, and CI.
3. macOS/Debian live capture and MIDI technical spike.
4. Pure musical mapper.
5. Flow registry, rule engine, and feedback exclusions.
6. Offline deterministic PCAP replay.
7. Live capture pipeline and metrics.
8. MIDI discovery, scheduler, channels, hot-plug recovery, and metrics.
9. Standalone composition.
10. Management API and transactional configuration.
11. Frontend foundations, setup assistant, and flow explorer.
12. Piano roll and musical viewer.
13. Peer WebSocket protocol with channel preservation.
14. Host/edge composition.
15. Security, accessibility, profiling, soak testing, packaging, and operations.

Instrumentation is implemented with each stage rather than added afterward.

## Testing strategy

Unit and golden tests cover every mode/root, canonical bidirectional hashing,
determinism, MIDI bounds, durations/rates, configuration, selector precedence,
device selection, scheduling, Note Off, and All Notes Off.

Property and fuzz tests cover arbitrary packet metadata, reversed endpoints,
WebSocket frames, PCAP records, accepted config invariants, and the guarantee that
every Note On is eventually stopped or reset.

Integration tests run sanitized PCAP fixtures through fake MIDI, real in-process
WebSockets, authentication/version failures, reconnects, stale-event dropping,
multiple edges, backpressure, metrics, health transitions, graceful shutdown,
the race detector, and goroutine leak checks.

Frontend tests use Vitest and React Testing Library for behavior and Playwright
against a real Go process with fake boundaries. They cover setup, monitoring,
solo/mute/pin, rule/channel assignment, multi-channel edge-to-host streaming,
live edits, reconnects, MIDI panic/recovery, authentication, accessibility, and
bounded high-traffic rendering.

Opt-in platform tests cover loopback capture, Linux network namespaces/veth and
virtual MIDI, macOS IAC, and real USB attach/detach/reattach. Benchmarks and soak
tests verify mapper throughput, allocations, PCAP replay, WebSocket encoding,
slow consumers, bounded memory, and absence of stuck notes.

CI gates include formatting, unit tests, race tests, vet/static analysis, fuzz
smoke corpus, deterministic replay, frontend unit/E2E tests, macOS/Debian build
coverage, dependency vulnerability checks, and reproducible build metadata.

## Acceptance criteria

- Live capture works on macOS and Debian with clear permission guidance.
- A fixed PCAP and seed always produce the same notes.
- Generated notes belong to their declared mode/root and remain in MIDI bounds.
- Traffic is silent by default while flows are monitored.
- Users can isolate, solo, mute, and persist traffic selectors.
- Rules assign exact or general flows to channels 1 through 16.
- USB MIDI is selected automatically and recovered after reconnection.
- No note remains stuck after shutdown, panic, device loss, or peer loss.
- Edge nodes reconnect and stream versioned, authenticated note events.
- Originating channels are preserved so remote flows address distinct host
  instruments.
- Stale disconnected events are dropped.
- Application control traffic cannot form a musical feedback loop by default.
- Queues and memory remain bounded under overload.
- Metrics cover capture, selection, mapping, MIDI, peers, and management without
  unbounded labels.
- The UI can complete configuration without hand-editing YAML, accurately shows
  accepted notes, and cannot delay the audio pipeline.
- Headless configuration and operation remain fully supported on Debian.
