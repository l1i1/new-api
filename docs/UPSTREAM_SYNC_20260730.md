# Upstream Sync 2026-07-30

## Scope

- Base: `l1i1/new-api:tokeness/main@4db2a50e7`
- Upstream: `QuantumNous/new-api:main@66ee6b8f9`
- Candidate: `sync/upstream-66ee6b8f9889`
- This candidate only merges and validates source changes. It does not publish an image or deploy production.

## Upstream Changes

- Preserve Qwen `thinking_budget` through OpenAI chat and Responses conversions, including explicit zero values.
- Allow administrators to configure the OIDC provider display name exposed by status and login surfaces.
- Add RelayKit usage documentation.
- Run Go vet for the root and RelayKit modules in CI.
- Simplify custom event synchronization and make TCP-based email tests more reliable.

## Tokeness Invariants

- Keep token-affinity key selection and prompt-cache accounting behavior.
- Keep raw request-body passthrough limited to OpenAI-compatible channels.
- Keep complete upstream streaming usage, including cache-token details.
- Keep the native Tokeness frontend, localization boundaries, wallet behavior, and protected New API attribution.
- Keep channel used-quota resets atomic across multiple New API processes.
- Keep immutable GHCR publishing and staged digest deployment workflows unchanged unless an upstream build contract requires a reviewed adjustment.

## Acceptance

- Merge retains the complete upstream history with no unresolved conflict markers.
- Root module: build, vet, and tests pass.
- RelayKit with `GOWORK=off`: build, vet, and tests pass.
- Frontend: i18n synchronization, type checking, tests, affected-file lint/format, and production build pass.
- Tokeness deployment transaction tests and workflow syntax checks pass.
- `Dockerfile.tokeness` builds the canonical frontend and root binary without changing the production release policy.
- Independent review finds no regression in authentication, request conversion, cache accounting, billing, or Tokeness UI behavior.

## Risks

- Conversion changes can silently drop explicit zero values or change provider request semantics.
- OIDC status fields affect both backend configuration and the login UI contract.
- CI conflict resolution can accidentally remove Tokeness regression coverage or upstream vetting.
- Locale conflict resolution can overwrite Tokeness-native copy if whole files are accepted from one side.
