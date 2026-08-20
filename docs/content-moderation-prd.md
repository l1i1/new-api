# Content Moderation PRD

## Goal

Add an operator-controlled conversation content moderation gate to New API. The first release must inspect the latest user content before an upstream request is sent, support an OpenAI-compatible `/v1/moderations` service, and make the decision observable without exposing raw conversation content in logs or the admin API.

## Scope

- OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, and Gemini request bodies.
- Text and image extraction from the latest user turn. Images are normalized from OpenAI Chat `image_url`, OpenAI Responses `input_image`, Anthropic image sources, and Gemini inline/file data.
- OpenAI-compatible multimodal moderation input using `omni-moderation-latest`; HTTP(S) image URLs and Base64 image data URLs are supported, and at most one image is sampled per audited request.
- Global enable switch, `observe` and `pre_block` modes, group and model filters, sample rate, timeout, retry count, API key rotation, and per-category thresholds.
- Audit records for flagged requests, optional non-hit records, highest category/score, request metadata, and a short redacted excerpt.
- Admin endpoints for configuration and paginated logs, protected by root authentication.
- Optional user notification through the existing notification service and optional automatic user disable after a configurable rolling count.
- Channel-affinity conversation de-duplication: when the distributor resolves a channel-affinity key, the same affinity conversation is moderated once per selected channel for the affinity TTL; later requests reuse the decision without a second API call or duplicate side effects.
- Repeated short-text allow de-duplication: the first in-scope request is still moderated, but a successful allow decision for the same normalized pure text may be reused briefly for the same user/group/protocol across target models. Flagged decisions, provider failures, image inputs, and long text are never eligible for this cache.
- Provider-capacity protection: each moderation API key has a bounded in-flight pool shared by all gateway nodes through Redis, with a short synchronous wait budget, process-local fallback protection, and per-key cooldown after transient provider failures.

## Non-goals

- Persisting complete request bodies, image URLs, or Base64 image data.
- Moderating assistant output.
- Replacing the existing `/v1/moderations` relay endpoint.
- Adding a persistent queue/worker system in the first release; both modes use a bounded synchronous moderation call with the configured timeout, while notification email is dispatched after the decision through a bounded background task.

## Acceptance criteria

1. With the feature disabled, relay behavior and latency are unchanged apart from a cheap configuration check.
2. In `observe`, a flagged request reaches the upstream and creates an audit log.
3. In `pre_block`, a flagged request returns the configured 4xx response before billing and upstream forwarding; the distributor's affinity-selected candidate channel is not contacted.
4. Group/model filters and deterministic sample rate are enforced before calling the moderation API.
5. Moderation API calls use `{BaseURL}/v1/moderations`, bearer authentication, timeout, and bounded retries with key rotation.
6. Raw prompt text is never stored or returned; only a bounded redacted excerpt/hash is kept for operator triage.
7. All supported databases continue to migrate and query through GORM.
8. Backend tests cover extraction, threshold evaluation, filtering, retry/key rotation, blocking, and log persistence. Frontend typecheck/lint/build remain clean if the admin screen is included.
9. A channel-affinity conversation is cached by a hashed key that includes the selected channel but not the rotating multi-key credential; a different channel is moderated independently.
10. Cached decisions are checked before request-level sampling, and affinity sampling is stable for the lifetime of the conversation key. A cached flagged decision can never be bypassed by a later sampling miss.
11. When Redis is enabled, concurrent first requests across gateway nodes share one bounded lease so only the lease owner calls the moderation API; waiters reuse the published decision or retry after a failed owner.
12. The moderation identity uses a full SHA-256 digest of the resolved channel-affinity cache identity. Raw affinity values are never stored in moderation cache keys or logs.
13. Relay moderation extracts the latest user turn from the already validated typed request and does not add another `BodyStorage.Bytes()` read. Channel-affinity rules that explicitly use a `gjson` body source may still materialize the request once before moderation.
14. A successful moderation response is not cached until required audit persistence succeeds. Configuration changes use a configuration-versioned cache identity so stale allow decisions cannot survive a failed cache purge.
15. Configuration persistence reports database failures, and rolling-window values are bounded before conversion to `time.Duration`.
16. A Redis first-audit owner renews its token lease until the decision is published, so work that outlives the original lease interval does not permit a second moderation API owner.
17. Flagged email notification does not block the relay response. A database claim allows only one node to send an email for a moderation log, explicit pre-delivery failures release the claim for retry, and ambiguous post-delivery failures keep the claim to prevent duplicates.
18. Partial threshold updates retain the category-specific defaults for omitted categories. The legacy `record_logs` field is accepted as an alias for `record_non_hits`, but the admin UI exposes only the latter.
19. Moderation API, configuration, and audit persistence failures remain fail-open but emit safe request-scoped logs that exclude prompt text, API keys, and credential-bearing URLs.
20. Auto-ban advances the existing authentication-version fence before committing the restrictive status, publishes the disabled user snapshot after commit, and remains retryable if Redis publication fails.
21. Operators can explicitly clear all stored moderation API keys; an empty textarea without the explicit clear action continues to preserve existing keys.
22. Long latest-user turns are split into bounded moderation inputs inside one API request, and scores are aggregated across every returned result so unsafe tail content is not truncated away.
23. A moderation response must contain exactly one result for every submitted input; empty or partial result sets are audit failures and are never cached as allow decisions.
24. In a multi-node deployment, each node refreshes the authoritative database-backed moderation configuration at a bounded interval, while a local update remains immediately visible.
25. A latest user turn containing only an image is auditable. A turn containing text and images is sent as one multimodal moderation input with every bounded text chunk and at most one image.
26. OpenAI Chat URL/Data URL images, OpenAI Responses `input_image`, Anthropic URL/Base64 images, and Gemini HTTP(S)/inline images are normalized to the OpenAI moderation `image_url` contract.
27. Image URLs and Base64 data are never stored in moderation logs. The stored input hash includes image identity through SHA-256 only.
28. Multiple images in one latest user turn do not create multiple moderation calls or results; one image is selected for the conversation-level audit, matching the bounded Sub2API behavior.
29. Only the final conversation item is eligible for extraction. Assistant/model turns, Responses function outputs, Anthropic `tool_result`, and Gemini `functionResponse` turns do not fall back to an older user request.
30. A generic request-size or character-count threshold never bypasses the first moderation check. Short harmful prompts remain auditable.
31. Repeated eligible short pure-text requests from the same user/group/protocol and policy call the provider once within the bounded de-duplication TTL even when target model names differ.
32. Only a successful provider response whose raw `flagged` value is false, with non-empty category scores, evaluated as allow by the configured local thresholds and durably persisted when required, enters the repeated-content cache. Flagged decisions, errors, malformed or partial responses, and failed log persistence remain independently auditable and retryable.
33. In-process singleflight and a Redis owner lease merge concurrent first checks for the same eligible text across gateway nodes. The cache key contains only SHA-256-derived content identity and policy/request metadata, never raw text.
34. With Redis available, active moderation provider calls never exceed `max_in_flight_per_key` for any API key across gateway nodes. Redis keys contain only a full SHA-256 credential fingerprint, a bounded slot index, and a random lease token.
35. Every provider slot is released after the call completes. A crashed owner cannot hold capacity beyond the lease TTL, which is longer than the configured moderation timeout.
36. Redis failure falls back to the same per-key process-local limit and emits a safe degradation log without an API key, prompt, image reference, or credential-bearing URL.
37. Capacity acquisition considers every non-cooled-down key before waiting. A 429, timeout, transport failure, response-read failure, or 5xx response places only the affected key into a bounded cooldown, and a retry may use another healthy key without exceeding the existing retry budget.
38. When the queue wait budget expires in `observe`, the relay remains fail-open but always persists an audit row with action `skipped_capacity`. It is never represented as a successful allow or cached decision.
39. When the queue wait budget expires in `pre_block`, the relay does not contact the model provider and returns the configured 429 or 503 with error code `content_moderation_overloaded`, distinct from a content-policy violation.
40. Affinity-cache and repeated-allow-cache hits do not acquire provider capacity because they do not call the moderation provider.

## Risks and controls

- External audit latency: synchronous mode has a hard timeout and fail-open behavior on audit service errors; operators can choose `pre_block` only when the audit service is reliable.
- Privacy: no full body persistence, no API key exposure, and bounded excerpt redaction.
- Account lockout: auto-ban is disabled by default, requires an explicit threshold, and uses the existing fail-closed authentication-version fence before changing status.
- Configuration errors: validate URL, mode, status code, thresholds, sample rate, and group/model filters before saving.
- Cache correctness: cache only successful, durably recorded moderation responses; version keys by normalized policy; cap TTL at one year; merge concurrent first requests in-process and across Redis-backed nodes; keep failures retryable.
- Input completeness: split oversized latest-user content rather than truncating it, keep one moderation HTTP request, and aggregate category maxima across chunks.
- Multimodal compatibility: pure text keeps the existing one-result-per-text-input contract. When an image is present, bounded text chunks and one image are combined into one multimodal input and require one aggregate result. The configured moderation provider must implement the OpenAI multimodal `/v1/moderations` contract.
- Conversation endpoint coverage: OpenAI Chat, OpenAI Responses and Responses Compaction, Anthropic Messages, and Gemini are covered; image generation, Realtime, Audio, and other non-conversation endpoints remain out of scope.
- Image privacy and size: image content is forwarded only to the configured moderation provider and is never persisted locally. Data URLs are restricted to PNG, JPEG, WEBP, or GIF, validated as strict Base64, and limited to 20 MB decoded bytes; image URLs are limited to 8 KiB and must be valid HTTP(S) URLs without embedded user info. More than 16 distinct candidate images is rejected before sampling one image. These limits follow the official OpenAI moderation 20 MB image boundary while bounding local parsing and provider payload cost.
- Side-effect idempotency: auto-ban uses a conditional status transition, and notification email uses a durable per-log claim. SMTP cannot provide exactly-once delivery after an ambiguous connection failure, so ambiguous claims require operator review instead of an automatic resend.
- Conversation semantics: both allow and flagged decisions are intentionally reused for the affinity TTL. This feature is a conversation-level gate; request-level enforcement requires an affinity key that advances when the auditable user-content revision changes.
- Short-request bypass: raw body size and text length are not trust signals because harmful prompts can be only a few characters. Length is used only to bound eligibility for allow-result reuse after one successful moderation check.
- Repeated-content correctness: the short-lived cache is scoped by user, group, protocol, normalized content hash, and normalized policy version, while intentionally excluding the target model. It stores only responses whose raw provider flag is false, whose category scores are non-empty, and which are allowed by local thresholds, so repeated flagged requests continue to produce their normal audit and account side effects. When non-hit persistence is enabled, a repeated allow cache hit intentionally does not create another allow log.
- Capacity correctness: sample rate controls which requests are audited, not instantaneous concurrency. The hard per-key limit is the resource boundary; the short wait budget bounds relay latency, cooldown prevents a failing credential from consuming retries, and lease expiry restores capacity after node failure.
- Degraded coordination: without Redis, each node still enforces the configured per-key limit locally, but the fleet-wide limit can temporarily rise to the number of active nodes multiplied by that value. Operators must treat the safe degradation log as an alert condition.
