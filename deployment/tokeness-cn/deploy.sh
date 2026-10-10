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
# The master (NODE_TYPE=master: the panel source, the only node running DB
# migrations and the system tasks, and the /v1 last resort) is the
# new-api-master docker container on the backup entry ECS since 2026-09-29. The
# deploy pipeline keeps it on the deployed digest: before every ESS
# rollout/rollback, deploy.sh re-rolls that container (master-first) and the
# ESS rollout only starts once the master is verified healthy on the new image.
# Since 2026-09-29 the re-roll is BLUE-GREEN (start green on the other port of
# the 3000/3001 pair, gate it, pin both lightweight hosts' nginx onto it, then
# retire blue): the panel no longer sees the old recreate window, at the cost
# of a minutes-long double-master overlap the user explicitly authorized
# (migrations still never run twice at once - they fire at container start).
# It replaced the SWAS-2 host container, which no longer exists.
# SWAS-2 still runs ml-sync and nginx, so the drain marker must be written
# there too; it no longer carries any New API container.
readonly SWAS2_HOST="${SWAS2_HOST:-101.133.234.135}"
readonly SWAS2_SSH_KEY_PATH="${SWAS2_SSH_KEY_PATH:-$SWAS_SSH_KEY_PATH}"
readonly SWAS2_SSH_KNOWN_HOSTS="${SWAS2_SSH_KNOWN_HOSTS:-}"
# The master bootstrap lives in the repository (not private/) so the CNB release
# pipeline can run master-first sync without a local checkout; it carries no
# secrets (it copies the env off the running container) and needs no registry
# credential (the CNB registry serves this image anonymously).
# HOST_BOOTSTRAP_SCRIPT still overrides it for local experiments.
readonly HOST_BOOTSTRAP_SCRIPT="${HOST_BOOTSTRAP_SCRIPT:-$SCRIPT_DIR/bootstrap-master-ecs.sh}"
readonly MASTER_HOST="${MASTER_HOST:-8.133.244.241}"
readonly MASTER_SSH_KEY_PATH="${MASTER_SSH_KEY_PATH:-$SWAS_SSH_KEY_PATH}"
# Inherits the SWAS known-hosts file on purpose: the pipeline materializes one
# pinned file (CNB_SWAS_KNOWN_HOSTS_B64) and remote_cmd_on hardcodes
# StrictHostKeyChecking=yes, so an empty default here would silently fall back
# to the CI user's own known_hosts and abort the master roll. That one file
# therefore has to cover the backup ECS as well as both lightweight hosts.
readonly MASTER_SSH_KNOWN_HOSTS="${MASTER_SSH_KNOWN_HOSTS:-$SWAS_SSH_KNOWN_HOSTS}"
# Master status probes run on the master host itself, against the currently
# serving port of the 3000/3001 pair (WEB_PRIMARY_HOST:PORT; see the blue-green
# section below). 127.0.0.1 would not do: the container publishes on the
# private address only, and nginx on that host owns :80.
readonly EDGEONE_TEST_URL="${EDGEONE_TEST_URL:-https://tokeness.cn/api/status}"
# Direct probe defaults to the plaintext upstream for a Host-pinned request.
# Override DIRECT_PROBE_URL / DIRECT_PROBE_INSECURE when the upstream serves HTTPS.
readonly DIRECT_PROBE_URL="${DIRECT_PROBE_URL:-http://127.0.0.1/health/ready}"
readonly DIRECT_PROBE_INSECURE="${DIRECT_PROBE_INSECURE:-0}"
readonly VERIFY_TIMEOUT_SECONDS="${VERIFY_TIMEOUT_SECONDS:-45}"
readonly ROLLOUT_VERIFY_ATTEMPTS="${ROLLOUT_VERIFY_ATTEMPTS:-6}"
readonly ROLLOUT_VERIFY_DELAY_SECONDS="${ROLLOUT_VERIFY_DELAY_SECONDS:-10}"
# CNB kills a running stage after ten minutes with no output. Long drain windows
# therefore emit a bounded heartbeat while preserving the full drain duration.
readonly ROLLOUT_HEARTBEAT_SECONDS="${ROLLOUT_HEARTBEAT_SECONDS:-30}"

# Drain-first rollout. Before the group scales back, both lightweight hosts
# are told to serve the NEW instance only, and the rollout then waits longer
# than the slowest request measured in production, so the retiring instance has
# nothing in flight when ESS removes it. Without this the platform SIGKILLs
# those requests mid-answer.
#
# The wait was 600s, sized against 2026-09-26 measurements (p95 181s, max 461s).
# 2026-09-30 measurements make that obsolete: the non-stream path has a hard
# wall at 599s (EdgeOne's inter-byte idle timeout) and streams reached 1807s.
# 600s would therefore cut in-flight customer requests on every release, so the
# default is now 1900s (> 1807s + margin) and the marker TTL outlives it.
# NOTE: this makes every release's relay rollout ~35 minutes longer by design -
# that is the price of not cutting a 30-minute request, not an oversight.
readonly DRAIN_MARKER_PATH="${DRAIN_MARKER_PATH:-/etc/ml-sync/drain-target}"
readonly ML_DRAIN_SECONDS="${ML_DRAIN_SECONDS:-1900}"
# Poll interval for the instance-count gates. Production keeps 20s; the tests
# shorten it because the fake ESS emulates an asynchronous scale-in window.
readonly HEALTH_POLL_SECONDS="${HEALTH_POLL_SECONDS:-20}"
# The tier carries a scale-in alarm (cpu-in-25: CPU <= 25% for 30 minutes, minus
# one instance, floored by MinSize) that trims capacity a release leaves behind.
# It must not run WHILE a release is rolling: the rollout deliberately grows the
# tier and then steps it back down, and the alarm would retire instances out from
# under the round. Suspended for the duration of every mutating verb, resumed on
# exit whatever happens. SCALE_IN_ALARM_ID pins the task id; otherwise it is
# looked up by name (an empty result just logs a warning - a missing alarm must
# never block a release).
readonly SCALE_IN_ALARM_NAME="${SCALE_IN_ALARM_NAME:-cpu-in-25}"
# The entry's ecs-shrink-guard performs graceful scale-ins (it parks the victim
# in the relay drain file, waits for in-flight streams, then shrinks). It must
# stand down for the duration of a release, which grows and steps the tier down
# on its own schedule; this marker is how it is told.
readonly SHRINK_HOLD_PATH="${SHRINK_HOLD_PATH:-/etc/ml-sync/shrink-hold}"
# The relay drain file: ecs-fleet-sync subtracts every IP listed here from the
# upstream list, so those members stop receiving NEW requests while their in-flight
# streams finish. ecs-shrink-guard uses it for its graceful scale-in; a release
# uses the same file for the instances ESS is about to retire.
readonly SHRINK_DRAIN_PATH="${SHRINK_DRAIN_PATH:-/etc/ml-sync/shrink-drain}"
# The drain marker is LEASED and OWNED (v2 format):
#   line 1: the writer's identity - only that writer may clear the marker;
#   line 2: an expiry epoch - a writer killed with SIGKILL never runs its EXIT
#           trap, so the consumer (ecs-fleet-sync) ignores the whole marker once
#           the lease has lapsed instead of keeping a healthy member out of the
#           relay forever, and any writer may collect an expired one;
#   line 3+: one drained IP per line.
# A live release refreshes the lease from its heartbeat, so the lease only has
# to outlive one heartbeat gap, not the whole drain window; the margin below
# covers the drain hold plus the removal poll for writers that cannot refresh.
# The old format (plain IP lines) is still cleared by this release: the v2
# writers replaced every producer, so a plain-IP marker can only be a leftover.
readonly SHRINK_DRAIN_LEASE_SECONDS="${SHRINK_DRAIN_LEASE_SECONDS:-$(( ML_DRAIN_SECONDS + 900 ))}"
# Every remote write to the shrink hold or the drain marker takes THIS lock on
# the entry host, and the shrink guard (running on that same host) takes it for
# its marker operations too. A lock file is the only mutual exclusion that
# actually holds across processes: an atomic write alone is not check-and-set,
# so two releases could both observe "no hold", both write, and both read back
# their own owner (the loser's write simply happened last).
readonly MARKER_LOCK_PATH="${MARKER_LOCK_PATH:-/var/lock/ml-sync-marker.lock}"
SCALE_IN_ALARM_ID="${SCALE_IN_ALARM_ID:-}"
# Set while the alarm is suspended, so the exit trap knows it has work to do.
scale_in_guard_suspended=""
# Set once the release's hold is on the entry, so the heartbeat keeps it fresh
# even when the alarm half of the suspension never engaged.
scale_in_hold_written=""
scale_in_alarm_was_enabled=""
readonly ML_DRAIN_CONVERGE_ATTEMPTS="${ML_DRAIN_CONVERGE_ATTEMPTS:-12}"
readonly ML_DRAIN_CONVERGE_DELAY_SECONDS="${ML_DRAIN_CONVERGE_DELAY_SECONDS:-15}"
# Must outlive convergence plus the drain wait, or ml-sync would drop the pin
# mid-rollout. The margin also covers a rollout that dies before clearing it.
readonly ML_DRAIN_MARKER_TTL_SECONDS="${ML_DRAIN_MARKER_TTL_SECONDS:-3600}"

# Blue-green master (2026-09-29, authorized multi-master overlap): the master
# container is re-rolled by starting a replacement on the OTHER port of the
# 3000/3001 pair and only then pointing the panel tier at it, so the panel
# never sees the old container's recreate window. This marker tells ml-sync
# which port currently serves the panel primary; it is persistent state (not
# a TTL pin) and ml-sync adopts it only while the pinned port answers
# /health/ready, so a dead pin can never be written into nginx.
readonly WEB_PRIMARY_MARKER_PATH="${WEB_PRIMARY_MARKER_PATH:-/etc/ml-sync/web-primary-port}"
# Private address the panel tier uses for the master. MASTER_HOST is that same
# host's public IP for SSH (the 2026-10-09 entry migration moved both roles onto
# the 8x8 ECS in tokeness-ml-vpc, where the master, nginx, the fleet list sync
# and the RDS all sit in one VPC).
readonly WEB_PRIMARY_HOST="${WEB_PRIMARY_HOST:-10.0.0.249}"
# The lightweight panel tier was retired on 2026-10-05 (user decision): the
# panel domain's EdgeOne origin is this ECS, and the two Chengdu hosts no longer
# run nginx or ml-sync. At the default the master blue-green relies on the
# bootstrap's commit to flip the ECS's own nginx and verifies the result through
# the bootstrap's probe, and the relay rollout waits on the ECS's upstream file
# instead of pinning a remote host. SWAS_PANEL_TIER=1 restores the retired path,
# which is kept — and still covered by tests — because that path's ml-sync was
# stopped rather than deleted, so it is one systemctl away from mattering again.
readonly SWAS_PANEL_TIER="${SWAS_PANEL_TIER:-0}"
# The ECS relay upstream file, written by ecs-fleet-sync from the local ESS view.
readonly ECS_UPSTREAM_CONF="${ECS_UPSTREAM_CONF:-/etc/nginx/fleet/newapi_ml_servers.conf}"
# Set by release-on-master.sh when the whole script runs on the entry ECS.
readonly MASTER_LOCAL="${MASTER_LOCAL:-0}"
readonly WEB_PRIMARY_CONVERGE_ATTEMPTS="${WEB_PRIMARY_CONVERGE_ATTEMPTS:-12}"
readonly WEB_PRIMARY_CONVERGE_DELAY_SECONDS="${WEB_PRIMARY_CONVERGE_DELAY_SECONDS:-15}"

# Container shutdown budget. The application drains in-flight requests for
# SHUTDOWN_TIMEOUT_SECONDS and then flushes background batches (log, quota,
# observability), whose own default timeout is 30s. The platform grace period
# must therefore exceed the sum, or the SIGKILL lands in the middle of the
# flush and the last log/quota window is lost. TerminationGracePeriodSeconds is
# applied by config_args.py in target mode only, so a rollback restores the
# previous snapshot verbatim, exactly as it does for probes.
readonly ECI_TERMINATION_GRACE_SECONDS="${ECI_TERMINATION_GRACE_SECONDS:-240}"
readonly APP_SHUTDOWN_TIMEOUT_SECONDS="${APP_SHUTDOWN_TIMEOUT_SECONDS:-180}"
readonly APP_BACKGROUND_DRAIN_ALLOWANCE_SECONDS="${APP_BACKGROUND_DRAIN_ALLOWANCE_SECONDS:-30}"

readonly REMOTE_RUN_DIR='/run/lock'
readonly REMOTE_LOCK_NAME='tokeness-cn-deploy.lock'

# Application-level rollout gate: ESS stayed "Healthy" through the 2026-09-06
# crash-loop, so the rollout waits for the dependency-aware readiness endpoint.
readonly APP_READY_TIMEOUT_SECONDS="${APP_READY_TIMEOUT_SECONDS:-300}"
readonly APP_READY_POLL_SECONDS="${APP_READY_POLL_SECONDS:-15}"
readonly APP_CONTAINER_NAME="${APP_CONTAINER_NAME:-newapi}"

# Throttled heartbeat for the release's shrink-guard hold, called from log().
# Every long wait in this script polls and logs, so this covers the waits
# sleep_with_heartbeat never sees (SSH reads, image pulls, the blue-green roll).
# Without it the hold goes stale during any step longer than the freshness window,
# which both loses the guard's protection and admits a second release.
hold_heartbeat() {
  # Refresh as soon as the hold exists - not only once the alarm was suspended:
  # a group with no scale-in alarm returns early from suspend_scale_in_guard,
  # and a release that then never heartbeats looks abandoned to the next one
  # (the hold goes stale after RELEASE_HOLD_FRESH_SECONDS while the rollout is
  # still rolling).
  [[ -n "$scale_in_guard_suspended" || -n "$scale_in_hold_written" ]] || return 0
  local _now; _now="$(date +%s)"
  (( _now - ${scale_in_hold_refresh:-0} >= 60 )) || return 0
  scale_in_hold_refresh="$_now"
  local holdrc=0
  shrink_hold_write set >/dev/null 2>&1 || holdrc=$?
  if (( holdrc == 3 )); then
    # Losing the lock mid-rollout means another release is now driving the same
    # scaling config and green container: the two must not interleave, so this
    # one aborts (its EXIT trap still runs).
    die "another release took the shrink guard hold mid-rollout; aborting so the two cannot interleave"
  elif (( holdrc != 0 )); then
    warn "could not refresh the shrink guard hold (rc=$holdrc)"
  fi
  # Extend our drain marker's lease in the same beat: a marker whose writer was
  # SIGKILLed must lapse, but one whose writer is alive must not.
  relay_drain_refresh || warn "our drain marker is gone or no longer ours; the retiring instance may be back in rotation"
}

log() { printf '[%s] %s\n' "$(date --iso-8601=seconds)" "$*"; hold_heartbeat; }
warn() { log "WARN: $*"; }
error() { log "ERROR: $*" >&2; }
die() { error "$*"; exit 1; }

sleep_with_heartbeat() {
  local seconds="$1" interval chunk elapsed=0
  [[ "$seconds" =~ ^[0-9]+$ ]] || die "sleep duration is not an integer: $seconds"
  interval="$ROLLOUT_HEARTBEAT_SECONDS"
  [[ "$interval" =~ ^[1-9][0-9]*$ ]] || die "ROLLOUT_HEARTBEAT_SECONDS must be a positive integer"
  while (( elapsed < seconds )); do
    chunk=$(( seconds - elapsed < interval ? seconds - elapsed : interval ))
    sleep "$chunk"
    elapsed=$(( elapsed + chunk ))
    if (( elapsed < seconds )); then
      log "rollout drain still active (${elapsed}/${seconds}s)"
      # Heartbeat the hold as well. The guard treats an old hold as abandoned (a
      # SIGKILLed release never runs its EXIT trap), so a release that runs longer
      # than that threshold has to keep proving it is alive - otherwise the guard
      # would resume shrinking while the rollout is still draining.
      local hbrc=0
      shrink_hold_write set >/dev/null 2>&1 || hbrc=$?
      if (( hbrc == 3 )); then
        die "another release took the shrink guard hold mid-drain; aborting so the two cannot interleave"
      elif (( hbrc != 0 )); then
        warn "could not refresh the shrink guard hold (rc=$hbrc)"
      fi
    fi
  done
}

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
  # Local mode: this script is running ON the master itself (release-on-master.sh),
  # and that host has no ssh key for itself. Everything it would fetch over ssh -
  # the relay include, the drain marker, the master container - is local there, so
  # run the payload here. Placed before the key check on purpose: no key exists.
  # Local execution is only correct for the master itself. remote_cmd_on is also
  # called with the SWAS hosts (nginx updates, drain and web-primary markers,
  # upstream reads); running those here would edit the master while reporting
  # success, so anything that is not MASTER_HOST keeps going over ssh (and fails
  # closed there, because this host has no keys for them).
  if [[ "$MASTER_LOCAL" == "1" && "$host" == "$MASTER_HOST" ]]; then
    bash -s -- "$@"
    return $?
  fi
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
  get_upstream_ip_on "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS"
}

# get_upstream_ip_on <host> <key> <known-hosts> - the active member of the
# managed upstream as seen by ONE lightweight host. Drain-first needs both
# hosts individually: they are equal-weight EdgeOne origins, so a pin that
# reached only one of them would still send half the traffic to the retiring
# instance.
get_upstream_ip_on() {
  local host="$1" key="$2" known="$3"
  local output addresses=()
  output="$(remote_cmd_on "$host" "$key" "$known" "$NGINX_CONF" "$NGINX_UPSTREAM_NAME" "$NGINX_UPSTREAM_PORT" <<'REMOTE_AWK'
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
  local -a probe_target
  local probe_label members
  if ! public_body="$(curl -fsS --connect-timeout 15 --max-time "$VERIFY_TIMEOUT_SECONDS" "$EDGEONE_TEST_URL")"; then
    error "EdgeOne public chain failed: $EDGEONE_TEST_URL"
    return 1
  fi
  validate_status_body "EdgeOne public chain" "$public_body" || return 1
  log "EdgeOne public chain is healthy"

  # SWAS_PANEL_TIER=1 (retired path): the relay entry is the two lightweight
  # hosts, so read their nginx and probe the private chain from there. Default:
  # the entry is this ECS — read its upstream file and probe from it, which is
  # the same hop the public chain takes, minus EdgeOne.
  if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
    upstream_ip="$(get_upstream_ip)" || return 1
    log "lightweight nginx upstream is $upstream_ip:$NGINX_UPSTREAM_PORT"
    probe_target=("$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS")
    probe_label="lightweight server to ECI private chain"
  else
    members="$(ecs_upstream_members)" \
      || { error "could not read the ECS relay upstream members from $MASTER_HOST:$ECS_UPSTREAM_CONF"; return 1; }
    upstream_ip="$(head -n1 <<<"$members")"
    [[ -n "$upstream_ip" ]] \
      || { error "the ECS relay upstream file names no member ($ECS_UPSTREAM_CONF)"; return 1; }
    log "ECS relay upstream is $(tr '\n' ' ' <<<"$members")(primary $upstream_ip:$NGINX_UPSTREAM_PORT)"
    probe_target=("$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS")
    probe_label="ECS to ECI private chain"
  fi

  if ! direct_body="$(remote_cmd_on "${probe_target[@]}" "$DIRECT_PROBE_URL" "$VERIFY_TIMEOUT_SECONDS" "$DIRECT_PROBE_INSECURE" <<'REMOTE_PROBE'
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
    error "$probe_label failed"
    return 1
  fi
  validate_status_body "$probe_label" "$direct_body" || return 1
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

# all_instance_ids - every id the group still reports, in ANY lifecycle state.
# A retired instance sits in Removing/Removing:Wait for a while before it is
# gone for good, and a gate that only watches InService passes while the
# removal is still in flight - the next round's scale-out then supersedes it.
all_instance_ids() {
  scaling_instances_json | jq -r '
    .ScalingInstances.ScalingInstance[]?
    | .InstanceId' | tr -d '\r' | sort -u
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
  aliyun_cmd eci DescribeContainerGroups --region "$ALIYUN_REGION" \
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
  eip_json="$(aliyun_cmd vpc DescribeEipAddresses --region "$ALIYUN_REGION" --EipAddress "$eip" | tr -d '\r')" \
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
  if ! aliyun_cmd vpc AddCommonBandwidthPackageIp --region "$ALIYUN_REGION" \
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
  # Probe from the ECS, which is the relay entry and reaches the ECI private
  # addresses directly — that is exactly what its nginx upstreams do. The
  # retired lightweight hosts used to be this probe's origin (SWAS_PANEL_TIER=1
  # restores them for a fleet that still has one), and with them gone this gate
  # timed out for its whole readiness window and rolled back a healthy instance:
  # the release that hit it was the first rollout after the hosts were deleted,
  # because .29's own rollout had finished before that.
  local -a probe_origin=("$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS")
  if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
    probe_origin=("$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS")
  fi
  body="$(remote_cmd_on "${probe_origin[@]}" "$ip" <<'REMOTE_PROBE'
#!/usr/bin/env bash
set -Eeuo pipefail
curl -fsS --connect-timeout 5 --max-time 10 "http://$1:3000/health/ready"
REMOTE_PROBE
)" || return 1
  jq -e '.success == true' >/dev/null 2>&1 <<<"$body"
}

# app_version_on <ip> - the version the app on that instance reports, probed
# from the relay entry (the same origin app_status_ok uses). Used by the batch
# rollout to report what the fleet actually converged onto: instance identity
# proves an instance was replaced, only the version proves it serves the image
# the release published.
app_version_on() {
  local ip="$1"
  is_valid_ipv4 "$ip" || return 1
  remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$ip" <<'REMOTE_VERSION' 2>/dev/null || return 1
#!/usr/bin/env bash
set -Eeuo pipefail
curl -fsS --connect-timeout 5 --max-time 10 "http://$1:3000/api/status" | jq -r '.data.version // empty'
REMOTE_VERSION
}

# Print the first fatal startup line in the container log, if any. Rate-limit
# parse warnings are non-fatal (the app continues with defaults) and must not
# match; only [FATAL] / Go panics abort the rollout early.
container_log_fatal_line() {
  local instance_id="$1"
  aliyun_cmd eci DescribeContainerLog --region "$ALIYUN_REGION" \
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
# drain_before_scale_in <out_var> <target> - park the instances a scale-in to
# <target> would remove, wait out the drain window, and leave the retire set in
# <out_var> for the caller's wait_retired. Shared by the rollback path (#10):
# a failed release shrinks the tier back, and ESS removes the OLDEST instances
# to get there - which are the healthy members still serving customers, not
# the failed new one that was just deleted. Removing them without parking cuts
# live streams on a release whose own gate already failed. Fail closed at
# every step: a rollback that cannot park simply fails, it never retires an
# unparked instance. The retire set is written through a variable because this
# runs outside command substitution, and its log() lines must stay on real
# stdout.
drain_before_scale_in() {
  local out_var="$1" target_var="$2" before_var="$3" target_hint="$4"
  local now_ids now_count remove_count retire_ids drain_ips id ip drain_comma live
  printf -v "$out_var" '%s' ""
  printf -v "$target_var" '%s' "$target_hint"
  if ! now_ids="$(in_service_instance_ids)"; then
    error "could not read the in-service set before a scale-in; refusing to shrink without one"
    return 1
  fi
  now_count=$(printf '%s\n' "$now_ids" | grep -c . || true)
  printf -v "$before_var" '%s' "$now_ids"
  remove_count=$(( now_count > target_hint ? now_count - target_hint : 0 ))
  if (( remove_count == 0 )); then
    return 0
  fi
  retire_ids="$(oldest_instance_ids "$remove_count" || true)"
  if [[ -z "$retire_ids" ]]; then
    error "could not resolve the $remove_count instance(s) a scale-in to $target would remove; refusing to retire unparked instances"
    return 1
  fi
  drain_ips=""
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    if ! ip="$(instance_private_ip "$id")" || ! is_valid_ipv4 "$ip"; then
      error "could not resolve a valid private IP for retiree $id; refusing to retire an unparked instance"
      return 1
    fi
    drain_ips+="${drain_ips:+$'\n'}$ip"
  done <<<"$retire_ids"
  drain_comma="$(printf '%s\n' "$drain_ips" | paste -sd, -)"
  if ! relay_drain_write "$drain_comma"; then
    error "could not write the relay drain marker before a scale-in; refusing to retire unparked instances"
    return 1
  fi
  if ! wait_drain_applied "$drain_comma"; then
    relay_drain_clear >/dev/null 2>&1 || true
    return 1
  fi
  log "drain-first: parked the instance(s) a scale-in toward $target_hint would remove: $(printf '%s' "$drain_ips" | tr '\n' ' ')"
  log "drain-first: holding ${ML_DRAIN_SECONDS}s so in-flight streams on the retiring instances finish"
  sleep_with_heartbeat "$ML_DRAIN_SECONDS"
  # Recompute from the LIVE set after the hold (#5): capacity that joined during
  # the window must survive, and the removal must stay exactly the parked set.
  # Scaling straight to the pre-hold target removed (current - target) members -
  # more than were parked whenever an external scale-out raised the count.
  if ! now_ids="$(in_service_instance_ids)"; then
    error "could not re-read the in-service set after the drain hold; refusing to shrink"
    return 1
  fi
  now_count=$(printf '%s\n' "$now_ids" | grep -c . || true)
  live=0
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    printf '%s\n' "$now_ids" | grep -qxF "$id" && live=$(( live + 1 ))
  done <<<"$retire_ids"
  printf -v "$target_var" '%s' "$(( now_count - live < target_hint ? target_hint : now_count - live ))"
  printf -v "$out_var" '%s' "$retire_ids"
  return 0
}

rollback_failed_rollout() {
  local failed_id="$1" previous_digest="$2" previous_snapshot="${3:-}"
  warn "rolling back to previous digest: ${previous_digest:-<unchanged>}"
  # Release the drain pin first: a rollback deletes the very instance the pin
  # names, and leaving it in place would keep the restored instance out of
  # rotation until the marker expired.
  if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
    ml_drain_end || warn "drain marker could not be cleared on every host during rollback"
  fi

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
    if ! aliyun_cmd eci DeleteContainerGroup --region "$ALIYUN_REGION" --ContainerGroupId "$failed_id" >/dev/null; then
      warn "could not delete failed container group $failed_id; ESS may recreate it with the pinned digest"
    fi
  fi
  # Restore the capacity the site actually runs with: a rollout that grew the
  # tier must hand it back, and a rollback outside a rollout keeps the floor of
  # one instance it has always used.
  local stable="${ROLLOUT_STABLE_CAPACITY:-1}"
  # Shrink through the same parked-set protocol (#10): scaling straight back to
  # the steady state removes the OLDEST instances, which are the healthy members
  # still serving traffic. Park them first so the rollback does not cut the
  # streams the release's own gates were protecting.
  local rollback_retire_ids="" rollback_target="$stable" rollback_before_ids=""
  if (( RETIREMENT_PENDING == 1 )); then
    # A retire set is still mid-flight from the failed round: re-parking now
    # would overwrite the marker protecting THAT removal, and shrinking again
    # stacks a second removal on top of an unsettled one. Leave the capacity
    # alone - the in-flight removal settles on its own, and the marker's lease
    # lapses once this process exits.
    warn "rollback skipped its capacity restore: a retire set from the failed round is still leaving the group"
  elif ! drain_before_scale_in rollback_retire_ids rollback_target rollback_before_ids "$stable"; then
    error "rollback could not park the instances its scale-in would remove; tier left at current capacity"
    return 1
  fi
  if (( RETIREMENT_PENDING != 1 )); then
    if ! scale_group "$rollback_target"; then
      error "rollback could not restore desired capacity (target $rollback_target)"
      return 1
    fi
    if [[ -n "$rollback_retire_ids" ]] && ! wait_retired "$rollback_retire_ids"; then
      error "rollback's retire set did not leave the group"
      return 1
    fi
    if [[ -n "$rollback_retire_ids" ]] \
       && ! verify_no_extra_removals "$rollback_before_ids" "$rollback_retire_ids" "rollback"; then
      error "rollback removed instances outside its parked set"
      return 1
    fi
  fi
  if ! wait_healthy_instances "$stable"; then
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
  local -a grace=()
  # restore mode must stay byte-faithful to the snapshot it was given.
  [[ "$mode" == target ]] && grace=(--termination-grace-seconds "$ECI_TERMINATION_GRACE_SECONDS")
  local -a emitted=()
  if ! printf '%s' "$snapshot" | python3 "$CONFIG_SERIALIZER" \
    --mode "$mode" --digest "$digest" --target-name "$APP_CONTAINER_NAME" "${grace[@]}" --emit \
    >/dev/null; then
    return 1
  fi
  # The validation pass above keeps this process substitution deterministic;
  # its output stays inside Bash and is never written to deploy logs.
  mapfile -d '' -t emitted < <(
    printf '%s' "$snapshot" | python3 "$CONFIG_SERIALIZER" \
      --mode "$mode" --digest "$digest" --target-name "$APP_CONTAINER_NAME" "${grace[@]}" --emit
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
  local -a grace=()
  [[ "$mode" == target ]] && grace=(--termination-grace-seconds "$ECI_TERMINATION_GRACE_SECONDS")
  printf '%s\n%s\n' "$expected" "$actual" | python3 "$CONFIG_SERIALIZER" \
    --mode "$mode" --digest "$digest" --target-name "$APP_CONTAINER_NAME" "${grace[@]}" \
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
# The ECI tier is a slave: migrations + system tasks belong to the master, which
# is the new-api-master container on the backup entry ECS (NODE_TYPE=master) and
# is rolled by sync_master_container / bootstrap-master-ecs.sh.
# Values overridable via ECI_NODE_NAME / ECI_NODE_TYPE.
inject_node_identity_envs() {
  local snapshot="$1"
  require_command python3
  printf '%s' "$snapshot" | ECI_NODE_NAME="${ECI_NODE_NAME:-new-api-ml000}" \
    ECI_NODE_TYPE="${ECI_NODE_TYPE:-slave}" \
    ECI_SHUTDOWN_TIMEOUT_SECONDS="${ECI_SHUTDOWN_TIMEOUT_SECONDS:-$APP_SHUTDOWN_TIMEOUT_SECONDS}" python3 -c '
import json, os, sys

snapshot = json.loads(sys.stdin.read())
name = os.environ["ECI_NODE_NAME"]
node_type = os.environ["ECI_NODE_TYPE"]
shutdown = os.environ["ECI_SHUTDOWN_TIMEOUT_SECONDS"]
if node_type not in ("master", "slave"):
    sys.exit("ECI_NODE_TYPE must be master or slave")
if not shutdown.isdigit() or int(shutdown) <= 0:
    sys.exit("ECI_SHUTDOWN_TIMEOUT_SECONDS must be a positive integer")

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
        elif key == "SHUTDOWN_TIMEOUT_SECONDS":
            # Drain budget of the application container. It must stay strictly
            # inside the platform grace period; validate_shutdown_budget
            # enforces that, and the readback check then proves the value
            # reached the configuration.
            out.append({"Key": "SHUTDOWN_TIMEOUT_SECONDS", "Value": shutdown}); seen.add("SHUTDOWN_TIMEOUT_SECONDS")
        else:
            out.append(entry)
    if "NODE_NAME" not in seen:
        out.append({"Key": "NODE_NAME", "Value": name})
    if "NODE_TYPE" not in seen:
        out.append({"Key": "NODE_TYPE", "Value": node_type})
    if "SHUTDOWN_TIMEOUT_SECONDS" not in seen:
        out.append({"Key": "SHUTDOWN_TIMEOUT_SECONDS", "Value": shutdown})
    return out

for container in config.get("Containers") or []:
    if container.get("Name") == "newapi":
        container["EnvironmentVars"] = inject(container.get("EnvironmentVars") or [])
print(json.dumps(snapshot, ensure_ascii=False))
'
}

# sync_master_container <expected_version> [digest] - blue-green re-roll of the
# master container (new-api-master on the backup entry ECS) onto the deployed
# digest, so the panel never sees the old container's recreate window.
#
# Sequence (all phases idempotent, every failure path fails closed):
#   1. start   - green container comes up on the free port of the 3000/3001
#                pair, health-gated. Blue keeps serving; nothing off-ECS moves.
#   2. gate    - green's /api/status version must match the release (canary).
#   3. pin     - both lightweight hosts get the web-primary marker for the
#                green port; ml-sync (30s cron, health-gated) rewrites their
#                nginx primary. Only when BOTH confs carry the green port is
#                the panel considered switched.
#   4. commit  - the ECS flips its own :80 last-resort upstream + serving-port
#                marker to green, drains blue briefly, retires and renames.
#
# Any failure before commit: master_sync_abort() reconciles (blue kept, green
# removed, pins restored). A failure DURING/AFTER commit never points anything
# at a retired blue: abort finalizes green instead (the bootstrap decides from
# live docker state, not from how far we think we got).
#
# master-first ordering vs the ESS rollout is unchanged: this whole function
# completes before the ECI tier moves. An empty digest argument means
# "whatever the scaling configuration now says" (recovery paths).
sync_master_container() {
  local expected_version="$1" digest="${2:-}" image_ref out green_port old_port master_version
  if [[ -z "$digest" ]]; then
    digest="$(snapshot_config_digest "$(oss_scaling_config_json)")" \
      || { error "could not read the current image digest from the scaling configuration"; return 1; }
  fi
  if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    error "master sync needs a sha256:<64 hex> digest, got '$digest'"
    return 1
  fi
  image_ref="$CNB_IMAGE_REPOSITORY@$digest"
  [[ -r "$HOST_BOOTSTRAP_SCRIPT" ]] \
    || die "master sync impossible: bootstrap script not readable at $HOST_BOOTSTRAP_SCRIPT"

  log "blue-green master roll ($MASTER_HOST): starting green onto $image_ref"
  if ! out="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$image_ref" start \
      < "$HOST_BOOTSTRAP_SCRIPT")"; then
    error "green master failed to start or pass readiness; the serving master was never touched"
    return 1
  fi
  green_port="$(sed -n 's/^SERVING_PORT=//p' <<<"$out" | tail -n1)"
  old_port="$(sed -n 's/^PREVIOUS_PORT=//p' <<<"$out" | tail -n1)"
  if [[ ! "$green_port" =~ ^[0-9]+$ || ! "$old_port" =~ ^[0-9]+$ || "$green_port" == "$old_port" ]]; then
    error "green bootstrap reported invalid ports (green='$green_port' previous='$old_port'); reconciling"
    master_sync_abort "$image_ref" || true
    return 1
  fi
  log "green master ready on :$green_port (blue still serving the panel on :$old_port)"

  # Version gate on the green port, before any traffic moves (release canary).
  master_version="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" \
    <<REMOTE_MASTER_VER
curl -fsS --max-time 10 "http://$WEB_PRIMARY_HOST:${green_port}/api/status" | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["version"])'
REMOTE_MASTER_VER
  )" || { error "could not read the green master version on :$green_port"; master_sync_abort "$image_ref" || true; return 1; }
  if [[ -n "$expected_version" && "$master_version" != "$expected_version" ]]; then
    error "green master version ($master_version) does not match the deployed release ($expected_version)"
    master_sync_abort "$image_ref" || true
    return 1
  fi
  log "green master version verified: $master_version"

  # Move the panel entry off blue and onto green, then prove it.
  #
  # With the lightweight tier retired (SWAS_PANEL_TIER=0, the default) the panel
  # entry *is* this ECS, and the bootstrap's commit is what flips its nginx; the
  # gate that matters is therefore the post-commit probe below, which reads the
  # live serving port rather than a marker another host has to converge on.
  # With SWAS_PANEL_TIER=1 the retired path runs unchanged: pin both hosts onto
  # the green port and wait for their ml-sync to rewrite nginx, aborting while
  # blue still serves if either host refuses.
  if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
    web_primary_pin "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS" "$green_port" \
      || { error "could not pin the panel primary on $SWAS_HOST"; master_sync_abort "$image_ref" || true; return 1; }
    web_primary_pin "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" "$green_port" \
      || { error "could not pin the panel primary on $SWAS2_HOST"; master_sync_abort "$image_ref" || true; return 1; }
    if ! wait_web_primary_converged "$green_port"; then
      master_sync_abort "$image_ref" || true
      return 1
    fi
  else
    log "blue-green: panel tier is this ECS (lightweight hosts retired); the commit flips its nginx"
  fi

  # Green is reachable and correct; commit on the ECS: :80 last-resort follows,
  # blue drains briefly and is retired, green takes the canonical name.
  if ! out="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$image_ref" commit \
      < "$HOST_BOOTSTRAP_SCRIPT")"; then
    error "blue-green commit failed; reconciling via abort"
    master_sync_abort "$image_ref" || true
    return 1
  fi

  # Post-commit gate. The commit is the point of no return for blue, so the
  # check must read live state rather than our intent: ask the bootstrap which
  # port the panel is actually served from. The marker the commit writes is that
  # answer, and it must name green — anything else means the flip did not land
  # and the abort phase has to reconcile. BLUE_OK is deliberately not consulted:
  # the bootstrap names the *serving* port's container "blue", so it is 1 both
  # before the commit and after it (there, the renamed green).
  if ! out="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$image_ref" probe \
      < "$HOST_BOOTSTRAP_SCRIPT" 2>/dev/null)"; then
    error "post-commit: could not probe the ECS serving state; reconciling via abort"
    master_sync_abort "$image_ref" || true
    return 1
  fi
  local serving_port
  serving_port="$(sed -n 's/^SERVING_PORT=//p' <<<"$out" | tail -n1)"
  if [[ "$serving_port" != "$green_port" ]]; then
    error "post-commit: ECS serves :${serving_port:-?} (expected :$green_port); reconciling via abort"
    master_sync_abort "$image_ref" || true
    return 1
  fi
  log "master container blue-green complete: :$old_port -> :$green_port (version $master_version); ECS serves green, blue retired"
}

# master_sync_abort <image-ref> - reconcile after a failed master roll.
# The bootstrap's abort phase looks at live docker state: blue still alive
# means pre-commit, so it restores :80 and removes green; blue already gone
# means the commit passed the point of no return, so it finalizes green. The
# panel pin is then set to whichever port actually serves. Best effort: the
# caller has already failed - this leaves a consistent state, and any remnant
# damage is spelled out in the log for manual follow-up.
master_sync_abort() {
  local image_ref="$1" out probe_out serving blue_ok
  probe_out="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$image_ref" probe \
      < "$HOST_BOOTSTRAP_SCRIPT" 2>/dev/null)" || {
    warn "abort: could not even probe $MASTER_HOST; leaving pins untouched (green was healthy when pinned)"
    return 1
  }
  serving="$(sed -n 's/^SERVING_PORT=//p' <<<"$probe_out" | tail -n1)"
  blue_ok="$(sed -n 's/^BLUE_OK=//p' <<<"$probe_out" | tail -n1)"
  if [[ "$blue_ok" == "1" && "$serving" =~ ^[0-9]+$ ]]; then
    # Blue can serve: move the panel back to it FIRST (green is still up, so
    # nothing breaks during the re-pin), then let the ECS clean up green.
    log "abort: blue is alive on :$serving; restoring the panel tier before cleanup"
    if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
      web_primary_pin "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS" "$serving" \
        || warn "abort: could not re-pin $SWAS_HOST to :$serving"
      web_primary_pin "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" "$serving" \
        || warn "abort: could not re-pin $SWAS2_HOST to :$serving"
      wait_web_primary_converged "$serving" \
        || warn "abort: panel tier did not re-converge to :$serving (green remains up meanwhile)"
    else
      warn "abort: lightweight panel tier retired; the ECS nginx restore is the bootstrap's abort phase"
    fi
  fi
  out="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$image_ref" abort \
      < "$HOST_BOOTSTRAP_SCRIPT" 2>&1)" || {
    warn "abort phase failed on $MASTER_HOST; manual check required: $out"
    return 1
  }
  while IFS= read -r line; do log "abort| $line"; done <<<"$out"
  # If the abort had to finalize green (blue was already gone), make sure the
  # panel pin follows it - the normal pin already names the green port, so this
  # is only belt-and-braces for a pin that failed mid-flight.
  serving="$(sed -n 's/^SERVING_PORT=//p' <<<"$out" | tail -n1)"
  if [[ "$serving" =~ ^[0-9]+$ && "$blue_ok" != "1" && "$SWAS_PANEL_TIER" == "1" ]]; then
    web_primary_pin "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS" "$serving" \
      || warn "abort: could not pin $SWAS_HOST to finalized :$serving"
    web_primary_pin "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" "$serving" \
      || warn "abort: could not pin $SWAS2_HOST to finalized :$serving"
    wait_web_primary_converged "$serving" \
      || warn "abort: panel tier did not converge to finalized :$serving"
  fi
  return 0
}

scale_group() {
  local desired="$1"
  aliyun_cmd ess ModifyScalingGroup --ScalingGroupId "$SCALING_GROUP_ID" --DesiredCapacity "$desired" --region "$ALIYUN_REGION" >/dev/null \
    || { error "ModifyScalingGroup to desired=$desired failed"; return 1; }
  log "scaling group desired capacity set to $desired"
}

# scale_in_alarm_id - the AlarmTaskId of the tier's scale-in alarm, or empty.
scale_in_alarm_id() {
  if [[ -n "$SCALE_IN_ALARM_ID" ]]; then
    printf '%s\n' "$SCALE_IN_ALARM_ID"
    return 0
  fi
  # The response SHAPE is validated before the name select: `[]?` swallows a
  # malformed or error body into an empty result with exit 0, which read as
  # "no alarm exists" and let the release run while a live alarm could remove
  # instances mid-round. Only a well-formed AlarmList may yield "no match".
  local resp id
  if ! resp="$(aliyun_cmd ess DescribeAlarms --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION")"; then
    return 1
  fi
  if ! printf '%s' "$resp" | jq -e 'type == "object" and has("AlarmList") and (.AlarmList | type == "object" and has("Alarm") and (.Alarm | type == "array"))' >/dev/null; then
    return 1
  fi
  id="$(printf '%s' "$resp" | jq -r --arg name "$SCALE_IN_ALARM_NAME" '[.AlarmList.Alarm[]? | select(.Name == $name) | .AlarmTaskId][0] // empty' | tr -d '\r')"
  printf '%s\n' "$id"
}

# suspend_scale_in_guard / resume_scale_in_guard - hold the alarm off for the
# duration of a rollout. Both are best-effort: an ESS hiccup here must not abort
# a release, and a release that fails after suspending still resumes it via the
# caller's EXIT trap.
# oldest_instance_ids <n> - instance IDs of the n oldest in-service instances.
#
# ESS removes by its OldestInstance policy, so the pre-existing instances a
# round retires are the oldest ones - IF the group really removes that way.
# Three preconditions are verified, because each silently changes which
# instance ESS actually picks while we park a different one:
#   - the group's removal policy is exactly OldestInstance;
#   - every in-service instance sits in ONE zone (ESS rebalances zones before
#     applying the policy in a multi-zone group, so age order stops predicting
#     the victim);
#   - the sort key is CreatedTime, which carries seconds. CreationTime is
#     minute-precise, and a batch scale-out joins several instances within the
#     same minute; when the boundary of the requested set falls inside a tie
#     the choice is a guess, so that case is warned about, never silent.
# Exits 1 with a warn when a precondition fails: the caller must not park, and
# must not read the empty result as "nothing to retire".
oldest_instance_ids() {
  local n="$1" policies zones ids boundary
  [[ "$n" =~ ^[1-9][0-9]*$ ]] || return 0
  policies="$(aliyun_cmd ess DescribeScalingGroups --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION" \
    | jq -r '[.ScalingGroups.ScalingGroup[0].RemovalPolicies.RemovalPolicy[]?] | join(",")' | tr -d '\r')" || policies=""
  if [[ "$policies" != "OldestInstance" ]]; then
    # stderr on purpose: this function runs inside a command substitution, and
    # a stdout diagnostic would be captured into the caller's IP list.
    log "WARN: the scaling group's removal policy is '${policies:-unknown}', not exactly OldestInstance; cannot predict which instance a scale-in removes" >&2
    return 1
  fi
  # ONE verified snapshot serves zone, sort and tie: this function runs inside
  # $() with || true at the call site, so set -e cannot catch a failed query
  # here - an empty zone count or a swallowed tie check reads as "all clear".
  local snapshot
  if ! snapshot="$(scaling_instances_json)"; then
    log "WARN: could not read the instance list for victim selection" >&2
    return 1
  fi
  zones="$(printf '%s' "$snapshot" | jq -r '[.ScalingInstances.ScalingInstance[]? | select(.LifecycleState=="InService") | .ZoneId] | unique | length' | tr -d '\r')" || zones=""
  if [[ "$zones" != "1" ]]; then
    log "WARN: in-service instances span ${zones:-?} zone(s); ESS rebalances zones before removing, so age order does not predict the victim" >&2
    return 1
  fi
  ids="$(printf '%s' "$snapshot" | jq -r --argjson n "$n" '
    [.ScalingInstances.ScalingInstance[]? | select(.LifecycleState=="InService" and .HealthStatus=="Healthy")]
    | sort_by(.CreatedTime) | .[0:$n] | .[].InstanceId' | tr -d '\r')" || return 1
  if (( n > 0 )) && [[ -n "$ids" ]]; then
    boundary="$(printf '%s' "$snapshot" | jq -r --argjson n "$n" '
      [.ScalingInstances.ScalingInstance[]? | select(.LifecycleState=="InService" and .HealthStatus=="Healthy")]
      | sort_by(.CreatedTime) | if (length > $n) and (.[$n-1].CreatedTime == .[$n].CreatedTime) then .[$n].CreatedTime else empty end' | tr -d '\r')"
    if [[ -n "$boundary" ]]; then
      # Refuse, not warn (#9): ESS removes by policy order and a tie at the
      # boundary means it may pick a DIFFERENT member of the tie than this sort
      # did. Parking B while ESS removes A is the exact stream-cut this whole
      # protocol exists to prevent, so an ambiguous boundary is treated like
      # every other unpredictable-selection case: no retirement this round.
      log "WARN: the oldest-$n boundary falls inside a CreatedTime tie ($boundary); refusing to retire an ambiguous set" >&2
      return 1
    fi
  fi
  printf '%s\n' "$ids"
}

# oldest_instance_ips <n> - the private IPs of those instances, resolved by ID.
oldest_instance_ips() {
  local n="$1" id
  oldest_instance_ids "$n" | while IFS= read -r id; do
    [ -n "$id" ] || continue
    instance_private_ip "$id"
  done
}

# relay_drain_write <comma_ips> - park those relay members: ecs-fleet-sync
# subtracts them from the upstream list within a minute, so they stop receiving
# NEW requests while their in-flight streams finish. The marker is leased and
# owned (v2, see SHRINK_DRAIN_LEASE_SECONDS) and written atomically, because
# fleet-sync may read it at any moment. The IPs travel comma-joined on purpose:
# remote_cmd_on's ssh joins its arguments into one remote command string, so a
# newline-separated list would split into arguments the remote shell re-parses
# - the first IP parked, the second executed as a command. The write is
# verified: a marker that dropped IPs on the floor must fail the caller rather
# than park a subset silently.
relay_drain_write() {
  local ips="$1" lease
  [[ -n "$ips" ]] || return 0
  lease=$(( $(date +%s) + SHRINK_DRAIN_LEASE_SECONDS ))
  remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" \
    "$SHRINK_DRAIN_PATH" "$DRAIN_MARKER_OWNER" "$lease" "$ips" "$MARKER_LOCK_PATH" <<'REMOTE_DRAIN' >/dev/null 2>&1
#!/usr/bin/env bash
set -Eeuo pipefail
path="$1"; owner="$2"; lease="$3"; ips="$4"; lock="$5"
mkdir -p "$(dirname "$path")" "$(dirname "$lock")"
exec 200>"$lock"
flock -x 200
# A live foreign marker (the shrink guard mid-drain, typically) must not be
# stomped: overwriting it would un-park the instance IT is protecting and send
# that instance new requests while it is being retired. Exit 3: the caller
# fails closed and retries after the other writer's window ends.
if [ -r "$path" ]; then
  found_owner="$(sed -n '1p' "$path" | tr -d ' \r' || true)"
  found_lease="$(sed -n '2p' "$path" | tr -d ' \r' || true)"
  if [ "$found_owner" != "$owner" ] && [ "$found_lease" -gt "$(date +%s)" ] 2>/dev/null; then
    printf 'drain marker is owned by %s with a live lease\n' "$found_owner" >&2
    exit 3
  fi
fi
want="$(printf '%s\n' "$ips" | tr ',' '\n' | grep -cE '^10\.0\.0\.[0-9]+$' || true)"
tmp="$(mktemp "${path}.tmp.XXXXXX")"
{
  printf '%s\n' "$owner"
  printf '%s\n' "$lease"
  printf '%s\n' "$ips" | tr ',' '\n' | grep -E '^10\.0\.0\.[0-9]+$' || true
} >"$tmp"
chmod 0644 "$tmp"
got="$(grep -cE '^10\.0\.0\.[0-9]+$' "$tmp" || true)"
if [ "$got" -ne "$want" ]; then
  rm -f -- "$tmp"
  printf 'drain marker would carry %s of %s requested IP(s)\n' "$got" "$want" >&2
  exit 1
fi
mv -f -- "$tmp" "$path"
REMOTE_DRAIN
}

# relay_drain_clear - remove OUR marker (or an expired one). A marker owned by
# someone else with a live lease stays: the other writer still needs it, and
# deleting it mid-window is exactly how a parked instance would go back into
# rotation before its retirement. Plain-IP legacy markers have no owner; every
# legacy producer was replaced by a v2 writer, so removing them is cleanup.
relay_drain_clear() {
  remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" \
    "$SHRINK_DRAIN_PATH" "$DRAIN_MARKER_OWNER" "$MARKER_LOCK_PATH" <<'REMOTE_DRAIN_CLEAR' >/dev/null 2>&1
#!/usr/bin/env bash
set -Eeuo pipefail
path="$1"; owner="$2"; lock="$3"
exec 200>"$lock"
flock -x 200
[ -f "$path" ] || exit 0
found="$(sed -n '1p' "$path" | tr -d ' \r' || true)"
lease="$(sed -n '2p' "$path" | tr -d ' \r' || true)"
if [ -n "$found" ] && [ "$found" != "$owner" ]; then
  if [[ "$lease" =~ ^[0-9]+$ ]] && (( $(date +%s) < lease )); then
    printf 'drain marker belongs to %s and its lease is live; not removing\n' "$found" >&2
    exit 3
  fi
fi
rm -f -- "$path"
REMOTE_DRAIN_CLEAR
}

# relay_drain_refresh - extend OUR marker's lease (dead-man switch). A marker
# that is absent is normal between rounds and returns 0; returning 3 means the
# file EXISTS but no longer belongs to us - someone took the marker over, and
# the drain protection this release believes it has may no longer exist.
relay_drain_refresh() {
  local lease=$(( $(date +%s) + SHRINK_DRAIN_LEASE_SECONDS ))
  remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" \
    "$SHRINK_DRAIN_PATH" "$DRAIN_MARKER_OWNER" "$lease" "$MARKER_LOCK_PATH" <<'REMOTE_DRAIN_REFRESH' >/dev/null 2>&1
#!/usr/bin/env bash
set -Eeuo pipefail
path="$1"; owner="$2"; lease="$3"; lock="$4"
exec 200>"$lock"
flock -x 200
[ -f "$path" ] || exit 0
found="$(sed -n '1p' "$path" | tr -d ' \r' || true)"
[ "$found" = "$owner" ] || exit 3
tmp="$(mktemp "${path}.tmp.XXXXXX")"
{
  printf '%s\n' "$owner"
  printf '%s\n' "$lease"
  tail -n +3 -- "$path"
} >"$tmp"
chmod 0644 "$tmp"
mv -f -- "$tmp" "$path"
REMOTE_DRAIN_REFRESH
}

# wait_drain_applied <comma_ips> - block until the entry's relay upstream no
# longer carries ANY of the parked IPs. Parking is only real once fleet-sync
# consumed the marker and nginx reloaded; starting the drain timer before that
# would understate how long the retiring instance kept receiving new requests.
# Fail closed: a tier that cannot prove the park within the gate loses the
# round (rollback), because retiring an unparked instance cuts live streams.
wait_drain_applied() {
  local ips="$1" ip i=0 members still
  while [ "$i" -lt "$ML_DRAIN_CONVERGE_ATTEMPTS" ]; do
    # One full read per attempt, and a FAILED read is a retry, never "still=0":
    # per-IP probes used to mix "not in the upstream" with "could not read the
    # upstream", and a tier whose every probe failed sailed through the gate
    # with zero members found - the fail-open direction this gate exists to
    # prevent.
    if ! members="$(ecs_upstream_members)"; then
      sleep "$ML_DRAIN_CONVERGE_DELAY_SECONDS"
      i=$(( i + 1 ))
      continue
    fi
    still=0
    while IFS= read -r ip; do
      [ -n "$ip" ] || continue
      # Members are host:port lines; compare the address part, not the whole
      # line (an exact-line compare of a bare IP against "IP:3000" never
      # matched, so this gate passed while the victim was still served).
      if printf '%s\n' "$members" | awk -v want="$ip" '{split($1, a, ":"); if (a[1] == want) found=1} END{exit !found}'; then still=$(( still + 1 )); fi
    done < <(printf '%s\n' "$ips" | tr ',' '\n')
    if (( still == 0 )); then
      log "drain applied: the relay upstream no longer serves $(printf '%s' "$ips" | tr ',' ' ')"
      return 0
    fi
    sleep "$ML_DRAIN_CONVERGE_DELAY_SECONDS"
    i=$(( i + 1 ))
  done
  error "drain was not applied to the relay upstream within $((ML_DRAIN_CONVERGE_ATTEMPTS * ML_DRAIN_CONVERGE_DELAY_SECONDS))s ($ips still served or unreadable)"
  return 1
}

# release_hold_is_fresh <max_age_seconds> - true when a hold written by a LIVE
# release is present. A release heartbeats its hold during every drain window, so
# a hold younger than a few minutes means another release is running: that is the
# mutual exclusion two publishers need (a CNB run and a local one, or two systemd
# units), because both would otherwise drive the same scaling config and green
# container. A stale hold is a dead release and is ignored here - the entry's
# shrink guard uses the same rule. Exit codes: 0 = fresh (a live release owns
# the tier), 1 = no hold at all, 2 = a hold EXISTS but cannot be parsed. The
# caller must refuse on 2 as well: an unreadable hold cannot prove nobody is
# releasing, and treating it as "no hold" is the direction that lets a second
# release start while the first is still rolling.
release_hold_is_fresh() {
  local max_age="$1" written age rc=0
  written="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$SHRINK_HOLD_PATH" <<'REMOTE_HOLD_AGE' 2>/dev/null
#!/usr/bin/env bash
set -Eeuo pipefail
# Absent is the only answer that means "nobody holds the tier". Unreadable,
# truncated, or unreachable must be told apart: a transport failure that reads
# as "no hold" lets a second release start while the first is mid-rollout.
if [ ! -e "$1" ]; then exit 1; fi
if [ ! -r "$1" ]; then exit 2; fi
if ! head -1 "$1" 2>/dev/null; then exit 2; fi
REMOTE_HOLD_AGE
)" || rc=$?
  if (( rc == 1 )); then
    return 1
  fi
  if (( rc != 0 )); then
    # exit 2 (unreadable) or an ssh failure: both mean the hold's state is
    # UNKNOWN, which cannot prove nobody is releasing.
    return 2
  fi
  written="$(printf '%s' "$written" | tr -d ' \r')"
  [[ -n "$written" ]] || return 2
  # A timestamp we cannot parse proves nothing: falling back to epoch made age
  # enormous and reported "nobody is releasing", which is the direction that
  # lets a second release in.
  local epoch
  epoch="$(date -d "$written" +%s 2>/dev/null)" || return 2
  (( epoch > 0 )) || return 2
  age=$(( $(date +%s) - epoch ))
  (( age >= 0 && age < max_age ))
}

# RELEASE_OWNER identifies this release to the hold: cleanup must not remove a hold
# another release wrote, or a late finisher would re-open the tier to a scale-in
# while the release that owns it is still rolling.
readonly RELEASE_OWNER="${RELEASE_OWNER:-$(hostname 2>/dev/null || printf host)-$$}"
# The same identity marks this release as the drain marker's owner (v2 line 1);
# see the SHRINK_DRAIN_LEASE_SECONDS block for the protocol.
readonly DRAIN_MARKER_OWNER="release:${RELEASE_OWNER}"

shrink_hold_write() {
  local action="$1"
  if [[ "$action" == "set" ]]; then
    # Check-and-acquire under the entry host's marker lock: an atomic write
    # alone is not mutual exclusion, and neither is write-then-verify (two
    # releases both see "no hold", both write, and the one whose write landed
    # last reads back its own owner). The check and the write happen inside one
    # flock, so only one of them can win. A hold that exists, is fresh, and is
    # NOT ours makes this exit 3: somebody else owns the tier, and the caller
    # must refuse. Stale and absent holds are taken over (a SIGKILLed release
    # never cleans up). Renews refresh our own timestamp under the same lock.
    remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" \
      "$SHRINK_HOLD_PATH" "$RELEASE_OWNER" "${RELEASE_HOLD_FRESH_SECONDS:-900}" "$MARKER_LOCK_PATH" <<'REMOTE_HOLD_SET' >/dev/null 2>&1
#!/usr/bin/env bash
set -Eeuo pipefail
path="$1"; owner="$2"; max_age="$3"; lock="$4"
mkdir -p "$(dirname "$path")" "$(dirname "$lock")"
exec 200>"$lock"
flock -x 200
now="$(date +%s)"
if [ -e "$path" ] && [ ! -r "$path" ]; then
  printf 'shrink hold exists but is unreadable\n' >&2
  exit 3
fi
if [ -r "$path" ]; then
  other="$(sed -n '2p' "$path" | tr -d ' \r' || true)"
  ts="$(sed -n '1p' "$path" | tr -d ' \r' || true)"
  if [ -n "$ts" ]; then
    if ! epoch="$(date -d "$ts" +%s 2>/dev/null)"; then
      # A foreign hold we cannot date is undecidable - taking it over is the
      # direction that lets a second release in mid-rollout.
      if [ "$other" != "$owner" ]; then
        printf 'shrink hold owned by %s has an unparsable timestamp\n' "$other" >&2
        exit 3
      fi
      epoch=0
    fi
  else
    epoch=0
    if [ "$other" != "$owner" ]; then
      printf 'shrink hold owned by %s carries no timestamp\n' "$other" >&2
      exit 3
    fi
  fi
  if [ "$other" != "$owner" ] && [ "$epoch" -gt 0 ] && [ $(( now - epoch )) -lt "$max_age" ]; then
    printf 'shrink hold is owned by %s and fresh\n' "$other" >&2
    exit 3
  fi
fi
tmp="$(mktemp "${path}.tmp.XXXXXX")"
printf '%s\n%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$owner" >"$tmp"
chmod 0644 "$tmp"
mv -f -- "$tmp" "$path"
found="$(sed -n '2p' "$path" | tr -d ' \r' || true)"
[ "$found" = "$owner" ] || exit 3
REMOTE_HOLD_SET
    return $?
  fi
  remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$SHRINK_HOLD_PATH" "$action" "$RELEASE_OWNER" "$MARKER_LOCK_PATH" <<'REMOTE_HOLD' >/dev/null 2>&1
#!/usr/bin/env bash
set -Eeuo pipefail
path="$1"; action="$2"; owner="$3"; lock="$4"
mkdir -p "$(dirname "$lock")"
exec 200>"$lock"
flock -x 200
case "$action" in
  clear)
    existing="$(sed -n '2p' "$path" 2>/dev/null | tr -d ' \r' || true)"
    if [ -z "$existing" ]; then
      # No owner recorded: an older-format hold, or a write truncated between the
      # two lines. A STALE hold is a dead release and may go; a fresh one belongs
      # to a release that simply does not record owners, and removing it would
      # re-open the tier to a scale-in while that release is still rolling.
      ts="$(head -1 "$path" 2>/dev/null | tr -d ' \r' || true)"
      epoch="$(date -d "$ts" +%s 2>/dev/null || printf 0)"
      now="$(date +%s)"
      if [ "${epoch:-0}" -gt 0 ] && [ $(( now - epoch )) -lt "${HOLD_FRESH_SECONDS:-900}" ]; then
        printf 'hold records no owner and is still fresh; not removing\n' >&2
        exit 3
      fi
      rm -f "$path"; exit 0
    fi
    if [ -n "$existing" ] && [ "$existing" != "$owner" ]; then
      printf 'hold belongs to %s; not removing\n' "$existing" >&2
      exit 3
    fi
    rm -f "$path" ;;
  *)     exit 2 ;;
esac
REMOTE_HOLD
  }

suspend_scale_in_guard() {
  local id rc=0
  # One release at a time. The hold doubles as the lock: a live release refreshes
  # it every heartbeat, so a fresh one means somebody else owns the tier.
  # `|| rc=$?` keeps set -e from acting on the non-zero exits we are inspecting.
  release_hold_is_fresh "${RELEASE_HOLD_FRESH_SECONDS:-900}" || rc=$?
  if (( rc == 0 )); then
    die "another release is in progress on $MASTER_HOST (its shrink guard hold is fresh); refusing to run two at once"
  fi
  if (( rc == 2 )); then
    # A hold that exists but cannot be parsed proves nothing about who is
    # releasing: proceeding here is the direction that lets a second release
    # start while the first is still rolling. The operator removes the broken
    # file by hand; a release refuses until then.
    die "the shrink guard hold on $MASTER_HOST is present but unreadable; refusing to release until it is inspected"
  fi
  # The acquire-attempt log line is the rendezvous marker the concurrent-release
  # test watches: it proves both contenders reached this point before either
  # could win the lock.
  log "acquiring the entry shrink guard hold"
  # Tell the entry's shrink guard to stand down for the whole release, whatever
  # happens to the alarm half below.
  if shrink_hold_write set; then
    log "entry shrink guard held off for the release"
    # The heartbeat must run from this moment on (see hold_heartbeat): the hold
    # is written even when the alarm half below never suspends anything.
    scale_in_hold_written=1
  else
    # Fail closed. The guard's only job is to keep the tier from shrinking while a
    # rollout owns it; releasing without that promise means the tier can shed an
    # instance mid-round, which is the failure mode the drain windows exist to
    # avoid. Better to refuse than to roll the dice.
    die "could not write the shrink guard hold on $MASTER_HOST; refusing to release (a scale-in could race the rollout)"
  fi
  # A FAILED query is not 'no alarm' and not 'disabled': both misreadings let
  # the release run while a live scale-in alarm may remove instances mid-round
  # (or permanently skip the restore of an alarm that was on). Only a
  # SUCCESSFUL query may take the no-alarm branch.
  if ! id="$(scale_in_alarm_id)"; then
    die "could not determine whether the scale-in alarm exists; refusing to release without knowing"
  fi
  if [[ -z "$id" ]]; then
    warn "no scale-in alarm named '$SCALE_IN_ALARM_NAME' on this scaling group; the tier may shed capacity during the rollout"
    return 0
  fi
  # Remember whether it was enabled BEFORE disabling: an operator may have turned
  # it off deliberately (cpu-in-25 is off today because it cut long streams), and
  # resuming must restore that state rather than switch it back on.
  local alarm_state
  # jq's `//` treats false as absent (false // empty -> empty), so a DISABLED
  # alarm read as "state unknown" and killed the release; the value is matched
  # explicitly instead.
  if ! alarm_state="$(aliyun_cmd ess DescribeAlarms --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION" \
        | jq -r --arg n "$SCALE_IN_ALARM_NAME" '[.AlarmList.Alarm[]? | select(.Name==$n)] | if length > 0 then (.[0].Enable | if . == true then "true" elif . == false then "false" else "invalid" end) else "absent" end' | tr -d '\r')"; then
    die "could not read the scale-in alarm's enable state; refusing to release without knowing what to restore"
  fi
  if [[ "$alarm_state" != "true" && "$alarm_state" != "false" ]]; then
    # 'absent' (the id was found but the state record is gone), 'invalid'
    # (not a boolean), or an error body: treating any of them as 'disabled'
    # would permanently skip the restore of an alarm that was actually on.
    die "the scale-in alarm's enable state is not readable ('$alarm_state'); refusing to release"
  fi
  if [[ "$alarm_state" == "true" ]]; then
    scale_in_alarm_was_enabled=1
  else
    scale_in_alarm_was_enabled=0
  fi
  if aliyun_cmd ess DisableAlarm --AlarmTaskId "$id" --region "$ALIYUN_REGION" >/dev/null; then
    scale_in_guard_suspended="$id"
    log "scale-in alarm suspended for the rollout ($id)"
  else
    # Fail closed: continuing with the alarm LIVE lets it remove an instance
    # mid-round (the exact streams the drain windows protect), and a later
    # RETIREMENT_PENDING exit would even claim the alarm was left off. The
    # hold was already written; the EXIT trap cleans it up.
    die "could not suspend scale-in alarm $id; refusing to release with the alarm live"
  fi
}

# Set when a retire set failed to leave the group before its gate timed out:
# the removal may still be terminating, and un-parking the victim now would
# send it new requests mid-retirement. The EXIT trap respects it and leaves the
# marker parked instead - its lease lapses once no heartbeat extends it, which
# is exactly the dead-writer case the lease exists for.
RETIREMENT_PENDING=0

# release_the_hold - clear the shrink guard hold with an explicit outcome: a
# FAILED clear must be said out loud, not swallowed by an `&& log` chain that
# leaves the caller's return 0 reporting success with the hold still parked.
release_the_hold() {
  if shrink_hold_write clear; then
    log "entry shrink guard released"
  else
    warn "could not clear the shrink guard hold; it stays in place and the guard's janitor treats it as abandoned after its max age"
  fi
  return 0
}

resume_scale_in_guard() {
  local id="${scale_in_guard_suspended:-}"
  # Never leave a drain marker behind: it keeps a healthy member out of the relay
  # list indefinitely, which reads as lost capacity rather than a failed release.
  # One exception: a removal that timed out mid-flight. The marker's lease is
  # the safety net there, and clearing it would un-park an instance ESS is still
  # terminating.
  if (( RETIREMENT_PENDING == 1 )); then
    warn "leaving the drain marker parked: a retire set timed out before leaving the group (its lease lapses without a heartbeat)"
  else
    relay_drain_clear >/dev/null 2>&1 || warn "could not clear the relay drain marker"
  fi
  # A removal still in flight (#4): the retired member may still be terminating,
  # so the scale-in alarm stays OFF and the hold STAYS - clearing either would
  # let the alarm (or the guard) remove another instance while this one is
  # mid-retirement. The hold goes stale on its own after HOLD_MAX_AGE and the
  # marker's lease lapses without a heartbeat; an operator re-enables the alarm
  # by hand once the victim has left the group (the message says so).
  if (( RETIREMENT_PENDING == 1 )); then
    log "scale-in alarm ${id:-<not suspended>} left disabled and the shrink hold kept: a retire set is still settling; re-enable it by hand once it leaves the group"
    return 0
  fi
  if [[ -z "$id" || "${scale_in_alarm_was_enabled:-0}" != "1" ]]; then
    # No alarm was suspended, or it was already off before this release:
    # nothing to restore. The heartbeat gates go down FIRST (log() heartbeats
    # while either is set and would re-acquire the hold the cleanup drops).
    if [[ -n "$id" ]]; then
      log "scale-in alarm $id was already disabled before this run; leaving it disabled"
    fi
    scale_in_hold_written=""
    scale_in_guard_suspended=""
    release_the_hold
    return 0
  fi
  # Restore the alarm BEFORE the hold is cleared, gates down first so no
  # heartbeat interleaves: clearing first would let a second release acquire
  # the tier, read the alarm as disabled, keep it off - and then THIS release
  # re-enables it underneath the new release.
  scale_in_hold_written=""
  scale_in_guard_suspended=""
  local ok=0 i
  for i in 1 2 3; do
    if aliyun_cmd ess EnableAlarm --AlarmTaskId "$id" --region "$ALIYUN_REGION" >/dev/null; then
      ok=1
      break
    fi
    sleep 3
  done
  if (( ok == 1 )); then
    log "scale-in alarm resumed ($id)"
    release_the_hold
    return 0
  fi
  # The restore failed (#3): the hold is the only thing keeping the guard from
  # shrinking while the alarm is off, so it STAYS (the janitor treats it as
  # abandoned only after HOLD_MAX_AGE). Fail loudly - this exit path runs from
  # the EXIT trap, and a non-zero exit records the incident.
  die "could not re-enable scale-in alarm $id after retries; the shrink hold is left in place - re-enable the alarm by hand, then remove $SHRINK_HOLD_PATH"
}

# current_desired_capacity - the group's DesiredCapacity right now.
#
# A rollout grows the tier by one and returns to exactly this number, so a site
# running two steady-state instances keeps two after a release. The capacity used
# to be hardcoded 2/1, which was correct only while the steady state was one
# instance: once the floor was raised (2026-09-28, after a single 4C4G ECI was
# saturated by an acceptance load test) a release would have scaled the tier
# back to one instance - and with MinSize=2 the scale-down call would have been
# rejected outright, failing the release after the drain window.
current_desired_capacity() {
  local desired
  desired="$(aliyun_cmd ess DescribeScalingGroups --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION" \
    | jq -r '.ScalingGroups.ScalingGroup[0].DesiredCapacity // empty' | tr -d '\r')" || return 1
  case "$desired" in
    ''|*[!0-9]*) return 1 ;;
  esac
  printf '%s\n' "$desired"
}

# current_max_size - the group's MaxSize, i.e. the ceiling a batch rollout may
# grow into. A batch rollout adds as many fresh instances at once as this
# allows; a group whose MaxSize equals its DesiredCapacity has no headroom and
# must keep using the one-at-a-time release path.
current_max_size() {
  local max
  max="$(aliyun_cmd ess DescribeScalingGroups --ScalingGroupId "$SCALING_GROUP_ID" --region "$ALIYUN_REGION" \
    | jq -r '.ScalingGroups.ScalingGroup[0].MaxSize // empty' | tr -d '\r')" || return 1
  case "$max" in
    ''|*[!0-9]*) return 1 ;;
  esac
  printf '%s\n' "$max"
}

# relay_member_cap - how many instances the ENTRY's relay upstream list can
# carry, i.e. MAX_MEMBERS in the entry's /usr/local/sbin/ecs-fleet-sync.
#
# This is a second ceiling, independent of the scaling group's MaxSize:
# ecs-fleet-sync truncates the list it writes with `head -n "$MAX_MEMBERS"`, so an
# instance beyond the cap never appears in the relay upstream, and
# wait_ecs_upstream_converged times out 180s later and rolls the whole release
# back. That is exactly how v1.0.0-rc.40-tokeness-mainland.42 died on 2026-10-09:
# the group allowed 9, the relay cap was 8, and the 9th instance was invisible.
#
# RELAY_MAX_MEMBERS overrides the lookup (the tests use it). When the value
# cannot be read the caller proceeds on the ESS ceiling alone and says so: a
# failed lookup must not block a release - only a KNOWN too-small cap does.
relay_member_cap() {
  if [[ -n "${RELAY_MAX_MEMBERS:-}" ]]; then
    case "$RELAY_MAX_MEMBERS" in
      ''|*[!0-9]*) error "RELAY_MAX_MEMBERS must be a positive integer"; return 1 ;;
    esac
    printf '%s\n' "$RELAY_MAX_MEMBERS"
    return 0
  fi
  local cap
  cap="$(remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" <<'REMOTE_CAP' 2>/dev/null
#!/usr/bin/env bash
set -Eeuo pipefail
f=/usr/local/sbin/ecs-fleet-sync
[[ -r "$f" ]] || exit 1
grep -oE 'MAX_MEMBERS:-[0-9]+' "$f" | head -1 | grep -oE '[0-9]+'
REMOTE_CAP
)" || cap=""
  case "$cap" in
    ''|*[!0-9]*) return 1 ;;
  esac
  printf '%s\n' "$cap"
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
    sleep "$HEALTH_POLL_SECONDS"
    i=$((i + 1))
  done
  error "timed out waiting for $want healthy instance(s) (have $got)"
  return 1
}

# wait_retired <ids> [tries] - wait until EVERY id in the newline-separated
# <ids> has left the group ENTIRELY: absent from DescribeScalingInstances in
# any lifecycle state.
#
# "Left InService" is not departure: a retired instance sits in
# Removing/Removing:Wait while the platform terminates it, and the next
# round's scale-out would supersede that pending removal - the race that
# killed v1.0.0-rc.40-tokeness-mainland.42 after two hours. The count could
# not tell a completed scale-in from an external scale-out either, so the gate
# is the exact retire set, fully gone. A failed query retries: reading
# "they all left" out of a failed query is the fail-open direction this gate
# exists to prevent. On timeout the drain marker is deliberately NOT cleared:
# the parked members are still draining, and un-parking them would send new
# requests to instances that are being terminated.
wait_retired() {
  local ids="$1" tries="${2:-40}" i=0 total=0 gone=0 current id
  total=$(printf '%s\n' "$ids" | grep -c . || true)
  if (( total == 0 )); then
    return 0
  fi
  while (( i < tries )); do
    if ! current="$(all_instance_ids)"; then
      sleep "$HEALTH_POLL_SECONDS"; i=$(( i + 1 )); continue
    fi
    [[ -n "$current" ]] || { sleep "$HEALTH_POLL_SECONDS"; i=$(( i + 1 )); continue; }
    gone=0
    while IFS= read -r id; do
      [ -n "$id" ] || continue
      printf '%s\n' "$current" | grep -qxF "$id" || gone=$(( gone + 1 ))
    done <<<"$ids"
    if (( gone >= total )); then
      log "scale-in complete: all $total retired instance(s) left the group"
      # The parked members are gone; drop the marker so the relay carries every
      # healthy instance again. Best-effort here because the EXIT trap retries it.
      relay_drain_clear >/dev/null 2>&1 || true
      return 0
    fi
    sleep "$HEALTH_POLL_SECONDS"
    i=$(( i + 1 ))
  done
  error "timed out waiting for the retired set to leave the group ($gone of $total gone)"
  RETIREMENT_PENDING=1
  return 1
}

# verify_no_extra_removals <before_ids> <retire_ids> <label> - after a removal
# settled, no member OUTSIDE the parked retire set may have left the group.
# ModifyScalingGroup carries an absolute capacity, so ESS picks its victims at
# execution time: anything that joined between our member read and the Modify
# (or an ESS-side re-pick) can remove MORE than the parked set, and a count
# gate cannot see that. This turns the residual race into a loud failure
# instead of a silently under-served tier.
verify_no_extra_removals() {
  local before_ids="$1" retire_ids="$2" label="$3" current id extra=0 i=0 read_ok=0
  [[ -n "$retire_ids" ]] || return 0
  # A failed read is NOT a pass: this check is the only witness that the
  # removal matched the parked set. The success FLAG is tracked separately
  # from the output - a command can emit a full list and still fail (partial
  # read), and non-empty stdout is not a success signal.
  while (( i < 3 )); do
    if current="$(in_service_instance_ids)"; then
      read_ok=1
      break
    fi
    sleep "$HEALTH_POLL_SECONDS"
    i=$(( i + 1 ))
  done
  if (( read_ok != 1 )); then
    error "$label: could not re-read the instance set to verify the removal set; failing closed"
    return 1
  fi
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    printf '%s\n' "$retire_ids" | grep -qxF "$id" && continue
    printf '%s\n' "$current" | grep -qxF "$id" && continue
    extra=$(( extra + 1 ))
    log "ERROR: $label: instance $id left the group although it was not in the parked retire set" >&2
  done <<<"$before_ids"
  (( extra == 0 ))
}

# --- drain-first rollout helpers ----------------------------------------------
# Both lightweight hosts run ml-sync on a 30s cron and each rewrites its own
# copy of the upstream, so a pin must be delivered to both. The marker is
# written atomically (tmp + rename) because ml-sync may read it at any moment.
ml_drain_write_marker() {
  local host="$1" key="$2" known="$3" target_ip="$4" expires="$5"
  remote_cmd_on "$host" "$key" "$known" "$DRAIN_MARKER_PATH" "$target_ip" "$expires" <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
marker="$1"
target_ip="$2"
expires="$3"
install -d -m 0755 "$(dirname "$marker")"
tmp="$marker.tmp.$$"
printf '%s %s\n' "$target_ip" "$expires" > "$tmp"
chmod 0644 "$tmp"
mv -f -- "$tmp" "$marker"
REMOTE_SCRIPT
}

ml_drain_remove_marker() {
  local host="$1" key="$2" known="$3"
  remote_cmd_on "$host" "$key" "$known" "$DRAIN_MARKER_PATH" <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
rm -f -- "$1"
REMOTE_SCRIPT
}

# ml_drain_begin <target_ip> - pin both relay tiers to the new instance.
# Fails closed: if either host cannot be pinned, the caller must abort rather
# than scale down, because the guarantee is only as good as the host that did
# not get the message.
ml_drain_begin() {
  local target_ip="$1" expires
  is_valid_ipv4 "$target_ip" || { error "drain target is not an IPv4 address: $target_ip"; return 1; }
  expires=$(( $(date +%s) + ML_DRAIN_MARKER_TTL_SECONDS ))
  log "drain-first: pinning relay tier to $target_ip on both lightweight hosts"
  ml_drain_write_marker "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS" "$target_ip" "$expires" \
    || { error "could not write the drain marker on $SWAS_HOST"; return 1; }
  ml_drain_write_marker "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" "$target_ip" "$expires" \
    || { error "could not write the drain marker on $SWAS2_HOST"; return 1; }
  return 0
}

# ml_drain_end - release the pin. Best effort by design: an unreachable host
# keeps a marker that expires on its own, and ml-sync ignores a marker whose
# target is no longer a healthy InService instance, so the failure mode is a
# delay, never a black-holed tier.
ml_drain_end() {
  local rc=0
  ml_drain_remove_marker "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS" \
    || { warn "could not remove the drain marker on $SWAS_HOST; it expires on its own"; rc=1; }
  ml_drain_remove_marker "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" \
    || { warn "could not remove the drain marker on $SWAS2_HOST; it expires on its own"; rc=1; }
  return "$rc"
}

# --- blue-green master helpers -------------------------------------------------
# The panel tier's nginx primary on each lightweight host follows
# $WEB_PRIMARY_MARKER_PATH (ml-sync rewrites the conf from it, health-gated).
# deploy.sh writes the pin AFTER the green master passed its readiness and
# version gates, waits until BOTH hosts' nginx carry the port, and only then
# asks the ECS bootstrap to commit (retire blue). Fails closed: if a host
# cannot be pinned, or ml-sync does not converge, the master switch aborts
# while blue is still serving.

web_primary_pin() { # <host> <key> <known-hosts> <port>
  local host="$1" key="$2" known="$3" port="$4"
  [[ "$port" =~ ^[0-9]+$ ]] || { error "web-primary pin is not a port number: $port"; return 1; }
  log "blue-green: pinning panel primary to $port on $host"
  remote_cmd_on "$host" "$key" "$known" "$WEB_PRIMARY_MARKER_PATH" "$port" <<'REMOTE_SCRIPT'
#!/usr/bin/env bash
set -Eeuo pipefail
web_port_marker="$1"
port="$2"
install -d -m 0755 "$(dirname "$web_port_marker")"
tmp="$web_port_marker.tmp.$$"
printf '%s\n' "$port" > "$tmp"
chmod 0644 "$tmp"
mv -f -- "$tmp" "$web_port_marker"
REMOTE_SCRIPT
}

# get_web_primary_port_on <host> <key> <known-hosts> - the host:port the
# panel-tier primary names in that host's nginx conf (empty when unreadable).
get_web_primary_port_on() {
  local host="$1" key="$2" known="$3"
  remote_cmd_on "$host" "$key" "$known" "$NGINX_CONF" <<'REMOTE_AWK' 2>/dev/null | tail -n1 || true
#!/usr/bin/env bash
set -Eeuo pipefail
conf="$1"
awk '
  $0 ~ "^[[:space:]]*upstream[[:space:]]+newapi_web[[:space:]]*\\{[[:space:]]*$" { inside = 1; next }
  inside && $0 ~ "^[[:space:]]*}" { inside = 0 }
  inside && $0 ~ "^[[:space:]]*server[[:space:]]+[0-9.]+:[0-9]+" && $0 !~ /backup/ {
    line = $0
    sub(/^[[:space:]]*server[[:space:]]+/, "", line)
    sub(/[[:space:]].*/, "", line)
    print line
    exit
  }
' "$conf"
REMOTE_AWK
}

wait_web_primary_converged() { # <port> - both lightweight hosts' nginx carry it
  local target_port="$1" i=0 p1='' p2=''
  while [ "$i" -lt "$WEB_PRIMARY_CONVERGE_ATTEMPTS" ]; do
    p1="$(get_web_primary_port_on "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS")"
    p2="$(get_web_primary_port_on "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS")"
    if [ "$p1" = "$WEB_PRIMARY_HOST:$target_port" ] && [ "$p2" = "$WEB_PRIMARY_HOST:$target_port" ]; then
      log "blue-green: both lightweight hosts serve the panel primary at $WEB_PRIMARY_HOST:$target_port"
      return 0
    fi
    sleep "$WEB_PRIMARY_CONVERGE_DELAY_SECONDS"
    i=$((i + 1))
  done
  error "blue-green: panel primary did not converge to $WEB_PRIMARY_HOST:$target_port within $((WEB_PRIMARY_CONVERGE_ATTEMPTS * WEB_PRIMARY_CONVERGE_DELAY_SECONDS))s (swas1=${p1:-?} swas2=${p2:-?})"
  return 1
}

# ecs_upstream_has <ip> - the ECS's relay upstream file names this instance.
#
# This replaces the retired drain pin (see SWAS_PANEL_TIER). The ECS *is* the
# relay entry now and ecs-fleet-sync writes its upstream file straight from the
# local ESS view, so "the ECS serves the new instance" is what the old
# marker+ml-sync pair used to establish. It is a strictly weaker guarantee — the
# file lists every in-service peer, so it cannot single out one instance — which
# is exactly the trade the retirement accepted: a rollout may briefly send new
# requests to the retiring instance, and the drain hold below is what still
# protects its in-flight streams. Fails closed: an unreadable file is not
# convergence, and neither is an empty answer.
ecs_upstream_has() {
  local ip="$1"
  is_valid_ipv4 "$ip" || { error "upstream probe target is not an IPv4 address: $ip"; return 1; }
  remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" "$ECS_UPSTREAM_CONF" "$ip" <<'REMOTE_HAS' 2>/dev/null | grep -qx yes
#!/usr/bin/env bash
set -Eeuo pipefail
conf="$1" ip="$2"
[[ -r "$conf" ]] || exit 1
# Same fragment shape as ecs_upstream_members; the IP is escaped so a /8 prefix
# cannot match 10.0.0.24 against 10.0.0.241.
grep -E "^[[:space:]]*server[[:space:]]+${ip//./\\.}:[0-9]+" "$conf" | grep -qv backup && printf 'yes\n'
REMOTE_HAS
}

# ecs_upstream_members - the relay members the ECS serves, one per line.
#
# The ECS is the relay entry (the lightweight tier retired, see
# SWAS_PANEL_TIER) and ecs-fleet-sync writes this file from the local ESS view,
# so reading it is the relay-side equivalent of reading both lightweight hosts'
# nginx. Path/config errors are reported, never papered over: a rollout that
# cannot read the entry must not conclude that it converged.
ecs_upstream_members() {
  remote_cmd_on "$MASTER_HOST" "$MASTER_SSH_KEY_PATH" "$MASTER_SSH_KNOWN_HOSTS" \
    "$ECS_UPSTREAM_CONF" <<'REMOTE_MEMBERS' 2>/dev/null
#!/usr/bin/env bash
set -Eeuo pipefail
conf="$1"
[[ -r "$conf" ]] || { echo "unreadable upstream file $conf" >&2; exit 1; }
# The file is an include fragment: ecs-fleet-sync writes the server lines and
# the vhost supplies the "upstream" block, so members are matched by shape
# rather than by block scope (a block-scoped parse finds nothing here).
awk '
  /^[[:space:]]*#/ { next }
  /^[[:space:]]*server[[:space:]]+[0-9.]+:[0-9]+/ && !/backup/ {
    line = $0
    sub(/^[[:space:]]*server[[:space:]]+/, "", line)
    sub(/[[:space:]].*/, "", line)
    sub(/;.*$/, "", line)
    print line
  }
' "$conf"
REMOTE_MEMBERS
}

# wait_ecs_upstream_converged <ip> - block until the ECS relay tier serves it.
wait_ecs_upstream_converged() {
  local target_ip="$1" i=0
  while [ "$i" -lt "$ML_DRAIN_CONVERGE_ATTEMPTS" ]; do
    if ecs_upstream_has "$target_ip"; then
      log "rollout: the ECS relay tier serves $target_ip"
      return 0
    fi
    sleep "$ML_DRAIN_CONVERGE_DELAY_SECONDS"
    i=$((i + 1))
  done
  error "rollout: the ECS relay tier did not start serving $target_ip within $((ML_DRAIN_CONVERGE_ATTEMPTS * ML_DRAIN_CONVERGE_DELAY_SECONDS))s"
  return 1
}

# wait_drain_converged <target_ip> - block until BOTH hosts serve the target.
# The drain timer must not start before this: ml-sync converges on a 30s cron,
# so sleeping first would leave new requests flowing to the retiring instance
# for part of the wait and understate how long it has actually been idle.
wait_drain_converged() {
  local target_ip="$1" i=0 seen1='' seen2=''
  while [ "$i" -lt "$ML_DRAIN_CONVERGE_ATTEMPTS" ]; do
    seen1="$(get_upstream_ip_on "$SWAS_HOST" "$SWAS_SSH_KEY_PATH" "$SWAS_SSH_KNOWN_HOSTS" 2>/dev/null || true)"
    seen2="$(get_upstream_ip_on "$SWAS2_HOST" "$SWAS2_SSH_KEY_PATH" "$SWAS2_SSH_KNOWN_HOSTS" 2>/dev/null || true)"
    if [ "$seen1" = "$target_ip" ] && [ "$seen2" = "$target_ip" ]; then
      log "drain-first: both lightweight hosts now serve $target_ip only"
      return 0
    fi
    sleep "$ML_DRAIN_CONVERGE_DELAY_SECONDS"
    i=$((i + 1))
  done
  error "drain-first: upstream did not converge to $target_ip within $((ML_DRAIN_CONVERGE_ATTEMPTS * ML_DRAIN_CONVERGE_DELAY_SECONDS))s (swas1=${seen1:-?} swas2=${seen2:-?})"
  return 1
}

# ess_rollout <sha256:digest> <previous_digest> <previous_snapshot> - scale out by one, gate the NEW
# instance on the application itself (probe + container log), and only then
# scale back to the steady-state capacity read at the start. While both
# instances are healthy ml-sync lists both upstream members, and nginx passive
# checks (max_fails=2 fail_timeout=5s) bridge the ~30s window in which the old
# member disappears after the scale-down. Any failure before convergence rolls
# back to the previous digest automatically.
ess_rollout() {
  local digest="$1" previous_digest="${2:-}" previous_snapshot="${3:-}" attempt
  local before_ids new_id new_ip stable retire_ids retire_ip roll_before_ids now_count live target scale_in_ok
  if ! before_ids="$(in_service_instance_ids)"; then
    error "could not read the current in-service instance set"
    return 1
  fi
  if ! stable="$(current_desired_capacity)"; then
    error "could not read the scaling group's desired capacity"
    return 1
  fi
  # Read by rollback_failed_rollout so every failure path restores the capacity
  # the site actually runs with, not a hardcoded one.
  ROLLOUT_STABLE_CAPACITY="$stable"
  # Every path needs one slot of headroom: even one-at-a-time scales the tier to
  # stable+1 while the old instance still serves. A group already at its
  # MaxSize cannot be rolled - ESS would reject the ModifyScalingGroup call and
  # the release would fail mid-flight with a half-open window. Refuse before
  # moving anything, with the reason.
  local group_max
  if ! group_max="$(current_max_size)"; then
    error "could not read the scaling group's MaxSize"
    return 1
  fi
  if (( stable + 1 > group_max )); then
    error "no headroom to roll: DesiredCapacity=$stable is already at MaxSize=$group_max; raise MaxSize or retire capacity before releasing"
    return 1
  fi
  log "steady-state capacity is $stable; rolling out through $((stable + 1)) instances"
  if ! scale_group $((stable + 1)); then
    rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || true
    return 1
  fi
  if ! wait_healthy_instances $((stable + 1)); then
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
  log "new instance $new_id at $new_ip; gating on application health before cutover"
  if ! wait_app_ready "$new_id" "$new_ip"; then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "rollout aborted and rollback could not be verified"
    fi
    return 1
  fi
  # Drain-first. Stop sending NEW requests to the retiring instance and let its
  # in-flight SSE streams finish before ESS removes it; otherwise the platform
  # grace period SIGKILLs them mid-answer (the grace period only applies to the
  # config an instance was created with, so this is what protects the very
  # first release that raises it).
  #
  # SWAS_PANEL_TIER=1 (retired path): pin both lightweight hosts and wait for
  # ml-sync. Default: the ECS is the relay entry, so the gate is the ECS's own
  # upstream file carrying the new instance — weaker (it cannot single out one
  # instance) but honest, and the drain hold below is what keeps the retiring
  # instance's streams alive. Either way the rollout fails closed: if the relay
  # tier never picks the new instance up, we abort while the old one still
  # serves, which is exactly the state rollback restores anyway.
  if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
    if ! ml_drain_begin "$new_ip" || ! wait_drain_converged "$new_ip"; then
      if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
        error "drain-first could not pin the relay tier and rollback could not be verified"
      fi
      return 1
    fi
  elif ! wait_ecs_upstream_converged "$new_ip"; then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "drain-first gate failed and rollback could not be verified"
    fi
    return 1
  fi
  # Park the instance this scale-down will remove BEFORE the hold, exactly like
  # the batch rounds (#5): the victim is the oldest in-service instance (the new
  # one is the newest by construction, and ESS removes by OldestInstance). The
  # old flow never parked anything here - it only confirmed the NEW instance was
  # carried, slept, and let ESS remove the old one while it still received new
  # requests, so the very first leg of every release cut its in-flight streams.
  retire_ids="$(oldest_instance_ids 1 || true)"
  if [[ -z "$retire_ids" ]]; then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "could not resolve the instance to park for drain-first and rollback could not be verified"
    fi
    return 1
  fi
  retire_ip=""
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    if ! retire_ip="$(instance_private_ip "$id" 2>/dev/null)" || ! is_valid_ipv4 "$retire_ip"; then
      if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
        error "could not resolve the parked instance's address and rollback could not be verified"
      fi
      return 1
    fi
  done <<<"$retire_ids"
  if ! relay_drain_write "$retire_ip"; then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "could not park the retiring instance in the relay drain file and rollback could not be verified"
    fi
    return 1
  fi
  if ! wait_drain_applied "$retire_ip"; then
    relay_drain_clear >/dev/null 2>&1 || true
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "the relay never dropped the parked instance and rollback could not be verified"
    fi
    return 1
  fi
  log "drain-first: parked the retiring instance ($retire_ip) in the relay drain file"
  log "drain-first: holding ${ML_DRAIN_SECONDS}s so in-flight streams (p95 181s, max 461s) finish on the retiring instance"
  sleep_with_heartbeat "$ML_DRAIN_SECONDS"
  # Scale down by exactly the parked set: remove one (the parked oldest) and
  # KEEP anything that joined meanwhile - an external scale-out during the
  # hold must not be undone by this release, and the removal count must stay
  # the count that was parked. Then wait for the set's FULL departure: a
  # member still in Removing is a removal the next scale-out would supersede.
  if ! roll_before_ids="$(in_service_instance_ids)"; then
    # Fail closed (#4): falling back to a healthy-count gate here let a failed
    # member query shrink the tier with NOTHING parked - the count gate cannot
    # prove the removal set matches the parked set.
    error "could not read the in-service set before the scale-in; refusing to shrink unparked"
    return 1
  fi
  scale_in_ok=0
  if [[ -n "$roll_before_ids" ]]; then
    now_count=$(printf '%s\n' "$roll_before_ids" | grep -c . || true)
    live=0
    while IFS= read -r id; do
      [ -n "$id" ] || continue
      printf '%s\n' "$roll_before_ids" | grep -qxF "$id" && live=$(( live + 1 ))
    done <<<"$retire_ids"
    target=$(( now_count - live ))
    (( target < stable )) && target="$stable"
    if scale_group "$target" && wait_retired "$retire_ids" \
       && verify_no_extra_removals "$roll_before_ids" "$retire_ids" "ess rollout"; then
      scale_in_ok=1
    fi
  fi
  if (( scale_in_ok == 0 )); then
    if ! rollback_failed_rollout "$new_id" "$previous_digest" "$previous_snapshot"; then
      error "scale-down gate failed and rollback could not be verified"
    fi
    return 1
  fi
  if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
    ml_drain_end || warn "drain marker left behind; ml-sync expires it on its own"
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

# rollout_batch <digest> [expected_version] [previous_digest] [snapshot]
#               [retire_ids] [batch_size] - retire EVERY instance in the retire
# set in one invocation, replacing batch_size of them per drain window.
#
# Why this exists: ess_rollout replaces exactly one instance per release, so a
# six-instance fleet needs six releases - six tags, six builds, roughly three
# hours - before the whole tier serves an image. Anything that image carries on
# the ECI tier (the OpenAI-compatible billing endpoints, for one) stays only
# partially live in the meantime. This batches the same tested steps and repeats
# them until no instance from the retire set is left:
#
#   scale to stable+batch -> gate EVERY new instance on the application ->
#   wait for the relay tier to serve them -> ONE drain hold -> scale to stable
#
# so the tier converges in ceil(n/batch) drain windows instead of n.
#
# batch_size is what the operator trades time against blast radius with. It
# defaults to every slot MaxSize allows, but the RELEASE path asks for
# ROLLOUT_BATCH (2 by default): a bad image starts serving its share of traffic
# the moment its instance joins the pool, so a wider batch widens that window in
# proportion. An explicit size is validated, never clamped - an operator who
# asked for 4 and silently got 2 would misread how wide the roll actually was.
#
# What an operator must know before using it:
#   * it is bounded by MaxSize and never grows past it; a group with no headroom
#     is refused up front rather than silently degrading to serial replacement;
#   * several instances are replaced at once, so a failure can strand more than
#     one fresh instance. rollback_failed_rollout still restores the scaling
#     configuration and the steady-state capacity, and ESS retires or replaces
#     whatever is left - but the blast radius of a bad image is wider here than
#     in a release, which is why every new instance is gated individually before
#     the drain hold starts;
#   * the default relay topology cannot single out one instance (the retired
#     lightweight tier used to), so all fresh instances join the pool and share
#     traffic. In-flight requests on the instances ESS retires are protected by
#     the drain hold plus the ECI termination grace, not by a pin.
rollout_batch() {
  local digest="$1" expected_version="${2:-}" previous_digest="${3:-}" previous_snapshot="${4:-}" retire_ids="${5:-}" requested_batch="${6:-}"
  local stable max relay_cap ceiling batch rounds=0 round_limit current_ids id ip want retiring base current_count scalein_before roll_before_ids drain_ips
  local -a original_ids=() remaining=() new_ids=()
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { error "rollout-batch needs a sha256:<64 hex> digest"; return 1; }

  stable="$(current_desired_capacity)" || { error "could not read the scaling group's desired capacity"; return 1; }
  max="$(current_max_size)" || { error "could not read the scaling group's MaxSize"; return 1; }
  # The group's MaxSize is not the only ceiling: the entry's relay list caps how
  # many members it can carry (relay_member_cap), and an instance past that cap
  # never reaches the relay tier at all. Grow into the smaller of the two.
  if relay_cap="$(relay_member_cap)"; then
    ceiling=$(( relay_cap < max ? relay_cap : max ))
    if (( relay_cap < max )); then
      log "relay upstream carries at most $relay_cap member(s), below MaxSize=$max; using $ceiling as the growth ceiling"
    fi
  else
    ceiling="$max"
    warn "could not read the relay list cap on $MASTER_HOST (/usr/local/sbin/ecs-fleet-sync); planning against MaxSize=$max alone - if that cap is lower, the new instance will never reach the relay tier"
  fi
  if [[ -n "$requested_batch" ]]; then
    case "$requested_batch" in
      ''|*[!0-9]*) error "batch size must be a positive integer (got '$requested_batch')"; return 1 ;;
    esac
    batch="$requested_batch"
  else
    # No size asked for: use every slot the ceilings allow.
    batch=$(( ceiling - stable ))
  fi
  if (( batch < 1 )); then
    if [[ -n "$requested_batch" ]]; then
      error "batch size must be at least 1 (got $batch)"
    else
      error "no headroom to batch: DesiredCapacity=$stable growth ceiling=$ceiling (MaxSize=$max, relay cap=${relay_cap:-unknown}) - raise the smaller one, or roll one instance per release"
    fi
    return 1
  fi
  # Both ceilings are hard, so an over-large request is refused rather than
  # silently clamped: an operator who asked for 8 wants 8, and quietly replacing
  # 4 would misreport how wide the blast radius was.
  if (( batch > ceiling - stable )); then
    error "batch size $batch exceeds the headroom (growth ceiling=$ceiling DesiredCapacity=$stable, so at most $(( ceiling - stable )); MaxSize=$max relay cap=${relay_cap:-unknown})"
    return 1
  fi
  if [[ -n "$retire_ids" ]]; then
    # The caller already replaced some instances (deploy-release rolls one
    # before calling this). Retiring "whatever is in service right now" would
    # re-replace the instance that release just rolled, so the caller hands in
    # the set it means to retire: the fleet as it stood before that roll.
    while IFS= read -r id; do
      [ -n "$id" ] && original_ids+=("$id")
    done <<<"$retire_ids"
    [[ "${#original_ids[@]}" -gt 0 ]] || { error "rollout-batch was given an empty retire set"; return 1; }
  else
    current_ids="$(in_service_instance_ids)" || { error "could not read the current in-service instance set"; return 1; }
    [[ -n "$current_ids" ]] || { error "the scaling group reports no in-service instance"; return 1; }
    while IFS= read -r id; do
      [ -n "$id" ] && original_ids+=("$id")
    done <<<"$current_ids"
  fi
  # Every failure path must hand back the capacity the site actually runs with.
  ROLLOUT_STABLE_CAPACITY="$stable"
  round_limit=$(( ${#original_ids[@]} * 2 + 2 ))
  log "batch rollout: ${#original_ids[@]} instance(s) to retire, up to $batch per round (stable=$stable max=$max)"

  while :; do
    current_ids="$(in_service_instance_ids)" || { error "could not re-read the in-service instance set"; return 1; }
    remaining=()
    for id in "${original_ids[@]}"; do
      printf '%s\n' "$current_ids" | grep -qxF "$id" && remaining+=("$id")
    done
    if (( ${#remaining[@]} == 0 )); then
      break
    fi
    rounds=$(( rounds + 1 ))
    if (( rounds > round_limit )); then
      error "batch rollout did not converge after $rounds round(s); still serving ${remaining[*]}"
      rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
      return 1
    fi
    # Size the round against what is ACTUALLY in service, not against the steady
    # state this run found at its start. The tier's own alarms add instances
    # without asking (cpu-out-70 -> +2), and a downstream ramp during a release
    # will trigger them; "scale to stable + k" would then scale IN, create no new
    # instance, and fail the round - a rollback caused by the autoscaler doing its
    # job. The extra instances are left in place for the scale-in alarm to trim.
    current_count=$(printf '%s\n' "$current_ids" | grep -c . || true)
    base="$stable"
    (( current_count > base )) && base="$current_count"
    want=$(( base + (batch < ${#remaining[@]} ? batch : ${#remaining[@]}) ))
    log "round $rounds: replacing up to $(( want - base )) of ${#remaining[@]} remaining pre-existing instance(s) via $want instances (steady state $base)"
    # The autoscaler may already have consumed this round's headroom: growing past
    # the relay cap creates an instance the relay never carries (the .42 failure),
    # and past MaxSize ESS simply refuses. Refuse loudly before moving anything.
    if (( want > ceiling )); then
      error "round $rounds: no headroom for this round's instance: $base in service against a growth ceiling of $ceiling (MaxSize=$max, relay cap=${relay_cap:-unknown})"
      rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
      return 1
    fi
    if ! scale_group "$want" || ! wait_healthy_instances "$want"; then
      rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
      return 1
    fi
    new_ids=()
    while IFS= read -r id; do
      [ -n "$id" ] || continue
      printf '%s\n' "$current_ids" | grep -qxF "$id" || new_ids+=("$id")
    done <<<"$(in_service_instance_ids)"
    if (( ${#new_ids[@]} == 0 )); then
      error "round $rounds: ESS reported no new instance after scaling to $want"
      rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
      return 1
    fi
    for id in "${new_ids[@]}"; do
      if ! ip="$(instance_private_ip "$id")" || ! is_valid_ipv4 "$ip"; then
        error "round $rounds: could not read a valid private IP for new instance $id"
        rollback_failed_rollout "$id" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
        return 1
      fi
      # Gate on the application and then on the relay tier picking it up, one
      # instance at a time: a batch is only safe if every member of it is.
      if ! wait_app_ready "$id" "$ip"; then
        rollback_failed_rollout "$id" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
        return 1
      fi
      if ! wait_ecs_upstream_converged "$ip"; then
        rollback_failed_rollout "$id" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
        return 1
      fi
    done
    # The retire set is fixed BY ID once, here: the same set is parked, removed
    # and waited for (H2). Retiring "everything above the steady state" let an
    # external scale-out during the 1900s hold turn the count-derived removal
    # into MORE instances than were parked - the round then reported success
    # while unparked members were cut - and made the wait condition count
    # departures from a different set than the one parked.
    if ! scalein_before_ids="$(in_service_instance_ids)"; then
      # Fail closed (#4): an empty snapshot made retiring=0 and the round shrink
      # to base with nothing parked at all.
      error "round $rounds: could not re-read the in-service set; refusing to shrink without one"
      rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
      return 1
    fi
    scalein_before=$(printf '%s\n' "$scalein_before_ids" | grep -c . || true)
    retiring=$(( scalein_before > base ? scalein_before - base : 0 ))
    retire_ids="" drain_ips=""
    if (( retiring > 0 )); then
      retire_ids="$(oldest_instance_ids "$retiring" || true)"
      # Fail closed: retiring an instance we could not park (the API failed, or
      # the marker write failed, or fleet-sync never consumed it) cuts that
      # instance's live streams - the exact failure the drain exists to prevent.
      # The round rolls back instead; the tier stays on the previous version.
      if [[ -z "$retire_ids" ]]; then
        error "round $rounds: no instance resolved to park (retiring=$retiring); refusing to retire an unparked instance"
        rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
        return 1
      fi
      while IFS= read -r id; do
        [ -n "$id" ] || continue
        if ! ip="$(instance_private_ip "$id")" || ! is_valid_ipv4 "$ip"; then
          error "round $rounds: could not resolve a valid private IP for retiree $id; refusing to retire an unparked instance"
          rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
          return 1
        fi
        drain_ips+="${drain_ips:+$'\n'}$ip"
      done <<<"$retire_ids"
      local drain_comma; drain_comma="$(printf '%s\n' "$drain_ips" | paste -sd, -)"
      if ! relay_drain_write "$drain_comma"; then
        error "round $rounds: could not write the relay drain marker; refusing to retire an unparked instance"
        rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
        return 1
      fi
      if ! wait_drain_applied "$drain_comma"; then
        relay_drain_clear >/dev/null 2>&1 || true
        rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
        return 1
      fi
      log "round $rounds: parked the retiring instance(s) in the relay drain file: $(printf '%s' "$drain_ips" | tr '\n' ' ')"
    fi
    log "round $rounds: drain-first hold ${ML_DRAIN_SECONDS}s so in-flight streams on the retiring instances finish"
    sleep_with_heartbeat "$ML_DRAIN_SECONDS"
    # Scale back by exactly the parked set and KEEP anything that joined
    # meanwhile: target = current in-service minus the parked members that are
    # still there (floored at the steady state). Forcing the group to `base`
    # instead would remove (current - base) instances - more than were parked
    # whenever an external scale-out raised the count during the hold - and
    # unparked members would be cut.
    if [[ -n "$retire_ids" ]]; then
      if ! now_ids="$(in_service_instance_ids)"; then
        error "round $rounds: could not re-read the in-service set; refusing to shrink without one"
        rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
        return 1
      fi
      now_count=$(printf '%s\n' "$now_ids" | grep -c . || true)
      live=0
      while IFS= read -r id; do
        [ -n "$id" ] || continue
        printf '%s\n' "$now_ids" | grep -qxF "$id" && live=$(( live + 1 ))
      done <<<"$retire_ids"
      target=$(( now_count - live ))
      (( target < base )) && target="$base"
    else
      target="$base"
    fi
    if ! scale_group "$target" || ! wait_retired "$retire_ids" \
       || ! verify_no_extra_removals "$now_ids" "$retire_ids" "round $rounds"; then
      rollback_failed_rollout "" "$previous_digest" "$previous_snapshot" || warn "rollback could not be verified"
      return 1
    fi
    # Scaling in is asynchronous: the group keeps reporting the removed
    # instances for a while after ModifyScalingGroup (in Removing state, then
    # gone). The gate below is the exact parked set leaving the group ENTIRELY,
    # so the next round's scale-out cannot supersede a removal that is still
    # in flight - the race that killed v1.0.0-rc.40-tokeness-mainland.42.
    if [[ "$SWAS_PANEL_TIER" == "1" ]]; then
      ml_drain_end || warn "drain marker left behind; ml-sync expires it on its own"
    fi
  done

  log "batch rollout: retired all ${#original_ids[@]} pre-existing instance(s) in $rounds round(s)"
  # Report what the tier converged onto rather than asserting it: the version
  # probe is advisory, and an instance that cannot answer it must not fail a
  # rollout whose whole point - retiring the old instances - already happened.
  while IFS= read -r id; do
    [ -n "$id" ] || continue
    ip="$(instance_private_ip "$id" 2>/dev/null || true)"
    [ -n "$ip" ] || continue
    log "  $id ($ip) reports $(app_version_on "$ip" || echo 'version unavailable')"
  done <<<"$(in_service_instance_ids)"
  if [[ -n "$expected_version" ]]; then
    log "batch rollout target version was $expected_version"
  fi
  if ! wait_verify_converged; then
    error "post-rollout verify failed after the fleet converged onto $digest"
    return 1
  fi
  # Advisory, exactly as in ess_rollout: a binding failure does not undo the roll.
  converge_eip_bandwidth || warn "EIP shared-bandwidth convergence failed; rerun deploy.sh eip-sync"
  return 0
}

# rollout_all - the manual "get the tier converged now" form: every slot MaxSize
# allows, in as few drain windows as the group can take. Exposed because the
# release path deliberately does NOT use it - see rollout_batch's default.
rollout_all() {
  local digest="$1" expected_version="${2:-}" previous_digest="${3:-}" previous_snapshot="${4:-}" retire_ids="${5:-}"
  rollout_batch "$digest" "$expected_version" "$previous_digest" "$previous_snapshot" "$retire_ids" ""
}

# rollout_verb <batch_size> <tag> <digest> - shared body of `rollout-all` and
# `rollout-batch`. An empty batch_size means "every slot MaxSize allows".
#
# With no tag and no digest it converges onto whatever the scaling configuration
# already pins: the "finish the job" form a release leaves behind, which changes
# no configuration and therefore has nothing to roll back to. With a tag it pins
# the digest and rolls the master FIRST - the fleet must never serve an image the
# master (panel + migrations + system tasks) has not taken yet.
rollout_verb() {
  local batch="$1" tag="$2" digest="$3" snapshot='' prev=''
  # The tier's scale-in alarm must not trim instances while a round is draining.
  suspend_scale_in_guard
  trap 'resume_scale_in_guard' EXIT
  if [[ -z "$tag" && -z "$digest" ]]; then
    snapshot="$(oss_scaling_config_json)" || die "failed to snapshot the current scaling configuration"
    digest="$(snapshot_config_digest "$snapshot")"
    prev="$digest"
    snapshot=''
  else
    if [[ -n "$digest" ]]; then
      [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "certified digest must be sha256:<64 lowercase hex>"
    else
      digest="$(resolve_ml_digest "$tag")"
    fi
    snapshot="$(oss_scaling_config_json)" || die "failed to snapshot the current scaling configuration"
    prev="$(snapshot_config_digest "$snapshot")"
    [[ "$prev" =~ ^sha256:[0-9a-f]{64}$ ]] || die "current scaling configuration image is not a valid digest; refusing a batch rollout without a rollback target"
    if ! apply_ml_digest "$digest" "$snapshot"; then
      restore_scaling_config "$snapshot" || die "image pin failed and the complete configuration could not be restored"
      die "batch rollout: pinning $digest failed; the previous configuration was restored"
    fi
    if ! sync_master_container "$tag" "$digest"; then
      restore_scaling_config "$snapshot" || error "master roll failed and the scaling configuration could not be restored"
      sync_master_container "" || error "master container could not be restored; manual intervention required (deploy.sh sync-host)"
      die "batch rollout aborted: master container failed to converge to $tag"
    fi
  fi
  if ! rollout_batch "$digest" "$tag" "$prev" "$snapshot" "" "$batch"; then
    die "batch rollout failed; rollback was attempted and must be verified before retrying"
  fi
  log "batch rollout: the ECI tier now serves $digest"
}

# resolve_release_batch - how many instances a release's batch convergence may
# replace per drain window. Prints 0 to skip convergence entirely.
#
# The distinction that matters here: an EXPLICIT ROLLOUT_BATCH is a demand, so an
# impossible one is refused (the caller does this before anything mutates); the
# built-in default is a preference, so it is clamped to the group's headroom and
# degrades to "skip" on a group that cannot batch at all. A release must never
# fail because of the shape of its scaling group.
resolve_release_batch() {
  local requested="${ROLLOUT_BATCH-}" batch stable max headroom relay_cap
  # Empty means "unset" and takes the built-in default; anything non-numeric is
  # an operator error. The two must not share a case branch: an unset variable is
  # the normal case, and failing on it would break every release.
  if [[ -n "$requested" ]]; then
    case "$requested" in
      *[!0-9]*) error "ROLLOUT_BATCH must be a non-negative integer (0 disables the batch convergence)"; return 1 ;;
    esac
  fi
  if [[ -z "$requested" ]]; then
    batch=2
  else
    batch="$requested"
  fi
  if (( batch == 0 )); then
    printf '0\n'
    return 0
  fi
  stable="$(current_desired_capacity)" || { error "could not read the scaling group's desired capacity"; return 1; }
  max="$(current_max_size)" || { error "could not read the scaling group's MaxSize"; return 1; }
  # The entry's relay list is the second ceiling (see relay_member_cap): the
  # release's own single-instance roll needs stable+1 members carried, so a cap
  # at or below the steady state cannot serve ANY rollout.
  if relay_cap="$(relay_member_cap)"; then
    if (( relay_cap < stable + 1 )); then
      error "the relay upstream can carry $relay_cap member(s) but the tier runs $stable: raise MAX_MEMBERS on the entry (or lower DesiredCapacity) before releasing - the new instance would never reach the relay tier"
      return 1
    fi
    (( relay_cap < max )) && max="$relay_cap"
  fi
  headroom=$(( max - stable ))
  if (( headroom < 1 )); then
    if [[ -n "$requested" ]]; then
      error "ROLLOUT_BATCH=$batch cannot run: MaxSize=$max DesiredCapacity=$stable leaves no headroom (use ROLLOUT_BATCH=0 to release one instance at a time)"
      return 1
    fi
    # stderr, not stdout: this function's stdout is the batch size the caller
    # captures, and log()/warn() write to stdout by design. A WARN here once got
    # parsed as part of the value and turned a graceful skip into a failed
    # release.
    warn "no batch headroom (MaxSize=$max DesiredCapacity=$stable): releasing one instance and leaving the rest of the tier on the one-at-a-time path" >&2
    printf '0\n'
    return 0
  fi
  if (( batch > headroom )); then
    if [[ -n "$requested" ]]; then
      error "ROLLOUT_BATCH=$batch exceeds the growth ceiling (relay cap=${relay_cap:-unknown}, MaxSize=$max, DesiredCapacity=$stable, so at most $headroom)"
      return 1
    fi
    batch="$headroom"
  fi
  printf '%s\n' "$batch"
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
  deploy.sh rollout-all [tag] [sha256:DIGEST]
                     # converge the WHOLE ECI tier now, using every slot
                     # MaxSize allows (manual emergency form)
  deploy.sh rollout-batch [N] [tag] [sha256:DIGEST]
                     # same convergence with N instances per drain window
                     # (default $ROLLOUT_BATCH, 2); 0 is refused, over-large is
                     # refused rather than clamped. With no tag/digest it
                     # converges onto the digest the scaling config already pins
  deploy.sh rollback <sha256:DIGEST>
  deploy.sh sync-host          # re-roll the master container (alias: sync-master)
  deploy.sh eip-sync
USAGE
}

# validate_shutdown_budget - refuse a configuration whose platform grace period
# cannot contain the application's own drain. The application serves in-flight
# requests for APP_SHUTDOWN_TIMEOUT_SECONDS and THEN flushes background batches
# (log, quota, observability) on their own timeout. Setting the two numbers
# equal - the obvious reading of "set both to 180" - puts the SIGKILL in the
# middle of that flush, losing the last log and quota window on every release.
validate_shutdown_budget() {
  local need=$((APP_SHUTDOWN_TIMEOUT_SECONDS + APP_BACKGROUND_DRAIN_ALLOWANCE_SECONDS))
  if (( ECI_TERMINATION_GRACE_SECONDS < need )); then
    die "ECI_TERMINATION_GRACE_SECONDS ($ECI_TERMINATION_GRACE_SECONDS) must be at least APP_SHUTDOWN_TIMEOUT_SECONDS + ${APP_BACKGROUND_DRAIN_ALLOWANCE_SECONDS}s background flush ($need); raise the grace period or lower the drain"
  fi
  local drain_need=$((ML_DRAIN_CONVERGE_ATTEMPTS * ML_DRAIN_CONVERGE_DELAY_SECONDS + ML_DRAIN_SECONDS + 120))
  if (( ML_DRAIN_MARKER_TTL_SECONDS < drain_need )); then
    die "ML_DRAIN_MARKER_TTL_SECONDS ($ML_DRAIN_MARKER_TTL_SECONDS) must exceed convergence + drain ($drain_need) or the pin expires mid-rollout"
  fi
}

main() {
  local operation="${1:-verify}"
  validate_shutdown_budget
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
      # Resolve the convergence switch before anything mutates: an operator typo,
      # or a batch size this group cannot serve, must not be discovered after the
      # tier has already started moving.
      local release_batch
      release_batch="$(resolve_release_batch)" || die "the release's batch convergence is not usable as configured"
      # Headroom gate BEFORE anything mutates: every roll path scales the tier to
      # at least desired+1 while the old instance still serves, and ESS rejects a
      # DesiredCapacity above MaxSize. Refusing after the config pin and the
      # master blue-green flip left those restored against the NEW image - the
      # rollback target itself was already wrong.
      local pre_stable pre_max
      pre_stable="$(current_desired_capacity)" || die "could not read the scaling group's desired capacity before the release"
      pre_max="$(current_max_size)" || die "could not read the scaling group's MaxSize before the release"
      if (( pre_stable + 1 > pre_max )); then
        die "no headroom to roll: DesiredCapacity=$pre_stable is already at MaxSize=$pre_max; raise MaxSize or retire capacity before releasing"
      fi
      # Everything below mutates the tier; hold the scale-in alarm off until the
      # release has finished (the trap fires on the die paths too).
      suspend_scale_in_guard
      trap 'resume_scale_in_guard' EXIT
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
      # The fleet as it stood BEFORE this release's single-instance roll. The
      # batch convergence below retires exactly this set, so it never re-replaces
      # the instance ess_rollout just brought onto the new image.
      local release_before_ids
      release_before_ids="$(in_service_instance_ids)" \
        || die "could not read the in-service instance set before the release"
      if ! apply_ml_digest "$release_digest" "$previous_snapshot"; then
        if ! restore_scaling_config "$previous_snapshot"; then
          die "release update failed and the complete scaling configuration could not be restored"
        fi
        die "release update failed; the previous scaling configuration was restored"
      fi
      # Master-first: the master container (panel + the only node that runs
      # migrations and system tasks) takes the new image before the ECI tier
      # rolls. Its bootstrap readiness gate + version check double as the
      # canary; a failure here aborts while the ECI tier is still serving the
      # previous image untouched.
      if ! sync_master_container "$release_tag" "$release_digest"; then
        if ! restore_scaling_config "$previous_snapshot"; then
          error "master roll failed and the scaling configuration could not be restored"
        fi
        if ! sync_master_container ""; then
          error "master container could not be restored to the previous digest; manual intervention required (deploy.sh sync-host)"
        fi
        die "release aborted: master container failed to converge to $release_tag"
      fi
      if ! ess_rollout "$release_digest" "$previous_digest" "$previous_snapshot"; then
        # rollback_failed_rollout re-pinned the scaling configuration + ECI to
        # the previous digest; bring the master back in line so node versions
        # never drift after an aborted release.
        if ! sync_master_container ""; then
          error "master container could not be restored after the failed rollout; manual intervention required (deploy.sh sync-host)"
        fi
        die "release rollout failed; rollback was attempted and must be verified before retrying"
      fi
      # Converge the WHOLE tier, not just the single instance ess_rollout
      # replaced. Anything the image carries on the ECI tier - the
      # OpenAI-compatible billing endpoints, for one - would otherwise serve
      # only 1/n of its traffic until n releases accumulate.
      # ROLLOUT_BATCH=0 opts out: a release that only changes master-served paths
      # (panel, migrations, system tasks) pays ~1 drain window per batch round
      # for nothing. The tier then converges on the next default release, or on
      # an explicit `deploy.sh rollout-all`.
      if (( release_batch == 0 )); then
        log "release $release_tag -> $release_digest deployed to one instance (batch convergence skipped: the rest of the tier keeps its current image until the next default release or an explicit rollout-all)"
      else
        if ! rollout_batch "$release_digest" "$release_tag" "$previous_digest" "$previous_snapshot" "$release_before_ids" "$release_batch"; then
          # rollout_batch already restored the scaling configuration and the
          # steady-state capacity on every failure path; keep the master on that
          # same (previous) digest so node versions never drift.
          if ! sync_master_container ""; then
            error "master container could not be restored after the failed batch rollout; manual intervention required (deploy.sh sync-host)"
          fi
          die "release batch rollout failed; rollback was attempted and must be verified before retrying"
        fi
        log "release $release_tag -> $release_digest deployed to the whole ECI tier (batches of $release_batch)"
      fi
      ;;
    sync-host|sync-master)
      [[ $# -eq 1 ]] || die "sync-host does not accept arguments"
      sync_master_container ""
      ;;
    rollout-all)
      # Manual emergency convergence: every slot MaxSize allows, in as few drain
      # windows as the group can take. The release path does not use this - it
      # asks for ROLLOUT_BATCH instead.
      [[ $# -le 3 ]] || die "rollout-all takes at most a version tag and a certified digest"
      rollout_verb "" "${2:-}" "${3:-}"
      ;;
    rollout-batch)
      [[ $# -le 4 ]] || die "rollout-batch takes an optional batch size, then an optional version tag and certified digest"
      local -a rb_rest=("${@:2}")
      local rb_batch="${ROLLOUT_BATCH:-2}"
      if [[ "${rb_rest[0]:-}" =~ ^[0-9]+$ ]]; then
        rb_batch="${rb_rest[0]}"
        rb_rest=("${rb_rest[@]:1}")
      fi
      [[ "${#rb_rest[@]}" -le 2 ]] || die "rollout-batch takes an optional batch size, then an optional version tag and certified digest"
      rollout_verb "$rb_batch" "${rb_rest[0]:-}" "${rb_rest[1]:-}"
      ;;
    eip-sync)
      [[ $# -eq 1 ]] || die "eip-sync does not accept arguments"
      converge_eip_bandwidth || die "EIP shared-bandwidth convergence failed; see the ERROR lines above for the specific cause"
      ;;
    rollback)
      [[ $# -eq 2 ]] || die "rollback requires one image digest (sha256:...)"
      local rollback_digest="$2" rollback_previous rollback_snapshot
      suspend_scale_in_guard
      trap 'resume_scale_in_guard' EXIT
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
      if ! sync_master_container "" "$rollback_digest"; then
        if ! restore_scaling_config "$rollback_snapshot"; then
          error "master roll failed and the scaling configuration could not be restored"
        fi
        if ! sync_master_container ""; then
          error "master container could not be restored to the previous digest; manual intervention required (deploy.sh sync-host)"
        fi
        die "rollback aborted: master container failed to converge to $rollback_digest"
      fi
      if ! ess_rollout "$rollback_digest" "$rollback_previous" "$rollback_snapshot"; then
        # rollback_failed_rollout re-pinned the scaling configuration + ECI to
        # the pre-rollback digest; keep the master on it too.
        if ! sync_master_container ""; then
          error "master container could not be restored after the failed rollout; manual intervention required (deploy.sh sync-host)"
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
