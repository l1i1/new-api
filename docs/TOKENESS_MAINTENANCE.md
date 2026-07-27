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

The `Tokeness Production Deploy` workflow is the only automated production rollout path. It accepts either `verify` or `deploy`:

1. Run `verify` after provisioning or whenever node drift is suspected.
2. For a release, copy the immutable digest from `Tokeness Publish GHCR`.
3. Run `deploy`, enter that digest, and set confirmation to `deploy-production`.
4. The workflow verifies all nodes first, then deploys `JP-N2`, `EV-JP`, `JP-M`, and finally the `EV-JP2` CDN origin.
5. Every node must report the selected digest, runtime digest, running state, health, start time, and application version.
6. After the origin changes, the workflow probes `/v1/models` through the public CDN domains and requires their `X-New-Api-Version` header to match EV-JP2.

If any node or CDN validation fails, the workflow redeploys the prior digest to every node changed by that run in reverse order. Node-local deployment also restores its previous image when pull, start, or health verification fails.

Both the selected rollback image and requested target must be trusted
`ghcr.io/l1i1/new-api@sha256:<digest>` references. Final success rechecks the
selected image, runtime image, container state, health (or the local status
endpoint when no Docker healthcheck exists), and application version. SSH,
Docker, Compose, and readiness operations have hard deadlines; an interrupted
or ambiguous node is included in idempotent rollback. Rollback reconciliation
waits for any still-running remote transaction lock to clear, repeatedly
verifies the selected image, and restores the preflight digest within a bounded
20-minute node budget. The workflow reserves enough total runtime for the
worst-case forward rollout and reverse-order fleet rollback.

The GitHub `tokeness-production` Environment contains only:

- `TOKENESS_DEPLOY_SSH_KEY`: a dedicated SSH private key.
- `TOKENESS_SSH_KNOWN_HOSTS`: pinned host keys for all four nodes.

The public key must be installed on each node with a forced command and SSH restrictions:

```text
restrict,command="/usr/local/sbin/tokeness-new-api-deploy" ssh-ed25519 ... tokeness-gha-deploy
```

Install `deployment/tokeness/remote-command.sh` as `/usr/local/sbin/tokeness-new-api-deploy`, owned by root and mode `0755`. The forced command permits only `verify` and `deploy ghcr.io/l1i1/new-api@sha256:<digest>`; it does not provide a general-purpose production shell.

Use a staged rollout and verify the version, container health, local status endpoint, and prompt-cache regression suite after deployment. The workflow performs deployment and routing checks; authenticated prompt-cache probes remain a separate release acceptance test so production API keys are not exposed to the deployment job.

Production hosts must not run periodic `docker compose pull && docker compose up -d`. Scheduled automation belongs in the upstream candidate workflow; production changes require an explicit reviewed digest.
