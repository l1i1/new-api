# Business Analysis Performance and Currency Display

## Scope

The administrator business analysis page must render monetary values in the
user-selected display currency (`CNY` or `USD`) and must not expose raw quota
units in value cells. The report endpoint must remain read-only and preserve
the existing daily/weekly periods and data contract.

## Implementation

- Convert report quota values to USD using `quota_per_unit`, then to CNY using
  `cny_per_usd` only when the selected display currency is CNY.
- Render one currency symbol per value. Request counts and user counts remain
  unitless counts; quota values in the used column and check-in range are also
  converted to currency.
- Replace the per-period consumption aggregate loop with one conditional-
  aggregate SQL query per period family (daily and weekly). The query keeps
  the existing time boundaries and log type filter while reducing 22 serial
  database scans to 2.
- Keep the React Query result fresh for five minutes so switching dashboard
  sections does not repeatedly request the same report during normal admin
  navigation.

## Acceptance Criteria

- CNY selection shows only `¥` values; USD selection shows only `$` values.
- No business analysis value cell contains the raw quota unit or a second
  currency representation.
- Daily and weekly consumption totals match the previous per-period sums.
- Empty periods return zero without an endpoint error.
- Frontend typecheck, focused currency tests, and focused Go tests pass.
