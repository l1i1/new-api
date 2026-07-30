# Waffo Pancake Currency Adjustment

## Scope

Keep wallet add-funds values in local CNY while Waffo Pancake checkout and
settlement remain in USD.

## Contract

- A wallet amount request for `71` with a local recharge price of `7` returns
  `497.00` for the user-facing payable amount.
- The Pancake checkout price is the local payable amount divided by the
  configured Waffo Pancake exchange rate (`CNY per USD`), then multiplied by
  the existing Pancake unit-price adjustment.
- The exchange-rate option falls back to the generic recharge price when it is
  unset or non-positive. This preserves existing deployments while making the
  conversion explicit and adjustable.
- The stored top-up `Money` and provider price snapshot use USD. The wallet
  amount endpoint uses CNY so the UI never formats a USD provider amount as
  RMB.

## Acceptance Criteria

- `71` with price `7`, group ratio `1`, no discount, and Pancake exchange rate
  `7` returns local `497.00` and provider `71.00`.
- The same calculation is used by the amount preview and checkout creation.
- Frontend custom-amount copy always shows the generic local multiplier
  (`x CNY 7`) for Waffo and Waffo Pancake; it does not infer a multiplier from
  the provider amount.
- Existing options without the new exchange-rate key continue to work through
  the fallback.
