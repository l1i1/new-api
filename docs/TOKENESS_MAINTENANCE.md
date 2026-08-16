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
6. After the origin changes, the workflow probes the dashboard root and `/api/status` through the main web origin, then probes `/v1/models` through each public API CDN domain. Every response must expose the expected `X-New-Api-Version` header where applicable.

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

Install the reviewed command on every node with
`bash deployment/tokeness/install-remote-command.sh`. The installer atomically
replaces `/usr/local/sbin/tokeness-new-api-deploy`, verifies SHA-256, root
ownership, mode `0755`, and command version `2026-08-17.3`. Fleet preflight
rejects any node that reports another command version. The forced command
permits only `verify` and `deploy ghcr.io/l1i1/new-api@sha256:<digest>`; it does
not provide a general-purpose production shell.

The `new-api` service on JP-M uses a blue-green update transaction. Before the
first deployment, an operator must explicitly mark only the New API proxy
snippet(s) under `/opt/1panel/www/sites/<site>/proxy/*.conf`:

```nginx
# TOKENESS_BLUE_GREEN_MANAGED host=tokeness.io
proxy_pass http://127.0.0.1:8201;
```

The marker must immediately precede each managed `proxy_pass`; its `host=` value
is the exact virtual host used for the live route probe. The release command
edits only those entries and never infers ownership from a shared loopback port
or a 1Panel directory name. A malformed or unpaired marker fails the deployment,
and exactly one OpenResty container must mount `/opt/1panel/www`. The deployer
starts the inactive slot with `docker compose run`, validates its immutable
image and exact `127.0.0.1:8201/8202 -> 3000/tcp` binding plus the EPay sidecar, and reloads
OpenResty only after `nginx -t` succeeds. Before draining the old container, it
waits for a new OpenResty worker generation and probes `/api/status` through
every marked virtual host. The host must expose the old container's
`/proc/<pid>/net/tcp{,6}` files, and the OpenResty container requires `curl`, for
these fail-closed checks.

The host-local `.blue-green.state` contains no secret values. A `pending` phase
is written before `release.env` changes, so a SIGKILL at any point before the
switch can restore the old slot. After the route is switched, a
`cleanup-pending` phase records the new slot and old container before draining;
startup retries that cleanup idempotently before accepting another deployment.
If cleanup is still pending after the retry, verification remains available but
a new deployment is rejected so the recovery record cannot be overwritten. If
the new active slot or its live OpenResty route is unhealthy during recovery,
the deployer immediately verifies the previous slot, switches OpenResty back to
it, and retains the failed slot as cleanup-pending instead of leaving the public
route on a broken container.
Existing connections on the old loopback port must reach zero before the old
container receives a stop signal; otherwise cleanup remains pending for a later
retry. An old container that is already stopped is removed without a connection
drain. Only then is the phase changed to `committed`. `new-api-slave` nodes retain
the existing recreate path.

Use a staged rollout and verify the version, container health, local status endpoint, and prompt-cache regression suite after deployment. The workflow performs deployment and routing checks; authenticated prompt-cache probes remain a separate release acceptance test so production API keys are not exposed to the deployment job.

Production hosts must not run periodic `docker compose pull && docker compose up -d`. Scheduled automation belongs in the upstream candidate workflow; production changes require an explicit reviewed digest.

## CDN Client IP and Authentication Compatibility

The dashboard and relay are behind two different CDN products. The EV-JP2
OpenResty origin uses `real_ip_header X-Forwarded-For` only after loading the
published Cloudflare and Tencent EdgeOne CIDRs from
`nodes/servers/configs/ev-jp2/nginx/cdn-real-ip.conf`. The generated include
must be refreshed with `sync-cdn-real-ip.ps1` when either provider changes its
network list. Never configure a wildcard trusted-proxy range or trust an
arbitrary client-supplied forwarding header. The resulting address is passed
to New API, so rate limits and audit logs are keyed by the real client rather
than the shared CDN egress address.

New API's stateless dashboard authentication retains the signed legacy `session`
cookie as a migration input. When a browser has no new refresh cookie but still
has a valid legacy cookie, `/api/user/auth/refresh` issues a new server-side
session and access token. This one-time bridge prevents an image upgrade from
logging out every existing browser. Invalid, disabled, or expired legacy state
is never converted into a new session.

The production rollout must remain on the previous digest until both the CDN
real-IP configuration and the legacy-session migration tests pass. A release is
not accepted when a repeated anonymous login probe returns 429, when the
dashboard root is unavailable, or when a legacy signed session cannot be
migrated through `/api/user/auth/refresh`.
