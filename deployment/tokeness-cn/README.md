# Tokeness China Production Deployment

The China site uses one Alibaba Cloud ECI instance behind a Shanghai lightweight nginx reverse proxy and EdgeOne. A Git push builds images only. It never changes production.

## Release Gate (tag is version)

The version identity is the tag name: `v<semver>-tokeness-mainland.<N>` (e.g. `v1.0.0-tokeness-mainland.1`). Pushing such a tag triggers a CNB `tag_push` build that bakes the tag into `VERSION` and publishes an immutable `ml-<tag>` image. No manual version or digest input anywhere.

1. Push `tokeness/main` to the internal `origin`. The mirror syncs the commit to CNB and GitHub.
2. Create a release tag `v1.0.0-tokeness-mainland.<N>` at the checked-out commit and push it. That single push is the whole release: the CNB `tag_push` pipeline validates the format, writes the tag as `VERSION`, publishes the immutable `ml-<tag>` image, and then chains into the gated deploy pipeline itself (`api_trigger`, `cnb:trigger` with `sync: true`). No button, no local machine, and a failed deploy fails the tag build instead of leaving a green tag behind.
3. The chained deploy runs the same gated release as the manual button:

   - **certify**: resolves the immutable `ml-<tag>` digest from the registry and re-checks it against the certified value between stages;
   - **release to production**: re-pins the ESS scaling configuration to that digest and runs the same master-first `deploy.sh deploy-release <tag> <digest>` used locally (SSH key and known-hosts materialize from the imported key-repo values into a `chmod 600` tmpfs path at run time);
   - **postcheck**: asserts the public `/api/status` version, the rendered head, and the 401 on `/v1/models`.

   The deploy pipeline is triggered with `cnb:trigger` rather than `cnb:apply` on purpose: `cnb:apply` runs the applied pipeline in the **tag** context, where `CNB_BRANCH` is the tag name, and the key repo authorizes imports by `allow_branches` matched against `CNB_BRANCH` — so the import was refused during Prepare and no stage ever ran. `cnb:trigger` pins the run to `tokeness/main` (keeping the credential boundary unchanged) and carries the version in `RELEASE_TAG`, with `sha` set to the tagged commit so the deploy tooling matches the image.

   The manual `cn-production` deploy button (`.cnb/tag_deploy.yml`, owner/master only) still exists as an approval-gated alternative and runs the identical stages; it is also subject to the same tag-context import rule, so use the tag push or a direct `api_trigger` on `tokeness/main`.

   Credentials live in the **imvhb/tokeness-secrets key repo** (CNB's native secret store — it has no repo-settings secrets; key repos are Web-edit-only, watermark-audited, and cannot be cloned). The pipeline imports `cnb-tokeness-secrets.yml` from it, and the file's `allow_slugs`/`allow_events`/`allow_branches` headers restrict the import to exactly this pipeline on `tokeness/main`; the import fails closed when those rules or the file do not match. Values it provides:

   | Key | Content | Least-privilege guidance |
   | --- | --- | --- |
   | `ALIBABA_CLOUD_ACCESS_KEY_ID` / `ALIBABA_CLOUD_ACCESS_KEY_SECRET` | RAM sub-account AK dedicated to this pipeline | Only the ESS/ECI/VPC Describe/Modify actions `deploy.sh` actually calls on `cn-shanghai` — never the main-account AK |
   | `CNB_SWAS_SSH_KEY_B64` | base64 of the `swas-ml` private key | Key is limited to the two lightweight hosts |
   | `CNB_SWAS_KNOWN_HOSTS_B64` | base64 of the `ssh-keyscan` output for both SWAS hosts | Pins both hosts (`StrictHostKeyChecking=yes`) |

   All four are mandatory: the release stage fails closed (`:?` expansions) when any is missing, so a misconfigured import aborts before touching anything.

   Any failure before convergence triggers an automatic rollback: the previous digest is re-pinned, the failed container is deleted so ESS recreates it from the pinned image, the verify loop must pass (the old instance keeps serving throughout the pre-scale-down window), and the SWAS-2 host container is re-synced from the restored configuration so node versions never drift.

   The local path remains available as a break-glass fallback (WSL or Linux only; Windows Git Bash is refused):

   ```bash
   bash deployment/tokeness-cn/deploy.sh deploy-release v1.0.0-tokeness-mainland.1            # resolves the digest itself
   bash deployment/tokeness-cn/deploy.sh deploy-release v1.0.0-tokeness-mainland.1 sha256:... # pins a pre-certified digest
   ```

4. Verify the release from the public internet (the cn-production pipeline already runs this stage; this is the manual form):

   ```bash
   bash deployment/tokeness-cn/deploy.sh postcheck
   ```

   `postcheck` asserts the public `/api/status` version, that the rendered head has exactly one `<title>` and no leaked `<!--head-html-->` placeholder (head content itself is admin-editable), and that `/v1/models` answers 401.

5. Roll back to a previous digest:

   ```bash
   bash deployment/tokeness-cn/deploy.sh rollback sha256:<previous-digest>
   ```

   `rollback` follows the same gated master-first path as `deploy-release` (host container rebuilt and gated before the ESS rollout).

6. Keep the egress EIP in the shared bandwidth package:

   ```bash
   bash deployment/tokeness-cn/deploy.sh eip-sync
   ```

   The scaling configuration uses `AutoCreateEip`, so every ESS-replaced instance gets a brand-new EIP that does not join the shared bandwidth package (`cbwp-2g`, 2 Gbps peak, PayByDominantTraffic) on its own. `deploy-release`/`rollback` run this convergence automatically after the rollout (advisory: a bind failure warns and egress keeps serving on the standalone EIP peak); `eip-sync` re-runs it manually — e.g. after fixing RAM permissions (`AliyunEIPFullAccess`) or console-side drift.

`ml-latest` is a non-production convenience tag only; never deploy it to a new production instance.

## Troubleshooting the pipeline

The unattended path was verified end to end on 2026-09-09 (`v1.0.0-rc.33-tokeness-mainland.16`/`.17`). Four defects kept every earlier CI release from ever reaching production; all four are now fixed and covered by the deploy test suite:

| Symptom | Root cause | Fix |
| --- | --- | --- |
| Chained/button build fails in `Prepare` with no log | `cnb:apply` runs the pipeline in the tag context, where `CNB_BRANCH` is the tag name, so the key repo's `allow_branches: tokeness/main` refuses the import | trigger with `cnb:trigger` on `tokeness/main`, pass the version via `RELEASE_TAG`, pin `sha` to the tagged commit |
| `curl: (22) ... error: 404` in the release stage | the pinned aliyun CLI asset `aliyun-cli-linux-latest.tgz` no longer exists | use `aliyun-cli-linux-latest-amd64.tgz` |
| `SWAS-2 host sync impossible: bootstrap script not readable at //private/scripts/...` | the host bootstrap lived under gitignored `private/`, absent from the CI checkout | moved to `deployment/tokeness-cn/bootstrap-newapi-host.sh` (no secrets; reads env/image/creds from the scaling config) and made it the default, `HOST_BOOTSTRAP_SCRIPT` still overrides |
| `ERROR: region can't be empty` → `WARN: EIP shared-bandwidth convergence failed` | aliyun CLI 3.x silently ignores camelCase `--RegionId` on eci/vpc/ess | pass lowercase `--region` (regression-checked in `tests/deploy-test.sh`) |

The fourth one is the subtle one: EIP convergence is advisory, so the release reported success while the new EIP stayed outside the bandwidth package (egress capped at the standalone 200 Mbps peak). If egress looks throttled after a release, run `deploy.sh eip-sync` and confirm `BandwidthPackageId` on the instance EIP.

## Cutover

`deploy-release` covers cutover automatically through `ml-sync` (both upstream members while two instances are healthy). The manual form remains for exceptional cases — e.g. recovering from console-side drift:

```bash
bash deployment/tokeness-cn/deploy.sh nginx-update <ECI_PRIVATE_IP>
```

`nginx-update` consumes the address, changes only the `newapi_ml` upstream, checks nginx, reloads it, and verifies both the EdgeOne and private paths. If the upstream changed and any check fails, the previous nginx configuration is restored and the script exits nonzero. If the upstream already pointed at the target IP (a no-op), no configuration changed and there is nothing to restore — re-run `deploy.sh verify` and query the ECI console if it still fails. Delete the old ECI instance only after `nginx-update` succeeds.

To validate an approved digest independently:

```bash
bash deployment/tokeness-cn/deploy.sh image-ref sha256:<digest>
```

## Rollback

If a new ECI fails before cutover, leave nginx unchanged and remove the failed instance. If cutover verification fails, the script restores the previous upstream automatically. Keep the previous ECI instance available until the production checks pass.

## Checks

```bash
bash -n deployment/tokeness-cn/deploy.sh
bash deployment/tokeness-cn/tests/deploy-test.sh
jq -e . deployment/tokeness-cn/nodes.json >/dev/null
```

### SSH host verification

`deploy.sh` runs with `StrictHostKeyChecking=yes` and `IdentitiesOnly=yes`. The lightweight host `8.133.172.195` must be present in the SSH client's `~/.ssh/known_hosts` (or set `SWAS_SSH_KNOWN_HOSTS` to a pinned file). On first use, record the host fingerprint through a trusted channel:

```bash
ssh-keyscan -H 8.133.172.195 >> ~/.ssh/known_hosts
```

Do not deploy without a verified host fingerprint.
