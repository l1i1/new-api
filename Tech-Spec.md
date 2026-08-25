# Per-User Model Rate Limits

## Goal

Allow administrators to limit how often one user can call one specific model. A user and model may have multiple concurrent rules, so RPM and longer-window quotas can be enforced together.

## Rule Semantics

- A rule is identified by `user_id`, exact trimmed `model_name`, and `window_seconds`.
- `max_requests` is the maximum number of incoming API requests allowed during the window.
- `10 requests / 60 seconds` is RPM 10. Any positive window in seconds is supported up to 30 days.
- Every incoming request counts, including requests that later fail validation, billing, or upstream processing after distribution.
- Automatic channel retries belong to the same incoming request and consume one count only.
- Task submission calls count. Task status, result, image, and content fetches do not count as model calls.
- Every enabled rule for the user and model must allow the request.
- Model matching is case-sensitive and uses the original client-facing model name after distributor defaults and suffix handling.
- A rejected request returns HTTP 429, an OpenAI-compatible error body, and `Retry-After`.

Redis uses the existing atomic fixed-window counter. Multi-node production requires shared Redis. Development without Redis uses the process-local limiter and does not provide cross-process consistency. Redis counter failures are fail-closed.

## Data Model

Table: `user_model_rate_limits`

| Column | Type | Constraints |
| --- | --- | --- |
| `id` | integer | primary key |
| `user_id` | integer | required, indexed |
| `model_name` | varchar(255) | required |
| `window_seconds` | integer | 1 to 2,592,000 |
| `max_requests` | integer | 1 to 1,000,000,000 |
| `enabled` | boolean | set explicitly by application code |
| `created_at` | integer timestamp | managed by GORM |
| `updated_at` | integer timestamp | managed by GORM |

`(user_id, model_name, window_seconds)` is unique.

## API Contract

Both endpoints require administrator authentication and the same target-role authorization used by user editing.

### GET `/api/user/:id/model-rate-limits`

Response data is the complete ordered rule list for the target user.

### PUT `/api/user/:id/model-rate-limits`

The request body replaces the target user's complete rule list atomically.

```json
{
  "rules": [
    {
      "model_name": "gpt-5.4-mini",
      "window_seconds": 60,
      "max_requests": 10,
      "enabled": true
    }
  ]
}
```

IDs and timestamps are server-owned. Duplicate model/window pairs and invalid bounds are rejected with HTTP 400. An empty list removes all rules.

## Request Path

The existing global/group model limiter remains unchanged. The new limiter runs after `TokenAuth` and `Distribute`, before the relay controller. At that point the authenticated user ID and original model name are available, while the relay retry loop has not started.

Rules are cached. Shared Redis is the cache in production, and administrator replacement refreshes the shared cache after the database transaction. Without Redis, a short process-local cache avoids a database query for every relay request.

## Acceptance Criteria

- An administrator can list and replace rules for a manageable target user.
- RPM and arbitrary-second windows can be configured in the user edit drawer.
- Same user and model is rejected after the configured count.
- Different users and different models use independent counters.
- Multiple rules for the same model are enforced together.
- A window expiry allows requests again.
- Concurrent requests cannot exceed a Redis-backed limit.
- Requests without a matching enabled rule are unchanged.
- Invalid or duplicate rules do not partially replace existing rules.
- Root/admin target-role restrictions remain intact.
- SQLite, MySQL, and PostgreSQL migrations use GORM-compatible schema definitions.

## Verification

- Focused Go model, controller, and middleware tests.
- `go test ./middleware ./model ./controller`
- `go vet ./middleware ./model ./controller`
- Frontend typecheck, focused lint, and production build.
- `git diff --check`.

## Risks

- Fixed-window limits allow a boundary burst of up to twice the configured rate.
- Non-Redis development mode is process-local and cannot enforce a fleet-wide limit.
- Cache refresh failure after a committed database update is surfaced to the administrator. The database remains the source of truth and existing cached rules expire within 30 seconds.

## Invoice Payment Method Allowlist

### Goal

Allow administrators to restrict invoice applications to orders paid with one or more configured payment methods, while keeping existing installations compatible when the setting is absent or empty.

### Configuration

- Option key: `InvoiceAllowedPaymentMethods`.
- Stored value: a JSON array of non-empty payment-method identifiers, for example `["alipay","wxpay"]`.
- Identifiers are trimmed and compared case-insensitively; the server stores the normalized lower-case, de-duplicated array.
- A missing option or an empty array means all otherwise invoiceable payment methods are allowed. This preserves existing behavior after upgrade and provides an explicit way to clear the restriction.
- Values are limited to the existing `TopUp.PaymentMethod` column width (50 bytes per identifier) and a bounded number of entries. The option endpoint rejects malformed JSON, non-array values, empty identifiers, and overlong arrays/identifiers. A corrupt non-empty stored value fails closed and temporarily blocks invoice option/application requests instead of silently disabling the restriction.

### API Contract

`GET /api/user/invoice/options` adds:

```json
{
  "allowed_payment_methods": ["alipay", "wxpay"]
}
```

The `orders` array is filtered using the same allowlist. `POST /api/user/invoice` applies the allowlist again inside the order-locking transaction; clients cannot bypass it by submitting a hidden or stale order id. A disallowed order returns the localized `invoice.payment_method_not_allowed` error.

### Order Semantics

- The comparison uses the immutable `TopUp.PaymentMethod` snapshot, not the current payment configuration or `PaymentProvider` gateway.
- This covers both direct top-up and subscription orders because subscription settlement creates/updates the same `TopUp` snapshot.
- When a non-empty allowlist is configured, an empty historical `PaymentMethod` does not match and is excluded/rejected. With an empty allowlist, legacy behavior remains unchanged.

### Admin UI

The existing Billing > Invoice Settings section exposes a multi-select editor. It includes the built-in payment method identifiers and identifiers found in the current EPay method configuration, while preserving an already-saved unknown identifier until the administrator removes it. Saving writes the normalized JSON option through the existing option API.

### Acceptance Criteria

- An administrator can select zero, one, or multiple payment methods and save them from the existing invoice settings page.
- Zero selected methods allows all invoiceable orders; one or more selected methods allow only matching case-insensitive snapshots.
- The user invoice options list omits disallowed orders and reports the active allowed methods.
- A forged create request containing a disallowed order is rejected server-side, including mixed allowed/disallowed selections.
- Recharge and subscription-created `TopUp` rows use their persisted `PaymentMethod` snapshot; no current gateway configuration lookup is used.
- Malformed or invalid option values are rejected without changing the previous setting.

### Verification

- Go tests cover normalization, empty/missing compatibility, list filtering, and transactional create rejection for disallowed methods.
- Frontend tests cover multi-select persistence and user-facing filtering/selection behavior.
- Run focused Go tests, frontend tests, `bun run typecheck`, affected-file lint/format checks, and the production frontend build.

## Notification Automatic Display

### Route Behavior

- `/` never opens the site Notice dialog or notification popover automatically.
- `/pricing` (with or without a trailing slash) automatically opens the site Notice dialog for the first unseen Notice revision and whenever that Notice content changes.
- Every other route with a notification header automatically opens the notification popover for an unread Notice revision.
- The pricing dialog and notification popover are mutually exclusive automatic surfaces, so `/pricing` never opens both.

### Repeat Behavior

- Automatically opening the notification popover selects Notice and marks the displayed revision read.
- Closing the popover keeps it closed across ordinary renders, query refreshes with unchanged content, and client-side route changes.
- A changed Notice revision may open the popover again on an eligible route.
- Manual notification-center behavior remains unchanged.

### Verification

- Hook tests cover route selection, excluded routes, changed Notice content, and no reopen after dismissal when content is unchanged.
- Run the focused notification test, frontend typecheck, affected-file lint/format checks, and the production frontend build.

## OpenAI Compact Model Alias Compatibility

### Goal

Accept a requested model ending in `-openai-compact` when the exact model is not available in the selected group but the same group exposes the model name with that suffix removed. For example, `gpt-5.6-sol-openai-compact` may route through `gpt-5.6-sol`.

### Resolution Semantics

- Exact available model names always win. An explicitly configured `gpt-5.5-openai-compact` is never replaced by `gpt-5.5` during channel selection.
- Alias fallback is considered only for a non-empty model name ending in the exact, case-sensitive suffix `-openai-compact`.
- The fallback target is the complete model name before the final suffix. Internal hyphens and version segments are preserved.
- The fallback target must be available in the same effective group and support the current request path. Auto-group selection resolves the alias independently for each candidate group; Advanced Custom routes are filtered before exact-first precedence is applied.
- Existing normalized model matching remains ahead of compact alias fallback.
- Token model limits allow the alias when the exact alias is absent from the token allowlist and its base model is allowed.
- The user-facing model name remains the compact alias for billing, rate limits, and request logs. Only channel selection and the upstream request model use the resolved base model.
- A channel model mapping is applied after compact alias resolution, so a mapping keyed by the available base model continues to work.
- Model discovery responses are unchanged; the server does not synthesize compact aliases into every model list.

### Request and Retry Behavior

- The distributor records the resolved base model only when alias fallback is actually used.
- Channel affinity, initial channel selection, auto groups, retry selection, and specific-channel selection share the same path-aware resolution rule.
- Relay request construction reads the resolved model and sends it upstream. Requests that select an exact compact model preserve existing behavior.
- The existing `/v1/responses/compact` suffix behavior remains compatible: a base model in the request is still represented internally by its compact billing name while the upstream receives the base model.

### Acceptance Criteria

- `gpt-5.5-openai-compact` routes to `gpt-5.5` when only `gpt-5.5` is available.
- `gpt-5.6-sol-openai-compact` routes to `gpt-5.6-sol` without version-specific code.
- An exact compact model takes precedence when both exact and base models are available.
- A compact alias does not fall back to a base model from another group.
- Missing exact and base models retain the existing no-available-channel error.
- The upstream request contains the resolved base model while billing and rate-limit identity retain the requested compact alias.
- HTTP response compact handling and channel model mappings continue to work.

### Verification

- Unit tests cover suffix parsing, exact precedence, memory-cache and database selection, token-model permission fallback, auto-group resolution, and upstream request rewriting.
- Run focused `go test` for `setting/ratio_setting`, `model`, `service`, `middleware`, and `relay/helper`.
- Run `go test ./...`, targeted `go vet`, `gofmt`, and `git diff --check`.

## Observe Content Moderation Affinity Re-audit

### Goal

Increase the cost of sustained violations in one affinity conversation without changing synchronous `pre_block` behavior.

### State Semantics

- In `observe` mode, an affinity cache entry with `flagged=true` is stale for reuse: each subsequent request with the same affinity key performs a fresh moderation check.
- A fresh flagged result writes a new moderation log and runs the existing violation count, auto-ban, and email side effects.
- A fresh allow result replaces the affinity entry with `flagged=false`; later requests reuse that allow result until the normal affinity TTL expires.
- A cached allow result remains a cache hit and does not call the moderation provider again.
- `pre_block` continues to reuse a flagged affinity result and blocks before pricing, quota reservation, and upstream forwarding.
- Provider failures and persistence failures remain fail-open and do not create a new violation count.

### Acceptance Criteria

- Two sequential `observe` checks whose affinity entry remains flagged produce two provider calls and two flagged audit rows.
- A subsequent allow result changes the same affinity entry to allow, and the next request is a cache hit with no provider call.
- A flagged `pre_block` affinity entry remains a blocking cache hit.

### Verification

- Run the focused content-moderation service tests, `gofmt`, `go vet`, and `git diff --check`.

## Group-Based Access and Content Policy

### Goal

Give administrators one policy surface for the user's base group to deny concrete channels, models, and target groups, and to exempt that base group from platform content moderation. This is an overlay on existing channel status, model abilities, token limits, usable-group rules, and global moderation configuration; it must not mutate those sources of truth.

### Policy Identity and Semantics

- The subject is the authenticated user's stored base group (`user_group`), not the token's selected group. Every token owned by that user inherits the same policy and cannot escape it by selecting another group.
- `auto` is filtered by the subject group's blocked target groups first; each remaining candidate group then applies the subject group's blocked model and channel sets.
- A policy entry is deny-only. No per-user or per-token allow override, expiry, schedule, or exception is included in the first version.
- Existing `GroupSpecialUsableGroup` remains the source of truth for the established user-group-to-target-group allow/deny mapping. New blocked target groups are an explicit deny overlay evaluated at runtime; they do not rewrite or compete with the existing `-:target_group` configuration.
- “屏蔽内容审查” means an explicit group-level moderation exemption (`content_moderation_disabled`). It skips this platform's pre-block/observe moderation check for that subject group; it does not disable provider safety controls, alter upstream model behavior, or erase historical moderation logs.
- Moderation is enabled by default. A missing, malformed, stale, or unavailable policy never grants the exemption; it falls back to normal moderation behavior. Routing restrictions fail closed when the policy cannot be loaded.
- The policy applies when selecting a channel for a new relay or async task. Existing task polling/result retrieval continues with its stored channel and does not re-evaluate the current policy.

### Data Model

Add one main-database `GroupAccessPolicy` row per subject group:

- `group_name` — trimmed non-empty string, unique/indexed.
- `blocked_channel_ids` — JSON text array of positive channel IDs.
- `blocked_models` — JSON text array of exact model names; no wildcard syntax in v1.
- `blocked_groups` — JSON text array of target group names.
- `content_moderation_disabled` — boolean, default false in application normalization rather than a dialect-sensitive DB default.
- `created_at` and `updated_at` — standard timestamps.

JSON arrays are normalized with the project's JSON wrapper, deduplicated, sorted, and bounded. Do not add policy fields to `User`, `Token`, `Channel`, or `Ability`, and do not create one row per user/token. Register the model in normal and fast migrations for SQLite, MySQL, and PostgreSQL.

### API Contract

Admin-only routes beside the existing group model-rate-limit administration:

- `GET /api/group-access-policies/:group` returns the normalized policy for one subject group.
- `PUT /api/group-access-policies/:group` accepts `{ blocked_channel_ids, blocked_models, blocked_groups, content_moderation_disabled }` and atomically replaces that group's complete policy.

The group must exist in the configured group-ratio/user-usable-group universe. Reject duplicate or invalid IDs, empty/oversized model names, unknown target groups, and oversized arrays before changing the previous policy. Channel IDs are positive and may remain stale after a channel is deleted; routing ignores missing channels and the UI marks them for cleanup. Existing admin audit middleware records every write. The API never returns moderation provider credentials.

The frontend should use the existing group selector and channel search/list API. One policy editor exposes four sections: blocked channels, blocked models, blocked target groups, and the moderation-exemption switch with an explicit warning. It must preserve unrelated `GroupSpecialUsableGroup` rules when editing target-group blocks, and show stale channel/model entries instead of silently dropping them.

### Enforcement Boundaries

1. Load and normalize the subject-group policy once per authenticated request, cache it in request context, and reuse the immutable snapshot for distribution, retries, model discovery, and content moderation.
2. In `TokenAuth`/playground group validation and `service.GetRequestAutoGroups`, remove blocked target groups from explicit and auto group candidates. A forged token or playground request naming a blocked target group receives the existing group-access-denied response.
3. In channel selection, filter blocked models before selecting and filter blocked channel IDs in both the memory-cache and database fallback paths before priority/weight selection. Apply the same predicate to affinity reuse, retries, WebSocket relay, playground, and new task submission.
4. The administrator-only `specific_channel_id` selector cannot bypass the subject group's policy; reject it if its requested model, resolved target group, or channel is blocked.
5. `/v1/models`, `/v1beta/models`, `/api/user/models`, and group discovery omit blocked models/groups. A model remains visible when at least one permitted target group and channel can serve it.
6. In `checkRelayContentModeration`, evaluate the subject-group exemption before extracting content or submitting to the moderation provider. When exempt, do not create a moderation log or violation side effect. When not exempt, keep the existing global `all_groups`, `group_ids`, `all_models`, and model-filter behavior unchanged.
7. A policy update that changes `content_moderation_disabled` changes the policy fingerprint used by content-moderation allow/affinity keys before success, so old allow results cannot be reused after moderation is re-enabled; no full cache purge is required.

### Cache, Failure, and Consistency Rules

- Use a shared Redis-first cache key such as `groupAccessPolicy:<group_name>` with a short local fallback TTL when Redis is disabled.
- Cache misses read the database and repopulate the cache. A routing-policy cache/database failure rejects the request before upstream traffic; a moderation-exemption cache/database failure treats the exemption as false and continues normal moderation.
- Successful replacement is transactional and synchronously refreshes/invalidate the shared policy cache. The subject-group policy is never queried once per candidate or retry.
- Updating `GroupSpecialUsableGroup` and this policy must not silently overwrite each other. If the UI presents them together, use a read-modify-write transaction or explicit conflict validation.

### Acceptance Criteria

- An unconfigured subject group preserves all existing behavior.
- A blocked target group cannot be selected through token creation/update, playground selection, explicit group requests, auto groups, or model discovery.
- A blocked model cannot be routed or advertised through any permitted target group, while another unblocked model remains usable.
- A blocked channel is never selected through normal routing, affinity, retries, WebSocket, or new task submission; the same channel may remain usable for another subject group.
- A subject group with `content_moderation_disabled=true` does not call the moderation provider, write new moderation logs, or execute moderation side effects; other subject groups remain governed by the existing global scope and mode.
- The exemption is never granted on policy/config/cache errors, and re-enabling it invalidates old moderation allow/affinity decisions.
- If every remaining channel/model/group candidate is blocked, no upstream request or quota charge occurs.
- Existing async tasks remain pollable after their stored channel or model becomes blocked.
- Policy replacement is atomic and cross-node visible through the shared cache; failed validation leaves the prior policy unchanged.
- Tests cover policy normalization, API authorization, group/model/channel filtering, auto-group and token validation, affinity/retry paths, model discovery, moderation exemption/default-deny/error fallback/cache invalidation, and existing-task polling.

### Implementation Tasks

1. Add `GroupAccessPolicy`, normalization, atomic replacement, migration registration, and cache helpers.
2. Extend group/token/model discovery helpers to apply policy overlays while preserving `GroupSpecialUsableGroup` behavior.
3. Thread the immutable policy snapshot through distributor, channel selection, affinity, retries, explicit-channel checks, and new task submission.
4. Add the moderation exemption check and cache invalidation hook without changing the existing global moderation scope semantics.
5. Add admin API/UI, conflict-safe target-group editing, i18n strings, and focused backend/frontend tests.
6. Run focused/full Go tests, `go vet`, frontend tests, `bun run typecheck`, i18n sync, production frontend build, and `git diff --check`.

### Non-Goals

- No per-user/token exceptions, wildcard model rules, temporary schedules, global channel status changes, provider safety bypass, moderation-log deletion, quota changes, or upstream-provider changes.
- Do not store the supplied production/test API key in source, documentation, tests, logs, or `MEMORY.md`.

## DeepSeek V4 Client Compatibility

### Goal

Keep the OpenAI-compatible `deepseek-v4-flash` route compatible with the customer's
request matrix without fabricating token probabilities or changing billing semantics.

### Request Semantics

- `reasoning_effort=extreme` is rejected with the official-shaped V4 validation error; it is not silently renamed to `max`.
- `top_p` outside the upstream-supported interval `(0, 1]` is rejected with the official-shaped V4 validation error; omitted values remain omitted.
- `thinking.type=disabled` remains disabled and must not be replaced by an enabled reasoning request.
- Advanced Custom selection and no-candidate diagnosis use the incoming request path; a model configured only for another path is not reported as temporary capacity loss.
- Tools, `tool_choice`, `stop`, streaming usage, and OpenAI `logprobs`/`top_logprobs` are forwarded without dropping response fields.
- If the DFLASH upstream rejects logprob generation because speculative decoding is enabled, NewAPI must return the upstream capability error rather than inventing logprobs. The channel configuration must be adjusted separately to disable speculative decoding for logprob requests if the upstream exposes such a control.

### Public Error Contract

- Middleware failures use an OpenAI-compatible `error` object and never expose `type=new_api_error`.
- Authentication failures return `type=authentication_error`, `code=invalid_request_error`, and `param=null`.
- Validation failures return `type=invalid_request_error` and `param=null`; typed internal selection errors keep their diagnostic code where applicable.
- Server-side selection failures return `type=server_error`, `code=server_error`, and `param=null`; detailed selection kinds remain internal diagnostics.
- The fit probe records protocol acceptance separately from effective success. HTTP 200 with neither final content nor a valid tool call remains an effective failure.

### Acceptance Criteria

- The four customer request shapes (basic, streaming usage, tools with disabled thinking, and stop) convert without request-local 400 validation errors.
- `reasoning_effort=extreme` and invalid `top_p` return the official-shaped 400 validation envelope; `tool_choice=required` is preserved because the official API currently accepts it.
- A valid `top_p` and an omitted `top_p` are preserved.
- A request with `logprobs=true` and `top_logprobs=5` preserves both fields and returns the upstream `logprobs` object when the selected channel supports it.
- Conversion tests cover the above cases and assert no API key or credential is present in fixtures.
- The fit runner defaults to gateway-only execution. Official API calls require an explicit opt-in because they may consume balance.

### Verification

- Run focused DeepSeek adaptor tests and `go test ./relay/channel/deepseek ./relay/channel/openai`.
- Run `gofmt` and `git diff --check` on changed files.

## Wallet Top-up Card Content

### Goal

Allow administrators to configure the wallet top-up card subtitle and an
optional contact dialog without changing the payment flow.

### Configuration And API Contract

- `payment_setting.topup_subtitle`: optional HTML content. When empty, the
  wallet keeps the localized `Choose an amount and payment method` default.
- `payment_setting.topup_contact`: optional Markdown or HTML content. When
  empty, the contact entry is hidden.
- `GET /api/user/topup/info` exposes these values as `topup_subtitle` and
  `topup_contact`.
- Both fields are public presentation content and must never contain secrets.

### Rendering And Safety

- HTML is rendered through the existing sanitized `RichContent`/DOMPurify
  path.
- Contact content is detected as HTML or Markdown and rendered accordingly.
- Localized `<tnt>` content remains supported through the existing content
  resolver.
- The contact dialog must be keyboard accessible and keep order history as a
  separate action.

### Acceptance Criteria

- Administrators can edit and clear both fields in payment settings.
- A non-empty contact value shows a `Contact Us` button and opens the rendered
  content in a dialog; an empty value shows no entry.
- A non-empty subtitle replaces the default and renders HTML safely; an empty
  value preserves the localized default.
- Existing installations retain their current wallet UI until configured.
- Focused frontend tests, typecheck, lint, production build, Go tests, and
  `git diff --check` pass.
