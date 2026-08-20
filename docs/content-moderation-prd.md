# Content Moderation PRD

## Goal

Add an operator-controlled conversation content moderation gate to New API. The first release must inspect the latest user content before an upstream request is sent, support an OpenAI-compatible `/v1/moderations` service, and make the decision observable without exposing raw conversation content in logs or the admin API.

## Scope

- OpenAI Chat Completions, OpenAI Responses, Anthropic Messages, and Gemini request bodies.
- Text extraction from the latest user turn. Image parts are detected and skipped until a multimodal moderation contract is added.
- Global enable switch, `observe` and `pre_block` modes, group and model filters, sample rate, timeout, retry count, API key rotation, and per-category thresholds.
- Audit records for flagged requests, optional non-hit records, highest category/score, request metadata, and a short redacted excerpt.
- Admin endpoints for configuration and paginated logs, protected by root authentication.
- Optional user notification through the existing notification service and optional automatic user disable after a configurable rolling count.
- Channel-affinity conversation de-duplication: when the distributor resolves a channel-affinity key, the same affinity conversation is moderated once per selected channel for the affinity TTL; later requests reuse the decision without a second API call or duplicate side effects.

## Non-goals

- Persisting complete request bodies or images.
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

## Risks and controls

- External audit latency: synchronous mode has a hard timeout and fail-open behavior on audit service errors; operators can choose `pre_block` only when the audit service is reliable.
- Privacy: no full body persistence, no API key exposure, and bounded excerpt redaction.
- Account lockout: auto-ban is disabled by default, requires an explicit threshold, and uses the existing fail-closed authentication-version fence before changing status.
- Configuration errors: validate URL, mode, status code, thresholds, sample rate, and group/model filters before saving.
- Cache correctness: cache only successful, durably recorded moderation responses; version keys by normalized policy; cap TTL at one year; merge concurrent first requests in-process and across Redis-backed nodes; keep failures retryable.
- Input completeness: split oversized latest-user content rather than truncating it, keep one moderation HTTP request, and aggregate category maxima across chunks.
- Side-effect idempotency: auto-ban uses a conditional status transition, and notification email uses a durable per-log claim. SMTP cannot provide exactly-once delivery after an ambiguous connection failure, so ambiguous claims require operator review instead of an automatic resend.
- Conversation semantics: both allow and flagged decisions are intentionally reused for the affinity TTL. This feature is a conversation-level gate; request-level enforcement requires an affinity key that advances when the auditable user-content revision changes.
