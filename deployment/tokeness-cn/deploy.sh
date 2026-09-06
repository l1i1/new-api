#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly WORKSPACE_ROOT="$(cd -- "$SCRIPT_DIR/../../../.." && pwd)"

readonly NGINX_CONF="${NGINX_CONF:-/etc/nginx/sites-available/tokeness-ml.conf}"
readonly NGINX_UPSTREAM_NAME="${NGINX_UPSTREAM_NAME:-newapi_ml}"
readonly NGINX_UPSTREAM_PORT="${NGINX_UPSTREAM_PORT:-3000}"
readonly CNB_IMAGE_REPOSITORY="${CNB_IMAGE_REPOSITORY:-docker.cnb.cool/imvhb/new-api-cn}"

readonly SWAS_HOST="${SWAS_HOST:-8.133.172.195}"
readonly SWAS_SSH_KEY_PATH="${SWAS_SSH_KEY_PATH:-$WORKSPACE_ROOT/private/access/keys/swas-ml}"
readonly SWAS_SSH_KNOWN_HOSTS="${SWAS_SSH_KNOWN_HOSTS:-}"
readonly EDGEONE_TEST_URL="${EDGEONE_TEST_URL:-https://tokeness.cn/api/status}"
# Direct probe defaults to the plaintext upstream for a Host-pinned request.
# Override DIRECT_PROBE_URL / DIRECT_PROBE_INSECURE when the upstream serves HTTPS.
readonly DIRECT_PROBE_URL="${DIRECT_PROBE_URL:-http://127.0.0.1/api/status}"
readonly DIRECT_PROBE_INSECURE="${DIRECT_PROBE_INSECURE:-0}"
readonly VERIFY_TIMEOUT_SECONDS="${VERIFY_TIMEOUT_SECONDS:-45}"
readonly ROLLOUT_VERIFY_ATTEMPTS="${ROLLOUT_VERIFY_ATTEMPTS:-6}"
readonly ROLLOUT_VERIFY_DELAY_SECONDS="${ROLLOUT_VERIFY_DELAY_SECONDS:-10}"

readonly REMOTE_RUN_DIR='/run/lock'
readonly REMOTE_LOCK_NAME='tokeness-cn-deploy.lock'

# Application-level rollout gate: ESS stayed "Healthy" through the 2026-09-06
# crash-loop, so the rollout waits for the app itself to answer /api/status.
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
    # Second Windows hazard: MSYS rewrites POSIX-looking arguments ("/api/status")
    # into Windows paths ("D:/.../api/status") for native executables, which
    # corrupted the liveness probe path. Disable the conversion in-script so the
    # explicit override stays survivable.
    export MSYS_NO_PATHCONV=1
    export MSYS2_ARG_CONV_EXCL='*'
    if [[ "${TOKENESS_TEST_SKIP_OS_GUARD:-0}" != "1" && "${TOKENESS_ALLOW_WINDOWS:-0}" != "1" ]]; then
      printf 'ERROR: deploy.sh must run from WSL or Linux, not Windows Git Bash (CRLF + path-mangling risk).\n' >&2
      printf '       Override with TOKENESS_ALLOW_WINDOWS=1 only if you accept that risk.\n' >&2
      exit 1
    fi
    if [[ "${TOKENESS_ALLOW_WINDOWS:-0}" == "1" ]]; then
      printf '[%s] %s\n' "$(date --iso-8601=seconds)" "WARN: running on Windows by explicit override; CR stripped at reads, MSYS path conversion disabled" >&2
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

# Run a Bash script from stdin on the lightweight host. Positional args passed
# after `--` become $1..$N on the remote. Starting Bash explicitly keeps the
# awk/heredoc logic independent of the remote login shell (dash/ash safe).
remote_cmd() {
  [[ -r "$SWAS_SSH_KEY_PATH" ]] || die "missing lightweight-server SSH key at $SWAS_SSH_KEY_PATH"
  local ssh_args=(
    -i "$SWAS_SSH_KEY_PATH"
    -o BatchMode=yes
    -o ConnectTimeout=15
    -o IdentitiesOnly=yes
    -o StrictHostKeyChecking=yes
  )
  if [[ -n "$SWAS_SSH_KNOWN_HOSTS" ]]; then
    ssh_args+=( -o "UserKnownHostsFile=$SWAS_SSH_KNOWN_HOSTS" )
  fi
  ssh "${ssh_args[@]}" "root@$SWAS_HOST" -- bash -s -- "$@"
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

# Digest currently pinned in the scaling configuration (may be empty when the
# config drifted in the console, as during the 2026-09-06 incident).
current_config_digest() {
  local image
  image="$(oss_scaling_config_json | jq -r '.ScalingConfigurations[0].Containers[0].Image // empty' | tr -d '\r')"
  printf '%s\n' "${image##*@}"
}

app_status_ok() {
  local ip="$1" body
  body="$(remote_cmd "$ip" <<'REMOTE_PROBE'
#!/usr/bin/env bash
set -Eeuo pipefail
curl -fsS --connect-timeout 5 --max-time 10 "http://$1:3000/api/status"
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
# application, never on ESS "Healthy": probe /api/status on the new instance
# through the lightweight server and fail fast on a fatal startup log line.
wait_app_ready() {
  local instance_id="$1" ip="$2" attempt=0 fatal
  local max_attempts=$(( APP_READY_TIMEOUT_SECONDS / APP_READY_POLL_SECONDS ))
  [[ "$max_attempts" -ge 1 ]] || die "APP_READY_TIMEOUT_SECONDS must exceed APP_READY_POLL_SECONDS"
  is_valid_ipv4 "$ip" || die "new instance has an invalid private IP: $ip"
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

# rollback_failed_rollout <failed_instance_id> <previous_digest> - recover from
# a failed rollout without extra downtime: the previous digest is re-pinned so
# ESS's replacement instance runs the old image, the failed container is
# deleted, and ESS + ml-sync converge while a healthy instance keeps serving.
rollback_failed_rollout() {
  local failed_id="$1" previous_digest="$2"
  warn "rolling back to previous digest: ${previous_digest:-<unchanged>}"
  if [[ -n "$previous_digest" ]]; then
    apply_ml_digest "$previous_digest"
  fi
  if [[ -n "$failed_id" ]]; then
    if ! aliyun_cmd eci DeleteContainerGroup --RegionId "$ALIYUN_REGION" --ContainerGroupId "$failed_id" >/dev/null; then
      warn "could not delete failed container group $failed_id; ESS may recreate it with the pinned digest"
    fi
  fi
  scale_group 1
  wait_healthy_instances 1
  if wait_verify_converged; then
    log "rollback verified; serving the previous image"
  else
    warn "post-rollback verify failed; investigate before retrying"
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

# apply_ml_digest <sha256:digest> - sets the scaling configuration image to the
# new digest while preserving every existing env var. ModifyEciScalingConfiguration
# is whole-replace semantics, so the full env list must be re-sent.
apply_ml_digest() {
  local digest="$1"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "new image digest must be sha256:<64 hex>"
  require_command jq
  local cfg
  cfg="$(oss_scaling_config_json)" || die "failed to read scaling configuration"
  local ct
  ct="$(jq -c '.ScalingConfigurations[0].Containers[0]' <<<"$cfg")"
  local container_name image_pull_policy
  # tr -d '\r' at every jq read: a Windows jq build writes CRLF on stdout, and
  # a stray \r here becomes container/env DATA (the 2026-09-06 incident).
  container_name="$(jq -r '.Name' <<<"$ct" | tr -d '\r')"
  image_pull_policy="$(jq -r '.ImagePullPolicy // "IfNotPresent"' <<<"$ct" | tr -d '\r')"

  local current_image
  current_image="$(jq -r '.Image // empty' <<<"$ct" | tr -d '\r')"
  if [[ -z "$current_image" ]]; then
    warn "scaling configuration has no image (console drift?); setting the target now"
  fi

  local args=(ess ModifyEciScalingConfiguration
    "--ScalingConfigurationId" "$SCALING_CONFIG_ID"
    "--region" "$ALIYUN_REGION"
    "--Container.1.Name" "$container_name"
    "--Container.1.Image" "docker.cnb.cool/imvhb/new-api-cn@$digest"
    "--Container.1.ImagePullPolicy" "$image_pull_policy"
  )
  local env_count i=0 key value
  env_count="$(jq '.EnvironmentVars | length' <<<"$ct" | tr -d '\r')"
  (( env_count > 0 )) || die "scaling configuration has no environment variables; refusing a deploy that would boot without SQL_DSN"
  while IFS=$'\t' read -r key value; do
    # Skip internal/immutable keys the API rejects on modify.
    case "$key" in
      SQL_DSN|BATCH_UPDATE_ENABLED|ERROR_LOG_ENABLED|REDIS_CONN_STRING|TZ|SESSION_SECRET|CRYPTO_SECRET|GLOBAL_API_RATE_LIMIT|GLOBAL_API_RATE_LIMIT_DURATION) ;;
      *) continue ;;
    esac
    i=$((i + 1))
    args+=("--Container.1.EnvironmentVar.$i.Key" "$key" "--Container.1.EnvironmentVar.$i.Value" "$value")
  done < <(jq -r '.EnvironmentVars[] | [.Key, .Value] | @tsv' <<<"$ct" | tr -d '\r')
  [[ "$i" -eq "$env_count" ]] || die "could not preserve all existing environment variables (kept $i of $env_count)"

  # Application-level liveness: ESS health checks never caught the 2026-09-06
  # crash-loop. Re-sent on every modify so the probe survives config updates.
  args+=(
    "--Container.1.LivenessProbe.HttpGet.Path" "/api/status"
    "--Container.1.LivenessProbe.HttpGet.Port" "$NGINX_UPSTREAM_PORT"
    "--Container.1.LivenessProbe.HttpGet.Scheme" "HTTP"
    "--Container.1.LivenessProbe.InitialDelaySeconds" "20"
    "--Container.1.LivenessProbe.PeriodSeconds" "10"
    "--Container.1.LivenessProbe.TimeoutSeconds" "5"
    "--Container.1.LivenessProbe.FailureThreshold" "3"
  )

  aliyun_cmd "${args[@]}" >/dev/null || die "ModifyEciScalingConfiguration failed"
  log "scaling configuration image set to $digest (env preserved: $i, liveness probe on /api/status)"
}

scale_group() {
  local desired="$1"
  aliyun_cmd ess ModifyScalingGroup --ScalingGroupId "$SCALING_GROUP_ID" --DesiredCapacity "$desired" --region "$ALIYUN_REGION" >/dev/null \
    || die "ModifyScalingGroup to desired=$desired failed"
  log "scaling group desired capacity set to $desired"
}

wait_healthy_instances() {
  local want="$1" tries="${2:-40}" i=0 got
  while [ "$i" -lt "$tries" ]; do
    got="$(aliyun_cmd ess DescribeScalingInstances --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION" \
      | jq -r '[.ScalingInstances.ScalingInstance[] | select(.LifecycleState=="InService" and .HealthStatus=="Healthy")] | length')" \
      || got="0"
    if [ "$got" -ge "$want" ]; then
      log "healthy instances: $got (want $want)"
      return 0
    fi
    sleep 20
    i=$((i + 1))
  done
  die "timed out waiting for $want healthy instance(s) (have $got)"
}

# ess_rollout <sha256:digest> <previous_digest> - scale out to 2, gate the NEW
# instance on the application itself (probe + container log), and only then
# scale back to 1. While both instances are healthy ml-sync lists both upstream
# members, and nginx passive checks (max_fails=2 fail_timeout=5s) bridge the
# ~30s window in which the old member disappears after the scale-down. Any
# failure before convergence rolls back to the previous digest automatically.
ess_rollout() {
  local digest="$1" previous_digest="${2:-}" attempt
  local before_ids new_id new_ip
  before_ids="$(in_service_instance_ids)"
  scale_group 2
  wait_healthy_instances 2
  new_id="$(comm -13 <(printf '%s\n' "$before_ids") <(in_service_instance_ids) | head -n1 || true)"
  if [[ -z "$new_id" ]]; then
    rollback_failed_rollout "" "$previous_digest"
    die "could not identify the instance created by scale-out; aborted before scale-down"
  fi
  new_ip="$(instance_private_ip "$new_id")"
  log "new instance $new_id at $new_ip; gating on application health before scale-down"
  if ! wait_app_ready "$new_id" "$new_ip"; then
    rollback_failed_rollout "$new_id" "$previous_digest"
    die "rollout aborted: new instance $new_id never became healthy"
  fi
  scale_group 1
  wait_healthy_instances 1
  if wait_verify_converged; then
    log "ess rollout to $digest complete"
    return 0
  fi
  # Verification failed after the scale-down: restore the previous image and
  # let ESS + ml-sync converge (brief interruption is unavoidable here, which
  # is exactly why the app gate runs before the scale-down).
  rollback_failed_rollout "$new_id" "$previous_digest"
  die "post-rollout verify failed after $ROLLOUT_VERIFY_ATTEMPTS attempts; rolled back to $previous_digest"
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
  deploy.sh deploy-release <tag>
  deploy.sh rollback <sha256:DIGEST>
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
      [[ $# -eq 2 ]] || die "deploy-release requires one version tag"
      local release_tag="$2"
      local release_digest previous_digest
      release_digest="$(resolve_ml_digest "$release_tag")"
      previous_digest="$(current_config_digest)"
      apply_ml_digest "$release_digest"
      ess_rollout "$release_digest" "$previous_digest"
      log "release $release_tag -> $release_digest deployed"
      ;;
    rollback)
      [[ $# -eq 2 ]] || die "rollback requires one image digest (sha256:...)"
      local rollback_digest="$2" rollback_previous
      rollback_previous="$(current_config_digest)"
      apply_ml_digest "$rollback_digest"
      ess_rollout "$rollback_digest" "$rollback_previous"
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
