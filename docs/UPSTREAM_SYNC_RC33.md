# Upstream Sync: v1.0.0-rc.33

## Scope

Merge the official `QuantumNous/new-api` release tag `v1.0.0-rc.33` into the
Tokeness integration candidate. The tag resolves to commit
`eb99ab1b40343c3317bb47981cccdbb2b159a5fa` and shares
`v1.0.0-rc.30` (`27ff6a876`) as the merge base with `tokeness/main`.

The candidate includes the 19 official commits from rc.30 through rc.33. It
does not include newer `upstream/main` commits, production deployment, image
publishing, or changes from the uncommitted canonical worktree.

## Acceptance Criteria

- The final merge commit has the current Tokeness candidate tip and rc.33 as
  parents.
- Upstream rc.33 behavior is adopted for expression pricing, model modifiers,
  relay conversion, task plugin polling, log metadata projection, ETag
  handling, and related tests.
- Tokeness billing settlement, official-fit routing and protocol behavior,
  security controls, payment paths, deployment gates, and frontend branding
  remain intact unless a deliberate compatibility decision documents the
  change.
- All 28 simulated merge conflicts are resolved without conflict markers or
  accidental deletion of Tokeness-only behavior.
- Backend, independent `relaykit`, frontend, formatting, and static checks
  pass at the candidate tip.
- No image is published and no production deployment is triggered.

## Work Plan

1. Merge the immutable rc.33 tag into the isolated candidate branch.
2. Resolve shared relay, billing, task, log, model, and test files by combining
   upstream fixes with the current Tokeness invariants.
3. Review all non-conflicting upstream additions and deletions, especially the
   46-file model-modifier change and relaykit conversion changes.
4. Add focused regression coverage for any conflict resolution that changes
   observable billing, routing, protocol, task, or log behavior.
5. Run the validation matrix below and inspect the final diff against both
   parents.

## Validation Plan

- `git diff --check`
- `go test ./...`
- `go vet ./...`
- `GOWORK=off go build ./...` from `relaykit/`
- Frontend tests, type checking, i18n synchronization, and production build
  using the repository's Bun scripts
- Confirm `git grep` finds no conflict markers and verify merge status/parents

## Risks and Decisions

- rc.33 changes 253 paths and overlaps 83 paths changed by Tokeness since
  rc.30. Git reports 28 content/add-add conflicts, so automatic preference for
  either parent is unsafe.
- Upstream's relaykit conversion and billing changes are cross-cutting. Their
  public DTO and accounting semantics must be preserved together with the
  Tokeness pre-consume and settlement safeguards.
- The canonical worktree has an unrelated uncommitted CustomHeadHTML and
  sidebar workstream. It remains untouched; those changes must be integrated
  separately after this candidate is reviewed.
- The result is a source-integration candidate only. Release and deployment
  require separate approval and immutable artifact verification.
