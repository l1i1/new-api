# Tokeness China Production Deployment

The China site uses one Alibaba Cloud ECI instance behind a Shanghai lightweight nginx reverse proxy and EdgeOne. A Git push builds images only. It never changes production.

## Release Gate

1. Push `tokeness/main` to the internal `origin`. The mirror syncs the commit to CNB and GitHub.
2. Wait for the CNB push pipeline to publish `ml-<full-commit-sha>`. This tag is immutable; the build fails rather than overwriting an existing tag.
3. Create a deployment tag named `cn-prod-<full-commit-sha>` at that exact commit in CNB and choose the `cn-production` environment. Only this tag namespace is accepted; anything else fails before any image is resolved.
4. An `owner` or `master` must approve the deployment. The approved job resolves the commit image and prints one immutable reference:

   ```text
   docker.cnb.cool/imvhb/new-api-cn@sha256:<digest>
   ```

5. Use that exact digest reference when recreating the ECI instance in the Alibaba Cloud console. `ml-latest` is a non-production convenience tag only; never deploy it to a new production instance.

CNB does not receive the lightweight-server SSH key or Alibaba Cloud credentials. The approval job certifies the production image; infrastructure changes remain an authorized local operation.

## Cutover

Keep the old ECI instance running until the new instance is healthy. After the console reports the new private IP:

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
