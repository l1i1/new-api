# Local Branch Remediation Review

## Scope

Review the local `tokeness/main` changes that are not yet part of the remote
mainline, repair confirmed regressions, and leave a verified local commit
series. Remote reconciliation, image publication, and deployment are out of
scope.

## Confirmed Issues

- Batch task polling can move tasks to `FAILURE` on a transient channel lookup
  error, then lose or duplicate a non-idempotent refund after the terminal
  transition.
- A non-empty batch task diagnostic reason can overwrite an explicit success
  status with `FAILURE`.
- Wallet settlement debits can be deferred by the batch updater even though
  settlement must be database-authoritative before the request completes.
- Wallet refunds that include usage accounting do not enforce the wallet quota
  ceiling used by ordinary quota increases.
- Database-first wallet mutations can asynchronously apply the same delta to a
  concurrently rehydrated Redis snapshot, making cached quota diverge.
- Request refunds mark completion before every funding and token stage has
  succeeded, preventing safe same-session retry after a partial failure.
- Ollama rejects non-object `thinking` values that the adaptor previously
  ignored.
- The frontend task-pricing integration has incomplete imports and type
  contracts, preventing typecheck and exposing runtime reference errors.
- Task price cards and tables discard calculated multi-tier ranges and usage
  units, translate schema-owned field names, and omit the usage schema from the
  model-details breakdown.

## Acceptance Criteria

- A missing channel leaves its tasks and reserved quota pending for a later poll
  or the existing timeout policy; no non-idempotent refund follows a premature
  terminal transition.
- Successful batch results remain successful even when they include a reason.
- Wallet settlement debits and refunds persist synchronously, positive changes
  respect `MaxWalletQuota`, and database reads remain the wallet authority.
- Wallet mutations invalidate the user snapshot instead of applying an
  asynchronous Redis delta; request refunds only complete after every stage
  succeeds and do not repeat completed stages within the same session.
- Ollama accepts legacy non-object `thinking` values without changing its
  explicit object toggle behavior.
- Task price surfaces show multi-tier ranges, usage units, and schema-owned
  labels; model details parse task pricing with the model usage schema.
- Frontend typecheck, tests, build, locale synchronization, and lint for every
  modified source file pass. Repository-wide lint is recorded separately when
  unchanged files still violate the current rules.
- Root Go tests and vet pass; RelayKit passes standalone tests and build with
  `GOWORK=off`; `git diff --check` is clean.

## Verification Result

- Frontend: typecheck, 674 tests across both runners, production build, locale
  synchronization, and lint/format checks for all 12 modified TypeScript files
  passed.
- Repository-wide frontend lint remains red on pre-existing violations in
  unchanged files; the first findings are in
  `channel-affinity/cache-stats-dialog.tsx`.
- Root `go test ./...` and `go vet ./...` passed.
- RelayKit standalone test and build passed with `GOWORK=off`.
- `git diff --check` passed.

## Residual Risk

- Refund stage progress is request-local. A process crash between successful
  funding and token stages still requires reconciliation; cross-process retry
  needs a durable idempotency ledger, which is outside this remediation.

## Exclusions

- Remote branch history reconciliation and push.
- Production publication or deployment.
- Existing RPM/TPM time-window semantics and cross-origin cookie policy.
