# Management API

The first stage-ten management slice provides local status and transactional
configuration endpoints on the standalone HTTP listener.

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
