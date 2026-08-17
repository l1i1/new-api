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
