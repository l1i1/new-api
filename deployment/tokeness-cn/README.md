# Tokeness China Production Deployment

The China site uses an Alibaba Cloud ECI fleet behind the Shanghai entry ECS's nginx and EdgeOne. A Git push builds images only. It never changes production.

The Chengdu lightweight tier (two hosts) retired on 2026-10-05: it no longer runs nginx or ml-sync, nothing resolves to it, and every role it held — EdgeOne origin, panel tier, relay entry, drain pinning — belongs to the entry ECS now. The release paths that used it are kept behind `SWAS_PANEL_TIER=1` (the hosts' ml-sync was stopped, not deleted) and fail closed if enabled while the hosts are gone; the default path never contacts them.

## Release Gate (tag is version)

The version identity is the tag name: `v<semver>-tokeness-mainland.<N>` (e.g. `v1.0.0-rc.37-tokeness-mainland.1`). Pushing such a tag triggers a CNB `tag_push` build that bakes the tag into `VERSION` and publishes an immutable `ml-<tag>` image. No manual version or digest input anywhere.

**The `<semver>` base tracks the upstream release the branch is currently synced to, and is re-aligned on every upstream sync — the same rule the overseas line follows.** Both sites are built from one `tokeness/main`, so a shared base keeps the two version labels directly comparable instead of implying that the mainland build is derived from an older upstream than it is. Keep the base in step with the overseas label whenever an upstream sync lands, and let only `<N>` advance for releases that carry no new upstream base.

Examples of a correct re-alignment: the rc37 sync re-bases the line at `v1.0.0-rc.37-tokeness-mainland.1`, and the label then stays `rc.37` for every subsequent fix-only release on that base (`...-mainland.2`, `...-mainland.3`, …) until the next upstream sync moves it again. `<N>` restarts at 1 on each re-base, matching the overseas line's per-base numbering. The rc.33→rc.37 re-base is the first time this rule was applied: the earlier rc34–rc36 syncs did **not** re-align, so the whole `rc.33-tokeness-mainland.1`…`.30` line carried three upstream bases' worth of code under one stale label.

Note the validation regexes in `.cnb.yml` and `deployment/tokeness-cn/deploy.sh` only pin the `-tokeness-mainland.<N>` suffix, so a stale base is accepted silently — nothing in the pipeline will catch a base that was not re-aligned. The re-alignment is a naming convention only: it changes the tag, the baked `VERSION` and the app's reported version, never the code or the digest.

1. Push `tokeness/main` to the internal `origin`. The mirror syncs the commit to CNB and GitHub.
2. Create a release tag `v<semver>-tokeness-mainland.<N>` at the checked-out commit and push it. That single push is the whole release: the CNB `tag_push` pipeline validates the format, writes the tag as `VERSION`, publishes the immutable `ml-<tag>` image, and then chains into the gated deploy pipeline itself (`api_trigger`, `cnb:trigger` with `sync: true`). No button, no local machine, and a failed deploy fails the tag build instead of leaving a green tag behind.
3. The chained deploy runs the same gated release as the manual button:

   - **certify**: resolves the immutable `ml-<tag>` digest from the registry and re-checks it against the certified value between stages;
   - **release to production**: re-pins the ESS scaling configuration to that digest and runs the same master-first `deploy.sh deploy-release <tag> <digest>` used locally (SSH key and known-hosts materialize from the imported key-repo values into a `chmod 600` tmpfs path at run time);
   - **postcheck**: asserts the public `/api/status` version, the rendered head, and the 401 on `/v1/models`.

   `deploy-release` converges the **whole** ECI tier, not one instance. It rolls one instance as before (master-first, then `ess_rollout`), then calls `rollout_all`, which retires the rest of the pre-release set in batches bounded by the scaling group's `MaxSize` (`batch = MaxSize − DesiredCapacity`), each batch taking one drain window. A release therefore costs `1 + ceil(n/batch)` drain windows instead of one — with `MaxSize=10` and six instances that is one roll plus two batch rounds, roughly two hours end to end. That is why the deploy stages in `.cnb.yml` declare `timeout: 3h`: the job default is 2 hours, and declaring a timeout also lifts the 10-minute no-output limit off the long drain holds. Set `ROLLOUT_ALL=0` for a release that only changes master-served paths (panel, migrations, system tasks) — it keeps the old one-at-a-time roll and leaves the rest of the tier for the next default release or for an explicit `deploy.sh rollout-all`.

   The snapshot of the pre-release instance set is taken **before** the release's own roll, so the batch step never re-replaces the instance that roll just brought onto the new image; a single-instance site therefore converges in zero batch rounds.


   The deploy pipeline is triggered with `cnb:trigger` rather than `cnb:apply` on purpose: `cnb:apply` runs the applied pipeline in the **tag** context, where `CNB_BRANCH` is the tag name, and the key repo authorizes imports by `allow_branches` matched against `CNB_BRANCH` — so the import was refused during Prepare and no stage ever ran. `cnb:trigger` pins the run to `tokeness/main` (keeping the credential boundary unchanged) and carries the version in `RELEASE_TAG`, with `sha` set to the tagged commit so the deploy tooling matches the image.

   The manual `cn-production` deploy button (`.cnb/tag_deploy.yml`, owner/master only) still exists as an approval-gated alternative and runs the identical stages; it is also subject to the same tag-context import rule, so use the tag push or a direct `api_trigger` on `tokeness/main`.

   Credentials live in the **imvhb/tokeness-secrets key repo** (CNB's native secret store — it has no repo-settings secrets; key repos are Web-edit-only, watermark-audited, and cannot be cloned). The pipeline imports `cnb-tokeness-secrets.yml` from it, and the file's `allow_slugs`/`allow_events`/`allow_branches` headers restrict the import to exactly this pipeline on `tokeness/main`; the import fails closed when those rules or the file do not match. Values it provides:

   | Key | Content | Least-privilege guidance |
   | --- | --- | --- |
   | `ALIBABA_CLOUD_ACCESS_KEY_ID` / `ALIBABA_CLOUD_ACCESS_KEY_SECRET` | RAM sub-account AK dedicated to this pipeline | Only the ESS/ECI/VPC Describe/Modify actions `deploy.sh` actually calls on `cn-shanghai` — never the main-account AK |
   | `CNB_SWAS_SSH_KEY_B64` | base64 of the `swas-ml` private key — the **entry ECS's** key since the lightweight tier retired (the secret name is kept for the Web-only key repo) | Key is limited to the entry ECS and the (retired) lightweight hosts |
   | `CNB_SWAS_KNOWN_HOSTS_B64` | base64 of the `ssh-keyscan` output for the **entry ECS** (`8.133.244.241` since the 2026-10-09 migration, previously `47.101.40.104`) and the (retired) lightweight hosts | One pinned file covers every host the deploy SSHes to (`StrictHostKeyChecking=yes`); the master-first step rolls the ECS master container, so an ECS entry is required or the release aborts at that step |

   All four are mandatory: the release stage fails closed (`:?` expansions) when any is missing, so a misconfigured import aborts before touching anything.

   Any failure before convergence triggers an automatic rollback: the previous digest is re-pinned, the failed container is deleted so ESS recreates it from the pinned image, the verify loop must pass (the old instance keeps serving throughout the pre-scale-down window), and the master container is re-rolled from the restored configuration so node versions never drift.

   The master re-roll is **blue-green** (2026-09-29): the replacement container starts on the other port of the `3000`/`3001` pair and passes its own readiness + version gates before the panel tier is moved onto it, so the panel no longer sees the old container's recreate window (previously a few seconds, stretched by nginx's `fail_timeout=10s` failover). During the gate window two master containers coexist — explicitly authorized; migrations still never run twice at once because they fire at container *start*, so only the green container migrates, and the old container keeps serving against the migrated schema exactly as the relay tier already does during every master-first rollout.

   The local path remains available as a break-glass fallback (WSL or Linux only; Windows Git Bash is refused):

   ```bash
   bash deployment/tokeness-cn/deploy.sh deploy-release v1.0.0-tokeness-mainland.1            # resolves the digest itself
   bash deployment/tokeness-cn/deploy.sh deploy-release v1.0.0-tokeness-mainland.1 sha256:... # pins a pre-certified digest
   ROLLOUT_ALL=0 bash deployment/tokeness-cn/deploy.sh deploy-release v1.0.0-tokeness-mainland.1  # one instance only
   ```

4. Converge the whole ECI tier without a release (no configuration change, no master roll):

   ```bash
   bash deployment/tokeness-cn/deploy.sh rollout-all                    # onto the digest the scaling config pins
   bash deployment/tokeness-cn/deploy.sh rollout-all <tag> [sha256:...] # pin, roll the master, then converge
   ```

   `rollout-all` is what `deploy-release` calls after its own roll, exposed on its own for the cases that need it out of band — the `ROLLOUT_ALL=0` release above, or a rollback (which stays single-instance on purpose: an emergency rollback must not take two hours, so converge afterwards with `rollout-all`, whose no-argument form targets the digest the rollback re-pinned). It is bounded by `MaxSize` and refuses to run when `MaxSize == DesiredCapacity` rather than silently degrading to one-at-a-time. Every failure path restores the scaling configuration and the steady-state capacity.

5. Verify the release from the public internet (the cn-production pipeline already runs this stage; this is the manual form):

   ```bash
   bash deployment/tokeness-cn/deploy.sh postcheck
   ```

   `postcheck` asserts the public `/api/status` version, that the rendered head has exactly one `<title>` and no leaked `<!--head-html-->` placeholder (head content itself is admin-editable), and that `/v1/models` answers 401.

6. Roll back to a previous digest:

   ```bash
   bash deployment/tokeness-cn/deploy.sh rollback sha256:<previous-digest>
   ```

   `rollback` follows the same gated master-first path as `deploy-release` (the master container is blue-green re-rolled and gated before the ESS rollout). It rolls back **one** instance and does not batch: speed is the point of an emergency rollback. The rest of the tier keeps the bad image until you run `deploy.sh rollout-all`, which with no argument converges onto the digest the rollback just re-pinned.

7. Keep the egress EIP in the shared bandwidth package:

   ```bash
   bash deployment/tokeness-cn/deploy.sh eip-sync
   ```

   The scaling configuration uses `AutoCreateEip`, so every ESS-replaced instance gets a brand-new EIP that does not join the shared bandwidth package (`cbwp-2g`, 2 Gbps peak, PayByDominantTraffic) on its own. `deploy-release`/`rollback` run this convergence automatically after the rollout (advisory: a bind failure warns and egress keeps serving on the standalone EIP peak); `eip-sync` re-runs it manually — e.g. after fixing RAM permissions (`AliyunEIPFullAccess`) or console-side drift.

`ml-latest` is a non-production convenience tag only; never deploy it to a new production instance.

## Blue-green master (2026-09-29)

The master container (`new-api-master` on the backup ECS) serves the panel and is the only node running migrations/system tasks, so a release must update it first. It used to be recreated in place, which blacked the panel out for the container's start time plus nginx's `fail_timeout` failover. It is now rolled blue-green:

1. `start` — a green container comes up on the **other** port of the `3000`/`3001` pair (`docker pull` first; blue is never touched) and must answer `/health/ready`.
2. `gate` — green's `/api/status` version must equal the release.
3. `commit` — on the ECS: the local `:80` last-resort upstream and `/etc/tokeness-cn/master-serving-port` follow green, blue gets a short quiet window, then `docker stop` → `rm` → green is renamed to `new-api-master`.
4. `probe` — the deploy asks the bootstrap which port the panel is actually served from and requires green's. `BLUE_OK` is not consulted: the bootstrap names the container on the *serving* port "blue", so after the commit that is the renamed green.

With `SWAS_PANEL_TIER=1` the retired step returns: `deploy.sh` wrote `/etc/ml-sync/web-primary-port` on both lightweight hosts and ml-sync (30s cron) rewrote the `newapi_web` primary line **only while the pinned port answered `/health/ready`**, with the deploy waiting for both hosts to carry it. That path is dead while the hosts are gone and fails closed.

The serving port therefore alternates per release (`:3000` → `:3001` → `:3000` …). That is expected: `deploy.sh` discovers it from the ECS marker, so never hand-edit the port in either nginx conf. A failed roll reconciles itself: `abort` keeps blue (removing green and restoring `:80`) when blue is still alive, and *finalizes* green when the commit already retired blue — nothing ever points at a dead container.

On the ECS itself the bootstrap edits `/etc/nginx/sites-enabled/tokeness-ml.conf` (host-maintained, not in this repository — it is a symlink into `sites-available`, so the edit follows the link). Both of its master-pointing upstreams move together: `newapi_ml` (the `/v1` last resort) and `newapi_web` (that host's own panel route). The edit is guarded by `nginx -t`, a Host-pinned `:80` probe, and an automatic restore from a timestamped `.bg-bak-*` copy.

`deploy.sh sync-host` (alias `sync-master`) re-runs the whole cycle against the digest the scaling configuration currently pins, and is the healing path for a half-finished roll (a leftover green is adopted when it is healthy on the right image, otherwise it is cleared and recreated).

## Troubleshooting the pipeline

The unattended path was verified end to end on 2026-09-09 (`v1.0.0-rc.33-tokeness-mainland.16`/`.17`). Four defects kept every earlier CI release from ever reaching production; all four are now fixed and covered by the deploy test suite:

| Symptom | Root cause | Fix |
| --- | --- | --- |
| Chained/button build fails in `Prepare` with no log | `cnb:apply` runs the pipeline in the tag context, where `CNB_BRANCH` is the tag name, so the key repo's `allow_branches: tokeness/main` refuses the import | trigger with `cnb:trigger` on `tokeness/main`, pass the version via `RELEASE_TAG`, pin `sha` to the tagged commit |
| `curl: (22) ... error: 404` in the release stage | the pinned aliyun CLI asset `aliyun-cli-linux-latest.tgz` no longer exists | use `aliyun-cli-linux-latest-amd64.tgz` |
| `master sync impossible: bootstrap script not readable at ...` | the bootstrap lived under gitignored `private/`, absent from the CI checkout | it now lives in the repository as `deployment/tokeness-cn/bootstrap-master-ecs.sh` (no secrets: it copies the env off the running master container, and the CNB registry serves the image anonymously) and is the default; `HOST_BOOTSTRAP_SCRIPT` still overrides |
| `ERROR: region can't be empty` → `WARN: EIP shared-bandwidth convergence failed` | aliyun CLI 3.x silently ignores camelCase `--RegionId` on eci/vpc/ess | pass lowercase `--region` (regression-checked in `tests/deploy-test.sh`) |

The fourth one is the subtle one: EIP convergence is advisory, so the release reported success while the new EIP stayed outside the bandwidth package (egress capped at the standalone 200 Mbps peak). If egress looks throttled after a release, run `deploy.sh eip-sync` and confirm `BandwidthPackageId` on the instance EIP.

## Drain-first cutover (2026-09-26)

Every request on this site is a streaming one (measured p50 26s, p95 181s, max 461s
over 500 requests on 2026-09-26), and the scaling configuration did not set
`TerminationGracePeriodSeconds`, so the platform default applied. The old
sequence - scale out to 2, gate the new instance, then scale straight back to 1 -
retired an instance that was still serving, and the SIGKILL cut its in-flight
streams mid-answer.

`ess_rollout` now drains first:

1. scale out to 2 and gate the new instance on the application itself;
2. wait until the ECS's relay upstream file names the new instance (the entry is
   the ECS now; `ecs-fleet-sync` writes that list from the local ESS view). This
   is weaker than the retired pin - the file lists every in-service peer, so it
   cannot single out one instance - which is the trade the lightweight
   retirement accepted;
3. hold `ML_DRAIN_SECONDS` (default 1900, above the measured 1807s stream); the release emits a 30s heartbeat so CNB's no-output watchdog does not kill the stage;
4. scale back to 1.

`rollout_all` repeats exactly those steps, but scales to `stable + batch` and gates
**every** newcomer individually before the hold, then loops until no instance from
its retire set is left. It shares `ess_rollout`'s gates and its
`rollback_failed_rollout` path, so a batch round is the same operation with more
than one instance in flight; what widens is the blast radius of a bad image, which
is why the per-instance application gate runs before the hold rather than after
the scale-down.

With `SWAS_PANEL_TIER=1` the retired step returns: write
`/etc/ml-sync/drain-target` (`<ipv4> <expires-epoch>`) on **both** lightweight
hosts, wait until both nginx copies serve the new instance only, scale back, and
clear the pin afterwards (the pin had to outlive the scale-down, or ml-sync would
re-add the still-InService old instance on its next 30s pass).

ml-sync honours the pin only while its target is healthy, and the marker expires
on its own, so the worst failure mode is a fall back to the previous behaviour
rather than a black-holed `/v1`. A host that refuses the pin aborts the rollout
before the scale-down, and the rollback path clears the pin too.

The shutdown budget is now declarative: `ECI_TERMINATION_GRACE_SECONDS`
(default 240) is written by `config_args.py` in target mode and covered by the
readback check, and `SHUTDOWN_TIMEOUT_SECONDS` (default 180) travels in the
container env. `validate_shutdown_budget` refuses any pair that does not leave
the application's 30s background flush inside the platform window - setting both
to 180, the obvious reading, would put the SIGKILL in the middle of that flush.
Restore mode injects neither field, so a rollback stays byte-faithful.

Note the ordering consequence: a scaling configuration only applies to instances
created after it changes, so raising the grace period cannot protect the instance
being retired by the very release that raises it. **Drain-first is what takes
effect immediately; the grace period is the second layer, from the next release
onward.**

Tuning: `ML_DRAIN_SECONDS` (default 1900), `ROLLOUT_HEARTBEAT_SECONDS` (default 30),
`ML_DRAIN_CONVERGE_ATTEMPTS` (default 12), `ML_DRAIN_CONVERGE_DELAY_SECONDS` (default 15),
`ML_DRAIN_MARKER_TTL_SECONDS` (default 3600, must exceed convergence plus drain).

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

`deploy.sh` runs with `StrictHostKeyChecking=yes` and `IdentitiesOnly=yes`. The
hosts it SSHes to are the **entry ECS** (`8.133.244.241`, `MASTER_SSH_*`) and, on
the retired path only, the two lightweight hosts (`SWAS_SSH_*`); each must be in
the client's `~/.ssh/known_hosts` or covered by the pinned
`SWAS_SSH_KNOWN_HOSTS`/`MASTER_SSH_KNOWN_HOSTS` file. On first use, record
fingerprints through a trusted channel:

```bash
ssh-keyscan -H 8.133.244.241 >> ~/.ssh/known_hosts
```

Do not deploy without a verified host fingerprint.
