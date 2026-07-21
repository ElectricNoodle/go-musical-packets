# Standalone Runtime

The stage-nine runtime composes live packet capture, bounded flow selection and
mapping, local MIDI scheduling, Prometheus metrics, and operational HTTP probes.
Only the `standalone` role is accepted at this stage; peer transport, edge, and
host composition remain later milestones.

## Start and validate

Configuration is strict YAML. Omitted fields inherit safe defaults, while
unknown fields, duplicate keys, additional YAML documents, and invalid values
are rejected before native capture or MIDI resources are opened.

```sh
cp config.example.yaml config.local.yaml
chmod 600 config.local.yaml
musical-packets validate-config --config config.local.yaml
musical-packets run --config config.local.yaml
```

The local copy is ignored by Git. Owner-only permissions are required before a
content-changing management write can atomically replace a config containing
write-only secrets.

The default selector state is `monitor`, so observed packets do not produce
notes until a rule or the default action explicitly selects `play`. Rules retain
file order and the first matching enabled rule wins after safety exclusions.
Lifecycle logs honor the configured `debug`, `info`, `warn`, or `error` level
and `text` or `json` format.

## Operational endpoints

The configured management listener exposes:

```text
GET /metrics
GET /healthz
GET /readyz
GET /api/v1/status
GET /api/v1/config
POST /api/v1/config/validate
PUT /api/v1/config
GET /api/v1/interfaces
GET /api/v1/midi/devices
POST /api/v1/midi/audition
POST /api/v1/midi/panic
GET /api/v1/flows
POST /api/v1/flows/mute
POST /api/v1/flows/solo
GET /api/v1/rules
POST /api/v1/rules
PATCH /api/v1/rules
PUT /api/v1/rules/{id}
DELETE /api/v1/rules/{id}
```

The `/api/v1` routes are mounted only on an actually loopback-bound listener;
metrics and probes remain available on a configured non-loopback listener.
The embedded single-page frontend is also mounted only on that loopback
listener. Its client-side routes fall back to the application shell while
missing static assets remain ordinary 404 responses.
Configuration writes use strong revision preconditions and atomic persistence.
See [management-api.md](management-api.md) for the request, security, redaction,
and failure contracts.

Health remains successful while the process can recover from a temporarily
missing MIDI device. Readiness is unavailable during startup and shutdown,
while MIDI is enabled but disconnected, or while durable and active runtime
configuration are not known to agree. Disabling MIDI makes it an optional
dependency; any selected notes are then rejected by an observable sink rather
than reported as played.

## Feedback exclusion

The actual bound HTTP port is excluded from capture in both directions. The
runtime adds a BPF clause before opening libpcap and also installs highest-
precedence source- and destination-port safety rules. The selector layer is a
defence in depth for injected sources and future capture adapters.

## Shutdown

SIGINT and SIGTERM initiate graceful shutdown. The runtime stops and awaits the
packet pipeline, closes the MIDI scheduler so it can send All Notes Off while
the selected output is still available, then cancels the MIDI manager and
gracefully stops HTTP. Temporary device absence is normal; genuine capture,
server, MIDI reset, and driver-close failures remain terminal errors.
