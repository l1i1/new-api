# Pricing Currency Display

## Goal

Allow visitors to switch all monetary displays between CNY and USD.

## Scope

- The selector persists as a browser-local user preference and applies to
  balances, pricing, recharge previews, orders, and API usage costs.
- CNY values use the system USD-to-CNY exchange rate.
- USD values display the calculated USD amount directly.
- The default selection is CNY.

## Non-Goals

- Do not change administrator currency settings, account balances, recharge
  products, orders, API contracts, model ratios, payment requests, or
  server-side billing.
- New top-up records persist an immutable `payment_currency` snapshot. CNY and
  USD paid amounts can follow the display preference; other currencies stay in
  their original unit.
- Legacy orders without that snapshot retain their raw paid amount. Payment
  providers can settle CNY, USD, or EUR; showing a fabricated conversion would
  be inaccurate.

## Acceptance

- Switching from CNY to USD changes monetary displays from `¥` to `$` using
  the supplied exchange rate, without changing any submitted payment amount.
- Recharge-price mode still changes the calculated displayed price before the
  selected currency is formatted.
- Small non-zero prices retain a non-zero formatted value.
