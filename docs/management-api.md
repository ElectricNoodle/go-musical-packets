# Management API

The stage-ten management API provides local status, transactional
configuration, live-flow, and persistent-rule endpoints on the standalone HTTP
listener.

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
GET  /api/v1/flows
POST /api/v1/flows/mute
POST /api/v1/flows/solo
GET    /api/v1/rules
POST   /api/v1/rules
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
must exactly match the decoded path ID. `DELETE` removes that item. `PATCH
/api/v1/rules` accepts a complete, duplicate-free permutation:

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
flags. The default limit is 500 and `?limit=N` accepts 1 through 5000. The
response also reports the registry total, whether the result is truncated, and
the complete temporary overlay. Rates and controlling-rule explanations are
not inferred from these aggregate snapshots; later event sampling will provide
those views accurately.

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
