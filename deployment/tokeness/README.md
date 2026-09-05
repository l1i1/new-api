# Tokeness Overseas (International) Production Deployment

The overseas site (`tokeness.ai` / `n.tokeness.dev`) runs New API on four nodes
behind edge CDNs, driven by GitHub Actions. The mainland China site
(`tokeness.cn`) uses a separate CNB-based pipeline under `deployment/tokeness-cn/`.

## Release Flow

1. `tokeness-publish.yml` builds and publishes an immutable image to
   `ghcr.io/l1i1/new-api` (release tag plus `sha-<git-sha>` tags).
2. Dispatch `tokeness-deploy.yml` with `operation=deploy`,
   `image_digest=sha256:<64 lowercase hex>`, and
   `environment=deploy-production`.
3. The `tokeness-production` GitHub environment requires manual approval.
4. `rollout.sh` performs the staged blue-green rollout across the fleet.

`operation=verify` runs a fleet-wide health check without deploying.

## Local Tooling

- `rollout.sh verify|deploy <sha256:digest>` — staged rollout against the node
  list in `nodes.json`; on failure an armed rollback restores the previous
  digest per node.
- `remote-command.sh` — the script shipped to each node by `rollout.sh`
  (blue-green swap, health check, rollback). Its version string must match
  `EXPECTED_REMOTE_COMMAND_VERSION` in `rollout.sh`.
- `install-remote-command.sh` — one-time setup of the restricted remote
  command on a node.
- `nodes.json` — machine-readable node inventory (names, SSH targets,
  registry image, public endpoints). `rollout.sh` validates its schema before
  running.

## Checks

```bash
bash -n deployment/tokeness/rollout.sh deployment/tokeness/remote-command.sh
bash deployment/tokeness/tests/rollout-test.sh
bash deployment/tokeness/tests/remote-command-test.sh
bash deployment/tokeness/tests/blue-green-test.sh
jq -e . deployment/tokeness/nodes.json >/dev/null
```

> Note: `rollout-test.sh` fails under Windows Git Bash because Windows `jq`
> emits CRLF; run it from WSL or Linux.

## SSH Keys

`rollout.sh` expects the deploy SSH key at `~/.ssh/tokeness-deploy`
(override with `TOKENESS_SSH_KEY_PATH`). Host fingerprints must already be in
the client's `known_hosts`; the script runs with `StrictHostKeyChecking=yes`.
The GitHub Actions runner receives the key via secrets; CNB and the mainland
pipeline never receive it.
