# Upstream Sync: v1.0.0-rc.23

## Objective

Merge the official `QuantumNous/new-api` tag `v1.0.0-rc.23`
(`0ab02020603d22e5613bc4cf46bfab06f8567769`) into the Tokeness mainline as a
review candidate.

The upstream delta after the previously integrated `df43f8015` baseline contains:

- hardened tiered retry billing when retries switch groups;
- Bedrock request cancellation when the client disconnects;
- configurable API-key Auto group ordering and its administration UI.

This synchronization produces a review PR only. It must not publish an image or
deploy production.

## Tokeness Invariants

- Preserve Tokeness billing saturation, audit, pre-consume, retry, and settlement
  protections.
- Preserve the global and per-user group/model rate-limit middleware ordering and
  Redis behavior.
- Preserve invoice, payment settlement, invite reward, and authenticated email
  binding workflows.
- Preserve Tokeness frontend localization boundaries, seven frontend locales,
  native home/wallet/footer behavior, and protected project attribution.
- Keep the root module and `relaykit` independently buildable.
- Keep SQLite, MySQL, and PostgreSQL compatibility.
- Keep immutable-digest publishing and staged production deployment workflows
  unchanged unless the upstream build contract requires an explicit reviewed fix.

## Acceptance Criteria

- The merge commit has the candidate pre-merge tip and upstream rc.23 as parents,
  with `origin/tokeness/main` preserved as the first-parent baseline ancestor.
- All conflicts are resolved intentionally, with no conflict markers or accidental
  deletion of Tokeness-specific files.
- Upstream Auto group ordering, tiered retry billing hardening, and Bedrock
  cancellation behavior remain covered by their upstream tests.
- Tokeness invoice, payment, rate-limit, localization, authentication, and
  deployment transaction regressions remain green.
- Root and `relaykit` build, test, and vet checks pass.
- Frontend dependency installation, type checking, focused tests, i18n
  synchronization check, and production build pass.
- `Dockerfile.tokeness` builds successfully for `linux/amd64` when the local
  environment provides a working Docker builder.
- The candidate branch is pushed and opened as a PR against `tokeness/main` with
  an AI-assisted disclosure and the repository PR template preserved.

## Validation Plan

1. Review the upstream commit and file-level delta before resolving conflicts.
2. Run focused tests for every conflicted package and each upstream feature.
3. Run root Go tests, build, and vet; run `relaykit` with `GOWORK=off`.
4. Run frontend type checking, tests, i18n synchronization/checks, and build.
5. Run deployment shell transaction tests and workflow syntax checks.
6. Build `Dockerfile.tokeness` when Docker is available.
7. Review the final branch diff against both parents before pushing.

## Integration Decisions

- Wallet pre-consume and retry reservation use a conditional database update so
  concurrent requests cannot overdraw the user quota. These reservations bypass
  process-local batching; direct refunds keep the database and cache recovery
  path aligned.
- API-key Auto group snapshots are constrained by the administrator's current
  global Auto allowlist at write time and again at routing time. Removing a group
  globally therefore invalidates stale token snapshots immediately.
- Group/model rate limits count each concrete group once per request. A retry in
  the same group does not consume another slot, while a cross-group retry consumes
  the destination group's bucket before contacting an upstream channel.
- Local task rate-limit failures retain their original 429 response and are not
  rewritten as upstream saturation or retried.
- The Auto-group UI retains Tokeness `<tnt>` localization and exposes explicit
  loading, failure, and retry states. Failed Auto configuration or API-key detail
  requests cannot initialize or save a truncated group order.
- The API-key table keeps a visible Auto identity and only displays Cross-group
  when the key enables cross-group retry.

## Validation Results

- Root module: `go test ./...`, `go build ./...`, and `go vet ./...` passed.
- RelayKit with `GOWORK=off`: test, build, and vet passed.
- Focused race tests for model, service, middleware, and controller changes passed.
- Frontend: `bun run typecheck`, all 63 test files, `bun run build`, and
  `bun run i18n:sync` passed. All seven locale files contain 5,535 keys.
- Focused lint and format checks passed for every manually modified TypeScript
  and TSX file. Repository-wide frontend lint/format/copyright checks still report
  pre-existing baseline violations outside this synchronization.
- Deployment remote-command and rollout transaction tests passed. On Windows,
  rollout validation used an LF-normalizing wrapper around the locally installed
  `jq.exe`, whose CRLF stdout otherwise leaves a carriage return in Bash command
  substitutions; production CI runs on Linux and requires no repository change.
- All 10 GitHub Actions workflow files parsed successfully as YAML.
- `git diff --check`, conflict-marker scanning, and candidate path review passed.
- Docker is unavailable in the local environment, so `Dockerfile.tokeness` was
  not built locally. Publishing and deployment remain outside this sync task.

## Rollback

Close the candidate PR and delete the candidate branch. Production remains on
the currently deployed immutable digest until a separately reviewed release is
published and deployed.
