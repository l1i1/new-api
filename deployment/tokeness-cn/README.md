# Tokeness China Production Deployment

The China site uses one Alibaba Cloud ECI instance behind a Shanghai lightweight nginx reverse proxy and EdgeOne. A Git push builds images only. It never changes production.

## Release Gate (tag is version)

The version identity is the tag name: `v<semver>-tokeness-mainland.<N>` (e.g. `v1.0.0-tokeness-mainland.1`). Pushing such a tag triggers a CNB `tag_push` build that bakes the tag into `VERSION` and publishes an immutable `ml-<tag>` image. No manual version or digest input anywhere.

1. Push `tokeness/main` to the internal `origin`. The mirror syncs the commit to CNB and GitHub.
2. Create a release tag `v1.0.0-tokeness-mainland.<N>` at the checked-out commit and push it. The CNB `tag_push` pipeline validates the format, writes the tag as `VERSION`, and publishes the immutable `ml-<tag>` image (a re-push of an existing tag fails rather than overwriting).
3. The optional `cn-production` deploy environment (`.cnb/tag_deploy.yml`) resolves and certifies the digest, printing `docker.cnb.cool/imvhb/new-api-cn@sha256:<digest>`. It no longer requires an approver.

   CNB does not receive the lightweight-server SSH key or Alibaba Cloud credentials. It only certifies the production image; infrastructure changes are an authorized local operation.

4. Deploy the certified digest locally:

   ```bash
   bash deployment/tokeness-cn/deploy.sh deploy-release v1.0.0-tokeness-mainland.1
   ```

   `deploy-release` resolves the digest from the registry, sets the ESS scaling configuration to it (preserving every existing env var and re-sending the `/api/status` liveness probe), then runs the gated rollout:

   - scale out to two ESS-healthy instances;
   - **application gate before any scale-down**: wait until the new instance answers `/api/status` (`APP_READY_TIMEOUT_SECONDS`, default 300 s) and its container log shows no `FATAL`/`panic` line — ESS "Healthy" alone is not trusted (it stayed green through the 2026-09-06 crash-loop);
   - scale back to one: while both instances are healthy `ml-sync` lists both upstream members, and nginx passive checks (`max_fails=2 fail_timeout=5s`) bridge the ~30 s window in which the old member disappears;
   - final verify (EdgeOne public + private chain).

   Any failure before convergence triggers an automatic rollback: the previous digest is re-pinned, the failed container is deleted so ESS recreates it from the pinned image, and the verify loop must pass (the old instance keeps serving throughout the pre-scale-down window).

   Run the script from **WSL or Linux**; Windows Git Bash is refused (`TOKENESS_ALLOW_WINDOWS=1` overrides at your own risk — a Windows-side CLI/jq can emit CRLF, and a stray CR in a re-sent env value crash-loops the container).

5. Verify the release from the public internet:

   ```bash
   bash deployment/tokeness-cn/deploy.sh postcheck
   ```

   `postcheck` asserts the public `/api/status` version, that the rendered head has exactly one `<title>` and no leaked `<!--head-html-->` placeholder (head content itself is admin-editable), and that `/v1/models` answers 401.

6. Roll back to a previous digest:

   ```bash
   bash deployment/tokeness-cn/deploy.sh rollback sha256:<previous-digest>
   ```

   `rollback` follows the same gated rollout path as `deploy-release`.

`ml-latest` is a non-production convenience tag only; never deploy it to a new production instance.

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
