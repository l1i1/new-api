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

readonly REMOTE_RUN_DIR='/run/lock'
readonly REMOTE_LOCK_NAME='tokeness-cn-deploy.lock'

log() { printf '[%s] %s\n' "$(date --iso-8601=seconds)" "$*"; }
warn() { log "WARN: $*"; }
error() { log "ERROR: $*" >&2; }
die() { error "$*"; exit 1; }

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

usage() {
  cat <<'USAGE'
Usage:
  deploy.sh verify
  deploy.sh nginx-update <ECI_PRIVATE_IP>
  deploy.sh image-ref <sha256:DIGEST>
USAGE
}

main() {
  local operation="${1:-verify}"
  case "$operation" in
    verify)
      [[ $# -eq 1 ]] || die "verify does not accept arguments"
      verify_node || exit 1
      ;;
    nginx-update)
      [[ $# -eq 2 ]] || die "nginx-update requires one ECI private IP"
      nginx_update "$2"
      ;;
    image-ref)
      [[ $# -eq 2 ]] || die "image-ref requires one image digest"
      image_ref "$2"
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
