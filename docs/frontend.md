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
