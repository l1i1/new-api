# Channel Used Quota Reset

## Objective

Allow an administrator to reset one channel's accumulated `used_quota` counter
from `/channels` without changing consume logs, billing records, or user
balances.

## API Contract

`POST /api/channel/:id/used_quota/reset`

- Requires the channel `operate` permission.
- Returns the previous persisted counter in `data.previous_used_quota`.
- Writes the `channel.used_quota_reset` management audit action.
- Returns the standard New API error response when the channel does not exist
  or the database update fails.

For multiple channels, use:

`POST /api/channel/used_quota/reset`

```json
{
  "ids": [12, 18, 25]
}
```

- Uses the same `operate` permission.
- Resets all existing channel IDs in one database transaction.
- Returns the number of channels reset in `data`.
- Duplicate IDs are collapsed; stale IDs are ignored when at least one
  selected channel still exists.
- Each reset channel keeps the existing `channel.used_quota_reset` audit shape,
  including its previous counter value.

## Concurrency

Channel usage increments bypass the per-process batch buffer and use an atomic
database update. The reset reads and clears the channel inside one row-locked
transaction, so every New API node shares the same ordering boundary. Usage
accepted after the reset increments the counter normally. The counter is
operational telemetry, so consume logs remain the source of truth for
historical billing and reconciliation.

## Acceptance

- The channel row action is executable only by administrators with channel
  operate permission; other administrators see it disabled.
- The request is sent only after explicit confirmation.
- A successful reset refreshes the channel list and shows zero until new usage
  is recorded.
- Route permission, buffered quota handling, controller response, and audit
  metadata are covered by regression tests.
