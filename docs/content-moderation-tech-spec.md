# Content Moderation Technical Specification

## Request path

`controller.Relay` validates the typed request, builds `relayInfo`, then calls `service.CheckContentModeration` before token counting, pricing, quota reservation, and any upstream request. The distribution middleware has already selected a candidate channel (including channel affinity); the first relay attempt reuses that candidate so the moderation cache's channel ID matches the actual upstream channel. Later retries may select another channel under the existing retry policy. Moderation extracts text and supported images from the latest user turn in the already validated typed request. Raw-body parsing remains a focused service fallback for tests and unsupported typed edge cases. The moderation path does not call `BodyStorage.Bytes`; a channel-affinity rule configured with a `gjson` source may still materialize the body once before moderation.

## Configuration

Configuration is stored as one JSON value in the existing `Option` table under `content_moderation.config`. `model.InitOptionMap` seeds the default. The root-only API exposes a redacted view and accepts a validated update. API keys are accepted as a newline-separated list but are never returned; only count and masked suffixes are exposed. A blank key field preserves stored keys, while the transient `clear_api_keys` update flag explicitly removes all keys and is never persisted.

The shared database is authoritative across gateway nodes. Each process keeps the parsed policy in memory for at most one second before re-reading the `Option` row; a local successful update changes the in-process value immediately. Database values are parsed and normalized before publication to the process option map. A transient read failure or invalid externally-written value uses the last known valid policy instead of silently replacing it with defaults.

Defaults: disabled, `observe`, `https://api.openai.com`, `omni-moderation-latest`, 1500 ms timeout, 100% sample rate, all groups, all models, no non-hit persistence, 1 retry, 403 block status, and auto-ban disabled. Partial threshold maps are merged over the category-specific defaults. `record_logs` remains a read/write compatibility alias for `record_non_hits`; normalized configuration and the admin UI use only `record_non_hits`.

## Data model

`ContentModerationLog` is a main-database GORM model. It stores user/group/model/protocol/request metadata, action, flagged state, highest category/score, category scores JSON, excerpt/hash, latency, timestamps, and the notification delivery state. It does not store the raw body or API keys. Email delivery uses an atomic claim token on the log row so only one node can send that notification.

## Service

`service/content_moderation.go` owns config normalization, scope/sample checks, API calls, score evaluation, typed request extraction, persistence, and side effects. The service is fail-open on moderation API/config errors and emits request-scoped logs without prompt text or API keys. Transport errors discard the complete request URL before logging so credentials accidentally placed in `BaseURL` are not exposed. When `GetChannelAffinityStatsContext` is present, moderation uses a full SHA-256 digest of the resolved affinity cache identity plus the selected channel ID and normalized moderation-policy version. The selected multi-key credential is intentionally excluded so credential rotation within one channel does not cause duplicate audits. Cached decisions are consulted before sampling; affinity sampling uses the stable moderation cache identity rather than request ID or message text.

The complete normalized latest-user turn is preserved. Turns longer than 16,000 runes are split without truncation and still use one moderation HTTP request. Pure-text requests preserve the existing OpenAI batch-input contract: one string input per chunk and exactly one result per chunk, followed by maximum-score aggregation. When the latest user turn includes images, every text chunk plus at most one selected image is encoded as one multimodal content array using `{type:"text",text:"..."}` and `{type:"image_url",image_url:{url:"..."}}`; the provider must return exactly one aggregate result. Empty or partial result sets remain fail-open but retryable and cannot create a cached allow decision.

Supported extraction forms are OpenAI Chat `image_url` objects or strings, OpenAI Responses and Responses Compaction `input_image.image_url`, Anthropic URL sources and Base64 sources with `media_type`, and Gemini HTTP(S) `fileData.fileUri` plus Base64 `inlineData`. HTTP(S) URLs must be structurally valid, omit URL user info, and fit within 8 KiB. Data URLs must be strict Base64 PNG, JPEG, WEBP, or GIF and decode to at most 20 MB, matching the official OpenAI moderation image limit. Duplicate images are removed by SHA-256 identity, no more than 16 distinct candidates are accepted, and one image is randomly selected before hashing, sampling, cache lookup, or provider payload construction. Invalid or oversized image inputs are rejected in `pre_block`; `observe` remains fail-open with a safe error. Only the final conversation item is eligible: assistant/model turns, unknown or unroled Responses message/history items, and tool/function response items produce no moderation input instead of falling back to older user content. Flat Responses arrays are accepted only when every item is an explicit direct `input_text`, `input_image`, or `input_file` part. This follows the OpenAI multimodal moderation contract documented at <https://developers.openai.com/api/docs/guides/moderation> and the bounded one-image behavior used by Sub2API.

The moderation log remains content-free. Text excerpts stay redacted, image URLs/Base64 are never persisted, and the input hash writes only SHA-256-derived image identity. The image is sent only to the configured moderation provider; provider retention and image-size behavior are deployment responsibilities.

The cached value contains only the decision and persistence metadata required to retry incomplete side effects. Its TTL follows the channel-affinity TTL, capped at 365 days. Successful API responses are cached only after required audit persistence succeeds; API failures remain retryable. Policy-versioned keys make old decisions unreachable immediately after a configuration change, while namespace purge remains storage cleanup rather than a correctness dependency. In-process `singleflight` merges local concurrent requests. With Redis enabled, an owner-token lease coordinates concurrent first requests across nodes and is renewed until the decision is published; waiters poll for the published result and retry ownership after an explicit owner failure or lost lease.

Auto-ban remains synchronous because it changes authorization state before the request proceeds. A dedicated status-only transaction publishes the next auth-version deny fence, commits only the disabled status and version, publishes the authoritative disabled user cache, revokes browser sessions, and invalidates relay-token caches; it never writes a stale full-user snapshot. A committed disable whose Redis publication failed remains fail-closed through the pending fence and is completed by a later side-effect retry. Email is claimed synchronously but sent by a background task with a 15-second SMTP deadline. A clearly pre-delivery failure releases the claim for a future cache-hit retry. A panic, lost SMTP confirmation, or failure after message submission leaves the claim held because automatic retry could duplicate a message that the server already accepted. Operators can inspect the log row to resolve that rare ambiguous state.

## Admin API

- `GET /api/content-moderation/config`
- `PUT /api/content-moderation/config`
- `GET /api/content-moderation/logs`
- `POST /api/content-moderation/users/:id/unban`

All endpoints use `RootAuth`. The log endpoint is paginated and filters by user, group, model, flagged state, and time range.

## Verification

- `go test ./...`
- `go vet ./...`
- `go test -race ./service -run ContentModeration -count=1`
- `go test -race ./common -run SendEmailContextBounds -count=1`
- `bun run test`, `bun run typecheck`, and `bun run build`.
- Changed frontend files pass Oxlint and `oxfmt --check`; the repository-wide protected-header formatting script still reports unrelated pre-existing files.
- A local `httptest.Server` verifies the exact `/v1/moderations` request contract and blocking behavior.
- Affinity regression coverage verifies one moderation call for repeated requests in the same conversation/channel, continued blocking from a cached flagged decision, and a fresh call after the channel changes.
- Redis regression coverage verifies that independent service executions cannot both own the same first-audit lease, a slow owner renews the lease, and a failed owner leaves the conversation retryable.
- Sampling regression coverage verifies cached flagged decisions are enforced before sampling and affinity sampling remains stable across changing request IDs and user text.
- Typed extraction coverage verifies all four supported protocols without reading a full body from storage.
- Multimodal extraction coverage verifies URL and Base64 images for OpenAI Chat, OpenAI Responses, Anthropic Messages, and Gemini, including image-only latest user turns.
- Image-boundary coverage verifies unsupported sources/MIME types, malformed Base64, oversized data URLs, oversized/invalid HTTP(S) URLs, and excess distinct candidates are rejected without exposing image references.
- Conversation-boundary coverage verifies that assistant/model and tool/function response turns never cause historical user content to be moderated again.
- Request-contract coverage verifies text-only long inputs still expect one result per text chunk, while text-plus-image and image-only requests use one multimodal result and include no more than one image part.
- Side-effect coverage verifies concurrent email compensation claims once, explicit email failure retries, ambiguous delivery does not duplicate, and automatic account disable is idempotent.
- Long-input coverage verifies tail content is included in a second moderation input while only one HTTP request is issued, and empty or partial result sets remain uncached.
- Configuration coverage verifies a second node's database update is observed after the bounded refresh interval.
