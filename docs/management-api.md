# Management API

The stage-ten management API provides local status, transactional
configuration, capture-interface and MIDI discovery, MIDI safety controls,
live-flow, and persistent-rule endpoints on the standalone HTTP listener.

## Exposure model

The API is mounted only when the listener's actual bound address is IPv4 or
IPv6 loopback. A wildcard or non-loopback listener continues to serve metrics
and probes, but `/api/v1` is not mounted. Remote management will require the
later authentication and TLS milestone.

Every API request must also come from a loopback peer, use `localhost` or a
loopback IP in `Host` with the actual bound port, and, when `Origin` is present,
use that exact same local origin. Forwarded headers are not trusted and CORS is
not enabled. Responses use `Cache-Control: no-store` and
`X-Content-Type-Options: nosniff`.

## Routes

```text
GET  /api/v1/status
GET  /api/v1/config
POST /api/v1/config/validate
PUT  /api/v1/config
GET  /api/v1/interfaces
GET  /api/v1/midi/devices
POST /api/v1/midi/audition
POST /api/v1/midi/panic
GET  /api/v1/flows
POST /api/v1/flows/mute
POST /api/v1/flows/solo
GET    /api/v1/rules
POST   /api/v1/rules
PUT    /api/v1/rules
PATCH  /api/v1/rules
PUT    /api/v1/rules/{id}
DELETE /api/v1/rules/{id}
```

`GET /api/v1/config` returns canonical full YAML and a strong `ETag` containing
an opaque, process-local revision token. The token is deliberately keyed rather
than exposing the persisted file digest, so a caller cannot use a redacted
response as an offline oracle for the mapping seed or peer URL. Tokens change
when the process restarts; clients must GET the representation again. The
management representation replaces both secrets with reserved write-only
placeholders. Sending an unchanged placeholder back preserves the active value.
When a write-only value is active, validation and update requests must retain
its placeholder; concrete values are rejected uniformly so the endpoints
cannot be used to test secret guesses. Change those values in the file while
the service is stopped, then restart it.

Configuration requests require `Content-Type: application/yaml`, identity or
no content encoding, and a body no larger than 1 MiB. Decoding is strict:
unknown fields, duplicate fields, invalid values, and multiple YAML documents
are rejected. Decoding overlays the submitted document on defaults, so an
omitted field means "reset to its default," not "leave unchanged." The safe
editing workflow is therefore GET, edit the full response, then validate and
PUT it. Canonical admission reserves a fixed 64 KiB allowance for hidden value
expansion, keeping the size decision independent of the actual secret lengths.

Validation returns the active revision it classified in the JSON body and
separates hot fields from fields that require restart. It does not return an
`ETag`, because the response also depends on the submitted candidate. The
current hot allowlist is:

- `mapping.default_state`
- `mapping.default_channel`
- `rules`

All other configuration fields require a process restart. Exact-flow rules are
evaluated in the pinned tier ahead of broad user rules.

## Capture interfaces

`GET /api/v1/interfaces` performs current packet-capture device discovery. Its
response contains the configured selector, the device to which that selector
currently resolves when one is available, and a stable name-ordered list of
devices. Each device includes its name, description, canonical address
prefixes, link-up state, and loopback state. An empty `selected` value means the
configured explicit device is absent or automatic selection found no up,
addressed, non-loopback device. Discovery remains available when capture is
disabled so the setup assistant can present valid choices.

The route accepts GET and HEAD without query parameters. Native discovery
failures return 503 without exposing driver details.

## MIDI management

`GET /api/v1/midi/devices` returns the runtime's cached discovery snapshot. It
does not enumerate the native driver on the request goroutine. The response
distinguishes disabled, successful, and failed discovery; a failed discovery
may accompany a still-connected current output. Device numbers are volatile
and are not durable identities.

`POST /api/v1/midi/audition` accepts strict JSON containing `channel` 1 through
16, `note` 0 through 127, `velocity` 1 through 127, and `duration_ms` 1 through
10000. The note passes through the same scheduler, rate, polyphony, retrigger,
device-transition, and Note Off guarantees as packet-triggered notes. Success
returns 202; scheduler safety limits return 429; disabled MIDI returns 409; and
temporary output failure returns 503.

`POST /api/v1/midi/panic` requires an empty, unencoded request and clears the
local scheduler before attempting All Notes Off on all 16 channels. Success
returns 204. Audition and panic are process-local operations and do not require
or change the configuration revision.

## Optimistic update workflow

```sh
curl --fail --dump-header headers.txt \
  http://127.0.0.1:8080/api/v1/config \
  --output config.edit.yaml

curl --fail \
  -H 'Content-Type: application/yaml' \
  --data-binary @config.edit.yaml \
  http://127.0.0.1:8080/api/v1/config/validate

etag=$(awk 'tolower($1) == "etag:" {sub(/\r$/, "", $2); print $2}' headers.txt)
curl --fail-with-body -X PUT \
  -H 'Content-Type: application/yaml' \
  -H "If-Match: ${etag}" \
  --data-binary @config.edit.yaml \
  http://127.0.0.1:8080/api/v1/config
```

`PUT` requires exactly one strong quoted opaque `If-Match` value. Missing and
malformed preconditions return 428 and 400. Status, GET, and validation identify
the active policy revision. A stale write returns 412 with a token for the
current durable revision in `ETag`; when external file drift has made the
controller out of sync, retrying with that returned token explicitly reconciles
the candidate against the durable generation. A successful PUT returns the new
active-and-durable token. Restart-required and read-only updates return 409;
invalid candidates return 422; persistence, reconciliation, and apply failures
return 503. Errors use `application/problem+json`.

Persistence uses compare-and-swap, atomic rename, directory sync, and
exact-byte/mode rollback. A content-changing write requires the existing config
file to have owner-only mode bits, normally `0600`; owner, group, ACLs,
extended attributes, and other metadata are not portably preserved by the
rename. The parent directory must therefore not confer broader default or
inherited ACL access on new files. Config, selector, and temporary overlays
publish as one immutable generation. Advisory-lock waits honor request and
shutdown cancellation; once a rename commits, the committed result is still
published even if the client disconnects. Durability uncertainty, external
drift, and failed rollback make readiness fail and are visible through the
sanitized status state.

The status `writable` field means the process was started with a durable config
repository. It does not promise that a particular mutation will pass mode,
revision, validation, or persistence checks.

## Persistent rules

`GET /api/v1/rules` returns the complete ordered rule collection, its writable
state, and the same opaque full-configuration revision used by the config API.
The response also carries that revision as a strong `ETag`. Rule writes require
the exact token in `If-Match`, participate in the same atomic concurrency domain
as full config writes, and return the updated collection and resulting `ETag`.

`POST /api/v1/rules` appends one rule object and returns 201 with its encoded item
URL in `Location`. `PUT /api/v1/rules/{id}` replaces a rule in place; the body ID
must exactly match the decoded path ID. `DELETE` removes that item. `PUT
/api/v1/rules` atomically replaces the complete collection and requires an
explicit array wrapper:

```json
{"rules":[{"id":"broad-web","name":"Web","enabled":true,"match":{"protocol":"tcp"},"action":{"state":"play","channel":4}}]}
```

An explicit empty array clears the collection; a missing or null array is
invalid. `PATCH /api/v1/rules` accepts a complete, duplicate-free permutation:

```json
{"order":["exact-dns","broad-web"]}
```

Rule requests use strict JSON with exact, case-sensitive field names, no
duplicates or unknown fields, identity encoding, and a 1 MiB limit. Existing
rule IDs remain arbitrary nonblank strings. Clients must encode an item ID as
one URL path component; the server unescapes it exactly once without trimming,
cleaning, or case normalization.

Missing and malformed preconditions return 428 and 400. A stale mutation
returns 412 and the current durable `ETag`; invalid rules and permutations
return 422, duplicate identities and path/body identity conflicts return 409,
and a missing item returns 404. Read-only changes return 409, while runtime,
persistence, and reconciliation failures return 503. During reconciliation, a
rule mutation is rebased atomically onto the durable configuration so unrelated
external changes are not overwritten; external changes that require restart
remain rejected.

Stored order is preserved, but exact-flow rules always occupy the pinned tier
ahead of broad rules. Reordering therefore changes effective first-match
precedence within each tier, not between those tiers.

## Live flows and temporary overlays

`GET /api/v1/flows` returns retained bidirectional flows newest-first, with a
stable ID, canonical endpoints, protocol, first and last observation times,
packet and byte counters, directional packet counters, and current mute/solo
flags. Every flow also includes its effective `state`, user-facing `channel`,
`rule_tier`, optional `rule_id` and `rule_name`, a backend-authored
`decision_reason`, the complete `matched_predicates` list, deterministic `mode`,
and numeric root pitch class. Empty predicate lists are encoded as `[]`.
`latest_source` and `latest_destination` expose the direction of the newest
retained metadata event so a client can construct directional rules without
interpreting canonical endpoint order as packet direction. The default limit
is 500 and `?limit=N` accepts 1 through 5000. The
response also reports the registry total, whether the result is truncated, and
the complete temporary overlay.

The registry retains the latest normalized metadata event for each bounded
flow; packet payload is absent from that type. One immutable selector and
overlay generation evaluates all flows in an API response, so a config or
overlay publication cannot produce a mixed page. Directional, size, and flag
rules are evaluated against that latest event, meaning the explanation can
truthfully change when the most recently observed packet travels in the other
direction. The API exposes cumulative counters rather than inventing a rate
window; the frontend labels deltas between successive snapshots as observed
rates.

Mute and solo requests use strict JSON:

```json
{"flow_ids":["0123456789abcdef01234567"]}
```

`POST /api/v1/flows/mute` and `POST /api/v1/flows/solo` each replace that
complete set; an explicit empty array clears it. IDs must be unique 24-character
lowercase hexadecimal values. The response contains both sets in sorted order.
These overlays are bounded, temporary, and deliberately not persisted or
revisioned, so concurrent writes to the same set are last-writer-wins. Mute and
solo changes are serialized with config swaps and preserve one another.

Safety exclusions still take precedence, followed by temporary mute, temporary
solo, exact-flow pinned rules, broad user rules, and the configured default.
Overlay writes are allowed for a read-only config runtime, but rejected while
the application is starting, stopping, or its policy state is unhealthy.

The frontend creates persistent rules through the normal rules collection,
never through the temporary overlay routes. It first reads the collection ETag,
then submits one exact-flow, protocol, or latest-destination-service rule with
that exact `If-Match` value. A 412 response causes a read-only refresh and asks
the user to review and resubmit; the client does not blindly retry a mutation.

## Metrics

Management instrumentation uses normalized, bounded labels. Request counts use
route, method, and `success`, `client_error`, or `server_error` result labels;
request latency uses route and method. Arbitrary rule IDs and unknown paths are
collapsed to fixed route templates. Configuration PUT attempts also record one
of `success`, `forbidden`, `precondition`, `conflict`, `invalid`, `unavailable`,
or `error`.

```text
musical_packets_management_api_requests_total{route,method,result}
musical_packets_management_api_request_duration_seconds{route,method}
musical_packets_management_config_updates_total{result}
```
