# Self-Service Invoice System — Tech Spec & PRD

Status: Implemented — this document is the source of truth for the current implementation.

## 1. Goal

Allow a signed-in user to apply for an invoice for eligible paid orders. The user can
select multiple orders, submit individual or company invoice material, track the
application through a six-state workflow, and cancel a pending application.
Administrators can review, progress, and complete applications. Root/super
administrators can configure the feature switch, invoice notice, and minimum amount.

The system does not generate invoices itself: an administrator uploads the real invoice
PDF during completion, and the service immediately sends it as the issued-email
attachment. The PDF is not persisted by this service.

## 2. Scope and explicit boundaries

### In scope

- User invoice application page with multi-select paid orders.
- Individual and company invoice applications with a mandatory reason for individuals.
- User application list, detail, status, and pending cancellation.
- Admin list, search, detail, approve, start-issue, complete-issue, and reject.
- Real invoice PDF upload and immediate attachment on the issued email.
- Backend models, migrations, APIs, authorization, audit, and email notification.
- Invoice notice, feature switch, and minimum amount settings.
- English, Chinese, Traditional Chinese, French, Japanese, Russian, and Vietnamese UI strings.

### Out of scope

- Tax-bureau/e-invoice provider integration or automatic invoice-number issuance.
- Refund handling.
- Automatic inclusion of balance credits.
- Balance-paid subscription orders (no TopUp snapshot is created for them).

### Invoiceable order source

V1 uses `TopUp` as the only invoiceable order source. An order must satisfy all of:

- belongs to the current user;
- `Status = common.TopUpStatusSuccess`;
- `PaymentProvider` is non-empty and is not `balance`;
- `PaymentCurrency` is non-empty;
- `Money` is positive and finite;
- is not currently claimed by another invoice application (`invoice_order_claims`);
- has not already been used by an approved, issuing, or issued application.

External subscription payments are invoiceable only when their `TopUp` row contains the
complete provider, currency, payment method, money, and trade-number snapshot. The
subscription payment path persists `PaymentProvider` and `PaymentCurrency` from the
subscription order onto the `TopUp` row (`upsertSubscriptionTopUpTx`). An order with an
empty currency is never treated as CNY: it is simply not invoiceable.

## 3. Business and money rules

- Invoice amount is the actual paid amount `TopUp.Money`, never quota or credited quota.
- All orders in one application must have the same `PaymentCurrency`.
- No cross-currency conversion is performed.
- `InvoiceMinAmount` is interpreted in the selected order currency.
- Amount addition, comparison, and persistence use decimal arithmetic
  (`github.com/shopspring/decimal`). Raw `float64` values are converted through
  `model.MoneyFromFloat`, which rejects NaN and infinite values; totals are summed as
  decimals and stored back as the float64 snapshot.
- Each item stores immutable snapshots of amount, currency, payment method, and trade number.
- The invoice total is calculated from the validated snapshots inside the creation transaction.
- `InvoiceMinAmount` must be finite and non-negative. Invalid, negative, NaN, or infinite
  values are rejected when the option is saved and treated as disabled/fail-closed when read.
- The minimum is re-checked inside the creation transaction with decimal comparison.

## 4. Data model

### `invoices`

| Column | Type | Constraints |
| --- | --- | --- |
| `id` | int | primary key |
| `user_id` | int | required, indexed |
| `invoice_type` | varchar(16) | `individual` or `company` |
| `title` | varchar(255) | required |
| `tax_id` | varchar(64) | required |
| `phone` | varchar(32) | optional |
| `address` | varchar(255) | optional |
| `bank_name` | varchar(128) | optional |
| `bank_account` | varchar(64) | optional |
| `email` | varchar(255) | required, validated |
| `reason` | varchar(512) | required for individual |
| `remark` | varchar(512) | optional, user to admin |
| `status` | varchar(16) | indexed, see state machine |
| `admin_note` | varchar(512) | optional, admin to user; never cleared by an empty note |
| `total_amount` | float64 | validated decimal sum snapshot |
| `currency` | varchar(8) | required |
| `create_time` | int64 | required |
| `update_time` | int64 | required |

### `invoice_items`

| Column | Type | Constraints |
| --- | --- | --- |
| `id` | int | primary key |
| `invoice_id` | int | required, indexed |
| `order_type` | varchar(16) | `topup` in v1 |
| `order_id` | int | required |
| `trade_no` | varchar(255) | required snapshot |
| `amount` | float64 | required snapshot |
| `currency` | varchar(8) | required snapshot |
| `payment_method` | varchar(50) | display snapshot |

There is intentionally no permanent unique constraint on `(order_type, order_id)` here:
historical items must remain available after rejection or cancellation.

### `invoice_order_claims`

This table represents the current active claim and is separate from historical items:

| Column | Type | Constraints |
| --- | --- | --- |
| `id` | int | primary key |
| `order_type` | varchar(16) | required |
| `order_id` | int | required |
| `invoice_id` | int | required, indexed |
| `create_time` | int64 | required |

A unique constraint on `(order_type, order_id)` provides database-level protection
against concurrent applications. A claim is inserted when an application is created and
deleted when the application becomes `rejected` or `cancelled`, keeping historical
rejected/cancelled records while preventing double-attachment.

All tables are registered with the existing cross-database AutoMigrate path using GORM
and the repository's locking helper.

### Real invoice PDF delivery

The administrator-uploaded PDF is size- and magic-byte-validated, then retained only in
the request while it is attached to the issued email. No filename, size, byte content, or
local-file reference is saved by the application. If SMTP delivery fails, the state stays
`issuing` and the administrator must upload the PDF again to retry. The service cannot
resend a previously uploaded PDF.

The `complete-issue` endpoint caps the total request body with `http.MaxBytesReader`
(12 MiB) and the file itself at 10 MiB.

## 5. State machine and transactions

```text
pending --approve------> approved --start-issue------> issuing --complete-issue------> issued
pending --reject-------> rejected
pending --cancel-------> cancelled
```

| Status | User meaning | Final |
| --- | --- | --- |
| `pending` | 待审核 | no |
| `approved` | 审核通过，待开票 | no |
| `issuing` | 开票处理中 | no |
| `issued` | 已开具 | yes |
| `rejected` | 已驳回 | yes |
| `cancelled` | 已取消 | yes |

Creation transaction:

1. Validate feature switch, request shape, required fields, and order list.
2. Sort order IDs and lock all selected `TopUp` rows with `lockForUpdate`.
3. Re-check ownership, success status, provider, currency, positive money, and eligibility.
4. Verify one currency and calculate the total with the money helper (decimal).
5. Enforce the configured minimum.
6. Insert `invoices`, snapshot `invoice_items`, and insert `invoice_order_claims`.
7. Roll back on any claim conflict or validation error.

Every admin transition and user cancellation runs in a transaction, locks the invoice
row, verifies the current state, then updates status, note, and timestamp. Repeating an
already-applied identical transition is an idempotent success that reports "no change"
to the controller, so a repeat never writes a second audit entry and never sends a
duplicate final-status email. All other invalid transitions (including transitions
outside the allowed state machine) return a localized business error. Claim deletion for
`rejected` and `cancelled` occurs in the same transaction as the state update. A
non-empty admin note is kept for the last state change; an empty note never clears the
previous one. For `complete-issue`, SMTP delivery runs while the invoice row is locked,
then the transaction changes the state to `issued` only after SMTP accepts the
request-local PDF attachment. This prevents concurrent completions from sending it
twice, while a delivery failure rolls back and leaves the application in `issuing`.

## 6. API contract

All responses use the existing API response conventions. Request bodies bind to explicit
DTOs, never directly to GORM models. Error messages are translated through the backend
i18n bundle based on the user's language; no hardcoded Chinese error strings remain in
the invoice controller.

### User endpoints

#### `GET /api/invoice/options`

Authenticated user. Returns:

```json
{
  "success": true,
  "data": {
    "enabled": true,
    "notice": "...",
    "min_amount": 100,
    "orders": [
      {
        "order_type": "topup",
        "order_id": 123,
        "trade_no": "...",
        "amount": 10.0,
        "currency": "CNY",
        "payment_method": "epay",
        "create_time": 1710000000
      }
    ]
  }
}
```

Only invoiceable orders are returned. `notice` may be empty.

#### `POST /api/invoice`

Authenticated user; apply the existing critical rate limit used by top-up flows.

```json
{
  "orders": [{ "order_type": "topup", "order_id": 123 }],
  "invoice_type": "company",
  "title": "Acme Inc.",
  "tax_id": "9131...",
  "phone": "",
  "address": "",
  "bank_name": "",
  "bank_account": "",
  "email": "billing@acme.example",
  "reason": "报销",
  "remark": ""
}
```

Validate and trim all fields. Required fields are title, tax ID, delivery email, and at
least one order. `reason` is required for `individual` applications and optional for
`company`. The account email must be bound before submission. Apply server-side
ownership, eligibility, same-currency, minimum, and claim checks inside the creation
transaction.

#### `GET /api/invoice?p=1&page_size=20`

Returns only PII-free list rows: ID, invoice type, status, total, currency, create/update
time. Never returns tax ID, bank account, address, phone, email, reason, remark, or admin
note. Default and maximum page sizes follow existing list APIs.

#### `GET /api/invoice/:id`

Requires ownership. Returns `InvoiceDetail`, including material, items, reason, remark,
and admin note.

#### `POST /api/invoice/:id/cancel`

Requires ownership and `pending` status. Updates the application and releases its claims
atomically. Records an `invoice.cancel` audit entry with invoice ID, from/to status, and
actor.

### Admin endpoints

An explicit `AdminAuth`-protected route group at `/api/invoice/admin`:

- `GET /api/invoice/admin?p=1&page_size=20&keyword=&status=`
- `GET /api/invoice/admin/:id`
- `POST /api/invoice/admin/:id/approve`
- `POST /api/invoice/admin/:id/start-issue`
- `POST /api/invoice/admin/:id/complete-issue`
- `POST /api/invoice/admin/:id/reject`

The list response uses `items`, `total`, `page`, and `page_size`, sorted by id DESC. The
admin list DTO contains no invoice material (no tax ID, bank account, address, phone,
email, reason, remark, or admin note) and does not return `[]*model.Invoice` directly.
`keyword` searches server-side across title, tax ID, email, and reason without echoing
sensitive columns in list rows. `status` accepts only the six known states.

Admin detail DTOs contain the material and necessary user context. Reject requires a
non-empty note; other transitions accept an optional note. `complete-issue` requires a
real invoice PDF upload for the first issuance (multipart form field `file`), and is
idempotent for repeats after issuance.

## 7. Settings and authorization

Options registered in `model.InitOptionMap` and the frontend billing settings types:

- `InvoiceEnabled: bool`, default `false`;
- `InvoiceNotice: string`, default empty;
- `InvoiceMinAmount: number`, default `0`.

The option controller validates the minimum as finite and non-negative (rejects NaN,
+Inf, -Inf, negative). The user options endpoint is the only settings data source for the
user page; the user page never calls the RootAuth option endpoint.

Authorization is deliberately split:

- User endpoints: authenticated user plus ownership where applicable.
- Review endpoints: `AdminAuth`.
- Invoice settings: Root/Super Admin only, matching existing system-settings access.
- Frontend `/invoices`: route-level `beforeLoad` admin guard plus backend enforcement.

## 8. Audit and notification

Audit actions: `invoice.approve`, `invoice.start_issue`, `invoice.complete_issue`,
`invoice.reject`, and `invoice.cancel`. Audit records contain the invoice ID, actor,
previous state, new state, timestamp, and a necessary note summary. The note summary is
truncated and scrubbed of email addresses. Never include tax ID, bank account, email,
full request bodies, or invoice material in logs or audit metadata. Email send failures
are logged without the recipient address.

Only final statuses (`issued`, `rejected`, `cancelled`) send email. Intermediate states
never send mail. The `issued` email carries the request-local real invoice PDF as an MIME
attachment, and the state changes to `issued` only after SMTP accepts that message. Email
content uses the backend i18n bundle (`invoice.email.status_subject` /
`invoice.email.status_body`) and is rendered in the **recipient user's saved language**
(`model.GetUserLanguage`), not the operator's request language. An idempotent repeat of
an already-applied transition never sends a duplicate email.

## 9. Frontend plan

Feature folders: `web/src/features/invoice/` (user) and `web/src/features/admin-invoices/`.

- User route `/invoice`: notice, eligible-order table with checkboxes, per-currency
  total, minimum feedback, invoice-type radio, material, reason (mandatory for
  individual), remark, delivery email (last field, defaults to the account email), and
  submit. Without an account email the bind-email dialog is forced open and submission
  is disabled.
- Records view: paginated list, six-state badges, detail dialog, and pending cancellation.
- Admin route `/invoices`: route-level `beforeLoad` admin guard. Server-paginated table
  with keyword/status search. Detail dialog shows administrator-authorized material and a
  real-invoice PDF status. `complete-issue` requires a PDF upload; reject requires a note.
- Settings section for `InvoiceEnabled`, `InvoiceNotice`, and `InvoiceMinAmount`.

Frontend locale files (`en`, `zh`, `zh-TW`, `fr`, `ja`, `ru`, `vi`) carry every
invoice key. The historical misplaced French value in `ja.json` for
"Failed to submit invoice application" is fixed.

## 10. Security and privacy

Invoice material includes title, tax ID, phone, address, bank name, bank account,
delivery email, reason, user remark, admin note, trade-number snapshots, and linked user
identity. Only the owner and authorized administrators may read the applicable details.

- Never return material from list endpoints (explicit DTOs on both user and admin lists).
- Never log request bodies or material; email addresses are scrubbed from audit notes and
  omitted from email-failure logs.
- Enforce ownership on every user detail/cancel route.
- Enforce AdminAuth on every admin route.
- Re-check every order in the locked creation transaction.
- Validate lengths, trim input, validate email, and reject unsupported order types.
- Validate the uploaded PDF magic bytes; reject non-PDF and oversized files.
- Do not add secrets or credentials.

## 11. Testing and verification

### Backend tests

`model` package covers: eligibility filtering (provider/currency/money), amount
snapshots, mixed-currency rejection, balance/pending rejection, missing snapshot
rejection, positive-amount requirement, minimum boundary (exact and below), float
boundary sums, claims table creation/release on reject and cancel, concurrent claim
protection, state transitions with idempotent repeats, admin-note retention, and
concurrent atomic profile upsert.

`controller` package covers: final-status-only email policy, PDF magic-byte validation,
filename sanitization, audit-note email scrubbing, min-amount fail-closed behavior,
request-local PDF delivery without multipart temporary-file persistence, PDF required
for first issuance, non-PDF rejection, delivery-failure rollback, PII-free user and
admin list responses, detail ownership enforcement, admin detail material, bound-email
requirement, translated business errors, owner-only cancel with claim release,
concurrent create (only one succeeds), concurrent completion (only one delivery), and
cross-language integrity of all invoice i18n keys.

`common` package covers: SMTP multipart attachment MIME with PDF payload.

All new tests use `require`/`assert` and the repository's locking conventions. Run with
the standard Go test commands including `go test -race`.

### Frontend tests

Cover order selection and totals, minimum and field validation, disabled state, all six
statuses, pending cancellation, admin route guard, state-aware actions, required reject
note, locale keys, and the complete-issue PDF upload flow.

### End-to-end acceptance

1. Create a successful paid top-up.
2. Select it and submit an invoice application.
3. Approve, start issue, upload a real PDF, and complete issue as an administrator.
4. Confirm the user sees `issued` and receives the email with the PDF attachment when
   SMTP is configured.
5. Reject a second application and confirm the order returns to invoice options.
6. Cancel a pending application and confirm the order returns to invoice options.
7. Attempt ownership, mixed-currency, duplicate, below-minimum, and unauthorized requests.
8. Check desktop/mobile UI, no horizontal overflow, and no PII in list responses/logs.

Required checks are the affected Go tests, `bun run i18n:sync`, the repository's actual
frontend typecheck, lint, and build commands.

## 12. Implementation notes

- `invoice_order_claims` must be registered in `model/main.go` (`migrateDB` and
  `migrateDBFast`) and the test migration list.
- Migration risk: the new `invoice_order_claims` table is additive and safe on SQLite,
  MySQL, and PostgreSQL. No PDF storage columns are introduced.
- The subscription order now persists `PaymentCurrency`; existing rows created before
  this change have an empty currency and remain non-invoiceable (fail-closed).
