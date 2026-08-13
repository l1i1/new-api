# Upstream Sync: v1.0.0-rc.24

## Scope

Merge the official `QuantumNous/new-api` release tag `v1.0.0-rc.24` into the
Tokeness integration branch through a review candidate. The annotated tag
resolves to commit `5c3abffe8572aa8a49f15c3916707d2019d66af4`.

This candidate deliberately excludes `upstream/main` commits after the release
tag. At the time of preparation, `upstream/main` was 18 commits ahead of rc.24.

## Acceptance Criteria

- The merge commit has the candidate tip and rc.24 as parents.
- Existing Tokeness production workflows, `Dockerfile.tokeness`, release
  metadata, and application customizations remain intact.
- Conflicts are resolved deliberately, without accepting upstream deletion of
  Tokeness-only files or controls.
- Backend and frontend focused regression checks pass, including relay request
  retry behavior and the locally developed channel used-quota reset workflow.
- No image is published and no production deployment is triggered by this
  synchronization candidate.

## Validation Plan

- `git diff --check`
- Focused Go tests for changed backend packages and relay request handling
- Frontend i18n synchronization, type checking, targeted tests, and production
  build
- `GOWORK=off go build ./...` from `relaykit/`

## Risks

- The official release changes shared relay request-body handling and removes
  several Tokeness-customized surfaces. Conflict resolution must retain the
  Tokeness behavior while adopting upstream correctness fixes.
- This is a source-integration candidate only. Publishing an immutable image
  and staged deployment require separate review and release approval.

## Conflict Resolution

- Retained Tokeness registration attribution: invite counts remain independent
  from payment-compliance status, while inviter quota is still conditional.
- Combined Tokeness raw-body protocol protection and diagnostics with rc.24's
  replayable request body contract, so transport retries retain the original
  body without allowing raw OpenAI payloads through non-OpenAI adapters.
- Retired the obsolete `RelayInfo` body-size metadata and its duplicate test;
  rc.24 now derives request length and retry factories directly from the
  replayable body.
