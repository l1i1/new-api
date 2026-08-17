#!/usr/bin/env bash
set -Eeuo pipefail

readonly TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REMOTE_SCRIPT="$TEST_DIR/../remote-command.sh"
readonly OLD_IMAGE='ghcr.io/l1i1/new-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
readonly NEW_IMAGE='ghcr.io/l1i1/new-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'

fail_test() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

source "$REMOTE_SCRIPT"
[[ "$(bash "$REMOTE_SCRIPT" --version)" == '2026-08-17.4' ]] ||
  fail_test "remote command version handshake is missing"
eval "$(declare -f blue_green_proxy_reload | sed '1s/blue_green_proxy_reload/real_blue_green_proxy_reload/')"
eval "$(declare -f blue_green_remove_container | sed '1s/blue_green_remove_container/real_blue_green_remove_container/')"
eval "$(declare -f discover_blue_green_proxy_container | sed '1s/discover_blue_green_proxy_container/real_discover_blue_green_proxy_container/')"
eval "$(declare -f blue_green_verify_container_binding | sed '1s/blue_green_verify_container_binding/real_blue_green_verify_container_binding/')"
eval "$(declare -f blue_green_verify_live_route | sed '1s/blue_green_verify_live_route/real_blue_green_verify_live_route/')"
eval "$(declare -f blue_green_proxy_route_status | sed '1s/blue_green_proxy_route_status/real_blue_green_proxy_route_status/')"
eval "$(declare -f drain_blue_green_container | sed '1s/drain_blue_green_container/real_drain_blue_green_container/')"
eval "$(declare -f wait_for_blue_green_container | sed '1s/wait_for_blue_green_container/real_wait_for_blue_green_container/')"

(
  compose_log="$test_root/standby-compose.args"
  BLUE_GREEN_NEW_PORT=8202
  BLUE_GREEN_NEW_CONTAINER='new-api-blue'
  SERVICE_NAME='new-api'
  blue_green_remove_container() { :; }
  compose_timed() {
    shift
    printf '%s\n' "$@" > "$compose_log"
  }
  wait_for_blue_green_container() { :; }
  run_timed() { :; }
  start_blue_green_standby "$NEW_IMAGE"
  mapfile -t compose_args < "$compose_log"
  [[ " ${compose_args[*]} " == *' run -d --no-deps --use-aliases '* ]] ||
    fail_test "standby container did not retain Compose service aliases"
)

case_dir="$test_root/proxy"
mkdir -p "$case_dir"
DEPLOY_DIR="$case_dir"
SERVICE_NAME='new-api'
BLUE_GREEN_STATE_FILE="$case_dir/.blue-green.state"
BLUE_GREEN_PROXY_FILES=("$case_dir/site.conf")
printf '%s\n' \
  'server {' \
  '    location / {' \
	'        # TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
  '        proxy_pass http://127.0.0.1:8201;' \
  '    }' \
  '}' > "$case_dir/site.conf"

# Keep the test independent from Docker and OpenResty while exercising the
# exact file rewrite and rollback path used by production.
discover_blue_green_proxy_container() { :; }
discover_blue_green_proxy_files() { :; }
TEST_PROXY_RELOAD_CALLS=0
TEST_PROXY_RELOAD_LOG="$test_root/proxy-reload.calls"
: > "$TEST_PROXY_RELOAD_LOG"
blue_green_proxy_reload() {
  TEST_PROXY_RELOAD_CALLS=$((TEST_PROXY_RELOAD_CALLS + 1))
  printf 'reload\n' >> "$TEST_PROXY_RELOAD_LOG"
  if [[ -n "${TEST_PROXY_RELOAD_SEQUENCE:-}" ]]; then
    read -r TEST_PROXY_RELOAD_STATUS TEST_PROXY_RELOAD_SEQUENCE <<< "$TEST_PROXY_RELOAD_SEQUENCE"
    return "${TEST_PROXY_RELOAD_STATUS:-0}"
  fi
  return "${TEST_PROXY_RELOAD_STATUS:-0}"
}
blue_green_verify_container_binding() { :; }
TEST_STATUS_BODY='{"success":true,"version":"blue-green-test","instance_id":"test-instance","start_time":100}'
blue_green_proxy_route_status() { printf '%s\n' "${TEST_ROUTE_STATUS_BODY:-$TEST_STATUS_BODY}"; }
status_json() { printf '%s\n' "$TEST_STATUS_BODY"; }

(
  fake_bin="$test_root/route-probe-bin"
  mkdir -p "$fake_bin"
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf '\''{"success":true,"data":{"version":"route-probe"},"instance_id":"route-instance"}\n'\''' \
    > "$fake_bin/curl"
  chmod +x "$fake_bin/curl"
  export PATH="$fake_bin:$PATH"
  BLUE_GREEN_PROXY_CONTAINER='proxy-container'
  discover_blue_green_proxy_container() { :; }
  run_timed() {
    shift
    [[ "$1" == docker && "$2" == exec && "$3" == 'proxy-container' ]] || return 1
    shift 3
    "$@"
  }
  route_probe_output="$(real_blue_green_proxy_route_status api.example.test)"
  grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<< "$route_probe_output" ||
    fail_test "OpenResty route probe rejected a healthy JSON response"
)

(
  BLUE_GREEN_PROXY_CONTAINER=''
  run_timed() {
    shift
    if [[ "$1" == docker && "$2" == ps ]]; then
      printf '%s\n' $'proxy-a\topenresty:latest' $'proxy-b\tnginx:latest'
      return 0
    fi
    if [[ "$1" == docker && "$2" == inspect ]]; then
      case "${@: -1}" in
        proxy-a) printf '%s\n' $'/opt/1panel/www\t/www/sites' ;;
        proxy-b) printf '%s\n' $'/other/sites\t/www/sites' ;;
      esac
      return 0
    fi
    return 1
  }
  real_discover_blue_green_proxy_container
  [[ "$BLUE_GREEN_PROXY_CONTAINER" == 'proxy-a' ]] ||
    fail_test "OpenResty discovery did not select the unique site-root mount"
)

set +e
(
  BLUE_GREEN_PROXY_CONTAINER=''
  run_timed() {
    shift
    if [[ "$1" == docker && "$2" == ps ]]; then
      printf '%s\n' $'proxy-a\topenresty:latest' $'proxy-b\tnginx:latest'
      return 0
    fi
    if [[ "$1" == docker && "$2" == inspect ]]; then
      printf '%s\n' $'/opt/1panel/www\t/www/sites'
      return 0
    fi
    return 1
  }
  real_discover_blue_green_proxy_container
) >/dev/null 2>&1
ambiguous_proxy_status=$?
set -e
[[ "$ambiguous_proxy_status" -ne 0 ]] ||
  fail_test "ambiguous OpenResty site-root mounts were accepted"

(
  run_timed() {
    shift
    if [[ "$*" == *'.Config.Image'* ]]; then
      printf '%s\n' $'container-id\t/new-api-blue\t'"$NEW_IMAGE"
      return 0
    fi
    if [[ "$*" == *'.NetworkSettings.Ports'* ]]; then
      printf '%s\n' $'127.0.0.1\t8202'
      return 0
    fi
    return 1
  }
  real_blue_green_verify_container_binding new-api-blue "$NEW_IMAGE" 8202
)

(
  run_timed() {
    shift
    if [[ "$*" == *'.Config.Image'* ]]; then
      printf '%s\n' $'container-id\t/new-api-blue\t'"$NEW_IMAGE"
      return 0
    fi
    if [[ "$*" == *'.NetworkSettings.Ports'* ]]; then
      printf '%s\n' $'127.0.0.1\t8202'
      return 0
    fi
    return 1
  }
  real_blue_green_verify_container_binding container-id "$NEW_IMAGE" 8202
)

set +e
(
  run_timed() {
    shift
    if [[ "$*" == *'.Config.Image'* ]]; then
      printf '%s\n' $'container-id\t/new-api-blue\t'"$NEW_IMAGE"
      return 0
    fi
    if [[ "$*" == *'.NetworkSettings.Ports'* ]]; then
      printf '%s\n' $'127.0.0.1\t8201'
      return 0
    fi
    return 1
  }
  real_blue_green_verify_container_binding new-api-blue "$NEW_IMAGE" 8202
) >/dev/null 2>&1
wrong_binding_status=$?
set -e
[[ "$wrong_binding_status" -ne 0 ]] ||
  fail_test "blue-green target identity accepted the wrong published port"

route_site="$test_root/sites/api.example.test/proxy"
mkdir -p "$route_site"
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=route.example.test' 'proxy_pass http://127.0.0.1:8201;' > "$route_site/proxy.conf"
BLUE_GREEN_PROXY_FILES=("$route_site/proxy.conf")
[[ "$(blue_green_proxy_route_host)" == 'route.example.test' ]] ||
	fail_test "live route probe did not read the explicit marker hostname"
BLUE_GREEN_PROXY_FILES=("$case_dir/site.conf")

ensure_blue_green_proxy 8201
grep -q "# $BLUE_GREEN_PROXY_MARKER" "$case_dir/site.conf" ||
  fail_test "initial proxy bootstrap did not preserve the management marker"
grep -q 'proxy_pass http://127.0.0.1:8201;' "$case_dir/site.conf" ||
  fail_test "initial proxy bootstrap changed the active port"

set +e
identity_mismatch_output="$(
  TEST_ROUTE_STATUS_BODY='{"success":true,"version":"blue-green-test","instance_id":"other-instance","start_time":100}'
  sleep() { :; }
  blue_green_verify_live_route 8201 new-api-blue "$NEW_IMAGE" 2>&1
)"
identity_mismatch_status=$?
set -e
[[ "$identity_mismatch_status" -ne 0 ]] ||
  fail_test "live route accepted a different container identity"

switch_blue_green_proxy 8202
grep -q 'proxy_pass http://127.0.0.1:8202;' "$case_dir/site.conf" ||
  fail_test "proxy switch did not select the standby port"
[[ "$BLUE_GREEN_SWITCHED" -eq 1 ]] || fail_test "proxy switch did not commit its transaction state"

blue_green_restore_proxy_backup || fail_test "proxy backup restore failed"
grep -q 'proxy_pass http://127.0.0.1:8201;' "$case_dir/site.conf" ||
  fail_test "proxy backup restore did not return to the old port"

TEST_PROXY_RELOAD_SEQUENCE='1 0'
set +e
reload_after_apply_output="$(switch_blue_green_proxy 8202 2>&1)"
reload_after_apply_status=$?
set -e
unset TEST_PROXY_RELOAD_SEQUENCE
[[ "$reload_after_apply_status" -ne 0 ]] ||
  fail_test "reload failure after applying the new route was accepted"
grep -q 'proxy_pass http://127.0.0.1:8201;' "$case_dir/site.conf" ||
  fail_test "reload failure after applying the new route left the new backend active"

bootstrap_without_marker="$test_root/bootstrap-without-marker.conf"
printf '%s\n' 'proxy_pass http://127.0.0.1:8201;' > "$bootstrap_without_marker"
BLUE_GREEN_PROXY_FILES=("$bootstrap_without_marker")
set +e
bootstrap_without_marker_output="$(ensure_blue_green_proxy 8201 2>&1)"
bootstrap_without_marker_status=$?
set -e
[[ "$bootstrap_without_marker_status" -ne 0 ]] ||
  fail_test "initial bootstrap inferred an unmarked proxy from its port"
grep -q 'active port could not be determined' <<< "$bootstrap_without_marker_output" ||
	fail_test "unmarked bootstrap did not fail closed"

invalid_marker_root="$test_root/invalid-marker"
mkdir -p "$invalid_marker_root"
printf '%s\n' \
  '# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
  '' \
  'proxy_pass http://127.0.0.1:8201;' > "$invalid_marker_root/proxy.conf"
set +e
blue_green_managed_proxy_entries "$invalid_marker_root/proxy.conf" >/dev/null 2>&1
invalid_marker_status=$?
set -e
[[ "$invalid_marker_status" -ne 0 ]] ||
  fail_test "an unpaired blue-green marker was silently ignored"

BLUE_GREEN_PROXY_FILES=("$case_dir/site.conf")

write_blue_green_state 8202 new-api-blue "$NEW_IMAGE"
read_blue_green_state
[[ "$BLUE_GREEN_ACTIVE_PORT" == 8202 ]] || fail_test "state parser lost the active port"
[[ "$BLUE_GREEN_ACTIVE_CONTAINER" == new-api-blue ]] || fail_test "state parser lost the active container"

recovery_case="$test_root/recovery"
mkdir -p "$recovery_case"
DEPLOY_DIR="$recovery_case"
RELEASE_ENV="$recovery_case/release.env"
BLUE_GREEN_STATE_FILE="$recovery_case/.blue-green.state"
SERVICE_NAME='new-api'
printf 'NEW_API_IMAGE=%s\n' "$OLD_IMAGE" > "$RELEASE_ENV"
BLUE_GREEN_STATE_BASELINE_PRESENT=0
write_blue_green_pending_state 8201 new-api "$OLD_IMAGE" 0 8202 new-api-blue "$NEW_IMAGE"
read_blue_green_state
blue_green_proxy_port() { printf '8202\n'; }
wait_for_blue_green_container() { return 0; }
wait_for_ready() { return 0; }
blue_green_remove_container() { :; }

(
  worker_generation_file="$test_root/proxy-worker-generation.calls"
  blue_green_proxy_worker_pids() {
    if [[ ! -f "$worker_generation_file" ]]; then
      : > "$worker_generation_file"
      printf '10 11\n'
    else
      printf '20 21\n'
    fi
  }
  blue_green_proxy_exec() { :; }
  sleep() { :; }
  real_blue_green_proxy_reload ||
    fail_test "OpenResty reload did not require a new worker generation"
)

(
  proc_root="$test_root/proc"
  mkdir -p "$proc_root/4242/net"
  printf '%s\n' \
    '  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode' \
    '   0: 0100007F:0BB8 0100007F:C001 01 00000000:00000000 00:00000000 00000000 0 0 1' \
    '   1: 0100007F:0BB8 0100007F:C002 06 00000000:00000000 00:00000000 00000000 0 0 2' \
    > "$proc_root/4242/net/tcp"
  printf '%s\n' \
    '  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode' \
    '   0: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:C003 01 00000000:00000000 00:00000000 00000000 0 0 3' \
    > "$proc_root/4242/net/tcp6"
  BLUE_GREEN_PROC_ROOT="$proc_root"
  run_timed() { printf '4242\n'; }
  [[ "$(blue_green_active_connection_count new-api)" == 2 ]] ||
    fail_test "container connection count did not parse IPv4 and IPv6 established sockets"
)

(
  connection_count_file="$test_root/connection-count.calls"
  blue_green_active_connection_count() {
    if [[ ! -f "$connection_count_file" ]]; then
      : > "$connection_count_file"
      printf '2\n'
    else
      printf '0\n'
    fi
  }
  sleep() { :; }
  wait_for_blue_green_connections_to_drain 8201 new-api ||
    fail_test "connection drain did not wait for established requests"
)

(
  BLUE_GREEN_NEW_CONTAINER='new-api-blue'
  blue_green_container_exists() { return 0; }
  wait_for_blue_green_connections_to_drain() { return 1; }
  run_timed() {
    if [[ "$*" == *'.State.Status'* ]]; then
      printf 'running\n'
      return 0
    fi
    fail_test "old container was stopped with active connections"
  }
  set +e
  drain_blue_green_container 8201 new-api
  drain_status=$?
  set -e
  [[ "$drain_status" -ne 0 ]] ||
    fail_test "old container drain accepted active connections"
)

(
  BLUE_GREEN_NEW_CONTAINER='failed-slot'
  blue_green_container_exists() { return 0; }
  wait_for_blue_green_connections_to_drain() { return 0; }
  run_timed() {
    if [[ "$*" == *'.State.Status'* ]]; then
      printf 'running\n'
    fi
    return 0
  }
  removed_container=''
  blue_green_remove_container() { removed_container="$1"; }
  real_drain_blue_green_container 8202 failed-slot restored-slot
  [[ "$removed_container" == 'failed-slot' ]] ||
    fail_test "failed slot was protected after the previous slot became active"
)

(
  BLUE_GREEN_NEW_CONTAINER='active-slot'
  blue_green_container_exists() { return 0; }
  run_timed() {
    if [[ "$*" == *'.State.Status'* ]]; then
      printf 'exited\n'
      return 0
    fi
    return 1
  }
  removed_container=''
  blue_green_remove_container() { removed_container="$1"; }
  real_drain_blue_green_container 8201 stopped-slot active-slot
  [[ "$removed_container" == 'stopped-slot' ]] ||
    fail_test "stopped old slot was not removed without a connection drain"
)

(
  run_timed() {
    shift
    if [[ "$*" == *'.Config.Image'* ]]; then
      printf '%s\n' "$NEW_IMAGE"
    elif [[ "$*" == *'.State.Health'* ]]; then
      printf 'unhealthy\n'
    elif [[ "$*" == *'.State.Status'* ]]; then
      printf 'running\n'
    fi
  }
  sleep() { fail_test "fail-fast health verification slept before rollback"; }
  set +e
  real_wait_for_blue_green_container new-api-blue "$NEW_IMAGE" 1
  health_status=$?
  set -e
  [[ "$health_status" -ne 0 ]] ||
    fail_test "fail-fast health verification accepted an unhealthy active slot"
)

(
  run_timed() {
    printf 'Cannot connect to the Docker daemon\n' >&2
    return 125
  }
  set +e
  real_blue_green_remove_container unavailable-container
  remove_status=$?
  set -e
  [[ "$remove_status" -ne 0 ]] ||
    fail_test "Docker inspect failure was treated as an absent container"
)

(
  run_timed() {
    printf 'Error: No such object: absent-container\n' >&2
    return 1
  }
  real_blue_green_remove_container absent-container ||
    fail_test "missing blue-green container was not idempotent"
)

TEST_DRAIN_CALLS=0
drain_blue_green_container() {
  TEST_DRAIN_CALLS=$((TEST_DRAIN_CALLS + 1))
  :
}
reload_calls_before_recovery="$(wc -l < "$TEST_PROXY_RELOAD_LOG")"
recover_blue_green_pending_state
reload_calls_after_recovery="$(wc -l < "$TEST_PROXY_RELOAD_LOG")"
[[ "$reload_calls_after_recovery" -gt "$reload_calls_before_recovery" ]] ||
  fail_test "pending recovery did not test and reload OpenResty before draining"
[[ "$(sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV")" == "$NEW_IMAGE" ]] ||
  fail_test "pending recovery did not commit the active new image"
grep -q '^PHASE=committed$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "pending recovery did not commit the new slot state"

pending_cleanup_case="$test_root/pending-deferred-cleanup"
mkdir -p "$pending_cleanup_case"
DEPLOY_DIR="$pending_cleanup_case"
RELEASE_ENV="$pending_cleanup_case/release.env"
BLUE_GREEN_STATE_FILE="$pending_cleanup_case/.blue-green.state"
printf 'NEW_API_IMAGE=%s\n' "$OLD_IMAGE" > "$RELEASE_ENV"
write_blue_green_pending_state 8201 new-api "$OLD_IMAGE" 1 8202 new-api-blue "$NEW_IMAGE"
read_blue_green_state
blue_green_proxy_port() { printf '8202\n'; }
wait_for_blue_green_container() { return 0; }
blue_green_verify_live_route() { return 0; }
drain_blue_green_container() { return 1; }
recover_blue_green_pending_state
grep -q '^PHASE=cleanup-pending$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "pending recovery rejected a committed slot with deferred cleanup"
[[ "$(sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV")" == "$NEW_IMAGE" ]] ||
  fail_test "pending recovery with deferred cleanup lost the active image"

[[ "$BLUE_GREEN_STATE_PHASE" == 'cleanup-pending' ]] ||
  fail_test "pending recovery did not refresh the in-memory cleanup-pending phase"
set +e
pending_recovery_block_output="$(
  BLUE_GREEN_MODE=1
  deploy_release "$NEW_IMAGE" 2>&1
)"
pending_recovery_block_status=$?
set -e
[[ "$pending_recovery_block_status" -ne 0 ]] ||
  fail_test "pending recovery allowed a new deployment while cleanup was pending"
grep -q 'cleanup is still pending' <<< "$pending_recovery_block_output" ||
  fail_test "pending recovery cleanup refusal was not actionable"

printf 'NEW_API_IMAGE=%s\n' "$NEW_IMAGE" > "$RELEASE_ENV"
write_blue_green_pending_state 8202 new-api-blue "$NEW_IMAGE" 0 8201 new-api-green "$OLD_IMAGE"
read_blue_green_state
blue_green_proxy_port() { printf '8202\n'; }
recover_blue_green_pending_state
[[ "$(sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV")" == "$NEW_IMAGE" ]] ||
  fail_test "old-route pending recovery changed the selected image"
[[ ! -f "$BLUE_GREEN_STATE_FILE" ]] ||
  fail_test "baseline-free old-route recovery left a stale state file"

pending_route_failure_case="$test_root/pending-route-failure"
mkdir -p "$pending_route_failure_case"
DEPLOY_DIR="$pending_route_failure_case"
RELEASE_ENV="$pending_route_failure_case/release.env"
BLUE_GREEN_STATE_FILE="$pending_route_failure_case/.blue-green.state"
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' 'proxy_pass http://127.0.0.1:8202;' > "$pending_route_failure_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$pending_route_failure_case/site.conf")
printf 'NEW_API_IMAGE=%s\n' "$NEW_IMAGE" > "$RELEASE_ENV"
write_blue_green_pending_state 8201 new-api "$OLD_IMAGE" 1 8202 new-api-blue "$NEW_IMAGE"
read_blue_green_state
blue_green_proxy_port() { printf '8202\n'; }
wait_for_blue_green_container() { return 0; }
blue_green_verify_live_route() { [[ "$1" == 8201 ]]; }
PENDING_ROUTE_FAILURE_REMOVED=''
blue_green_remove_container() { PENDING_ROUTE_FAILURE_REMOVED="$1"; }
recover_blue_green_pending_state
grep -q 'proxy_pass http://127.0.0.1:8201;' "$pending_route_failure_case/site.conf" ||
  fail_test "pending route failure did not restore the previous proxy slot"
[[ "$(sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV")" == "$OLD_IMAGE" ]] ||
  fail_test "pending route failure did not restore the previous image selection"
[[ "$PENDING_ROUTE_FAILURE_REMOVED" == 'new-api-blue' ]] ||
  fail_test "pending route failure did not remove the failed standby slot"

eval "$(declare -f real_blue_green_verify_live_route | sed '1s/real_blue_green_verify_live_route/blue_green_verify_live_route/')"
blue_green_remove_container() { :; }
drain_blue_green_container() {
  TEST_DRAIN_CALLS=$((TEST_DRAIN_CALLS + 1))
  :
}

# Restore the real port parser after the recovery cases that intentionally
# force a particular live route.
blue_green_proxy_port() {
  local file ports port expected=''
  for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
    ports="$(sed -nE 's/^[[:space:]]*proxy_pass[[:space:]]+http:\/\/127\.0\.0\.1:(8201|8202);[[:space:]]*$/\1/p' "$file")"
    while IFS= read -r port; do
      [[ -z "$port" ]] && continue
      if [[ -z "$expected" ]]; then
        expected="$port"
      elif [[ "$expected" != "$port" ]]; then
        fail "OpenResty proxy backends use mixed ports: $file"
      fi
    done <<< "$ports"
  done
  [[ -n "$expected" ]] || fail "OpenResty proxy active port could not be determined"
  printf '%s\n' "$expected"
}

cleanup_case="$test_root/cleanup-pending"
mkdir -p "$cleanup_case"
DEPLOY_DIR="$cleanup_case"
RELEASE_ENV="$cleanup_case/release.env"
BLUE_GREEN_STATE_FILE="$cleanup_case/.blue-green.state"
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' 'proxy_pass http://127.0.0.1:8202;' > "$cleanup_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$cleanup_case/site.conf")
printf 'NEW_API_IMAGE=%s\n' "$NEW_IMAGE" > "$RELEASE_ENV"
write_blue_green_cleanup_pending_state \
  8202 new-api-blue "$NEW_IMAGE" 8201 new-api "$OLD_IMAGE"
read_blue_green_state
recover_blue_green_cleanup_pending_state
grep -q '^PHASE=committed$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "cleanup-pending recovery did not commit after idempotent cleanup"
[[ "$TEST_DRAIN_CALLS" -ge 2 ]] ||
  fail_test "cleanup-pending recovery did not retry old-container cleanup"

rollback_case="$test_root/cleanup-pending-rollback"
mkdir -p "$rollback_case"
DEPLOY_DIR="$rollback_case"
RELEASE_ENV="$rollback_case/release.env"
BLUE_GREEN_STATE_FILE="$rollback_case/.blue-green.state"
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' 'proxy_pass http://127.0.0.1:8202;' > "$rollback_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$rollback_case/site.conf")
printf 'NEW_API_IMAGE=%s\n' "$NEW_IMAGE" > "$RELEASE_ENV"
write_blue_green_cleanup_pending_state \
  8202 new-api-blue "$NEW_IMAGE" 8201 new-api "$OLD_IMAGE"
read_blue_green_state
BLUE_GREEN_SWITCHED=0
BLUE_GREEN_PROXY_BACKUP_DIR=''
wait_for_blue_green_container() { return 0; }
blue_green_verify_live_route() { [[ "$1" == 8201 ]]; }
TEST_ROLLBACK_DRAIN_TARGET=''
drain_blue_green_container() {
  TEST_ROLLBACK_DRAIN_TARGET="$2"
}
recover_blue_green_cleanup_pending_state
grep -q 'proxy_pass http://127.0.0.1:8201;' "$rollback_case/site.conf" ||
  fail_test "cleanup-pending recovery did not restore the previous proxy slot"
[[ "$(sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV")" == "$OLD_IMAGE" ]] ||
  fail_test "cleanup-pending recovery did not restore the previous image selection"
[[ "$(sed -n 's/^PORT=//p' "$BLUE_GREEN_STATE_FILE")" == 8201 ]] ||
  fail_test "cleanup-pending recovery did not persist the previous active slot"
[[ "$TEST_ROLLBACK_DRAIN_TARGET" == 'new-api-blue' ]] ||
  fail_test "cleanup-pending rollback did not remove the failed slot"
wait_for_blue_green_container() { return 0; }
eval "$(declare -f real_blue_green_verify_live_route | sed '1s/real_blue_green_verify_live_route/blue_green_verify_live_route/')"

commit_window_case="$test_root/commit-window"
mkdir -p "$commit_window_case"
DEPLOY_DIR="$commit_window_case"
RELEASE_ENV="$commit_window_case/release.env"
BLUE_GREEN_STATE_FILE="$commit_window_case/.blue-green.state"
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' 'proxy_pass http://127.0.0.1:8202;' > "$commit_window_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$commit_window_case/site.conf")
printf 'NEW_API_IMAGE=%s\n' "$OLD_IMAGE" > "$RELEASE_ENV"
TEST_DRAIN_PHASE=''
drain_blue_green_container() {
  TEST_DRAIN_PHASE="$(sed -n 's/^PHASE=//p' "$BLUE_GREEN_STATE_FILE")"
}
blue_green_commit_active_slot \
  8202 new-api-blue "$NEW_IMAGE" 8201 new-api "$OLD_IMAGE"
[[ "$TEST_DRAIN_PHASE" == 'cleanup-pending' ]] ||
  fail_test "commit path did not persist cleanup-pending before draining"
grep -q '^PHASE=committed$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "commit path did not persist committed state after cleanup"

deferred_cleanup_case="$test_root/deferred-cleanup"
mkdir -p "$deferred_cleanup_case"
DEPLOY_DIR="$deferred_cleanup_case"
RELEASE_ENV="$deferred_cleanup_case/release.env"
BLUE_GREEN_STATE_FILE="$deferred_cleanup_case/.blue-green.state"
printf 'NEW_API_IMAGE=%s\n' "$OLD_IMAGE" > "$RELEASE_ENV"
DEPLOY_IN_PROGRESS=1
drain_blue_green_container() { return 1; }
set +e
blue_green_commit_active_slot \
  8202 new-api-blue "$NEW_IMAGE" 8201 new-api "$OLD_IMAGE"
deferred_cleanup_status=$?
set -e
[[ "$deferred_cleanup_status" -eq 2 ]] ||
  fail_test "deferred cleanup did not return the committed-pending status"
[[ "$DEPLOY_IN_PROGRESS" -eq 0 ]] ||
  fail_test "deferred cleanup left deployment rollback armed"
grep -q '^PHASE=cleanup-pending$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "deferred cleanup did not retain cleanup-pending state"

set +e
cleanup_block_output="$(
  BLUE_GREEN_MODE=1
  BLUE_GREEN_STATE_PHASE='cleanup-pending'
  deploy_release "$NEW_IMAGE" 2>&1
)"
cleanup_block_status=$?
set -e
[[ "$cleanup_block_status" -ne 0 ]] ||
  fail_test "a new deployment overwrote cleanup-pending state"
grep -q 'cleanup is still pending' <<< "$cleanup_block_output" ||
  fail_test "cleanup-pending deployment refusal was not actionable"

mixed_case="$test_root/mixed-ports"
mkdir -p "$mixed_case"
printf '%s\n' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
	'proxy_pass http://127.0.0.1:8202;' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
	'proxy_pass http://127.0.0.1:8201;' > "$mixed_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$mixed_case/site.conf")
set +e
mixed_output="$(verify_blue_green_proxy 2>&1)"
mixed_status=$?
set -e
[[ "$mixed_status" -ne 0 ]] || fail_test "mixed proxy ports were accepted"
grep -q 'mixed ports' <<< "$mixed_output" ||
  fail_test "mixed proxy ports did not produce an actionable error"

bootstrap_mixed_case="$test_root/bootstrap-mixed"
mkdir -p "$bootstrap_mixed_case"
printf '%s\n' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
	'proxy_pass http://127.0.0.1:8201;' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
	'proxy_pass http://127.0.0.1:8202;' > "$bootstrap_mixed_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$bootstrap_mixed_case/site.conf")
BLUE_GREEN_STATE_WAS_PRESENT=0
set +e
bootstrap_output="$(ensure_blue_green_proxy 8201 2>&1)"
bootstrap_status=$?
set -e
[[ "$bootstrap_status" -ne 0 ]] || fail_test "mixed initial proxy ports were normalized"
grep -q 'mixed ports' <<< "$bootstrap_output" ||
  fail_test "mixed initial proxy ports did not fail closed"

partial_case="$test_root/partial-rewrite"
mkdir -p "$partial_case"
printf '%s\n' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
  'proxy_pass http://127.0.0.1:8201;' > "$partial_case/first.conf"
printf '%s\n' 'proxy_pass http://127.0.0.1:8201;' > "$partial_case/second.conf"
BLUE_GREEN_PROXY_FILES=("$partial_case/first.conf" "$partial_case/second.conf")
set +e
partial_output="$(switch_blue_green_proxy 8202 2>&1)"
partial_status=$?
set -e
[[ "$partial_status" -ne 0 ]] || fail_test "partial proxy rewrite unexpectedly succeeded"
grep -q 'proxy_pass http://127.0.0.1:8201;' "$partial_case/first.conf" ||
  fail_test "partial proxy rewrite did not restore the first file"
! grep -q 'proxy_pass http://127.0.0.1:8202;' "$partial_case/first.conf" ||
  fail_test "partial proxy rewrite left the first file on the standby port"

invalid_case="$test_root/invalid"
mkdir -p "$invalid_case"
printf '%s\n' 'server { location / { proxy_pass http://127.0.0.1:8201; } }' > "$invalid_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$invalid_case/site.conf")
set +e
invalid_output="$(blue_green_rewrite_proxy_files 8202 2>&1)"
invalid_status=$?
set -e
[[ "$invalid_status" -ne 0 ]] || fail_test "unsupported proxy backend was accepted"
grep -q 'no managed backend' <<< "$invalid_output" ||
  fail_test "unsupported proxy backend error was not actionable"

reload_case="$test_root/reload-failure"
mkdir -p "$reload_case"
printf '%s\n' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
  'proxy_pass http://127.0.0.1:8201;' > "$reload_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$reload_case/site.conf")
BLUE_GREEN_PROXY_BACKUP_DIR=''
TEST_PROXY_RELOAD_STATUS=1
set +e
reload_output="$(switch_blue_green_proxy 8202 2>&1)"
reload_status=$?
set -e
unset TEST_PROXY_RELOAD_STATUS
[[ "$reload_status" -ne 0 ]] || fail_test "reload failure was accepted"
grep -q 'proxy_pass http://127.0.0.1:8201;' "$reload_case/site.conf" ||
  fail_test "reload failure did not restore the old proxy file"

proxy_exec_log="$test_root/proxy-exec.args"
run_timed() {
  shift
  printf '%s\n' "$@" > "$proxy_exec_log"
}
BLUE_GREEN_PROXY_CONTAINER='1Panel-openresty-test'
blue_green_proxy_exec -s reload
mapfile -t proxy_exec_args < "$proxy_exec_log"
proxy_exec_count="${#proxy_exec_args[@]}"
[[ "${proxy_exec_args[$((proxy_exec_count - 2))]}" == '-s' ]] ||
  fail_test "OpenResty reload lost the -s argument"
[[ "${proxy_exec_args[$((proxy_exec_count - 1))]}" == 'reload' ]] ||
  fail_test "OpenResty reload lost the reload argument"

printf 'blue-green proxy transaction tests passed\n'
