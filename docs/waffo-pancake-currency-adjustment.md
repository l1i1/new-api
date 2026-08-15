# Waffo Pancake Wallet Currency Selection

## Scope

Use the wallet's top-level CNY/USD display selection as the Waffo Pancake
checkout currency for wallet add-funds orders.

This applies to the one-time wallet product only. Pancake's current payment
market supports CNY for one-time products, while subscription checkout keeps
using the subscription plan currency.

## Contract

- The frontend sends the selected `CNY` or `USD` value to both the amount
  preview and checkout endpoints. Missing currency remains compatible and
  defaults to `CNY`.
- For `CNY`, a wallet amount request for `71` with a local recharge price of
  `7` sends a `497.00` price snapshot.
- For `USD`, the local CNY payable amount is divided by the configured CNY/USD
  exchange rate and multiplied by the existing Pancake unit-price adjustment.
- The stored wallet top-up `Money`, `PaymentCurrency`, provider price
  snapshot, and signed settlement validation all use the selected currency.
- Newly created wallet products advertise both CNY and USD prices so either
  checkout currency can be selected at runtime.

## Acceptance Criteria

- `71` with price `7`, group ratio `1`, and no discount returns `497.00` for
  CNY and the matching converted amount for USD.
- The same selected-currency calculation is used by the amount preview and
  checkout creation.
- The provider amount is formatted in the selected currency in the wallet
  card and confirmation dialog.
- Newly created wallet products advertise both CNY and USD prices.
