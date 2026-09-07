#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly WORKSPACE_ROOT="$(cd -- "$SCRIPT_DIR/../../../.." && pwd)"
readonly CONFIG_SERIALIZER="$SCRIPT_DIR/config_args.py"

readonly NGINX_CONF="${NGINX_CONF:-/etc/nginx/sites-available/tokeness-ml.conf}"
readonly NGINX_UPSTREAM_NAME="${NGINX_UPSTREAM_NAME:-newapi_ml}"
readonly NGINX_UPSTREAM_PORT="${NGINX_UPSTREAM_PORT:-3000}"
readonly CNB_IMAGE_REPOSITORY="${CNB_IMAGE_REPOSITORY:-docker.cnb.cool/imvhb/new-api-cn}"

readonly SWAS_HOST="${SWAS_HOST:-8.133.172.195}"
readonly SWAS_SSH_KEY_PATH="${SWAS_SSH_KEY_PATH:-$WORKSPACE_ROOT/private/access/keys/swas-ml}"
readonly SWAS_SSH_KNOWN_HOSTS="${SWAS_SSH_KNOWN_HOSTS:-}"
# SWAS-2 hosts the panel-tier New API container (also the /v1 last-resort
# fallback). The deploy pipeline keeps it on the deployed digest: before every
# ESS rollout/rollback, deploy.sh reruns the bootstrap there (master-first),
# which pulls env + image + registry creds live from the (already updated)
# scaling config. The ESS rollout only starts once the master is verified
# healthy on the new image.
readonly SWAS2_HOST="${SWAS2_HOST:-101.133.234.135}"
readonly SWAS2_SSH_KEY_PATH="${SWAS2_SSH_KEY_PATH:-$SWAS_SSH_KEY_PATH}"
readonly SWAS2_SSH_KNOWN_HOSTS="${SWAS2_SSH_KNOWN_HOSTS:-}"
readonly HOST_BOOTSTRAP_SCRIPT="${HOST_BOOTSTRAP_SCRIPT:-$WORKSPACE_ROOT/private/scripts/bootstrap-newapi-host.sh}"
readonly HOST_STATUS_URL="${HOST_STATUS_URL:-http://172.24.63.126:8300/api/status}"
readonly EDGEONE_TEST_URL="${EDGEONE_TEST_URL:-https://tokeness.cn/api/status}"
# Direct probe defaults to the plaintext upstream for a Host-pinned request.
# Override DIRECT_PROBE_URL / DIRECT_PROBE_INSECURE when the upstream serves HTTPS.
readonly DIRECT_PROBE_URL="${DIRECT_PROBE_URL:-http://127.0.0.1/health/ready}"
readonly DIRECT_PROBE_INSECURE="${DIRECT_PROBE_INSECURE:-0}"
readonly VERIFY_TIMEOUT_SECONDS="${VERIFY_TIMEOUT_SECONDS:-45}"
readonly ROLLOUT_VERIFY_ATTEMPTS="${ROLLOUT_VERIFY_ATTEMPTS:-6}"
readonly ROLLOUT_VERIFY_DELAY_SECONDS="${ROLLOUT_VERIFY_DELAY_SECONDS:-10}"

readonly REMOTE_RUN_DIR='/run/lock'
readonly REMOTE_LOCK_NAME='tokeness-cn-deploy.lock'

# Application-level rollout gate: ESS stayed "Healthy" through the 2026-09-06
# crash-loop, so the rollout waits for the dependency-aware readiness endpoint.
readonly APP_READY_TIMEOUT_SECONDS="${APP_READY_TIMEOUT_SECONDS:-300}"
readonly APP_READY_POLL_SECONDS="${APP_READY_POLL_SECONDS:-15}"
readonly APP_CONTAINER_NAME="${APP_CONTAINER_NAME:-newapi}"

log() { printf '[%s] %s\n' "$(date --iso-8601=seconds)" "$*"; }
warn() { log "WARN: $*"; }
error() { log "ERROR: $*" >&2; }
die() { error "$*"; exit 1; }

# Refuse Windows Git Bash/MSYS: Windows-side aliyun CLI or jq can emit CRLF,
# and a stray \r inside a re-sent scaling-config env value crash-loops the
# container (2026-09-06 production incident). Run this script from WSL or
# Linux; automated tests set TOKENESS_TEST_SKIP_OS_GUARD=1.
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    if [[ "${TOKENESS_TEST_SKIP_OS_GUARD:-0}" != "1" && "${TOKENESS_ALLOW_WINDOWS:-0}" != "1" ]]; then
      printf 'ERROR: deploy.sh must run from WSL or Linux, not Windows Git Bash (CRLF + path-mangling risk).\n' >&2
      printf '       Override with TOKENESS_ALLOW_WINDOWS=1 only if you accept that risk.\n' >&2
      exit 1
    fi
    if [[ "${TOKENESS_ALLOW_WINDOWS:-0}" == "1" ]]; then
      printf '[%s] %s\n' "$(date --iso-8601=seconds)" "WARN: running on Windows by explicit override; CR stripped at reads and probe paths" >&2
    fi
    ;;
esac

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is not installed: $1"
}

is_valid_ipv4() {
  local ip="$1" octet
  [[ "$ip" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]] || return 1
  IFS=. read -r -a octets <<< "$ip"
  for octet in "${octets[@]}"; do
    # Reject non-decimal and values above 255; avoids oct (e.g. 08, 010) traps.
    [[ "$octet" =~ ^(0|[1-9][0-9]{0,2})$ ]] || return 1
    (( octet <= 255 )) || return 1
  done
  return 0
}

# Run a Bash script from stdin on a lightweight host. Positional args passed
# after `--` become $1..$N on the remote. Starting Bash explicitly keeps the
# awk/heredoc logic independent of the remote login shell (dash/ash safe).
remote_cmd_on() {
  local host="$1" key="$2" known="$3"
  shift 3
  [[ -r "$key" ]] || die "missing lightweight-server SSH key at $key"
  local ssh_args=(
    -i "$key"
    -o BatchMode=yes
    -o ConnectTimeout=15
    -o IdentitiesOnly=yes
    -o StrictHostKeyChecking=yes
  )
  if [[ -n "$known" ]]; then
    ssh_args+=( -o "UserKnownHostsFile=$known" )
  fi
  ssh "${ssh_args[@]}" "root@$host" -- bash -s -- "$@"
}

remote_cmd() {
  remote_cmd_on "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS" "$@"
}

get_upstream_ip() {
  local output addresses=()
  output="$(remote_cmd "$NGINX_CONF" "$NGINX_UPSTREAM_NAME" "$NGINX_UPSTREAM_PORT" <<'REMOTE_AWK'
#!/usr/bin/env bash
set -Eeuo pipefail
conf="$1"
name="$2"
port="$3"
awk -v name="$name" -v port="$port" '
  $0 ~ "^[[:space:]]*upstream[[:space:]]+" name "[[:space:]]*\\{[[:space:]]*$" { inside = 1; next }
  inside && $0 ~ "^[[:space:]]*}" { inside = 0 }
  inside && $0 ~ "^[[:space:]]*server[[:space:]]+[0-9.]+:" port "[^;]*;" {
    line = $0
    sub(/^[[:space:]]*server[[:space:]]+/, "", line)
    sub(/:.*/, "", line)
    print line
  }
' "$conf"
REMOTE_AWK
)" || return 1
  mapfile -t addresses <<< "$output"
  [[ "${#addresses[@]}" -eq 1 && -n "${addresses[0]}" ]] || {
    error "expected exactly one active server in upstream $NGINX_UPSTREAM_NAME"
    return 1
  }
  is_valid_ipv4 "${addresses[0]}" || {
    error "invalid upstream IPv4 address: ${addresses[0]}"
    return 1
  }
  printf '%s\n' "${addresses[0]}"
}

validate_status_body() {
  local label="$1" body="$2"
  if ! jq -e '.success == true' >/dev/null 2>&1 <<< "$body"; then
    error "$label returned an invalid or unsuccessful status response"
    return 1
  fi
}

verify_node() {
  require_command curl
  require_command jq

  local public_body direct_body upstream_ip
  if ! public_body="$(curl -fsS --connect-timeout 15 --max-time "$VERIFY_TIMEOUT_SECONDS" "$EDGEONE_TEST_URL")"; then
    error "EdgeOne public chain failed: $EDGEONE_TEST_URL"
    return 1
  fi
  validate_status_body "EdgeOne public chain" "$public_body" || return 1
  log "EdgeOne public chain is healthy"

  upstream_ip="$(get_upstream_ip)" || return 1
  log "lightweight nginx upstream is $upstream_ip:$NGINX_UPSTREAM_PORT"

  if ! direct_body="$(remote_cmd "$DIRECT_PROBE_URL" "$VERIFY_TIMEOUT_SECONDS" "$DIRECT_PROBE_INSECURE" <<'REMOTE_PROBE'
#!/usr/bin/env bash
set -Eeuo pipefail
url="$1"
timeout="$2"
insecure="$3"
extra=()
if [ "$insecure" = "1" ]; then
  extra=( -k )
fi
curl -fsS "${extra[@]}" --connect-timeout 15 --max-time "$timeout" -H 'Host: tokeness.cn' "$url"
REMOTE_PROBE
)"; then
    error "lightweight server to ECI private chain failed"
    return 1
  fi
  validate_status_body "lightweight server to ECI private chain" "$direct_body" || return 1
  log "verify: OK (upstream=$upstream_ip)"
}

# Rewrite the single active server line in the named upstream block. Trailing
# nginx parameters (weight=, max_fails=, backup, ...) are preserved. Asserts
# exactly one such line exists so a bare + parameterized pair is never split
# into two upstream members. Runs the whole transaction under a remote flock so
# concurrent cutovers cannot interleave.
apply_upstream() {
  local target_ip="$1"
  remote_cmd "$NGINX_CONF" "$NGINX_UPSTREAM_NAME" "$NGINX_UPSTREAM_PORT" "$target_ip" \
    "$REMOTE_RUN_DIR" "$REMOTE_LOCK_NAME" <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
conf="$1"
name="$2"
port="$3"
target_ip="$4"
rundir="$5"
lockname="$6"

# Serialize the read/mutate/reload window on the lightweight host.
install -d -m 0755 "$rundir" 2>/dev/null || true
exec 9>"$rundir/$lockname"
if command -v flock >/dev/null 2>&1; then
  if ! flock -n 9; then
    echo "ERROR: another Tokeness China deployment is running" >&2
    exit 1
  fi
else
  echo "WARNING: flock unavailable; update is not serialized" >&2
fi

backup="$(mktemp "${conf}.tokeness-backup.XXXXXX")"
candidate="$(mktemp "${conf}.tokeness-candidate.XXXXXX")"
cp -p -- "$conf" "$backup"

if ! awk -v name="$name" -v port="$port" -v target_ip="$target_ip" '
  BEGIN { count = 0 }
  $0 ~ "^[[:space:]]*upstream[[:space:]]+" name "[[:space:]]*\\{[[:space:]]*$" { inside = 1 }
  inside && $0 ~ "^[[:space:]]*server[[:space:]]+[0-9.]+:" port "[^;]*;" {
    count++
    sub("[0-9.]+:" port, target_ip ":" port)
  }
  { print }
  inside && $0 ~ "^[[:space:]]*}" { inside = 0 }
  END { if (count != 1) exit 42 }
' "$conf" > "$candidate"; then
  rm -f -- "$candidate" "$backup"
  echo "expected exactly one active server in upstream $name" >&2
  exit 42
fi

if cmp -s -- "$conf" "$candidate"; then
  rm -f -- "$candidate" "$backup"
  printf 'NOOP\n'
  exit 0
fi

mv -- "$candidate" "$conf"
if ! nginx -t; then
  cp -p -- "$backup" "$conf"
  rm -f -- "$backup"
  echo "nginx -t failed; restored previous config" >&2
  exit 1
fi
if ! systemctl reload nginx; then
  cp -p -- "$backup" "$conf"
  if ! nginx -t || ! systemctl reload nginx; then
    echo "nginx reload failed and rollback could not be verified; backup retained at $backup" >&2
    exit 2
  fi
  rm -f -- "$backup"
  echo "nginx reload failed; restored previous config" >&2
  exit 1
fi
printf 'CHANGED\t%s\n' "$backup"
REMOTE_SCRIPT
}

nginx_update() {
  local target_ip="$1" result backup=''
  is_valid_ipv4 "$target_ip" || die "invalid ECI private IPv4 address: $target_ip"

  result="$(apply_upstream "$target_ip")" || die "nginx upstream apply failed; inspect the preceding rollback status before retrying"
  if [[ "$result" == NOOP ]]; then
    log "nginx upstream already points to $target_ip:$NGINX_UPSTREAM_PORT"
  elif [[ "$result" == $'CHANGED\t'* ]]; then
    backup="${result#*$'\t'}"
    log "nginx upstream now points to $target_ip:$NGINX_UPSTREAM_PORT"
  else
    die "unexpected response from nginx update: $result"
  fi

  if ! verify_node; then
    if [[ -n "$backup" ]]; then
      restore_upstream "$backup" || die "deployment verification failed and nginx rollback also failed"
      verify_node || die "deployment verification and post-rollback probe both failed"
      die "deployment verification failed; nginx upstream was rolled back"
    fi
    die "deployment verification failed; no configuration was changed"
  fi

  [[ -z "$backup" ]] || discard_backup "$backup"
}

restore_upstream() {
  local backup="$1"
  remote_cmd "$NGINX_CONF" "$backup" "$REMOTE_RUN_DIR" "$REMOTE_LOCK_NAME" <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
conf="$1"
backup="$2"
rundir="$3"
lockname="$4"
# Mutating the config is mutually exclusive with apply_upstream.
install -d -m 0755 "$rundir" 2>/dev/null || true
exec 9>"$rundir/$lockname"
if command -v flock >/dev/null 2>&1; then
  if ! flock -n 9; then
    echo "ERROR: another Tokeness China deployment is running; cannot roll back" >&2
    exit 1
  fi
fi
test -f "$backup"
cp -p -- "$backup" "$conf"
nginx -t
systemctl reload nginx
rm -f -- "$backup"
REMOTE_SCRIPT
}

discard_backup() {
  local backup="$1"
  if ! remote_cmd "$backup" <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
rm -f -- "$1"
REMOTE_SCRIPT
  then
    warn "could not remove backup $backup"
  fi
}

image_ref() {
  local digest="$1"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "image digest must be sha256:<64 lowercase hex>"
  require_command docker
  docker buildx imagetools inspect "$CNB_IMAGE_REPOSITORY@$digest" >/dev/null
  printf '%s@%s\n' "$CNB_IMAGE_REPOSITORY" "$digest"
}

scaling_instances_json() {
  aliyun_cmd ess DescribeScalingInstances --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION" | tr -d '\r'
}

in_service_instance_ids() {
  scaling_instances_json | jq -r '
    .ScalingInstances.ScalingInstance[]?
    | select(.LifecycleState == "InService") | .InstanceId' | tr -d '\r' | sort -u
}

instance_private_ip() {
  local id="$1"
  scaling_instances_json | jq -r --arg id "$id" '
    .ScalingInstances.ScalingInstance[]?
    | select(.InstanceId == $id) | .PrivateIpAddress // empty' | tr -d '\r'
}

# The ECI tier reaches upstream model channels through its auto-created EIP
# (ESS AutoCreateEip=true). EIPs newly created by a scale replacement do NOT
# join the shared bandwidth package on their own (2026-09-06: egress was
# capped at the default 200 Mbps / per-traffic billing until the IP was added
# by hand), so every rollout converges this binding.
readonly SHARED_BANDWIDTH_PACKAGE_ID="${SHARED_BANDWIDTH_PACKAGE_ID:-cbwp-uf6cup45a4jmnbnbgth04}"
# Grace window for a just-replaced instance whose EIP attach has not shown up
# in DescribeContainerGroups yet (0 disables the retry).
readonly EIP_ATTACH_GRACE_SECONDS="${EIP_ATTACH_GRACE_SECONDS:-30}"

instance_public_ip() {
  local id="$1"
  aliyun_cmd eci DescribeContainerGroups --RegionId "$ALIYUN_REGION" \
    --ContainerGroupIds "[\"$id\"]" | tr -d '\r' \
    | jq -r '.ContainerGroups[0].InternetIp // empty'
}

# ensure_eip_in_bandwidth_package <instance_id> - make the instance's EIP a
# member of $SHARED_BANDWIDTH_PACKAGE_ID. Idempotent: skips when the EIP is
# already in the package or the instance has no EIP (private-only fallback).
ensure_eip_in_bandwidth_package() {
  local id="$1" eip eip_json allocation_id package_id
  [[ -n "$SHARED_BANDWIDTH_PACKAGE_ID" ]] || { log "no shared bandwidth package configured; skipping EIP convergence"; return 0; }
  eip="$(instance_public_ip "$id")" \
    || { error "could not read the public IP of $id"; return 1; }
  if [[ -z "$eip" && "$EIP_ATTACH_GRACE_SECONDS" -gt 0 ]]; then
    # A just-replaced instance may briefly report no EIP while the attach is
    # still propagating; retry once before treating it as private-only.
    log "instance $id has no public EIP yet; retrying in ${EIP_ATTACH_GRACE_SECONDS}s"
    sleep "$EIP_ATTACH_GRACE_SECONDS"
    eip="$(instance_public_ip "$id")" || { error "could not read the public IP of $id"; return 1; }
  fi
  if [[ -z "$eip" ]]; then
    warn "instance $id still has no public EIP; skipping shared-bandwidth convergence (rerun deploy.sh eip-sync)"
    return 0
  fi
  eip_json="$(aliyun_cmd vpc DescribeEipAddresses --RegionId "$ALIYUN_REGION" --EipAddress "$eip" | tr -d '\r')" \
    || { error "could not query the EIP object of $eip"; return 1; }
  IFS=$'\t' read -r allocation_id package_id < <(
    jq -r '(.EipAddresses.EipAddress[0] // {})
      | [(.AllocationId // ""), (.BandwidthPackageId // "")] | @tsv' <<<"$eip_json")
  if [[ -z "$allocation_id" ]]; then
    error "no EIP object found for public IP $eip ($id)"
    return 1
  fi
  if [[ "$package_id" == "$SHARED_BANDWIDTH_PACKAGE_ID" ]]; then
    log "EIP $eip already in shared bandwidth package $SHARED_BANDWIDTH_PACKAGE_ID"
    return 0
  fi
  if ! aliyun_cmd vpc AddCommonBandwidthPackageIp --RegionId "$ALIYUN_REGION" \
    --BandwidthPackageId "$SHARED_BANDWIDTH_PACKAGE_ID" --IpInstanceId "$allocation_id" >/dev/null; then
    error "could not add EIP $eip to shared bandwidth package $SHARED_BANDWIDTH_PACKAGE_ID"
    return 1
  fi
  log "EIP $eip ($allocation_id) joined shared bandwidth package $SHARED_BANDWIDTH_PACKAGE_ID"
}

# converge_eip_bandwidth - bind the EIPs of every in-service ECI instance to
# the shared bandwidth package. Advisory only: a failure warns (egress keeps
# serving on the standalone EIP peak) but must not abort a converged rollout.
converge_eip_bandwidth() {
  local id rc=0
  while IFS= read -r id; do
    [[ -n "$id" ]] || continue
    ensure_eip_in_bandwidth_package "$id" || rc=1
  done < <(in_service_instance_ids)
  return "$rc"
}

# Digest currently pinned in the scaling configuration (may be empty when the
# config drifted in the console, as during the 2026-09-06 incident).
current_config_digest() {
  local image
  image="$(oss_scaling_config_json | jq -r '.ScalingConfigurations[0].Containers[0].Image // empty' | tr -d '\r')"
  printf '%s\n' "${image##*@}"
}

snapshot_config_digest() {
  local snapshot="$1" image
  image="$(jq -r '.ScalingConfigurations[0].Containers[0].Image // empty' <<<"$snapshot" | tr -d '\r')"
  printf '%s\n' "${image##*@}"
}

app_status_ok() {
  local ip="$1" body
  body="$(remote_cmd "$ip" <<'REMOTE_PROBE'
#!/usr/bin/env bash
set -Eeuo pipefail
curl -fsS --connect-timeout 5 --max-time 10 "http://$1:3000/health/ready"
REMOTE_PROBE
)" || return 1
  jq -e '.success == true' >/dev/null 2>&1 <<<"$body"
}

# Print the first fatal startup line in the container log, if any. Rate-limit
# parse warnings are non-fatal (the app continues with defaults) and must not
# match; only [FATAL] / Go panics abort the rollout early.
container_log_fatal_line() {
  local instance_id="$1"
  aliyun_cmd eci DescribeContainerLog --RegionId "$ALIYUN_REGION" \
    --ContainerGroupId "$instance_id" --ContainerName "$APP_CONTAINER_NAME" --Tail 60 \
    | tr -d '\r' | jq -r '.Content // empty' \
    | grep -m1 -E '\[FATAL\]|panic:' || true
}

# wait_app_ready <instance_id> <private_ip> - gate the rollout on the
# application, never on ESS "Healthy": probe /health/ready on the new instance
# through the lightweight server and fail fast on a fatal startup log line.
wait_app_ready() {
  local instance_id="$1" ip="$2" attempt=0 fatal
  if [[ ! "$APP_READY_TIMEOUT_SECONDS" =~ ^[1-9][0-9]*$ || ! "$APP_READY_POLL_SECONDS" =~ ^[1-9][0-9]*$ ]]; then
    error "APP_READY_TIMEOUT_SECONDS and APP_READY_POLL_SECONDS must be positive integers"
    return 1
  fi
  local max_attempts=$(( APP_READY_TIMEOUT_SECONDS / APP_READY_POLL_SECONDS ))
  if [[ "$max_attempts" -lt 1 ]]; then
    error "APP_READY_TIMEOUT_SECONDS must exceed APP_READY_POLL_SECONDS"
    return 1
  fi
  if ! is_valid_ipv4 "$ip"; then
    error "new instance has an invalid private IP"
    return 1
  fi
  while (( attempt < max_attempts )); do
    fatal="$(container_log_fatal_line "$instance_id")"
    if [[ -n "$fatal" ]]; then
      error "container log shows a fatal startup error: $fatal"
      return 1
    fi
    if app_status_ok "$ip"; then
      log "app answers on $ip:$NGINX_UPSTREAM_PORT (attempt $((attempt + 1))/$max_attempts)"
      return 0
    fi
    attempt=$(( attempt + 1 ))
    if (( attempt < max_attempts )); then
      sleep "$APP_READY_POLL_SECONDS"
    fi
  done
  error "app on $ip did not become ready within ${APP_READY_TIMEOUT_SECONDS}s"
  return 1
}

wait_verify_converged() {
  local attempt
  for ((attempt = 1; attempt <= ROLLOUT_VERIFY_ATTEMPTS; attempt++)); do
    if verify_node; then
      log "verify passed on attempt $attempt"
      return 0
    fi
    if (( attempt < ROLLOUT_VERIFY_ATTEMPTS )); then
      sleep "$ROLLOUT_VERIFY_DELAY_SECONDS"
    fi
  done
  return 1
}

# rollback_failed_rollout <failed_instance_id> <previous_digest> <snapshot> -
# restore the complete pre-update scaling configuration before removing the
# failed container. The snapshot path also preserves probes from old images.
rollback_failed_rollout() {
  local failed_id="$1" previous_digest="$2" previous_snapshot="${3:-}"
  warn "rolling back to previous digest: ${previous_digest:-<unchanged>}"
  if [[ -n "$previous_snapshot" ]]; then
    if ! restore_scaling_config "$previous_snapshot"; then
      error "rollback stopped before deleting the failed container: configuration restore failed"
      return 1
    fi
  elif [[ -n "$previous_digest" ]]; then
    if ! apply_ml_digest "$previous_digest"; then
      error "rollback stopped before deleting the failed container: image restore failed"
      return 1
    fi
  fi
  if [[ -n "$failed_id" ]]; then
    if ! aliyun_cmd eci DeleteContainerGroup --RegionId "$ALIYUN_REGION" --ContainerGroupId "$failed_id" >/dev/null; then
      warn "could not delete failed container group $failed_id; ESS may recreate it with the pinned digest"
    fi
  fi
  if ! scale_group 1; then
    error "rollback could not restore desired capacity"
    return 1
  fi
  if ! wait_healthy_instances 1; then
    error "rollback could not restore a healthy instance"
    return 1
  fi
  if wait_verify_converged; then
    log "rollback verified; serving the previous image"
    # A replacement instance (new auto-created EIP) may be serving here too.
    converge_eip_bandwidth || warn "EIP shared-bandwidth convergence failed; rerun deploy.sh eip-sync"
  else
    error "post-rollback verify failed; release remains blocked"
    return 1
  fi
}

# --- Alibaba Cloud ESS helpers -------------------------------------------------
# These read credentials only from the ALIBABA_CLOUD_ACCESS_KEY_ID / _KEY_SECRET
# env vars (aliyun CLI standard), never from client code or checks, so the same
# deploy.sh works locally and inside the CNB tag_deploy pipeline.

readonly ALIYUN_REGION="${ALIYUN_REGION:-cn-shanghai}"
readonly SCALING_GROUP_ID="${SCALING_GROUP_ID:-asg-uf641n1j5akwa1ozcz6t}"
readonly SCALING_CONFIG_ID="${SCALING_CONFIG_ID:-asc-uf641n1j5akwa1p0smug}"
readonly SCALE_OUT_RULE_ARI="${SCALE_OUT_RULE_ARI:-ari:acs:ess:cn-shanghai:1563974331677521:scalingrule/asr-uf65is8x4oiinwm3oidm}"

aliyun_cmd() {
  # Prefer the local aliyun CLI; CNB resolves it from PATH.
  if command -v aliyun >/dev/null 2>&1; then
    aliyun "$@"
  elif command -v "$HOME/bin/aliyun" >/dev/null 2>&1; then
    "$HOME/bin/aliyun" "$@"
  else
    die "aliyun CLI is not installed"
  fi
}

desired_capacity_out() {
  [[ -n "${SCALING_OUT_DESIRED_CAPACITY:-}" ]] || die "SCALING_OUT_DESIRED_CAPACITY not set"
}

oss_scaling_config_json() {
  # tr -d '\r': a Windows-side aliyun CLI/jq can emit CRLF, and a stray CR in a
  # re-sent env value (e.g. SQL_DSN) makes the container crash-loop at startup
  # (2026-09-06 production incident). No legit value here contains a carriage
  # return, so stripping them everywhere fails safe.
  aliyun_cmd ess DescribeEciScalingConfigurations --ScalingConfigurationId "$SCALING_CONFIG_ID" --region "$ALIYUN_REGION" | tr -d '\r'
}

# resolve_ml_digest <tag> -> prints sha256:<64 hex> for the ml-<tag> image,
# using the registry's two-step token auth (no local cnb-token dependency).
resolve_ml_digest() {
  local tag="$1" token digest
  [[ "$tag" =~ ^v[0-9A-Za-z._-]+-tokeness-mainland\.[0-9]+$ ]] || die "invalid mainland release tag: $tag"
  require_command curl
  require_command python3
  token="$(curl -fsSL "https://docker.cnb.cool/service/token?service=cnb-registry&scope=repository:imvhb/new-api-cn:pull" \
    -u "cnb:${CNB_REGISTRY_TOKEN:?}" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("token",""))')"
  digest="$(curl -fsSL -H "Authorization: Bearer $token" \
    -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json" \
    -D - -o /dev/null "https://docker.cnb.cool/v2/imvhb/new-api-cn/manifests/ml-$tag" \
    | awk 'tolower($1)=="docker-content-digest:"{digest=$2; gsub(/\r/, "", digest); print digest; exit}')"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "could not resolve an immutable digest for ml-$tag"
  printf '%s\n' "$digest"
}

# Build a full replacement request from a private JSON snapshot. The helper
# emits NUL-delimited arguments so tabs, backslashes and escaped newlines in
# environment/command values never pass through a line-oriented format.
scaling_config_args() {
  local mode="$1" digest="${2:-}" snapshot="$3"
  require_command python3
  [[ -r "$CONFIG_SERIALIZER" ]] || die "missing scaling configuration serializer: $CONFIG_SERIALIZER"
  local -a emitted=()
  if ! printf '%s' "$snapshot" | python3 "$CONFIG_SERIALIZER" \
    --mode "$mode" --digest "$digest" --target-name "$APP_CONTAINER_NAME" --emit \
    >/dev/null; then
    return 1
  fi
  # The validation pass above keeps this process substitution deterministic;
  # its output stays inside Bash and is never written to deploy logs.
  mapfile -d '' -t emitted < <(
    printf '%s' "$snapshot" | python3 "$CONFIG_SERIALIZER" \
      --mode "$mode" --digest "$digest" --target-name "$APP_CONTAINER_NAME" --emit
  )
  (( ${#emitted[@]} > 0 )) || return 1
  SCALING_CONFIG_ARGS=(ess ModifyEciScalingConfiguration
    --ScalingConfigurationId "$SCALING_CONFIG_ID"
    --region "$ALIYUN_REGION"
    "${emitted[@]}"
  )
}

verify_scaling_config_readback() {
  local mode="$1" digest="${2:-}" expected="$3" actual="$4"
  require_command python3
  printf '%s\n%s\n' "$expected" "$actual" | python3 "$CONFIG_SERIALIZER" \
    --mode "$mode" --digest "$digest" --target-name "$APP_CONTAINER_NAME" \
    --expected-id "$SCALING_CONFIG_ID" --verify
}

modify_scaling_config() {
  local mode="$1" digest="${2:-}" snapshot="$3" updated
  scaling_config_args "$mode" "$digest" "$snapshot" || return 1
  # MSYS rewrites POSIX-looking arguments ("D:/.../health/ready" reached the
  # scaling configuration, caught by readback 2026-09-06). None of these
  # arguments ever need POSIX->Windows conversion (ids, numbers, probe paths,
  # env values), so disable conversion for this aliyun invocation entirely.
  if [[ "$(uname -s)" == MINGW* || "$(uname -s)" == MSYS* || "$(uname -s)" == CYGWIN* ]]; then
    MSYS2_ARG_CONV_EXCL="*" aliyun_cmd "${SCALING_CONFIG_ARGS[@]}" >/dev/null \
      || return 1
  else
    aliyun_cmd "${SCALING_CONFIG_ARGS[@]}" >/dev/null || return 1
  fi
  updated="$(oss_scaling_config_json)" || return 1
  verify_scaling_config_readback "$mode" "$digest" "$snapshot" "$updated"
}

# apply_ml_digest <sha256:digest> [snapshot] - update the image and the
# application probes. Callers pass the original snapshot so a failed update
# can restore every supported field, including credentials and commands.
apply_ml_digest() {
  local digest="$1" snapshot="${2:-}"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "new image digest must be sha256:<64 hex>"
  [[ -n "$snapshot" ]] || snapshot="$(oss_scaling_config_json)" || return 1
  # Node identity envs travel with every config write so ESS-recreated
  # instances always come up with them (see inject_node_identity_envs).
  snapshot="$(inject_node_identity_envs "$snapshot")" || return 1
  modify_scaling_config target "$digest" "$snapshot" \
    || { error "ModifyEciScalingConfiguration failed or readback drifted"; return 1; }
  log "scaling configuration image set to $digest (full config preserved, liveness=tcp:$NGINX_UPSTREAM_PORT readiness=/health/ready)"
}

# Restore the exact pre-update snapshot. In particular, this does not add
# readiness probes that an older image/configuration never declared.
restore_scaling_config() {
  local snapshot="$1"
  modify_scaling_config restore "" "$snapshot" \
    || { error "could not restore the complete scaling configuration snapshot"; return 1; }
  log "scaling configuration snapshot restored"
}

# inject_node_identity_envs <snapshot-json> - upsert NODE_NAME / NODE_TYPE into
# the app container env of a scaling-configuration snapshot. Without them every
# instance self-reports as master (new-api defaults NODE_TYPE!=slave), so a
# scale-out to 2 ECI instances ran every background/system task twice (2026-09-06).
# The ECI tier is a slave (migrations + system tasks belong to the stable SWAS-2
# host container, which bootstrap-newapi-host.sh pins to NODE_TYPE=master).
# Values overridable via ECI_NODE_NAME / ECI_NODE_TYPE.
inject_node_identity_envs() {
  local snapshot="$1"
  require_command python3
  printf '%s' "$snapshot" | ECI_NODE_NAME="${ECI_NODE_NAME:-new-api-ml000}" \
    ECI_NODE_TYPE="${ECI_NODE_TYPE:-slave}" python3 -c '
import json, os, sys

snapshot = json.loads(sys.stdin.read())
name = os.environ["ECI_NODE_NAME"]
node_type = os.environ["ECI_NODE_TYPE"]
if node_type not in ("master", "slave"):
    sys.exit("ECI_NODE_TYPE must be master or slave")

# The Describe output wraps the configuration in ScalingConfigurations; the
# serializer also accepts a bare configuration object. Handle both.
if isinstance(snapshot.get("ScalingConfigurations"), list) and snapshot["ScalingConfigurations"]:
    config = snapshot["ScalingConfigurations"][0]
else:
    config = snapshot

def inject(envs):
    out, seen = [], set()
    for entry in envs:
        key = entry.get("Key")
        if key == "NODE_NAME":
            out.append({"Key": "NODE_NAME", "Value": name}); seen.add("NODE_NAME")
        elif key == "NODE_TYPE":
            out.append({"Key": "NODE_TYPE", "Value": node_type}); seen.add("NODE_TYPE")
        else:
            out.append(entry)
    if "NODE_NAME" not in seen:
        out.append({"Key": "NODE_NAME", "Value": name})
    if "NODE_TYPE" not in seen:
        out.append({"Key": "NODE_TYPE", "Value": node_type})
    return out

for container in config.get("Containers") or []:
    if container.get("Name") == "newapi":
        container["EnvironmentVars"] = inject(container.get("EnvironmentVars") or [])
print(json.dumps(snapshot, ensure_ascii=False))
'
}

# sync_host_container <expected_version> - rebuild the SWAS-2 host container
# from the current scaling configuration (bootstrap pulls env + digest +
# registry creds live). Runs BEFORE the ESS rollout/rollback (master-first):
# the master takes the new image, runs its DB migrations, and must pass the
# bootstrap readiness gate + version check before the ECI tier moves. Also
# used as the recovery path when a rollout fails (the config has already been
# re-pinned to the previous digest by then). expected_version pins the
# /api/status identity when the release tag is known (deploy-release); other
# callers pass an empty string and rely on the bootstrap's own readiness gate.
sync_host_container() {
  local expected_version="$1" host_version
  [[ -r "$HOST_BOOTSTRAP_SCRIPT" ]] \
    || die "SWAS-2 host sync impossible: bootstrap script not readable at $HOST_BOOTSTRAP_SCRIPT"
  log "syncing SWAS-2 host container ($SWAS2_HOST) to the deployed digest"
  remote_cmd_on "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" \
    < "$HOST_BOOTSTRAP_SCRIPT" \
    || { error "SWAS-2 host bootstrap failed; rerun $HOST_BOOTSTRAP_SCRIPT against $SWAS2_HOST"; return 1; }
  host_version="$(remote_cmd_on "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" \
    <<REMOTE_HOST_VER
curl -fsS --max-time 10 "$HOST_STATUS_URL" | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["version"])'
REMOTE_HOST_VER
  )" || { error "could not read the SWAS-2 host version from $HOST_STATUS_URL"; return 1; }
  if [[ -n "$expected_version" && "$host_version" != "$expected_version" ]]; then
    error "SWAS-2 host version ($host_version) does not match the deployed release ($expected_version)"
    return 1
  fi
  log "SWAS-2 host container synced (version $host_version)"
}

scale_group() {
  local desired="$1"
  aliyun_cmd ess ModifyScalingGroup --ScalingGroupId "$SCALING_GROUP_ID" --DesiredCapacity "$desired" --region "$ALIYUN_REGION" >/dev/null \
    || { error "ModifyScalingGroup to desired=$desired failed"; return 1; }
  log "scaling group desired capacity set to $desired"
}

wait_healthy_instances() {
  local want="$1" tries="${2:-40}" i=0 got
  while [ "$i" -lt "$tries" ]; do
    got="$(aliyun_cmd ess DescribeScalingInstances --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION" \
      | jq -r '[.ScalingInstances.ScalingInstance[] | select(.LifecycleState=="InService" and .HealthStatus=="Healthy")] | length' | tr -d '\r')" \
      || got="0"
    if [ "$got" -ge "$want" ]; then
      log "healthy instances: $got (want $want)"
      return 0
    fi
    sleep 20
    i=$((i + 1))
  done
  error "timed out waiting for $want healthy instance(s) (have $got)"
  return 1
}

# ess_rollout <sha256:digest> <previous_digest> <previous_snapshot> - scale out to 2, gate the NEW
# instance on the application itself (probe + container log), and only then
# scale back to 1. While both instances are healthy ml-sync lists both upstream
# members, and nginx passive checks (max_fails=2 fail_timeout=5s) bridge the
# ~30s window in which the old member disappears after the scale-down. Any
# failure before convergence rolls back to the previous digest automatically.
ess_rollout() {
  local digest="$1" previous_digest="${2:-}" previous_snapshot="${3:-}" attempt
  local before_ids new_id new_ip
  if ! before_ids="$(in_service_instance_ids)"; then
    error "could not read the current in-service instance set"
    return 1
  fi
  if ! scale_group 2; then
    rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || true
    return 1
  fi
  if ! wait_healthy_instances 2; then
    if ! rollback_failed_rollout "" "$previous_digest" "$previous_snapshot"; then
      error "rollout gate failed and rollback could not be verified"
    fi
    return 1
  fi
  new_id="$(comm -13 <(printf '%s\n' "$before_ids") <(in_service_instance_ids) | head -n1 || true)"
  if [[ -z "$new_id" ]]; then
    if ! rollback_failed_rollout "" "$previous_digest" "$previous_snapshot"; then
      error "could not identify the scaled instance and rollback could not be verified"
    fi
    return 1
  fi
  if ! new_ip="$(instance_private_ip "$new_id")"; then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "could not resolve the new instance address and rollback could not be verified"
    fi
    return 1
  fi
  log "new instance $new_id at $new_ip; gating on application health before scale-down"
  if ! wait_app_ready "$new_id" "$new_ip"; then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "rollout aborted and rollback could not be verified"
    fi
    return 1
  fi
  if ! scale_group 1 || ! wait_healthy_instances 1; then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "scale-down gate failed and rollback could not be verified"
    fi
    return 1
  fi
  if wait_verify_converged; then
    log "ess rollout to $digest complete"
    # EIP → shared-bandwidth convergence is advisory (a binding failure does
    # not undo the rollout; egress keeps serving on the standalone EIP peak).
    converge_eip_bandwidth || warn "EIP shared-bandwidth convergence failed; rerun deploy.sh eip-sync"
    return 0
  fi
  # Verification failed after the scale-down: restore the previous image and
  # let ESS + ml-sync converge (brief interruption is unavoidable here, which
  # is exactly why the app gate runs before the scale-down).
  if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
    error "post-rollout verify failed and rollback could not be verified"
  fi
  return 1
}

# postcheck - post-deployment public verification: version identity, the
# server-rendered head (exactly one <title>; the <!--head-html--> placeholder
# must have been replaced — head CONTENT itself is admin-editable via the
# CustomHeadHTML option and is not asserted), and the authenticated API
# surface answering 401.
postcheck() {
  require_command curl
  require_command jq
  local base="${SITE_BASE_URL:-https://tokeness.cn}"
  local version html title_count code
  version="$(curl -fsS --max-time 30 "$base/api/status" | jq -r '.data.version // empty')" \
    || die "postcheck: /api/status is not reachable"
  [[ -n "$version" ]] || die "postcheck: /api/status did not report a version"
  log "postcheck: public version $version"
  html="$(curl -fsSL --max-time 30 "$base/")" || die "postcheck: homepage fetch failed"
  title_count="$(grep -o '<title' <<<"$html" | wc -l)"
  [[ "$title_count" -eq 1 ]] || die "postcheck: expected exactly one <title>, found $title_count"
  grep -Fq '<!--head-html-->' <<<"$html" \
    && die "postcheck: <!--head-html--> placeholder leaked into the rendered page"
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 "$base/v1/models")"
  [[ "$code" == "401" ]] || die "postcheck: /v1/models returned $code, expected 401"
  log "postcheck: OK (head rendered server-side, /v1/models answered 401)"
}

usage() {
  cat <<'USAGE'
Usage:
  deploy.sh verify
  deploy.sh postcheck
  deploy.sh nginx-update <ECI_PRIVATE_IP>
  deploy.sh image-ref <sha256:DIGEST>
  deploy.sh deploy-release <tag> [sha256:DIGEST]
  deploy.sh rollback <sha256:DIGEST>
  deploy.sh sync-host
  deploy.sh eip-sync
USAGE
}

main() {
  local operation="${1:-verify}"
  case "$operation" in
    verify)
      [[ $# -eq 1 ]] || die "verify does not accept arguments"
      verify_node || exit 1
      ;;
    postcheck)
      [[ $# -eq 1 ]] || die "postcheck does not accept arguments"
      postcheck
      ;;
    nginx-update)
      [[ $# -eq 2 ]] || die "nginx-update requires one ECI private IP"
      nginx_update "$2"
      ;;
    image-ref)
      [[ $# -eq 2 ]] || die "image-ref requires one image digest"
      image_ref "$2"
      ;;
    deploy-release)
      [[ $# -eq 2 || $# -eq 3 ]] || die "deploy-release requires a version tag and optionally a certified digest"
      local release_tag="$2"
      local release_digest previous_digest previous_snapshot
      if [[ $# -eq 3 ]]; then
        # CI (cn-production tag-deploy) already certified this digest against
        # the tag via the registry; skip the token-authenticated lookup.
        release_digest="$3"
        [[ "$release_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "certified digest must be sha256:<64 lowercase hex>"
      else
        release_digest="$(resolve_ml_digest "$release_tag")"
      fi
      previous_snapshot="$(oss_scaling_config_json)" || die "failed to snapshot the current scaling configuration"
      previous_digest="$(snapshot_config_digest "$previous_snapshot")"
      [[ "$previous_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "current scaling configuration image is not a valid digest; refusing a release without rollback target"
      if ! apply_ml_digest "$release_digest" "$previous_snapshot"; then
        if ! restore_scaling_config "$previous_snapshot"; then
          die "release update failed and the complete scaling configuration could not be restored"
        fi
        die "release update failed; the previous scaling configuration was restored"
      fi
      # Master-first: the SWAS-2 host container (sole master, runs migrations)
      # takes the new image before the ECI tier rolls. Its bootstrap readiness
      # gate + version check double as the canary; a failure here aborts while
      # the ECI tier is still serving the previous image untouched.
      if ! sync_host_container "$release_tag"; then
        if ! restore_scaling_config "$previous_snapshot"; then
          error "host sync failed and the scaling configuration could not be restored"
        fi
        if ! sync_host_container ""; then
          error "host container could not be restored to the previous digest; manual intervention required (deploy.sh sync-host)"
        fi
        die "release aborted: SWAS-2 host container failed to converge to $release_tag"
      fi
      if ! ess_rollout "$release_digest" "$previous_digest" "$previous_snapshot"; then
        # rollback_failed_rollout re-pinned the scaling configuration + ECI to
        # the previous digest; bring the master back in line so node versions
        # never drift after an aborted release.
        if ! sync_host_container ""; then
          error "host container could not be restored after the failed rollout; manual intervention required (deploy.sh sync-host)"
        fi
        die "release rollout failed; rollback was attempted and must be verified before retrying"
      fi
      log "release $release_tag -> $release_digest deployed"
      ;;
    sync-host)
      [[ $# -eq 1 ]] || die "sync-host does not accept arguments"
      sync_host_container ""
      ;;
    eip-sync)
      [[ $# -eq 1 ]] || die "eip-sync does not accept arguments"
      converge_eip_bandwidth || die "EIP shared-bandwidth convergence failed; see the ERROR lines above for the specific cause"
      ;;
    rollback)
      [[ $# -eq 2 ]] || die "rollback requires one image digest (sha256:...)"
      local rollback_digest="$2" rollback_previous rollback_snapshot
      rollback_snapshot="$(oss_scaling_config_json)" || die "failed to snapshot the current scaling configuration"
      rollback_previous="$(snapshot_config_digest "$rollback_snapshot")"
      [[ "$rollback_previous" =~ ^sha256:[0-9a-f]{64}$ ]] || die "current scaling configuration image is not a valid digest; refusing a rollback without rollback target"
      if ! apply_ml_digest "$rollback_digest" "$rollback_snapshot"; then
        if ! restore_scaling_config "$rollback_snapshot"; then
          die "rollback update failed and the complete scaling configuration could not be restored"
        fi
        die "rollback update failed; the previous scaling configuration was restored"
      fi
      # Master-first (same rationale as deploy-release).
      if ! sync_host_container ""; then
        if ! restore_scaling_config "$rollback_snapshot"; then
          error "host sync failed and the scaling configuration could not be restored"
        fi
        if ! sync_host_container ""; then
          error "host container could not be restored to the previous digest; manual intervention required (deploy.sh sync-host)"
        fi
        die "rollback aborted: SWAS-2 host container failed to converge to $rollback_digest"
      fi
      if ! ess_rollout "$rollback_digest" "$rollback_previous" "$rollback_snapshot"; then
        # rollback_failed_rollout re-pinned the scaling configuration + ECI to
        # the pre-rollback digest; keep the master on it too.
        if ! sync_host_container ""; then
          error "host container could not be restored after the failed rollout; manual intervention required (deploy.sh sync-host)"
        fi
        die "rollback rollout failed; rollback was attempted and must be verified before retrying"
      fi
      log "rollback to $rollback_digest complete"
      ;;
    -h|--help)
      usage
      ;;
    *)
      usage >&2
      die "unknown operation: $operation"
      ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
