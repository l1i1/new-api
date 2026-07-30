# Invite First Top-Up Reward

Date: 2026-07-30

## Problem

Tokeness currently pays a 20% invite reward through the external
`tools/tokeness-ops` polling script. That script scans the complete user and
top-up lists, stores idempotency state in a local JSON file, and calls the
non-idempotent administrator quota endpoint. A retry after an ambiguous HTTP
failure can therefore pay twice, while a lost or node-local state file can
repeat historical payouts.

The wallet also no longer matches the historical `home.js` contract. The
historical view promises an automatic 20% reward, shows only the invite count,
and hides pending/history balances and the manual transfer action.

## Scope

- Wallet top-ups settled through EPay, Stripe, Creem, Waffo, and Waffo
  Pancake, including administrator completion paths that use the same model
  settlement operations.
- A database-backed first-top-up reward ledger and retry task.
- Native wallet referral content parity with the historical `home.js`.

Subscription purchases, registration-time fixed invite rewards, historical
reward backfills, and production deployment are outside this change.

## Product Contract

1. A valid registration through an invite link increments the inviter's
   `aff_count` immediately; it does not require a top-up and does not create a
   reward ledger row.
2. A user registered through an invite link can generate one reward from the
   first successful positive wallet top-up in the user's lifetime.
3. The invitee registration time and first top-up completion time must both be
   on or after the configured campaign start.
4. The inviter receives 20% of the exact quota credited by settlement, rounded
   down to a whole quota unit.
5. The reward is added directly to the inviter's ordinary `quota`, matching the
   existing Tokeness Ops behavior. It does not use `aff_quota` and does not
   require a manual transfer.
6. Missing, disabled, deleted, or self-referencing inviters are not paid.
7. The wallet shows the historical 20% explanation, invite count, referral
   link, ledger-backed first-top-up completion count, applied reward total,
   pending count, and recent reward status. The old `aff_quota` balance and
   transfer action remain hidden.
8. User-facing reward data never exposes invitee identity, trade numbers,
   payment providers, or the invitee's top-up amount.

## Configuration

The native reward path is fail-closed unless both values are present:

- `INVITE_FIRST_TOPUP_REWARD_ENABLED=true`
- `INVITE_FIRST_TOPUP_REWARD_START_TIMESTAMP=<unix-seconds>`

The explicit start timestamp is the cutover boundary. It prevents a new image
from replaying rewards already paid by the legacy script. The legacy cron must
be disabled at the same boundary. Existing JSON records with `pending` or
`failed` status require manual reconciliation; they are not assumed unpaid.

The reward rate is fixed at 20% because the public wallet copy is also fixed at
20%. A future configurable rate must expose the same server-side value to the
wallet instead of allowing copy and settlement to diverge.

## Data Contract

`top_ups.credited_quota` stores the exact quota credited by the successful
settlement. Provider-specific formulas are not recalculated by the reward
processor.

`invite_top_up_rewards` is the durable outbox and audit ledger:

- campaign ID and start timestamp;
- top-up, invitee, and inviter IDs;
- base quota, rate in basis points, and reward quota;
- `pending`, `applied`, or `skipped` status;
- creation, update, and application timestamps.

The top-up ID is unique. `(campaign_id, invitee_id)` is also unique. These
constraints are the cross-node idempotency boundary.

## User API Contract

`GET /api/user/invite-topup-rewards?p=1&page_size=5` is an authenticated,
read-only endpoint. The inviter ID always comes from the authenticated request
context; the endpoint does not accept an arbitrary inviter ID.

The response contains:

- the current program-enabled flag and server-owned reward rate;
- counts for `applied`, `pending`, and `skipped` ledger rows. The wallet's
  completed-first-top-ups metric uses the applied count;
- the sum of `reward_quota` for applied rows only;
- a newest-first page of sanitized reward items containing only reward ID,
  reward quota, status, creation time, and application time.

Pagination is capped by the shared page-size limit. Queries and schema must
remain compatible with SQLite, MySQL, and PostgreSQL.

## Wallet UI Contract

The referral surface is an extension of the existing wallet design system:

- one shared card surface, without nested metric cards;
- existing Card, IconBadge, Button, StatusBadge, Skeleton, theme colors, and
  global radius tokens only;
- a compact summary grid for invite count, completed rewards, applied reward
  total, and pending count;
- a divider-based recent-reward list with localized status and time;
- reward quota displayed as CNY using the wallet's configured USD-to-CNY rate,
  regardless of the global quota display mode;
- responsive two-column summary at narrow widths and a denser desktop layout;
- independent loading, empty, and retryable error states so referral-link copy
  remains available when the reward query fails.

## Settlement Contract

1. The provider-specific path validates and locks the top-up row.
2. Before changing the order to success, it locks the invitee user row. This
   serializes concurrent successful top-ups for the same user.
3. It checks all historical successful positive top-ups. Only a user with no
   earlier success can create a reward event.
4. The top-up status, completion time, exact credited quota, invitee quota, and
   pending reward event commit in one transaction.
5. After commit, reward application runs in a separate transaction that locks
   the reward and inviter rows, increments inviter quota, and marks the event
   applied atomically.
6. Successful settlement updates the invitee quota cache, and a successful
   reward updates the inviter quota cache. Cache failures are logged without
   changing the database ledger result.
7. A reward-application failure leaves the top-up successful and the event
   pending. The master-node system task retries pending events.
8. Replayed callbacks process the existing event but never create another
   event or increment quota twice.

## Acceptance Criteria

- EPay, Stripe, Creem, Waffo, and Waffo Pancake persist the exact credited
  quota, refresh the invitee cache, and create the same reward event contract.
- The first eligible top-up pays exactly 20% to ordinary inviter quota.
- Repeated callbacks and repeated pending-event processing pay once.
- A second top-up never pays, including when the first top-up predates the
  campaign.
- Missing configuration, pre-campaign registration/top-up, no inviter,
  inactive/deleted inviter, and self-invite do not pay.
- A transient reward application failure does not roll back the successful
  top-up and remains retryable.
- The schema migrates through GORM on SQLite, MySQL, and PostgreSQL.
- The authenticated reward API cannot read another inviter's ledger and does
  not expose invitee or order details.
- The wallet keeps the historical explanation and hides `aff_quota` and the
  transfer action while showing real reward summary and recent status data.
- Loading, empty, API-error, long-content, and narrow/mobile states keep the
  referral link copy action accessible and do not overflow the card.
- Focused tests, `go test ./...`, `go vet ./...`, frontend tests, typecheck,
  affected-file lint/format, i18n checks, and production build pass.

## Rollout

1. Audit the legacy JSON state and resolve ambiguous `pending`/`failed` rows.
2. Record a cutover timestamp and stop the legacy reward cron.
3. Deploy the native image with the same start timestamp on every New API node.
4. Enable the reward only after all nodes use the new schema and code.
5. Verify one real first top-up, one replayed callback, inviter quota, reward
   ledger state, cache state, and system task history.

Rollback disables `INVITE_FIRST_TOPUP_REWARD_ENABLED` before selecting the
previous trusted image. The ledger is additive and must not be deleted.
