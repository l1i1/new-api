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
readonly EDGEONE_TEST_URL="${EDGEONE_TEST_URL:-https://tokeness.cn/api/status}"
readonly DIRECT_PROBE_URL="${DIRECT_PROBE_URL:-http://127.0.0.1/api/status}"
readonly VERIFY_TIMEOUT_SECONDS="${VERIFY_TIMEOUT_SECONDS:-45}"

log() { printf '[%s] %s\n' "$(date --iso-8601=seconds)" "$*"; }
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
    [[ "$octet" -le 255 ]] || return 1
  done
}

remote() {
  [[ -r "$SWAS_SSH_KEY_PATH" ]] || die "missing lightweight-server SSH key at $SWAS_SSH_KEY_PATH"
  local argument command=''
  for argument in "$@"; do
    printf -v argument '%q' "$argument"
    command+="${command:+ }$argument"
  done
  ssh \
    -i "$SWAS_SSH_KEY_PATH" \
    -o BatchMode=yes \
    -o ConnectTimeout=15 \
    -o StrictHostKeyChecking=no \
    "root@$SWAS_HOST" -- "$command"
}

get_upstream_ip() {
  local output
  output="$(remote awk -v name="$NGINX_UPSTREAM_NAME" -v port="$NGINX_UPSTREAM_PORT" '
    $0 ~ "^[[:space:]]*upstream[[:space:]]+" name "[[:space:]]*\\{" { inside = 1; next }
    inside && $0 ~ "^[[:space:]]*}" { inside = 0 }
    inside && $0 ~ "^[[:space:]]*server[[:space:]]+[0-9.]+:" port "[[:space:]]*;" {
      line = $0
      sub(/^[[:space:]]*server[[:space:]]+/, "", line)
      sub(/:.*/, "", line)
      print line
    }
  ' "$NGINX_CONF")" || return 1

  local -a addresses=()
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

  if ! direct_body="$(remote curl -fsS --connect-timeout 15 --max-time "$VERIFY_TIMEOUT_SECONDS" \
    -H 'Host: tokeness.cn' "$DIRECT_PROBE_URL")"; then
    error "lightweight server to ECI private chain failed"
    return 1
  fi
  validate_status_body "lightweight server to ECI private chain" "$direct_body" || return 1
  log "verify: OK (upstream=$upstream_ip)"
}

apply_upstream() {
  local target_ip="$1"
  remote bash -s -- "$NGINX_CONF" "$NGINX_UPSTREAM_NAME" "$NGINX_UPSTREAM_PORT" "$target_ip" <<'REMOTE_SCRIPT'
set -Eeuo pipefail
conf="$1"
name="$2"
port="$3"
target_ip="$4"
backup="$(mktemp "${conf}.tokeness-backup.XXXXXX")"
candidate="$(mktemp "${conf}.tokeness-candidate.XXXXXX")"
cp -p -- "$conf" "$backup"

if ! awk -v name="$name" -v port="$port" -v target_ip="$target_ip" '
  $0 ~ "^[[:space:]]*upstream[[:space:]]+" name "[[:space:]]*\\{" { inside = 1 }
  inside && $0 ~ "^[[:space:]]*server[[:space:]]+[0-9.]+:" port "[[:space:]]*;" {
    count++
    sub("[0-9.]+:" port, target_ip ":" port)
  }
  { print }
  inside && $0 ~ "^[[:space:]]*}" { inside = 0 }
  END { if (count != 1) exit 42 }
' "$conf" > "$candidate"; then
  rm -f -- "$candidate" "$backup"
  echo "expected exactly one active server in upstream $name" >&2
  exit 1
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
  exit 1
fi
if ! systemctl reload nginx; then
  cp -p -- "$backup" "$conf"
  if ! nginx -t || ! systemctl reload nginx; then
    echo "nginx reload failed and rollback could not be verified; backup retained at $backup" >&2
    exit 2
  fi
  rm -f -- "$backup"
  exit 1
fi
printf 'CHANGED\t%s\n' "$backup"
REMOTE_SCRIPT
}

restore_upstream() {
  local backup="$1"
  remote bash -s -- "$NGINX_CONF" "$backup" <<'REMOTE_SCRIPT'
set -Eeuo pipefail
conf="$1"
backup="$2"
test -f "$backup"
cp -p -- "$backup" "$conf"
nginx -t
systemctl reload nginx
rm -f -- "$backup"
REMOTE_SCRIPT
}

discard_backup() {
  remote rm -f -- "$1"
}

nginx_update() {
  local target_ip="$1" result backup=''
  is_valid_ipv4 "$target_ip" || die "invalid ECI private IPv4 address: $target_ip"

  result="$(apply_upstream "$target_ip")" || die "nginx upstream update failed; inspect the preceding rollback status before retrying"
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
      die "deployment verification failed; nginx upstream was rolled back"
    fi
    die "deployment verification failed"
  fi

  [[ -z "$backup" ]] || discard_backup "$backup"
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
