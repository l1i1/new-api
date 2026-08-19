# Upstream Sync: v1.0.0-rc.25

## Scope

Merge the official `QuantumNous/new-api` release tag `v1.0.0-rc.25` into the
Tokeness integration branch through a review candidate. The tag resolves to
commit `f116414284162ad15d8925f7bca494c109b83e93`.

This candidate deliberately excludes commits after the release tag on
`upstream/main`. The release contains 45 commits and 207 changed files after
`v1.0.0-rc.24`; it is a source integration candidate, not a production release.

## Acceptance Criteria

- The merge commit has the Tokeness candidate tip and `v1.0.0-rc.25` as parents.
- Tokeness billing, top-up/payment settlement, quota reservation, channel
  credentials/proxies/observability, localization, `Dockerfile.tokeness`, and
  release workflows remain present and behaviorally covered.
- Upstream fixes for Responses cached-token settlement, channel testing,
  advanced custom routes, Claude tool conversion, wallet quota guards, and
  relay request conversion are retained.
- All conflicts are resolved deliberately; no Tokeness-only file is removed by
  accepting an upstream deletion.
- Backend, independent `relaykit`, frontend typecheck/tests/build, i18n sync,
  and whitespace checks pass.
- No image is published and no production deployment is triggered by this
  synchronization candidate.

## Validation Plan

- `git diff --check`
- Focused Go tests for changed controller/model/service/relay packages, then
  `go test ./...` and `go vet ./...` where the local toolchain permits
- `cd relaykit && GOWORK=off go test ./... && GOWORK=off go build ./...`
- From `web/`: Bun i18n synchronization, typecheck, focused tests, and
  production build
- Review the final merge tree for protected Tokeness files and inspect all
  conflict-resolution hunks

## Risks

- rc25 removes or reshapes several upstream surfaces that Tokeness has extended,
  especially channel credentials/observability, payment settlement, quota
  accounting, and frontend test infrastructure.
- rc25 removes the upstream compact-model suffix implementation while Tokeness
  still owns compact alias routing; the local implementation must remain unless
  the new upstream routing can prove equivalent behavior.
- Upstream top-up/quota changes overlap Tokeness's payment-provider and wallet
  guards; both the no-overdraft and no-double-credit invariants must survive.

## Conflict Resolution

The merge keeps Tokeness-owned payment and wallet behavior while adopting the
rc25 atomic quota ceiling and reservation path. HotPay/Waffo Pancake amount and
method snapshots, signature checks, first-top-up rewards, and provider guards
remain authoritative; Stripe, Creem, Waffo, Pancake, and EPay credit paths use
the same quota-cap protection. Channel-used-quota remains an intentional direct
write because the local reset endpoint reads it transactionally; the generic
signed usage test documents this exception.

The local `-openai-compact` alias fallback and `compact_suffix.go` remain in
place because rc25 removes the upstream suffix implementation. Midjourney
credential/proxy snapshots and refund settlement remain combined with rc25's
unified settlement logic. Advanced Custom request redaction, concurrent
channel tests, OAuth binding safeguards, relay request replay metadata, and
Responses cached-token settlement are adopted from rc25.

For frontend tests, the rc25 Vitest migration is kept alongside Tokeness's
existing `node:test` suite. `web/scripts/run-tests.mjs` detects each test's
runner import, and `bun run test` executes both suites without reporting the
Node-test files as empty Vitest suites. The rc25 Auto group cell contract is
also restored: the cell shows the Cross-group badge and only wraps an
available Auto Ratio; the duplicate Auto badge is not rendered.

## Verification Result

- `go test ./...`: passed. The Windows-only HTTP/2 graceful-GOAWAY loopback
  case is explicitly skipped because the platform transport returns a socket
  abort before reconnect; non-Windows CI retains the test.
- `go vet ./...`: passed.
- `relaykit`: `GOWORK=off go test ./...` and `GOWORK=off go build ./...` passed.
- Frontend `bun run test`: Vitest 35 files/191 tests and Bun `node:test` 68
  files/231 tests passed.
- Frontend `bun run typecheck`, `bun run build`, and `bun run i18n:sync` passed.
- `git diff --check` passed; no image was published and no deployment was
  triggered.
