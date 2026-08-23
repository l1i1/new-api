# Quota Warning Delivery

## Goal

Send at most one wallet or subscription low-balance notification during a
continuous low-balance episode, without blocking API requests.

## State Ownership

- Wallet warning state belongs to `users.quota_warning_sent`.
- Subscription warning state belongs to
  `user_subscriptions.quota_warning_sent` and is isolated by subscription ID.
- Newly migrated nullable values are read as `false` with `COALESCE`.

## Claim Rules

- The current balance and warning state are read in one transaction with a row
  lock on MySQL and PostgreSQL.
- A balance at or above the configured threshold rearms the next episode.
- The first request observed below the threshold claims the warning.
- Stale concurrent request snapshots cannot claim the same episode twice.
- A request that crossed the threshold after an unobserved recharge or quota
  reset may claim a new episode.

## Delivery Rules

- Notification delivery stays asynchronous and never blocks the model API.
- The claim represents one delivery attempt. Delivery failures are logged but
  do not clear the claim, because clearing a boolean claim after an external
  call can erase a newer episode claimed concurrently and cause duplicates.

## Acceptance

- Repeated requests below the threshold produce one wallet notification.
- Wallet and subscription states do not suppress each other.
- Different active subscriptions do not suppress each other.
- Recharge or subscription quota reset rearms a later low-balance episode.
- SQLite, MySQL, and PostgreSQL migrations and queries remain compatible.
