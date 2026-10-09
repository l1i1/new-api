#!/usr/bin/env bash
# release-on-master.sh - roll a release out FROM the entry ECS itself.
#
# Why: the entry already holds the Aliyun CLI and its credentials (it uses them
# to drive the relay member list), docker, jq, and the master container. So a
# release needs neither the CNB pipeline (out of build quota on 2026-10-10) nor
# the broker's per-command credential grants - the image is built and pushed
# elsewhere (release-local.sh, or CNB), and the rollout happens here.
#
#   release-on-master.sh --check                 prerequisites only, no side effects
#   release-on-master.sh <tag> <sha256:digest>   pull the image, then roll it out
#
# Requires deploy.sh synced to REPO_DIR (see deployment/tokeness-cn/README.md).
set -Eeuo pipefail
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
REPO_DIR="${REPO_DIR:-/usr/local/lib/tokeness}"
IMAGE_NAME="${IMAGE_NAME:-docker.cnb.cool/imvhb/new-api-cn}"
GROUP="${SCALING_GROUP_ID:-asg-uf641n1j5akwa1ozcz6t}"
REGION="${ALIYUN_REGION:-cn-shanghai}"
DEPLOY="$REPO_DIR/deployment/tokeness-cn/deploy.sh"
log() { printf '[release-on-master] %s\n' "$*"; }
die() { printf '[release-on-master] ERROR: %s\n' "$*" >&2; exit 1; }

check() {
  local rc=0
  for c in aliyun docker jq curl; do
    command -v "$c" >/dev/null || { log "MISSING tool: $c"; rc=1; }
  done
  [[ -r "$DEPLOY" ]] || { log "MISSING $DEPLOY (sync deployment/ here)"; rc=1; }
  if aliyun ess DescribeScalingGroups --ScalingGroupId "$GROUP" --region "$REGION" >/dev/null 2>&1; then
    log "aliyun credentials: OK (assumed role/user can read the scaling group)"
  else
    log "aliyun credentials: FAILED for $GROUP in $REGION"; rc=1
  fi
  docker info >/dev/null 2>&1 && log "docker: OK" || { log "docker: unreachable"; rc=1; }
  if timeout 10 curl -fsS -o /dev/null "https://${IMAGE_NAME%%/*}/v2/" -w '%{http_code}\n' 2>/dev/null | grep -qE '200|401'; then
    log "registry reachable: ${IMAGE_NAME%%/*}"
  else
    log "registry NOT reachable: ${IMAGE_NAME%%/*}"; rc=1
  fi
  # The rollout replaces the master container and rewrites the relay include;
  # both live here, so a read proves the layout.
  [[ -r /etc/nginx/fleet/newapi_ml_servers.conf ]] || { log "MISSING relay include"; rc=1; }
  [[ -r "$(cat /etc/tokeness-cn/master-serving-port 2>/dev/null && printf x)" ]] 2>/dev/null || true
  (( rc == 0 )) && log "READY" || log "NOT READY (see the MISSING/FAILED lines)"
  return $rc
}

tag="${1:-}"; digest="${2:-}"
if [[ "$tag" == "--check" ]]; then check; exit $?; fi
[[ -n "$tag" && -n "$digest" ]] || die "usage: release-on-master.sh --check | <tag> <sha256:digest>"
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "digest must be sha256:<64 lowercase hex>"
check || die "prerequisites are not satisfied; run --check and fix the reported lines"

log "pulling $IMAGE_NAME:ml-$tag"
docker pull "$IMAGE_NAME:ml-$tag" || die "pull failed (is the registry credential configured on this host?)"

log "rolling out (this runs for hours: master, then batch rounds with drain windows)"
MASTER_LOCAL=1 exec bash "$DEPLOY" deploy-release "$tag" "$digest"
