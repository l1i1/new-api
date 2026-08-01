# Global Group+Model Rate Limits (Per-User)

## Goal

Extend the existing per-user model rate limits with fleet-wide rules managed in one
place: an administrator defines that **group X / model Y** allows each user at most
**N requests per window** (e.g. RPM). The same rule set applies to every user, and
each user has an independent counter.

Configuration is centralized under `/system-settings/security/rate-limit`, next to the
existing group-based request limits.

## Rule Semantics

- A rule is identified by exact trimmed `group_name`, exact trimmed `model_name`, and
  `window_seconds`.
- `max_requests` is the maximum number of incoming API requests one user may send for
  that group+model during the window. `10 requests / 60 seconds` is RPM 10; any window
  from 1 second to 30 days is supported.
- Every incoming request counts once, including requests that later fail validation,
  billing, or upstream processing after distribution. Automatic channel retries belong
  to the same incoming request and consume one count only.
- Task submission calls count. Task status/result/image/content fetches do not count as
  model calls (same skip set as the per-user model limiter).
- The effective group is the group the request was actually routed through: the
  auto-selected group when `auto` routing resolved to a concrete group, otherwise the
  `group` context value (user group overridden by token group).
- All enabled rules matching the request group and original model must allow the
  request. Rules for other groups/models are ignored.
- A rejected request returns HTTP 429, an OpenAI-compatible error body, and
  `Retry-After`.

Redis uses the existing atomic fixed-window counter; the counter key includes the user
ID so users never share a bucket. Multi-node production requires shared Redis.
Development without Redis uses the process-local limiter and does not provide
cross-process consistency. Redis counter failures are fail-closed.

## Data Model

Table: `group_model_rate_limits`

| Column | Type | Constraints |
| --- | --- | --- |
| `id` | integer | primary key |
| `group_name` | varchar(255) | required, indexed |
| `model_name` | varchar(255) | required |
| `window_seconds` | integer | 1 to 2,592,000 |
| `max_requests` | integer | 1 to 1,000,000,000 |
| `enabled` | boolean | set explicitly by application code |
| `created_at` | integer timestamp | managed by GORM |
| `updated_at` | integer timestamp | managed by GORM |

`(group_name, model_name, window_seconds)` is unique.

## API Contract

Both endpoints require administrator authentication (AdminAuth).

### GET `/api/group-model-rate-limits`

Response data is the complete rule list ordered by group, model, and window.

### PUT `/api/group-model-rate-limits`

The request body replaces the complete rule list atomically.

```json
{
  "rules": [
    {
      "group_name": "default",
      "model_name": "gpt-5.4-mini",
      "window_seconds": 60,
      "max_requests": 10,
      "enabled": true
    }
  ]
}
```

IDs and timestamps are server-owned. Duplicate group/model/window pairs and invalid
bounds are rejected with HTTP 400. An empty list removes all rules.

## Request Path

The new limiter runs after `TokenAuth` and `Distribute`, alongside the per-user model
limiter. At that point the authenticated user ID, effective group, and original model
name are available, while the relay retry loop has not started.

Rate-limit counter keys:

`rateLimit:v2:group:<sha256(group)>:model:<sha256(model)>:user:<user_id>:window:<window_seconds>`

Rules are cached fleet-wide. Shared Redis is the cache in production; administrator
replacement refreshes the shared cache after the database transaction. Without Redis, a
short process-local cache avoids a database query for every relay request.

## Acceptance Criteria

- An administrator can list and replace the global rule set from
  `/system-settings/security/rate-limit`.
- A request in group X for model Y is rejected after the configured per-user count.
- Different users, different groups, and different models use independent counters.
- Multiple windows/rules for the same group+model are enforced together.
- A window expiry allows requests again.
- Concurrent requests cannot exceed a Redis-backed limit.
- Requests without a matching enabled rule are unchanged.
- Invalid or duplicate rules do not partially replace existing rules.
- The per-user model limiter (user drawer) and existing group request limits stay
  unchanged.
- SQLite, MySQL, and PostgreSQL migrations use GORM-compatible schema definitions.

## Verification

- Focused Go model, controller, and middleware tests.
- `go test ./middleware ./model ./controller`
- `go vet ./middleware ./model ./controller`
- Frontend typecheck, focused lint, and tests.
- `git diff --check`.

## Risks

- Fixed-window limits allow a boundary burst of up to twice the configured rate.
- Non-Redis development mode is process-local and cannot enforce a fleet-wide limit.
- Cache refresh failure after a committed database update is surfaced to the
  administrator. The database remains the source of truth and existing cached rules
  expire within 30 seconds.
- Rules match the effective routed group; configuring a rule for a group that `auto`
  never resolves to will not match anything. Administrators should configure rules for
  concrete groups (for example `default`, `vip`).
