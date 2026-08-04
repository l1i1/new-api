# Referral rewards display support fix

## Problem

The wallet should present native invite first-top-up rewards as direct-to-balance credits. Legacy registration fields remain on the user record for accounting and API compatibility, but they are not part of the current wallet surface.

## Scope

This is an additive wallet extension. It keeps the existing referral API, native first-top-up ledger API, transfer endpoint, card route, and responsive layout contracts. The production rollout changes only the application image; it does not mutate balances or ledger rows.

The card shows the invite count and the native first-top-up reward ledger (`invite_top_up_rewards.summary.total_reward_quota`) with its applied/pending status. The description makes clear that qualifying rewards are credited directly to the main balance.

The legacy `aff_history_quota`, `aff_quota`, transfer dialog, and `POST /api/user/aff_transfer` remain backend-compatible but are intentionally hidden from this wallet surface. This UI change does not perform an automatic transfer for existing legacy balances.

## Acceptance criteria

- The card explains that native first-top-up rewards are credited directly to the main balance.
- Legacy historical/available fields and transfer controls are not rendered, even when legacy `aff_quota` is positive.
- Existing native reward loading, empty, error, paused, status, and recent-item states remain intact.
- All locale files contain the new user-facing strings.

## Verification

- Focused `affiliate-rewards-card` component tests, including zero and positive legacy balances.
- Frontend typecheck, focused lint/format, production build, and `git diff --check`.
- Read-only production reconciliation for the reported account after the code change; no direct SQL write or manual credit.
