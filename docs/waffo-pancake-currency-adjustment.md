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
- Existing bound wallet products must be migrated before CNY checkout is
  enabled. A product created before this change may still advertise USD only;
  changing the checkout request currency does not add a provider price.
- Saving a wallet binding verifies the authoritative Pancake catalog before
  persisting it. The selected store must be active with production enabled,
  and the selected one-time product must belong to that store, be active, have
  a published production version, and support both CNY and USD in that
  production version.
- A blank private key keeps the persisted key only when the merchant ID is
  unchanged. Changing merchants requires a new private key so credentials
  from different accounts cannot be combined accidentally.
- The wallet display currency and provider settlement currency are separate.
  `CNY + wechat_pay` settles in CNY; `CNY + card/apple_pay/google_pay` converts
  the same local payable amount to USD and settles in USD. Every USD selection
  settles in USD, including USD + WeChat.
- Wallet payment buttons reuse the existing administrator-managed `PayMethods`
  list. The processing identifiers are `waffo_pancake:wechat`,
  `waffo_pancake:card`, `waffo_pancake:applepay`, and
  `waffo_pancake:googlepay`; each suffix sends a single provider-method
  whitelist. Bare `waffo_pancake` sends no whitelist and uses an unrestricted
  USD checkout so the buyer can choose on Pancake's page. The confirmation
  dialog shows the actual provider charge currency.
- A CNY WeChat checkout may retry once in USD only when Pancake returns an
  explicit client error that CNY is unsupported for the product or market.
  Network failures, timeouts, authentication errors, and ambiguous server
  errors must not trigger a second checkout session.

## Acceptance Criteria

- `71` with price `7`, group ratio `1`, and no discount returns `497.00` for
  CNY and the matching converted amount for USD.
- The same selected-currency calculation is used by the amount preview and
  checkout creation.
- The provider amount is formatted in the selected currency in the wallet
  card and confirmation dialog.
- Newly created wallet products advertise both CNY and USD prices.
- An existing USD-only product is rejected when an administrator saves the
  wallet binding, with no partial settings update.
- A selected product without a production version is rejected even if a test
  version exists.
- A provider canary creates a CNY `1.00` checkout session with
  `includePaymentMethods: [wechat]` against the selected production product.
- CNY display + card, Apple Pay, or Google Pay previews and creates an USD
  checkout with the selected single-method whitelist.
- Bare `waffo_pancake` previews and creates an unrestricted USD checkout even
  when the wallet display is CNY, so Pancake can offer all supported methods.
- CNY display + WeChat creates CNY first and retries once with the equivalent
  USD amount only for an explicit unsupported-CNY provider response.
- The pending order's amount and currency snapshots are updated to USD before
  the fallback checkout is created, so signed settlement validates the actual
  provider charge rather than the original display currency.
