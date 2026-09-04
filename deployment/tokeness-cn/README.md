# Tokeness China Production Deployment

The China site uses one Alibaba Cloud ECI instance behind a Shanghai lightweight nginx reverse proxy and EdgeOne. A Git push builds images only. It never changes production.

## Release Gate

1. Push `tokeness/main` to the internal `origin`. The mirror syncs the commit to CNB and GitHub.
2. Wait for the CNB push pipeline to publish `ml-<full-commit-sha>`. This tag is immutable; the build fails rather than overwriting an existing tag.
3. Create a deployment tag at that exact commit in CNB and choose the `cn-production` environment.
4. An `owner` or `master` must approve the deployment. The approved job resolves the commit image and prints one immutable reference:

   ```text
   docker.cnb.cool/imvhb/new-api-cn@sha256:<digest>
   ```

5. Use that exact digest reference when recreating the ECI instance in the Alibaba Cloud console. Do not use `ml-latest` for a new production deployment.

CNB does not receive the lightweight-server SSH key or Alibaba Cloud credentials. The approval job certifies the production image; infrastructure changes remain an authorized local operation.

## Cutover

Keep the old ECI instance running until the new instance is healthy. After the console reports the new private IP:

```bash
bash deployment/tokeness-cn/deploy.sh nginx-update <ECI_PRIVATE_IP>
bash deployment/tokeness-cn/deploy.sh verify
```

`nginx-update` validates the address, changes only the `newapi_ml` upstream, checks nginx, reloads it, and verifies both the EdgeOne and private paths. Any failed check restores the previous nginx configuration and exits nonzero. Delete the old ECI instance only after both commands pass.

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
