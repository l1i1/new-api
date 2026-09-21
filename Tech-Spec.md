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

## Upstream rc35 Synchronization

### Goal

Merge upstream `v1.0.0-rc.35` into `tokeness/main` while retaining all local
Tokeness worktree changes and custom behavior.

### Scope

The target tag is based on upstream `rc.34`. Because this fork currently follows
upstream `rc.33`, the merge includes the `rc.33..rc.35` history: account-security
and audit changes, model/vendor/pricing management, Wan 3.0 and Kimi K3 support,
plugin-routing changes, relay/performance fixes, and pricing UI updates.

### Merge Invariants

- Preserve the current Tokeness payment, invoice-fee, multi-key, DeepSeek,
  deployment, and other local behavior unless a conflict requires an explicit
  compatibility decision.
- Capture the dirty worktree in a reversible checkpoint before merging; do not
  reset, clean, or overwrite user files.
- Resolve conflicts by keeping the narrowest combined behavior and remove all
  conflict markers before verification.
- Do not push or deploy as part of this synchronization.

### Acceptance Criteria

- Every pre-merge tracked and untracked user change is recoverable and present
  after conflict resolution.
- `tokeness/main` contains upstream `v1.0.0-rc.35` as an ancestor.
- `git diff --check` passes and no conflict markers remain.
- Focused Go tests for changed packages, `go test ./...`, `go vet ./...`, relaykit
  standalone build/tests, frontend typecheck/tests/build, and deployment/config
  validation pass where applicable.

## Upstream rc36 Synchronization

### Goal

Merge upstream `v1.0.0-rc.36` into the Tokeness candidate while preserving
Tokeness payment, billing, routing, localization, pricing, and deployment
behavior and incorporating rc36's model, quota, plugin, redemption, and UI
changes.

### Acceptance Criteria

- The merge result has rc36 as an ancestor and no unresolved paths or conflict
  markers.
- Overlapping files contain the union of upstream behavior and Tokeness-owned
  behavior; no whole-file side selection is used to resolve semantic conflicts.
- Frontend locale JSON files have identical key sets and valid JSON.
- Root Go and standalone `relaykit` builds/tests, frontend typecheck/build, and
  `git diff --check` pass, with pre-existing baseline failures recorded.
- Parent/result blob comparison, fork-only commit survival, and orphan-consumer
  scans are run before the candidate is considered reviewable.

### Constraints

- Work is performed on an isolated candidate branch; no deploy or push is part
  of this synchronization.
- Existing dirty changes in the main checkout remain untouched.

## Upstream Snapshot 2026-09-15 Synchronization

### Scope and immutable inputs

- Fork parent: `b5b7c2d089dac58fe511985b4ca91b754f010766`.
- Upstream parent: `8529f209c85913de9ec98c5af49dcfce4a41361a` (rc37 plus 16 commits).
- Common base: rc36 `ea7cb0ba4e0f82e2bfa5e55752eb68bdf902f71b`.
- Integrate the 41 upstream commits in an isolated worktree, review the candidate,
  then merge into `tokeness/main`. Push and production deployment are separate work.
- Preserve the main checkout's existing MEMORY.md modification. The failed-request
  refund-log feature was intentionally removed before this sync and must not be
  resurrected from the obsolete `70c3984da` baseline.

### Tasks and contract priorities

1. Resolve conflicts as a behavioral union, with separate ownership for routing,
   authentication/options, relay/billing, frontend pricing, channel editing,
   application state, commerce/logs, and locales.
2. Trace Responses HTTP/WebSocket validation, authorization, billing, retries,
   concurrency slot lifetime, affinity and credential selection together. Preserve
   existing official-fit and content-policy behavior and synchronous refunds.
3. Combine multi-RP passkeys with configured user-verification policy, single-use
   challenges, trusted origins and atomic domain settings. Review applicable OWASP
   Authentication/Session guidance before modifying authentication behavior.
4. Adopt upstream expression parsing while retaining time conditions, currency
   preferences, original prices, task/video prices, localized model text and TTFT.
   Trace backend usage fields through summaries and audit details to UI consumers.
5. Preserve channel settings through create/edit/readback, especially concurrency,
   official-fit models, credential revisions/proxies and scheduled multi-key tests.
6. Preserve HotPay per-application settlement, invoice currency/fees, security and
   deployment controls; verify options/index migrations on representative schemas.
7. Merge locale keys without dropping translations; require byte-identical JSON
   round-trip before rewriting, identical final key sets and no duplicate keys.

### Acceptance and verification

- The reviewed result contains both immutable parents, has no unresolved paths or
  conflict markers, and passes `git diff --check`.
- Record both directions of parent/result blob equality, inspect suspicious cases,
  scan fork-only commit additions for surviving behavior, and inspect orphaned
  producers. Renamed/replaced implementations need semantic evidence, not matching
  line counts alone.
- Root Go build/vet/test use `-p 2` on this Windows machine; independently build and
  test relaykit with `GOWORK=off`. Exercise changed billing/auth/relay contracts.
- Run frontend typecheck, lint on changed code, production build, i18n synchronization
  and both Vitest and node:test phases. Compare failures with this exact fork parent
  in a separate baseline checkout; do not reuse historic rc36 failure counts.
- Use representative UI smoke checks for pricing, channel settings, passkeys,
  notifications and payments. Record any runtime limitations explicitly.
- Independent review must close new merge regressions before landing. Preserve
  unrelated mainline changes if the branch advances while the candidate is tested.

### Post-merge review (2026-09-15)

- Review merge `7b7a4b55a` on `tokeness/main`, including unresolved test failures,
  credential/proxy continuity, billing, authentication and frontend consumers.
- Fix reproducible defects and incomplete test fixtures without weakening access
  controls or changing assertions merely to match failing output.
- Artifact requests must resolve the persisted task credential and proxy snapshot
  through the existing task-access resolver, including reordered or removed keys.
- Run both frontend test phases and root/relaykit Go checks to completion. Record
  exact remaining failures and their evidence; an unfinished process is not a pass.
- Preserve the user's existing MEMORY.md edits. Commit only reviewed task changes;
  pushing and deployment remain outside this review.

## Upstream rc38 Synchronization (2026-09-20)

### Immutable inputs and scope

- Fork parent: `474fe9470e7c2809543293709911198e65b13f0d`.
- Upstream target: tag `v1.0.0-rc.38`, commit `2906e4f779b715f282ae11203211dca77051d5af`.
- Merge base: `69a50029819a26c53e6babd276d49cfe2f8880ad`; 26 upstream commits,
  239 changed paths, 88 paths also changed by the fork, and 42 predicted conflicts.
- Work in isolated candidate `codex/sync-upstream-rc38`. Exclude the eight later
  commits on upstream/main. Do not publish images or deploy production.
- Mainline advanced independently to `3c06c3183` during candidate verification
  (six commits after the immutable fork parent) and has concurrent uncommitted
  work. This candidate remains scoped to the immutable inputs. Landing it must
  preserve and revalidate the newer mainline changes in a subsequent integration;
  candidate acceptance is not approval to overwrite or reset mainline.
- A later refresh found mainline/origin at `1acf0f0c1` (eight commits after the
  fork input), adding operator-editable never-retry keywords. Landing must move
  that option into the rc38 request-policy snapshot/UI and preserve its tests
  and locale keys, together with any still-uncommitted concurrent edits.

### Tasks and preserved contracts

1. Capture build, vet, test, frontend typecheck/build, both test-runner phases,
   lint and format results on the exact fork parent before merging.
2. Merge without committing; resolve each conflict by behavior, retaining both
   sides unless replacement is proven. Preserve GitHub identity evidence and
   fork authentication controls, request-policy routing, retry/dedup/affinity and
   concurrency cleanup, rate-limit reservations and rejected-request LRU rules.
3. Preserve empty Chat output, cancellation settlement, image validation, quota
   saturation/auditing, official-fit behavior, task credential/proxy snapshots,
   WebSocket passthrough limits and response stream correlation.
4. Merge channel/settings UI, request policies, pricing/log/performance consumers,
   mainland CNY/language behavior and partner features. Maintain seven frontend
   locale key sets without duplicates; prove byte round-trip before writes.
5. Audit both directions of parent/result blob equality, fork-only commit content
   survival, orphaned producers and every changed cross-layer field/enum.
6. Save candidate-specific executor, independent review, manual QA and execution
   records under the ignored `.review/rc38/` directory; summarize durable findings
   in MEMORY.md. Do not inherit rc37's rejected release evidence.

### Verification and acceptance

- Root Go build/vet/test and independent relaykit build/vet/test with GOWORK=off.
- Frontend typecheck/build, full official test runner (both phases even on failure),
  lint, format, locale parity and git diff --check. Attribute every failure against
  the exact parent; no unexplained or newly introduced failures may remain.
- Focused regression checks for authentication, task access, routing/concurrency,
  stream outcomes, quota settlement and frontend configuration persistence.
- Representative local browser QA for changed settings, pricing and logs, with
  limitations recorded rather than treating unavailable checks as passed.
- The merge must have exactly the immutable parents above; no conflict markers,
  lost fork behavior, unresolved review findings or missing verification phases.
- Stop publication if target/ancestry is wrong, a contract regresses, evidence is
  incomplete, or a gate remains unexplained. Keep the isolated candidate reviewable.

### Open authentication gates identified during review

The exact fork parent already contains three security gaps: anonymous OAuth
login state is not bound to its initiating browser, built-in provider subjects
lack database-enforced ownership, and GitHub profile email can become account
evidence without verified-email confirmation. The candidate retains these root
causes; their baseline attribution does not grant production approval.

The remediation plan must preserve existing authentication and account data:

1. Bind anonymous login state to a browser-held, HttpOnly nonce validated by the
   callback; retain expiry and single-use checks. Verify same-browser success,
   cross-browser rejection, replay, expiry and concurrent login flows.
2. Evaluate the existing external-identity ownership table before adding a new
   schema. Protect every built-in binding and legacy migration writer with one
   database uniqueness rule. Detect legacy duplicate subjects before migration;
   never silently choose an owner. Verify concurrent binds, rollback, fresh
   startup and repeated upgrades against SQLite, Oracle MySQL and PostgreSQL.
3. Use GitHub's verified email list for account evidence. If the endpoint fails
   or yields no verified address, do not auto-bind the profile email. Verify
   endpoint failure, denied scope, unverified-only and verified-address cases.

These security-model and legacy-data decisions require their own explicit
implementation scope. Until the controls are implemented and verified, keep
authentication approval open and do not publish or deploy this candidate.

### Routing acceptance addendum (rc38 P1 closure)

The distributor and shared selector must use one request-scoped candidate
predicate for request path, task-plugin identity, Responses WebSocket capability,
channel status, group/model ability, and the immutable group-access-policy
snapshot. A candidate rejected by a request filter is excluded for this attempt
and selection continues through remaining priorities and auto groups. A resolved
explicit pin remains a single-candidate decision and returns its existing
validation/access error without falling through to another channel.

Channel affinity is a candidate reuse path, not an authorization bypass. Before
an unpinned affinity hit is accepted, routing must re-check the current request
policy for the channel, resolved model, selected target group, request path, and
channel ability. The same checks apply to Responses WebSocket selection and
HTTP retries; a denied affinity hit is evicted or skipped according to the
existing session-mode semantics, then normal eligible selection decides whether
to fall back.

Acceptance requires regression coverage for: (1) an HTTP task-plugin request
where a high-priority identity-mismatched channel is skipped in favor of a
lower-priority matching channel; (2) an unpinned affinity hit rejected after a
channel/model/group policy block; (3) a valid lower-priority Responses WebSocket
channel after an incompatible higher-priority candidate; and (4) explicit pin,
locale/status, token-limit, auto-group, retry, and group-access-denied behavior
remaining unchanged. Focused middleware, service, and relay tests must pass,
followed by root build/vet and `git diff --check`.

## Upstream rc39 Synchronization (2026-09-21)

The user advanced the synchronization target to exact `v1.0.0-rc.39` at
`9978ee1e25a647bfe004e96c8719a2cb62c24732`. This supersedes the rc38-only
target above. The new isolated candidate is `codex/sync-upstream-rc39`, based on
fresh fork/origin `dacee08ae13734cbc7b658e7e37d19f7e06d3cf9`.

Keep the reviewed rc38 tree `5cb5074ace552dac0d617e1af26d668dbcc38ed0` and its
original worktree as a checkpoint. Reconstruct its behavioral resolutions with
explicit-base tree merges: first integrate the ten newer fork commits against
the original fork `474fe9470`, then integrate rc39 against rc38 `2906e4f779`.
The real pending merge must retain the new fork HEAD and exact rc39 MERGE_HEAD.
No intermediate tree is a releasable commit. Do not commit, push or deploy.

Preserve operator-editable never-retry keywords, their precedence after
keep-alive-only writes, snapshot consistency and translated grouped settings;
preserve the new partner username lookup. Review rc39's task-plugin image API,
multiple plugin bindings, async terminal metrics, trust threshold and input
pre-consume multiplier, metadata sync, usage-log and pricing contracts. Retain
fork credential/proxy snapshots, quota saturation/audit, CNY and TNT rendering,
TTFT, original prices, official-fit routing and protected attribution.

The rc38 verification matrix and three fixed merge audits above apply again to
the new immutable parents. Re-run the exact-parent baseline, then candidate Go,
relaykit and frontend gates, real database checks for affected paths and local
desktop/mobile browser QA. Every failure needs current-parent attribution;
rc38 checks cannot establish rc39 acceptance. Freeze only when conflicts,
contract regressions and unexplained new failures are zero and evidence records
the final index tree. Existing authentication gaps remain production blockers.

Detailed execution evidence is kept under `.review/rc39/`; the main checkout
must remain untouched. Refresh origin and inspect concurrent changes before
any later landing.

### rc39 billing reservation closure addendum (2026-09-21)

The tiered-expression reservation must retain the fork's conservative output
estimate while adopting rc39's configurable input pre-consume multiplier. For
token-priced expressions, evaluate the frozen expression twice at request
time: `F = expr(P, C_est, Len)` and `I = expr(P, 0, Len)`, where `C_est` is
the explicit `max_tokens` estimate or `8192` for a paid group when it is
omitted. Reserve `max(F, F + (m - 1) * I)` after quota conversion and group
scaling, so a multiplier below one never reduces the full-output safety
estimate. Fixed request pricing reserves only `F` and is never multiplied.
Free groups reserve zero. Freeze `C_est` and the resulting reservation inputs
in `BillingSnapshot`; settlement continues to evaluate actual usage without
the reservation multiplier.

The closure must use one small shared estimator for normal tiered pricing and
image quantity retries, preserve strict quota conversion and older snapshots
where a missing multiplier means `1`, and avoid AST changes, new settings or
dependencies. Acceptance requires focused cases for explicit and omitted
output limits, multipliers below and above one, fixed and free pricing,
nonlinear completion branches, and image retry reuse of the frozen formula.

### rc39 + 最新主线合并闭环（2026-09-22）

合并结果树 `a1efa7481349380e8a2d11df2bc9c798a38b0ccb`，parents 仍为 fork
`dacee08ae` 与 rc39 `9978ee1e2`；最新主线 8 提交（`3a2b41d86`..`a6cab488f`）
以显式 base `dacee08ae` 的 tree-merge 并入后再人工解决 7 处目标冲突。
仍是**未提交候选**，不 move 任何 ref，不改主 checkout。

重试策略的最终契约（合并后的唯一实现）：

- 决策顺序固定为「永不重试 → 换同渠道另一个 Key → 换渠道」；失效的
  `ForceRetryStatusCodes` 从前端下线，后端保留为兼容/迁移选项，failover 默认
  为旧 automatic 范围 ∪ {400}。
- `MultiKeyCredentialRetryKeywords` 进入 request-policy 快照的默认值与
  allowlist（缺任一处则设置页无法保存），并同时更新全局
  `operation_setting.MultiKeyCredentialRetryKeywords`。
- 永不重试关键词判定读不可变快照（`CurrentRequestPolicy().MatchesNeverRetryKeywords`），
  状态码与自动重试关键词读全局；两条写路径都会刷新快照。
- 设置页分组顺序、字段与文案由
  `web/src/features/system-settings/request-policies/channel-health-section.tsx`
  定义，7 个 locale 键已对齐（7401 keys × 7）。

证据（均在 `.review/rc39/`）：

- `mainline-blob-compare2.mjs`：双边改动 28 路径、主线覆盖候选 0 路径；3 个
  「候选胜出」路径是 rc39 的 section 搬迁。
- `go-test-merged-2.log`、`go-build-merged.log`、`go-vet-merged.log`、
  `relaykit-*-merged.log`：root 与独立 relaykit 全绿。
- `db-lifecycle-merged.log`（run `20260922002005_2c7d554e`）与
  `db-tests-merged-2.log`：SQLite 3.50.4 / Oracle MySQL 8.4.7 /
  PostgreSQL 18.6，fresh 与 rc38→rc39 upgrade、两次重启、无重复 `ALTER TABLE`。
- `web-typecheck-merged.log`、`web-build-merged.log`、`web-test-merged.log`：
  typecheck/build 绿；vitest 5 失败与 node:test 4 失败与 exact-parent 基线一致
  （基线 12 失败 / 7 失败，本候选更少）。
- Playwright 真浏览器：桌面 1440×900 与移动 390×844 打开
  `/system-settings/request-policies/health`，分组顺序、`换 Key 重试错误关键词`
  字段与保存往返均通过（UI 改值后用独立 API 读回校验，随后恢复默认值）。

已知未覆盖：`agent`/Computer Use 的 in-app 浏览器在本机因
`unsupported Codex auth method: apikey` 无法初始化，因此改用 Playwright 完成
真实渲染检查；独立 `LOG_DB`（`LOG_SQL_DSN` 指向另一套库）仍未覆盖。
