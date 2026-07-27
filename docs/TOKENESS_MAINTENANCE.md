# Tokeness Fork Maintenance

## Repository Roles

- `origin`: `l1i1/new-api`, the source of truth for Tokeness changes.
- `upstream`: `QuantumNous/new-api`, read-only input for official updates.
- `tokeness/main`: reviewed Tokeness production mainline.
- `sync/upstream-*`: disposable upstream merge candidates.

Never commit credentials to this repository or embed them in remote URLs. Use the GitHub CLI or the operating system credential store.

## Upstream Updates

The `Tokeness Upstream Sync` workflow checks official `main` weekly and can also be started manually. It merges upstream into a candidate branch and opens a PR against `tokeness/main`.

An upstream PR must pass the Tokeness regression tests and a container build before merge. Conflicts must be resolved in the candidate branch. Merging a sync PR does not publish an image and does not deploy production.

## Image Releases

1. Commit and push the reviewed source to `tokeness/main`.
2. Run `Tokeness Publish GHCR` manually on that exact ref and enter a unique version matching `v*-tokeness-*`.
3. Confirm the workflow summary records the intended source commit and version.
4. Record the workflow commit and resulting `sha256` image digest.
5. Make the GHCR package publicly readable, or authenticate production hosts with read-only package credentials.
6. Deploy the image by digest, never by `latest` or another mutable tag.

The publish workflow emits both a human-readable version tag and a commit tag, but deployment must use:

```text
ghcr.io/l1i1/new-api@sha256:<digest>
```

Tokeness builds only the canonical `web` UI. Upstream retired the Classic frontend and the Go embed contract now reads `web/dist`, so `Dockerfile.tokeness` builds and embeds that single bundle directly.

## Production Rollout

Use a staged rollout and verify the version, container health, local status endpoint, and prompt-cache regression suite after each step. Deploy non-origin replicas first and the CDN origin last. Keep the previous digest available for rollback.

Production hosts must not run periodic `docker compose pull && docker compose up -d`. Scheduled automation belongs in the upstream candidate workflow; production changes require an explicit reviewed digest.
