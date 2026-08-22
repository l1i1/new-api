# Cyber Policy Interception and Risk-Control Technical Specification

## Implementation boundary

This feature adds a protocol-independent cyber-policy error contract to the
existing New API relay. It is not a second content moderation provider. The
implementation reuses the existing Gin context, `RelayInfo`, relay adapters,
GORM models, `LogTypeError`/consume logs, Redis cache, and notification
service.

The current request path is approximately:

```text
Request ID / auth
  -> TokenAuth
  -> CyberSessionGuard          (new, before channel distribution)
  -> ModelRequestRateLimit
  -> Distribute                  (selects group/channel)
  -> user/group rate limits
  -> controller.Relay
       -> typed request validation and RelayInfo
       -> upstream adapter
       -> usage/billing or cyber finalization
```

`CyberSessionGuard` is the only pre-distribution component. Cyber-policy
detection itself runs at upstream response boundaries because the signal is
returned by the upstream service. The implementation must not depend on the
content moderation gate being enabled.

## Configuration contract

Store one normalized JSON value in the existing `Option` table under
`cyber_policy.config`. Use a dedicated root-authenticated controller instead
of the generic option editor so the API can redact secrets and validate the
security-sensitive fields.

### Stored configuration

```json
{
  "risk_control_enabled": true,
  "all_groups": true,
  "group_ids": [],
  "all_models": true,
  "models": [],
  "cyber_policy_exclude_from_ban_count": true,
  "cyber_session_block_enabled": false,
  "cyber_session_block_ttl_seconds": 3600
}
```

Normalization rules:

- `group_ids` and `models` are trimmed, de-duplicated, and retained only when
  their corresponding `all_*` field is false.
- `cyber_session_block_ttl_seconds` must be positive and bounded before it is
  converted to `time.Duration`; the initial upper bound is 30 days.
- An absent field receives the defaults above for upgrade compatibility.
- A malformed or invalid persisted value is an error. It must not silently
  enable a broader risk scope or session blocking.
- `risk_control_enabled` gates risk event, email, ban-count, and session-block
  side effects. It does not disable detection, client forwarding, no-retry,
  or usage settlement.

### Admin API

All endpoints require `RootAuth`.

- `GET /api/cyber-policy/config` returns normalized settings. It never returns
  request credentials or raw Redis identifiers.
- `PUT /api/cyber-policy/config` validates and atomically replaces the
  normalized settings. A database failure leaves the previous in-memory
  policy active and returns an error.
- `GET /api/cyber-policy/logs` returns cyber-policy risk/operations records
  with offset/limit pagination and filters for user, group, model, protocol,
  request ID, action, and time range. It must not expose raw prompt text or
  session identifiers.

The implementation may use the existing content moderation log screen and
query code internally, but `cyber_policy` must remain an explicit action and
must not be mixed into ordinary content-moderation configuration.

## Data contracts

### `CyberPolicyMark`

Keep the mark in Gin request context for HTTP and in a per-turn state object
for WebSocket requests. The first mark wins.

```go
type CyberPolicyMark struct {
    Code             string
    Message          string
    RawBody          []byte // bounded to 4096 bytes, in-memory only
    UpstreamStatus   int
    ClientStatus     int
    Protocol         string
    RequestID        string
    UpstreamRequestID string
    InputTokens      int
    OutputTokens     int
    UsagePresent     bool
    Stream           bool
    SessionKey       string // SHA-256 digest, never raw
}
```

The actual project type should match repository naming and use pointer fields
where zero and absent usage must be distinguished. All token values are
normalized to non-negative bounded integers before entering billing.

`RawBody` is retained only long enough for the response/operations builder.
Persisted logs keep the stable code/message, a bounded sanitized preview or
hash, and error metadata; they do not persist the full body.

### Detection

Provide one service-level detector used by all adapters:

```text
DetectCyberPolicy(body, upstreamStatus, stream, protocol, usage) -> mark, hit
```

The detector:

1. parses the bounded response envelope with the repository JSON helpers;
2. reads both `error.code` and `response.error.code`;
3. compares the trimmed value case-insensitively with `cyber_policy`;
4. extracts the message from the same error object;
5. stores the first mark only; and
6. returns an explicit skip-retry/forwarded result to the caller.

The detector must accept a response where the error is nested in a protocol
adapter's converted envelope. It must not classify an arbitrary message that
merely contains the text `cyber_policy`.

### Status semantics

- Non-stream: `UpstreamStatus` and `ClientStatus` are the upstream HTTP status
  (normally `400`); the original body is forwarded once.
- Stream: `UpstreamStatus` is the HTTP status that opened the stream and
  `ClientStatus` is `200` once SSE headers have been committed. The event's
  policy error remains machine-readable even though HTTP status cannot be
  changed after the first write.
- WebSocket: status is represented by the protocol error event; a local
  session block uses HTTP `403` before upgrade or close code `1008` after an
  already-established connection, according to the current handshake stage.

## Request lifecycle

### 1. Pre-distribution session check

Add `CyberSessionGuard` after token authentication and before
`middleware.Distribute` for HTTP relay routes. The guard must use reusable
body storage and rewind the body before returning so distributor and relay
validation see the original bytes.

Derive the session identity from the first non-empty value among:

1. `session_id` header;
2. `conversation_id` header;
3. body `prompt_cache_key` when it is a JSON string.

The guard requires a positive API-key/token ID. Build the Redis key from the
normalized identifier and token ID, then hash the complete seed with SHA-256:

```text
cyber_session_block:<hex sha256(token_id + NUL + identifier)>
```

Never use the raw identifier as a Redis key or log field. Missing identifiers,
missing token IDs, non-string body values, and Redis read failures are
fail-open. A Redis hit returns an OpenAI-compatible permission error with
HTTP `403` and code `session_blocked_by_cyber_policy`; it must stop the chain
before `Distribute`, quota estimation, pre-consume, and upstream forwarding.

For `/v1/realtime`, the guard checks handshake headers/query state before
upgrade. For an already-established connection, the Realtime turn handler
uses the same key derivation when a turn carries an explicit session value.

### 2. Typed request and channel selection

`controller.Relay` continues to validate the typed request and build
`RelayInfo` as it does today. No cyber inspection is performed on client prompt
text. Channel affinity and the existing distributor are allowed to select the
initial channel; a cyber result never triggers a second selection.

The request context stores the selected channel ID, token ID, group, model,
protocol, request ID, and session digest so asynchronous finalization does not
depend on mutable Gin context values.

### 3. Upstream response interception

Every supported response boundary must call the detector before returning a
generic adapter error:

| Boundary | Required hook |
| --- | --- |
| OpenAI Chat/Completions non-stream | After response body read and before `RelayErrorHandler` result is returned. |
| OpenAI Chat SSE | In the event scanner when a failure event/chunk is parsed, after extracting any usage in that event. |
| OpenAI Responses non-stream | After `GetOpenAIError` and usage extraction. |
| OpenAI Responses SSE | In `response.failed`/`response.error` handling, with `response.usage` parsed first. |
| Responses compact | Reuse the Responses detector before compaction-specific response rewriting. |
| Anthropic Messages | In both JSON and SSE handlers, before conversion hides the original error. |
| OpenAI-compatible advanced/passthrough | At the common HTTP status/error parser and stream scanner. |
| Realtime WebSocket | In the per-turn server-error/failed event parser, before the turn exits. |

The hook order is mandatory for streams:

```text
parse failure event
  -> merge usage into turn usage
  -> detect cyber_policy
  -> mark once
  -> emit protocol error and terminal marker
  -> return forwarded sentinel
```

The hook must not mark a generic 400 merely because a body is malformed or
because another error code is present.

### 4. Forwarded sentinel and retry suppression

Add a stable internal result such as `ErrCyberPolicyForwarded` plus a
`CyberPolicyHandled` request state. The exact Go symbol may follow repository
naming, but the observable contract is fixed:

- `IsSkipRetryError` returns true for the forwarded result;
- the relay loop treats the result as already written/handled and does not
  write a second generic response;
- `shouldRetry` returns false before status-code policy evaluation;
- `processChannelError` does not call channel disable/cooldown logic;
- generic `RecordErrorLog` is skipped when a mark exists; and
- the finalizer is invoked once after the adapter has returned.

For non-stream responses, the adapter or common error handler writes the
bounded original body exactly once, with the original status. For Chat SSE,
the protocol writer emits the standard error chunk and `[DONE]`. For Responses
and Anthropic, the existing protocol writer emits the corresponding terminal
error envelope. The forwarded sentinel prevents controller-level deferred
error handling from appending another JSON body.

## Billing design

### State machine

```text
pre-consume reservation
       |
       +-- normal success --> existing Post*ConsumeQuota settlement
       |
       +-- ordinary error --> existing refund/error path
       |
       +-- cyber mark ------> CyberPolicyFinalizer
                              |-- usage present: settle once + consume log
                              |-- usage absent: no fabricated output usage
                              |                   safe refund/fallback policy
                              +-- risk/session/ops side effects
```

The controller must set `CyberPolicyHandled` before the ordinary error defer
runs. That defer must not blindly refund a reservation that the cyber
finalizer is going to settle. The implementation must choose one explicit
mechanism and test it:

- preferred: run the billing settlement step synchronously from the finalizer
  before returning, then dispatch risk/session/email/ops work asynchronously;
  or
- use a durable pending settlement record that makes the reservation state
  recoverable before the request defer returns.

The first implementation should use the preferred path unless an existing
billing transaction API makes a durable pending record cheaper and safer.

### Usage mapping

Use upstream values only:

- `input_tokens` maps to `PromptTokens`;
- `output_tokens` maps to `CompletionTokens`;
- total usage is recomputed from normalized non-negative fields when the
  upstream total is missing;
- explicit upstream zero remains zero;
- negative, overflowing, or malformed values are rejected/clamped through the
  existing quota safety helpers and never cast unchecked to `int`.

The final consume log uses the client-facing model, selected group/channel,
token ID, stream flag, request ID, and:

```json
{
  "cyber_policy": true,
  "request_type": "cyber",
  "usage_source": "upstream",
  "upstream_error_code": "cyber_policy",
  "input_tokens": 123,
  "output_tokens": 7
}
```

`request_type=cyber` is the stable reporting discriminator. The implementation
must preserve all existing tiered/model/group ratio calculations and use the
same quota conversion helpers as ordinary text billing. Cyber policy must not
invoke `ChargeViolationFeeIfNeeded`.

If the upstream failure contains no usable usage, the implementation must not
pretend that output tokens were consumed. It should settle any safe prompt
usage explicitly supported by the existing billing contract; otherwise refund
the reservation and write a bounded billing-unavailable operations signal.
This case must be visible in tests and metrics rather than silently becoming
a free, zero-token consume record.

### Idempotency

Use a request/turn-local finalization key composed of request ID and WebSocket
turn ID. A second callback, adapter conversion, or deferred handler must
observe the key and return without another quota mutation or consume row.
Cross-node duplicate finalization is prevented by the durable consume/request
identifier contract or a database uniqueness guard, depending on the existing
log implementation. Redis alone is not sufficient for billing correctness.

## Risk event and ban-count behavior

### Event persistence

Reuse `ContentModerationLog` storage when possible to avoid a second prompt
audit table. Create one row with:

```text
Action          = cyber_policy
Flagged         = true
Blocked         = true
Category        = cyber_policy
Score           = 1.0
Mode            = post_upstream
Protocol        = normalized protocol
RequestID       = gateway request/turn ID
RequestPath     = client path
Error           = sanitized bounded upstream message
Excerpt         = [redacted]
ExcerptHash     = hash of the bounded error identity, not prompt text
```

The row is created only when `risk_control_enabled` is true and group/model
scope matches. It does not depend on `content_moderation.config.enabled`,
`mode`, `sample_rate`, or `record_non_hits`.

If the existing schema needs a discriminator for efficient count exclusion,
use `Action` and an indexed query rather than a database-specific enum or JSON
column. Migrations must remain valid for SQLite, MySQL, and PostgreSQL.

### Count exclusion

Extend the existing flagged-count query with an explicit
`excludeCyberPolicy` decision. With the default setting true:

```text
flagged = true
AND action <> 'cyber_policy'
AND id > violation_reset_after_id
AND created_at >= rolling_window_start
```

The exclusion applies to current and historical rows. When an operator turns
the setting off, cyber rows become eligible without rewriting old records.
The user violation reset boundary continues to retain rows and excludes only
records at or below the stored boundary.

### Email

Use a dedicated notification key such as
`notify.content_moderation.cyber_policy_notice` and a subject equivalent to
`[{{.SystemName}}] Cyber policy interception notice`. Do not reuse the ordinary
account-risk wording. The body may include category, score, count, model, and
request ID, but not prompt text, headers, API keys, raw session IDs, or the
full upstream body.

The existing durable email claim pattern is appropriate:

- claim one event row atomically;
- send in a background task with a bounded 15-second SMTP context;
- release the claim only for an explicitly pre-delivery-safe failure; and
- retain an ambiguous claim to avoid duplicate mail.

The user-facing relay response must not wait for SMTP completion.

## Session-block implementation

### Key and storage

Use the exact Redis key shape:

```text
cyber_session_block:<hex sha256(token_id + NUL + explicit_identifier)>
```

Set value `1` with `EX cyber_session_block_ttl_seconds`. The initial write and
subsequent hit may refresh the TTL; whichever behavior is chosen must be
covered by an expiry test and documented in the admin UI. A successful
cyber-policy finalizer writes the marker only when all of the following hold:

- `risk_control_enabled` is true;
- `cyber_session_block_enabled` is true;
- the request is in group/model scope;
- token ID is positive; and
- an explicit identifier was present and normalized.

A failed Redis write does not change the client-visible upstream error and
does not fail the request. Emit a safe degradation log containing only the
digest and request ID.

### Local response

The pre-distribution guard returns:

```json
{
  "error": {
    "message": "Session blocked by cyber policy",
    "type": "permission_error",
    "code": "session_blocked_by_cyber_policy"
  }
}
```

Use HTTP `403` for HTTP requests. A WebSocket handshake that can still be
rejected as HTTP returns `403`; an established Realtime connection sends the
same machine-readable error event and closes with `1008`.

No local block performs model pricing, quota reservation, upstream selection,
risk-event persistence, email, violation counting, or account auto-ban.

## Operations logging

The current New API error log stores structured metadata in `Log.Other`. Add a
dedicated cyber builder (or equivalent adapter to the Tokeness ops sink) and
make the generic error logger skip a request with a cyber mark.

### Upstream hit

```json
{
  "error_type": "cyber_policy",
  "error_source": "upstream_http",
  "priority": "P3",
  "is_business_limited": true,
  "status_code": 400,
  "client_status_code": 400,
  "upstream_status_code": 400,
  "error_code": "cyber_policy",
  "request_type": "cyber"
}
```

For an SSE/WS failure, `status_code` mirrors the actual client-visible
transport status (`200` for a committed SSE stream); the event error code
still identifies the failure. Include account/channel IDs for an upstream
hit only when they are already part of the normal authenticated relay context.

### Local session block

```json
{
  "error_type": "cyber_policy_session_blocked",
  "error_source": "gateway_local",
  "priority": "P3",
  "status_code": 403,
  "error_code": "session_blocked_by_cyber_policy",
  "request_type": "cyber_session"
}
```

Do not attach `AccountID`/channel ID to a local block when no upstream call
was made. Do not create a risk event or feed the violation counter for a
local block. The generic middleware must not write a second error row after
the dedicated entry is queued.

## Endpoint wiring matrix

### Relay routes

The HTTP middleware is added to the common `/v1` relay group before
`Distribute`. Detection hooks are added to adapter handlers, not to route
names, so converted compatibility paths share the same contract.

| Route/format | Request guard | Response detector | Finalizer |
| --- | --- | --- | --- |
| `/v1/completions` / OpenAI | Yes | non-stream + SSE | HTTP request |
| `/v1/chat/completions` / OpenAI | Yes | non-stream + SSE | HTTP request |
| `/v1/responses` | Yes | non-stream + `response.failed` SSE | HTTP request |
| `/v1/responses/compact` | Yes | Responses/compaction response boundary | HTTP request |
| `/v1/messages` / Anthropic compatibility | Yes | JSON + SSE | HTTP request |
| Advanced custom/OpenAI-compatible adapters | Yes | common non-2xx + stream boundary | HTTP request |
| `/v1/realtime` | Header/query guard | per-turn failed/server-error event | WS turn |
| `/v1/moderations` | Existing guard only | common upstream error detector | HTTP request |

Image, audio, embeddings, rerank, Gemini-native, Midjourney, Suno, and task
routes do not gain a new cyber-specific protocol in v1. If an adapter returns
an OpenAI-compatible error through the common relay boundary, the detector
may classify it; otherwise it remains governed by the adapter's current error
contract.

## Failure handling

| Failure | Client | Retry/failover | Risk side effects | Billing |
| --- | --- | --- | --- | --- |
| Upstream `cyber_policy` with usage | Original policy error | Never | Scoped event/email/session/ops | Settle actual usage once |
| Upstream `cyber_policy` without usage | Original policy error | Never | Scoped event/email/session/ops | No fabricated output; safe settle/refund policy |
| Ordinary upstream 400 | Existing behavior | Existing status policy | Existing logs | Existing refund/error behavior |
| Content moderation block | Existing `content_policy_violation` | Skip retry | Existing moderation side effects | Existing pre-block behavior |
| Local cyber session block | 403 permission error | No upstream attempt | None | No quota mutation |
| Redis session read/write error | Request continues | Existing upstream behavior | Safe degradation log | Existing billing behavior |
| Risk DB/email/ops failure | Original upstream policy error | Never | Log safe failure and retry side effect where supported | Billing remains independently finalized |

## Concurrency and consistency

- Request-context marks are guarded by an idempotent first-write check.
- A WebSocket mark is scoped to a turn, not the lifetime of the connection.
- Billing finalization uses a durable request/turn identity; Redis is not the
  sole billing lock.
- Risk rows and email claims use database atomic updates compatible with all
  supported GORM dialects.
- Session blocks use Redis TTL and fail open when Redis is unavailable.
- Configuration reads may use an in-process cache, but database persistence is
  authoritative. A local successful update is visible immediately; remote
  nodes refresh within a bounded interval of at most one second.
- Asynchronous side effects use a detached context with a 30-second bound and
  must copy all required request metadata before the Gin request is released.

## Privacy and security

- Never log or persist API keys, authorization headers, raw session IDs, or
  full request bodies.
- Limit the in-memory error body to 4096 bytes and sanitize upstream URLs
  before logging.
- Treat upstream error messages as untrusted text; use existing local-preview
  and template escaping helpers for logs/email.
- Hash session identifiers with SHA-256 and include token ID in the seed to
  prevent cross-key collisions.
- Do not use user ID alone as a session block identity.
- Keep `cyber_policy` risk rows separate from content moderation sampling and
  do not allow a client-controlled field to disable detection.

## Verification plan

### Service and adapter tests

- nested `error.code` and `response.error.code`, case/whitespace variants;
- unrelated error codes and message-only occurrences are not classified;
- 4096-byte mark bound and first-mark-wins behavior;
- non-stream status/body pass-through;
- Chat SSE error chunk plus `[DONE]`;
- Responses `response.failed` usage extraction before detection;
- Anthropic JSON/SSE mapping;
- advanced/passthrough common error boundary;
- Realtime per-turn mark reset and failed event handling.

### Controller and channel tests

- marked requests never call `shouldRetry` with a retry budget;
- no `DisableChannel`, cooldown, or failover call after a cyber mark;
- generic error logging is suppressed and one dedicated ops entry is written;
- the forwarded sentinel does not append a second response body;
- stream status 200 is recorded as 200 while the error code remains cyber.

### Billing and risk tests

- actual input/output usage settles exactly once;
- pre-consume/refund and cyber settlement permutations cannot double charge;
- missing output usage is not fabricated;
- `request_type=cyber` consume log contains normalized upstream usage;
- `ChargeViolationFeeIfNeeded` is not invoked;
- risk control and group/model scope gate only side effects;
- cyber rows are excluded from current and historical ban counts by default;
- explicit exclusion disable includes cyber rows;
- notification claim is idempotent and ambiguous delivery is not duplicated;
- finalizer repeats are no-ops.

### Session and database tests

- key derivation is stable, token-key isolated, and raw identifiers absent;
- header precedence and body `prompt_cache_key` fallback;
- no explicit identifier means no block;
- TTL is written and expiry releases the request;
- Redis read/write errors fail open;
- local block occurs before `Distribute` and produces no quota/risk record;
- SQLite migration and GORM queries execute; MySQL/PostgreSQL SQL remains
  dialect-neutral.

### Commands

- `go test ./...`
- `go vet ./...`
- focused race tests for cyber finalization, Redis/session state, and email
  claims;
- changed-file formatting and `git diff --check`.

## Rollout notes

No production deployment or credential configuration is part of this
specification. Roll out the code with `risk_control_enabled=true` only after
staging fixtures prove client framing, no-retry/channel safety, and actual
usage settlement. Keep `cyber_session_block_enabled=false` for the first
observe period. Enable it only after shared Redis, key isolation, TTL, and
pre-distribution 403 behavior are verified across all gateway nodes.
