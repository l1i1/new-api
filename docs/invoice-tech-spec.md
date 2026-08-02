# Self-Service Invoice System — Tech Spec & PRD

Status: Ready for implementation review — this document is the source of truth for v1.

## 1. Goal

Allow a signed-in user to apply for an enterprise invoice for eligible paid top-up
orders. The user can select multiple orders, submit enterprise invoice material,
track the application through a six-state workflow, and cancel a pending application.
Administrators can review and progress applications. Root/super administrators can
configure the feature switch, invoice notice, and minimum amount.

V1 does not generate invoice files or integrate with a tax-bureau/e-invoice provider.

## 2. Scope and explicit boundaries

### In scope

- User invoice application page with multi-select paid orders.
- User application list, detail, status, and pending cancellation.
- Admin list, search, detail, approve, start-issue, complete-issue, and reject.
- Backend models, migrations, APIs, authorization, audit, and optional email.
- Invoice notice, feature switch, and minimum amount settings.
- English, Chinese, Traditional Chinese, French, Japanese, Russian, and Vietnamese UI strings.

### Out of scope

- Personal invoices.
- PDF/e-invoice generation or automatic invoice-number issuance.
- Refund handling.
- Automatic inclusion of balance credits.
- Balance-paid subscription orders.

### Invoiceable order source

V1 uses `TopUp` as the only invoiceable order source. An order must satisfy all of:

- belongs to the current user;
- `Status = common.TopUpStatusSuccess`;
- `PaymentProvider` is non-empty and is not `balance`;
- `PaymentCurrency` is non-empty;
- `Money` is positive;
- is not currently claimed by another invoice application;
- has not already been used by an approved, issuing, or issued application.

External subscription payments are invoiceable only when their `TopUp` row contains
the complete provider, currency, payment method, money, and trade-number snapshot.
The subscription payment path must populate those fields before this feature is
enabled. Balance-paid subscriptions create only a `SubscriptionOrder` and are not
invoiceable in v1.

## 3. Business and money rules

- Invoice amount is the actual paid amount `TopUp.Money`, never quota or credited quota.
- All orders in one application must have the same `PaymentCurrency`.
- No cross-currency conversion is performed.
- `InvoiceMinAmount` is interpreted in the selected order currency.
- Amount addition, comparison, and persistence must use decimal arithmetic or integer
  minor units. Do not sum raw `float64` values for the minimum check.
- Each item stores immutable snapshots of amount, currency, payment method, and trade number.
- The invoice total is calculated from the validated snapshots inside the creation transaction.
- `InvoiceMinAmount` must be finite and non-negative. Invalid, negative, NaN, or infinite
  values are rejected when the option is saved and treated as disabled/fail-closed when read.

## 4. Data model

### `invoices`

| Column | Type | Constraints |
| --- | --- | --- |
| `id` | int | primary key |
| `user_id` | int | required, indexed |
| `title` | varchar(255) | required |
| `tax_id` | varchar(64) | required |
| `phone` | varchar(32) | optional |
| `address` | varchar(255) | optional |
| `bank_name` | varchar(128) | optional |
| `bank_account` | varchar(64) | optional |
| `email` | varchar(255) | required, validated |
| `reason` | varchar(512) | required |
| `remark` | varchar(512) | optional, user to admin |
| `status` | varchar(16) | indexed, see state machine |
| `admin_note` | varchar(512) | optional, admin to user |
| `total_amount` | project-compatible money type | validated total snapshot |
| `currency` | varchar(8) | required |
| `create_time` | int64 | required |
| `update_time` | int64 | required |

If the implementation keeps the existing `float64` money representation for database
compatibility, all business arithmetic must convert through one decimal/money helper
and tests must cover exact minimum-boundary values.

### `invoice_items`

| Column | Type | Constraints |
| --- | --- | --- |
| `id` | int | primary key |
| `invoice_id` | int | required, indexed, FK to `invoices` |
| `order_type` | varchar(16) | `topup` in v1 |
| `order_id` | int | required |
| `trade_no` | varchar(255) | required snapshot |
| `amount` | project-compatible money type | required snapshot |
| `currency` | varchar(8) | required snapshot |
| `payment_method` | varchar(50) | display snapshot |

Do not add a permanent unique constraint on `(order_type, order_id)` here. Historical
items must remain available after rejection or cancellation.

### `invoice_order_claims`

This table represents the current active claim and is separate from historical items:

| Column | Type | Constraints |
| --- | --- | --- |
| `order_type` | varchar(16) | required |
| `order_id` | int | required |
| `invoice_id` | int | required, indexed |
| `create_time` | int64 | required |

Create a unique constraint on `(order_type, order_id)`. Insert a claim when creating an
application and delete it when the application becomes `rejected` or `cancelled`.
This provides database-level protection against concurrent applications while keeping
historical rejected/cancelled records.

All three tables must be registered with the existing cross-database AutoMigrate path.
Use GORM and the repository's database-locking helper; do not introduce dialect-specific
DDL without SQLite, MySQL, and PostgreSQL fallbacks.

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
4. Verify one currency and calculate the total with the money helper.
5. Enforce the configured minimum.
6. Insert `invoices`, snapshot `invoice_items`, and insert `invoice_order_claims`.
7. Roll back on any claim conflict or validation error.

Every admin transition and user cancellation must run in a transaction, lock the invoice
row, verify the current state, then update status, note, and timestamp. Transition and
audit must commit together. Repeating an already-applied identical transition is an
idempotent success; all other invalid transitions return a business error. Claim deletion
for `rejected` and `cancelled` occurs in the same transaction as the state update.

## 6. API contract

All responses use the existing API response conventions. Request bodies bind to explicit
DTOs, never directly to GORM models.

### User endpoints

#### `GET /api/user/invoice/options`

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

#### `POST /api/user/invoice`

Authenticated user; apply the existing critical rate limit used by top-up flows.

```json
{
  "orders": [{ "order_type": "topup", "order_id": 123 }],
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

Validate and trim all fields. Required fields are title, tax ID, email, reason, and at
least one order. Apply server-side ownership, eligibility, same-currency, minimum, and
claim checks inside the creation transaction.

#### `GET /api/user/invoice?p=1&page_size=20`

Returns only `InvoiceListItem` fields: ID, status, total, currency, create/update time.
Default and maximum page sizes follow existing list APIs. Sort by `create_time DESC, id DESC`.

#### `GET /api/user/invoice/:id`

Requires ownership. Returns `InvoiceDetail`, including material, items, reason, remark,
and admin note.

#### `POST /api/user/invoice/:id/cancel`

Requires ownership and `pending` status. Updates the application and releases its claims
atomically.

### Admin endpoints

Create an explicit `AdminAuth`-protected route group at `/api/admin/invoice`:

- `GET /api/admin/invoice?p=1&page_size=20&keyword=&status=`
- `GET /api/admin/invoice/:id`
- `POST /api/admin/invoice/:id/approve`
- `POST /api/admin/invoice/:id/start-issue`
- `POST /api/admin/invoice/:id/complete-issue`
- `POST /api/admin/invoice/:id/reject`

The list response uses `items`, `total`, `page`, and `page_size`, sorted by
`create_time DESC, id DESC`. `keyword` searches server-side across title, tax ID, trade
number, email, and the permitted user search fields. It must not cause sensitive fields
to be echoed in list rows. `status` accepts only the six known states.

Admin list DTOs contain no invoice material. Admin detail DTOs contain the material and
necessary user context. Reject requires a non-empty note; other transitions accept an
optional note. Every transition returns the updated non-sensitive summary.

## 7. Settings and authorization

Register these options in `model.InitOptionMap` and the frontend billing settings types,
defaults, and section registry:

- `InvoiceEnabled: bool`, default `false`;
- `InvoiceNotice: string`, default empty;
- `InvoiceMinAmount: number`, default `0`.

The option controller must validate the minimum as finite and non-negative. The user
options endpoint is the only settings data source for the user page; the user page must
never call the RootAuth option endpoint.

Authorization is deliberately split:

- User endpoints: authenticated user plus ownership where applicable.
- Review endpoints: `AdminAuth`.
- Invoice settings: Root/Super Admin only, matching existing system-settings access.
- Frontend `/invoices`: route-level `beforeLoad` admin guard plus backend enforcement.

## 8. Audit and notification

Add audit actions for `invoice.approve`, `invoice.start_issue`, `invoice.complete_issue`,
`invoice.reject`, and `invoice.cancel`. Audit records contain invoice ID, actor, previous
state, new state, timestamp, and a necessary note summary. Never include tax ID, bank
account, email, full request bodies, or invoice material in logs or audit metadata.

Reuse `common.SendEmail` after the transaction commits for approve, start-issue,
complete-issue, and reject. SMTP/configuration failures are logged without PII and do
not fail or roll back the admin transition. If strict `text/plain` MIME is required,
extend the mail helper; otherwise document that the existing HTML MIME transport carries
plain-text content. No attachments are sent in v1.

## 9. Frontend plan

Use the existing TanStack Router, feature, settings, sidebar, and i18n conventions.
Do not use the nonexistent `pnpm generate:types` command. Route-tree generation is
performed by the existing Rsbuild/TanStack Router flow; verify the generated tree using
the repository's actual typecheck/build commands and commit it only if that is the local
route convention.

### User route: `/invoice`

Feature folder: `web/src/features/invoice/`.

- Apply view: notice, eligible-order table with checkboxes, per-currency total, minimum
  feedback, enterprise material, reason, remark, email, and submit.
- Records view: paginated list, six-state badges, detail dialog, and pending cancellation.
- Preserve active view and records pagination in route search state.
- On disabled feature, show a non-submittable disabled state.
- Add the personal sidebar entry, sidebar module/config mapping, wallet link, and route.

### Admin route: `/invoices`

Feature folder: `web/src/features/admin-invoices/`.

- Add route-level `beforeLoad` admin guard that redirects unauthorized users to `/403`.
- Add server-paginated table with keyword and status URL search state.
- Detail dialog shows administrator-authorized material and user context.
- Enable actions only for valid current states; reject requires a note and confirmation.
- Add the admin sidebar entry, module/config mapping, and route.

### Typed client and settings work

Create feature-local `api.ts` and `types.ts` with explicit types for `InvoiceStatus`,
options, create request, list/detail DTOs, transition requests, and pagination. Keep
list and detail types separate. Add `InvoiceEnabled`, `InvoiceNotice`, and
`InvoiceMinAmount` to the settings type/default/registry/component pipeline.

Add real translations to `en`, `zh`, `zh-TW`, `fr`, `ja`, `ru`, and `vi`, then run
`bun run i18n:sync` to validate structure. Sync is not a translation generator.

## 10. Security and privacy

Invoice material includes title, tax ID, phone, address, bank name, bank account,
delivery email, reason, user remark, admin note, trade-number snapshots, and linked user
identity. Only the owner and authorized administrators may read the applicable details.

- Never return material from list endpoints.
- Never log request bodies or material.
- Enforce ownership on every user detail/cancel route.
- Enforce AdminAuth on every admin route.
- Re-check every order in the locked creation transaction.
- Validate lengths, trim input, validate email, and reject unsupported order types.
- Do not add secrets or credentials.

## 11. Testing and verification

### Backend tests

Cover successful paid orders, balance/provider rejection, ownership, payment status,
missing subscription snapshots, positive amount, exact minimum boundary, same-currency
and mixed-currency applications, duplicate orders, concurrent claims, rejected/cancelled
reuse, final-state reuse rejection, all valid/invalid transitions, idempotent repeats,
ownership, AdminAuth, PII-free list responses, and email failure after committed state.

Use repository-standard GORM locking and `require`/`assert` conventions. Verify all
affected packages with the existing Go test commands.

### Frontend tests

Cover order selection and totals, minimum and field validation, disabled state, all six
statuses, pending cancellation, admin route guard, state-aware actions, required reject
note, pagination/filter URL state, settings visibility, and locale keys.

### End-to-end acceptance

1. Create a successful paid top-up.
2. Select it and submit an invoice application.
3. Approve, start issue, and complete issue as an administrator.
4. Confirm the user sees `issued` and receives notification when SMTP is configured.
5. Reject a second application and confirm the order returns to invoice options.
6. Cancel a pending application and confirm the order returns to invoice options.
7. Attempt ownership, mixed-currency, duplicate, below-minimum, and unauthorized requests.
8. Check desktop/mobile UI, no horizontal overflow, and no PII in list responses/logs.

Required checks are the affected Go tests, `bun run i18n:sync`, the repository's actual
frontend typecheck, lint, and build commands, plus the route-tree verification described
above. The feature is complete only when the API, transaction, privacy, authorization,
frontend, and manual workflow checks all pass.

## 12. Implementation order

1. Update and approve this contract.
2. Add models, claim table, AutoMigrate registration, and money/option helpers.
3. Add user APIs and transaction tests.
4. Add admin APIs, transition locking, audit, and notification handling.
5. Register routes and authorization.
6. Add typed frontend clients, user page, admin page, and navigation.
7. Add system-settings section and all locale translations.
8. Run backend/frontend checks and manual acceptance.
