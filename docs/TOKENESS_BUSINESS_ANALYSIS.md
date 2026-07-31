# Tokeness Business Analysis

## Scope

Add an administrator-only, read-only business analysis view to the authenticated dashboard. The view ports the reporting semantics from `tools/tokeness-ops` without exposing dashboard credentials or making the browser enumerate users, top-ups, or logs.

## API contract

`GET /api/tokeness/business-analysis?daily_periods=14&weekly_periods=8`

The endpoint returns one JSON document containing:

- conversion metadata (`generated_at`, `quota_per_unit`, `cny_per_usd`)
- quota inventory metrics and enabled-account Top 20
- ordinary-quota origin metrics and no-top-up Top 20
- daily and weekly operating-flow buckets plus daily totals

The route uses `AdminAuth` and has no mutation side effects. Period counts are bounded by the server (daily 1-60, weekly 1-52). Shanghai time (UTC+8) is the reporting timezone and weeks start on Monday.

## Accounting rules

- Visible balance is `quota + aff_quota`.
- Inventory stocking uses enabled, non-deleted users and positive visible balance; negative balances remain in net-balance metrics only.
- A completed top-up is a success-like status or a positive `complete_time`.
- Top-up quota follows the existing `tokeness-ops` report rule: Creem `amount` is already quota; other providers use `money / 7 * quota_per_unit` when `money` is present, otherwise `amount * quota_per_unit`.
- Operating flow is an increment report: `top-up quota + non-recharge increases - consume quota`.
- Non-recharge increases are registration grants and check-ins from system logs plus manual quota additions and positive quota overrides from operation logs.
- All quota values remain available in raw quota units; the UI presents CNY and quota references and labels flow as period increments versus inventory as current balance.

## Acceptance criteria

- Admins can open `/dashboard/business` and switch between business analysis and existing dashboard sections without losing the existing routes.
- Non-admins do not see or access the section.
- The page has loading, error, empty, and refresh states; daily/weekly views and Top 20 tables are responsive at desktop and 390px widths.
- Frontend text is localized through the existing i18next locale contract.
- Backend tests cover timezone bucket boundaries, completed top-up detection, provider conversion, and non-recharge log parsing.
- New API frontend typecheck, lint, and production build pass; backend focused tests and `go test ./...` pass when the local runtime permits.
