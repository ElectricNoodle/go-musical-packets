# Embedded frontend

Stage 11 uses a React and TypeScript single-page application built by Vite. The
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

The first vertical slice loads status, the redacted canonical configuration,
capture-interface discovery, and cached MIDI discovery in parallel. It covers:

- capture enablement, interface choice, and broad BPF editing;
- MIDI enablement, preferred output, channel choice, audition, and panic;
- quiet-by-default selection plus global note-rate and polyphony limits;
- authoritative server-side validation and optimistic live-safe apply.

The active backend currently rejects configuration fields that require a
process restart. The assistant therefore reports those fields explicitly and
does not offer a false apply path. Completing configuration without replacing
the YAML file manually requires a later stage-11 pending-configuration
transaction that can persist a validated generation for the next restart.

Frontend tests use Vitest, jsdom, and Testing Library. They cover YAML/ETag
transport, live-safe validation and apply, restart classification, and MIDI
panic. The UI uses semantic controls, keyboard-visible focus, non-color status
cues, responsive layouts, and reduced-motion behavior.

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
channel; precedence tier and controlling rule ID; and deterministic root/mode
identity. The backend evaluates the latest normalized event for every flow
against one atomic policy generation, so the browser never reimplements rule
precedence.

One selected flow or any table row can open persistent rule creation. The
available scopes are an exact-flow pin, the entire observed protocol, or the
latest directional destination host/service. Users choose the durable rule ID,
name, play/monitor/ignore action, and channel. The dialog reads the isolated
rules resource, disables writes for a read-only runtime, submits the exact ETag
in `If-Match`, and reloads without retrying when another session wins the
revision race. A successful creation refreshes both live annotations and the
application's configuration snapshot so later setup edits cannot overwrite a
new rule from stale state.

A full per-predicate match explanation and the complete ordered rule editor are
the remaining stage-11 frontend work.
