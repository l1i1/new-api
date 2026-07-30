# Payment Settlement Hardening

Date: 2026-07-30

## Problem

Signed payment notifications can currently be acknowledged before the local
settlement is durable. Waffo Pancake also compares a provider-returned email
address with a synthetic user identity, which rejects valid paid orders. EPay
updates the top-up row and user quota separately after already returning
`success` to the provider.

The user-visible failure is a paid order that remains pending or a successful
order whose wallet quota was not credited. Manual completion can compound the
problem when a delayed notification later arrives or the supplied order number
does not identify a paid provider order.

## Scope

- Wallet top-ups through Waffo Pancake and EPay.
- EPay subscription notifications that share the same acknowledgement risk.
- Payment webhook routing and rate limiting.
- Operational reconciliation for pending payment orders.

This change does not automatically credit historical orders. Historical
settlement remains a separate, evidence-backed reconciliation operation.

## Settlement Contract

### Common invariants

1. A provider notification is trusted only after provider signature
   verification succeeds.
2. The merchant order number must resolve to exactly one local order whose
   `payment_provider` matches the callback route.
3. Provider-reported paid amount and currency must match the immutable local
   checkout amount and currency whenever those fields are present.
4. Order status, completion time, and wallet/subscription entitlement changes
   are committed in one database transaction under a row lock.
5. Repeated delivery of the same successful notification is idempotent and
   never credits twice.
6. Literal provider success is returned only after the transaction commits or
   after confirming that the same order was already settled successfully.
7. Unknown orders, provider mismatches, amount mismatches, and database errors
   are not acknowledged as successful delivery.
8. Logs may contain order identifiers, event identifiers, provider, amount,
   currency, and a one-way digest of sensitive identity fields. They must not
   contain email addresses, API keys, signatures, or raw callback bodies.

### Waffo Pancake

- `orderMerchantExternalID` is the local `trade_no` and is the lookup key.
- `MerchantProvidedBuyerIdentity` is not an authorization invariant because
  production callbacks return the customer's email instead of the synthetic
  identity sent by New API.
- Wallet top-ups must match the callback amount in USD against `TopUp.Money`.
- Subscription purchases must match the callback amount and currency against
  the plan/order price snapshot available to the local settlement path.
- A verified `order.completed` event that cannot settle returns a retryable
  non-2xx response.

### EPay

- Signed callback fields `out_trade_no`, `trade_status`, `type`, and `money`
  are validated before settlement.
- The callback `money` must equal the local `TopUp.Money` or subscription order
  price at two-decimal currency precision.
- A successful wallet callback uses one model-layer transaction to update the
  top-up and increment user quota.
- Both notify and return handlers use the same idempotent settlement operation;
  browser return handling must not create a second credit path.

## Webhook Availability

Payment callback routes must not consume the general dashboard `/api` IP rate
limit bucket. They receive a dedicated limiter keyed by provider and client IP,
with a capacity suitable for provider retry bursts. Signature verification and
request-body limits remain mandatory.

If a provider does not expose a safe order-query API through the configured
integration, reconciliation must not guess or auto-credit. The minimum safe
recovery path is a periodic pending-order scan that emits deduplicated,
actionable alerts and exposes the affected order IDs to administrators.

Administrator completion is provider-verified and fail-closed:

- Waffo Pancake is queried through its signed merchant GraphQL API. A payment
  must be uniquely successful, completed, production-mode, unrefunded, in the
  configured store, and match the local external order ID, USD currency, and
  checkout subtotal.
- EPay verification requires `EPAY_RECONCILIATION_QUERY_URL` to point to a
  dedicated internal `/api.php` origin whose access log does not record query
  strings. Plain HTTP is accepted only when the URL host is a literal private
  or loopback IP, preserving direct Docker-to-origin deployments such as
  `172.18.0.250`. Private and loopback IP literals may also use HTTPS. Every
  other HTTPS host must be listed exactly in the comma-separated
  `EPAY_RECONCILIATION_ALLOWED_HOSTS` allowlist; URL entries, ports, wildcards,
  and suffix matching are not accepted. Host comparisons normalize DNS case,
  trailing dots, and IP spelling. The configured public payment host is always
  rejected after the same normalization, even if it appears in the allowlist,
  because the standard `act=order` API carries the merchant key in its query
  string. Redirects and environment HTTP proxies are disabled, the timeout is
  five seconds, and the response body is capped at 64 KiB.
- Providers without a verified query implementation cannot use the generic
  administrator completion endpoint.

## Acceptance Criteria

- A valid signed Waffo Pancake wallet callback using an email identity settles
  exactly once and returns 200 only after commit.
- Waffo callbacks with an unknown order, wrong provider, wrong amount, wrong
  currency, invalid signature, or database failure do not return success.
- Waffo logs do not contain the callback buyer identity or buyer email.
- A valid signed EPay callback atomically updates status, completion time, and
  quota, then returns `success`.
- Replaying the same EPay or Waffo callback does not change quota again.
- EPay amount mismatch and forced database failure return `fail` and leave the
  order and quota unchanged.
- EPay subscription callbacks follow the same acknowledgement and amount
  validation rules.
- Payment callback routes are not rejected by the global API rate limiter, but
  remain protected by a dedicated limiter and body-size limit.
- Administrator completion cannot credit an EPay or Waffo Pancake order until
  the provider confirms paid state, identity, amount, currency/environment,
  and non-refunded state. Unsupported or unavailable verification fails closed.
- Focused controller/model/middleware tests, `go test ./...`, and `go vet ./...`
  pass. Database code remains compatible with SQLite, MySQL, and PostgreSQL.
- Production deployment uses the existing immutable-digest staged workflow and
  is followed by public callback availability checks plus database/log
  reconciliation without exposing secrets.

## Rollback

Rollback uses the previous trusted immutable image digest through the existing
four-node deployment workflow. No schema migration is required unless a later
implementation explicitly updates this document and adds cross-database
migration coverage.
