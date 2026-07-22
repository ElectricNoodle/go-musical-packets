# Embedded frontend

The stage-12 frontend uses a React and TypeScript single-page application built by Vite. The
production build is emitted into `internal/webui/dist` and embedded into the Go
binary. Hashed assets receive an immutable one-year cache policy; the HTML
shell and client-side route fallbacks are never cached.

The frontend and management API are exposed only when the actual HTTP listener
is loopback-bound. Metrics and probes retain their existing behavior on other
listeners. The embedded handler allows GET and HEAD, applies a restrictive
Content Security Policy and browser security headers, serves real files when
present, and falls back to `index.html` only for extensionless client routes.

## Development

Install the locked dependencies and start the development server:

```sh
npm --prefix web ci
npm --prefix web run dev
```

Vite listens on `127.0.0.1:5173` and proxies management, metrics, and probe
requests to `127.0.0.1:8080`. Run the Go process separately with a writable
owner-only configuration file.

Production builds and checks are integrated with the repository Makefile:

```sh
make build
make test
make check
```

`make build` runs the frontend build before `go build`, ensuring the generated
assets are present when the binary is compiled. Generated assets and
`node_modules` are ignored; the tracked placeholder lets Go-only packages
compile from a clean checkout before Vite runs.

## Setup assistant

The setup assistant loads status, the redacted canonical configuration,
capture-interface discovery, and cached MIDI discovery in parallel. It covers:

- capture enablement, interface choice, and broad BPF editing;
- MIDI enablement, preferred output, channel choice, audition, and panic;
- quiet-by-default selection plus global note-rate and polyphony limits;
- authoritative server-side validation and optimistic live-safe apply.

Live-safe drafts apply atomically to the active runtime. When validation finds
restart-required fields, the assistant instead saves the complete generation
through the pending-configuration resource. The saved draft survives browser
reloads, can be updated under its own ETag or discarded, and becomes active on
the next process start. The active status rail remains operational while making
the pending restart explicit.

Frontend tests use Vitest, jsdom, and Testing Library. They cover YAML/ETag
transport, live-safe validation and apply, restart classification, MIDI panic,
flow explanations, ordered rule mutations, malformed viewer frames, bounded
note history, pause behavior, and live rate derivation. The UI uses semantic
controls, keyboard-visible focus, non-color status cues, responsive layouts,
and reduced-motion behavior.

## Flow explorer

The first flow-explorer slice is available at `/flows`. It reads at most 500
newest registry entries every three seconds, pauses polling while the document
is hidden, and never starts a second request while the previous snapshot is in
flight. This keeps the browser workload bounded independently of capture rate.

The table supports address, port, protocol, and pseudonymous-ID search; sortable
protocol, endpoint, packet, byte, observed-rate, and last-activity columns;
canonical IPv4 and IPv6 endpoint display; visible-row and individual selection;
and responsive horizontal scrolling. Successive authoritative counters provide
packet and byte rates over the actual browser sampling interval. Mute and solo
changes replace the complete authoritative process-local set exposed by the
management API. Selected flows can be muted as a group or used as the complete
solo set, and every row also provides toggles.

Each row shows the backend-evaluated play, monitor, or ignore state; effective
channel; precedence tier and controlling rule name; and deterministic root/mode
identity. It also labels that identity as automatic per flow or fixed by the
controlling rule. An expandable explanation includes the backend-authored
decision reason and every configured predicate that matched. The backend
evaluates the latest normalized event for every flow against one atomic policy
generation, so the browser never reimplements rule precedence or match
semantics.

One selected flow or any table row can open persistent rule creation. The
available scopes are an exact-flow pin, the entire observed protocol, or the
latest directional destination host/service. Users choose the durable rule ID,
name, play/monitor/ignore action, channel, and whether a play rule derives its
key and mode per flow or fixes one identity across every match. Fixed identity
defaults to the selected flow's current key and mode. The dialog reads the
isolated rules resource, disables writes for a read-only runtime, submits the
exact ETag in `If-Match`, and reloads without retrying when another session wins
the revision race. A successful creation refreshes both live annotations and
the application's configuration snapshot so later setup edits cannot overwrite
a new rule from stale state.

## Ordered rule workspace

The `/rules` workspace loads the isolated rules resource and retained flows in
parallel. It shows pinned and broad tiers explicitly, authoritative counts of
currently retained flows controlled by each rule, and conservative warnings
for obvious earlier-rule shadowing. Users can reorder with buttons or
Alt+Arrow keys, enable or disable, edit, duplicate, and delete with a two-step
confirmation. The match editor accepts the complete management rule schema as
JSON rather than maintaining a second partial rule model in the browser. The
action editor exposes the same automatic or fixed musical-identity choice as
flow-based rule creation.

Every mutation sends the currently displayed strong ETag. A 412 reloads the
winning collection and asks the user to review instead of blindly replaying the
operation. Full collection import uses one atomic `PUT`; export contains only
the portable ordered rule array. Edit dialogs warn before discarding unsaved
work and protect dirty forms from accidental navigation. Read-only runtimes
remain inspectable and exportable while all write controls are disabled.

## Peer workspace

The `/peers` workspace polls the bounded `GET /api/v1/peers` snapshot every two
seconds while the page is visible. An edge sees its safe configured target,
negotiated host identity, connection or backoff state, bounded queue pressure,
send rate and totals, stale/full drops, retries, RTT, last send, safe error, and
channels used. A host sees a responsive searchable grid of connected and recent
edge nodes with identity, endpoint, authentication and protocol state,
connection/activity times, accepted-note rate, result totals, and channels.

Node identities and endpoints are display data only and never metric labels.
Tokens and raw authorization data are absent from the API. Each host node links
to `/viewer?origin=<instance>` so accepted notes can be inspected by source.
Standalone mode presents an explicit inactive state rather than fabricating a
connection.

## Musical viewer

The `/viewer` workspace consumes `WS /api/v1/events`. It displays only notes
whose Note On was accepted by the local scheduler, including management MIDI
auditions; rate-limited, polyphony-limited, retrigger-limited, unavailable, and
failed writes are absent. Every event carries a server-authored acceptance time
so the moving playhead represents local playback rather than packet-capture
time.

The primary Apache ECharts Canvas piano roll uses time horizontally and MIDI
pitch vertically. Note width represents scheduled duration, brightness
represents velocity, and color can represent channel, mode, source, or flow.
Supporting views provide an illuminated keyboard, selectable root/mode/flow
explanation with scale notes, 16
channel activity lanes, packet-versus-accepted-note rates, and a semantic event
log.

Server connections receive private queues bounded by
`performance.ui_queue_capacity`. Publishers never wait for a browser; a full
queue drops its oldest visual event in favor of the newest and reports the drop
to that client. Updates are batched at 10 Hz. The browser retains at most 512
notes, 60 rate samples, and 24 rendered log rows, reconnects with bounded
exponential delay, ignores malformed frames, and can pause or clear local
history without affecting the runtime.

The viewer accepts an optional `origin` query parameter and provides a source
selector over its bounded local history. Stage 13 peer transport, operational
snapshots, and the peer workspace are complete. Host/edge runtime composition
is the next delivery stage.
