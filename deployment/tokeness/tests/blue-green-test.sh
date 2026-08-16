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
eval "$(declare -f blue_green_proxy_reload | sed '1s/blue_green_proxy_reload/real_blue_green_proxy_reload/')"
eval "$(declare -f blue_green_remove_container | sed '1s/blue_green_remove_container/real_blue_green_remove_container/')"
eval "$(declare -f discover_blue_green_proxy_container | sed '1s/discover_blue_green_proxy_container/real_discover_blue_green_proxy_container/')"
eval "$(declare -f blue_green_verify_container_binding | sed '1s/blue_green_verify_container_binding/real_blue_green_verify_container_binding/')"

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
  '        proxy_pass http://127.0.0.1:3000;' \
  '    }' \
  '}' > "$case_dir/site.conf"

# Keep the test independent from Docker and OpenResty while exercising the
# exact file rewrite and rollback path used by production.
discover_blue_green_proxy_container() { :; }
discover_blue_green_proxy_files() { :; }
TEST_PROXY_RELOAD_CALLS=0
blue_green_proxy_reload() {
  TEST_PROXY_RELOAD_CALLS=$((TEST_PROXY_RELOAD_CALLS + 1))
  return "${TEST_PROXY_RELOAD_STATUS:-0}"
}
blue_green_verify_container_binding() { :; }
TEST_STATUS_BODY='{"success":true,"version":"blue-green-test"}'
blue_green_proxy_route_status() { printf '%s\n' "$TEST_STATUS_BODY"; }
status_json() { printf '%s\n' "$TEST_STATUS_BODY"; }

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
        proxy-a) printf '%s\n' $'/opt/1panel/www/sites\t/www/sites' ;;
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
      printf '%s\n' $'/opt/1panel/www/sites\t/www/sites'
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
      printf '%s\n' $'127.0.0.1\t3001'
      return 0
    fi
    return 1
  }
  real_blue_green_verify_container_binding new-api-blue "$NEW_IMAGE" 3001
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
      printf '%s\n' $'127.0.0.1\t3000'
      return 0
    fi
    return 1
  }
  real_blue_green_verify_container_binding new-api-blue "$NEW_IMAGE" 3001
) >/dev/null 2>&1
wrong_binding_status=$?
set -e
[[ "$wrong_binding_status" -ne 0 ]] ||
  fail_test "blue-green target identity accepted the wrong published port"

route_site="$test_root/sites/api.example.test/proxy"
mkdir -p "$route_site"
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=route.example.test' 'proxy_pass http://127.0.0.1:3000;' > "$route_site/proxy.conf"
BLUE_GREEN_PROXY_FILES=("$route_site/proxy.conf")
[[ "$(blue_green_proxy_route_host)" == 'route.example.test' ]] ||
	fail_test "live route probe did not read the explicit marker hostname"
BLUE_GREEN_PROXY_FILES=("$case_dir/site.conf")

ensure_blue_green_proxy 3000
grep -q "# $BLUE_GREEN_PROXY_MARKER" "$case_dir/site.conf" ||
  fail_test "initial proxy bootstrap did not preserve the management marker"
grep -q 'proxy_pass http://127.0.0.1:3000;' "$case_dir/site.conf" ||
  fail_test "initial proxy bootstrap changed the active port"

switch_blue_green_proxy 3001
grep -q 'proxy_pass http://127.0.0.1:3001;' "$case_dir/site.conf" ||
  fail_test "proxy switch did not select the standby port"
[[ "$BLUE_GREEN_SWITCHED" -eq 1 ]] || fail_test "proxy switch did not commit its transaction state"

blue_green_restore_proxy_backup || fail_test "proxy backup restore failed"
grep -q 'proxy_pass http://127.0.0.1:3000;' "$case_dir/site.conf" ||
  fail_test "proxy backup restore did not return to the old port"

bootstrap_without_marker="$test_root/bootstrap-without-marker.conf"
printf '%s\n' 'proxy_pass http://127.0.0.1:3000;' > "$bootstrap_without_marker"
BLUE_GREEN_PROXY_FILES=("$bootstrap_without_marker")
set +e
bootstrap_without_marker_output="$(ensure_blue_green_proxy 3000 2>&1)"
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
  'proxy_pass http://127.0.0.1:3000;' > "$invalid_marker_root/proxy.conf"
set +e
blue_green_managed_proxy_entries "$invalid_marker_root/proxy.conf" >/dev/null 2>&1
invalid_marker_status=$?
set -e
[[ "$invalid_marker_status" -ne 0 ]] ||
  fail_test "an unpaired blue-green marker was silently ignored"

BLUE_GREEN_PROXY_FILES=("$case_dir/site.conf")

write_blue_green_state 3001 new-api-blue "$NEW_IMAGE"
read_blue_green_state
[[ "$BLUE_GREEN_ACTIVE_PORT" == 3001 ]] || fail_test "state parser lost the active port"
[[ "$BLUE_GREEN_ACTIVE_CONTAINER" == new-api-blue ]] || fail_test "state parser lost the active container"

recovery_case="$test_root/recovery"
mkdir -p "$recovery_case"
DEPLOY_DIR="$recovery_case"
RELEASE_ENV="$recovery_case/release.env"
BLUE_GREEN_STATE_FILE="$recovery_case/.blue-green.state"
SERVICE_NAME='new-api'
printf 'NEW_API_IMAGE=%s\n' "$OLD_IMAGE" > "$RELEASE_ENV"
BLUE_GREEN_STATE_BASELINE_PRESENT=0
write_blue_green_pending_state 3000 new-api "$OLD_IMAGE" 0 3001 new-api-blue "$NEW_IMAGE"
read_blue_green_state
blue_green_proxy_port() { printf '3001\n'; }
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
  wait_for_blue_green_connections_to_drain 3000 ||
    fail_test "connection drain did not wait for established requests"
)

(
  BLUE_GREEN_NEW_CONTAINER='new-api-blue'
  blue_green_container_exists() { return 0; }
  wait_for_blue_green_connections_to_drain() { return 1; }
  run_timed() { fail_test "old container was stopped with active connections"; }
  set +e
  drain_blue_green_container 3000 new-api
  drain_status=$?
  set -e
  [[ "$drain_status" -ne 0 ]] ||
    fail_test "old container drain accepted active connections"
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
reload_calls_before_recovery="$TEST_PROXY_RELOAD_CALLS"
recover_blue_green_pending_state
[[ "$TEST_PROXY_RELOAD_CALLS" -gt "$reload_calls_before_recovery" ]] ||
  fail_test "pending recovery did not test and reload OpenResty before draining"
[[ "$(sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV")" == "$NEW_IMAGE" ]] ||
  fail_test "pending recovery did not commit the active new image"
grep -q '^PHASE=committed$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "pending recovery did not commit the new slot state"

printf 'NEW_API_IMAGE=%s\n' "$NEW_IMAGE" > "$RELEASE_ENV"
write_blue_green_pending_state 3001 new-api-blue "$NEW_IMAGE" 0 3000 new-api-green "$OLD_IMAGE"
read_blue_green_state
blue_green_proxy_port() { printf '3001\n'; }
recover_blue_green_pending_state
[[ "$(sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV")" == "$NEW_IMAGE" ]] ||
  fail_test "old-route pending recovery changed the selected image"
[[ ! -f "$BLUE_GREEN_STATE_FILE" ]] ||
  fail_test "baseline-free old-route recovery left a stale state file"

# Restore the real port parser after the recovery cases that intentionally
# force a particular live route.
blue_green_proxy_port() {
  local file ports port expected=''
  for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
    ports="$(sed -nE 's/^[[:space:]]*proxy_pass[[:space:]]+http:\/\/127\.0\.0\.1:(3000|3001);[[:space:]]*$/\1/p' "$file")"
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
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' 'proxy_pass http://127.0.0.1:3001;' > "$cleanup_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$cleanup_case/site.conf")
printf 'NEW_API_IMAGE=%s\n' "$NEW_IMAGE" > "$RELEASE_ENV"
write_blue_green_cleanup_pending_state \
  3001 new-api-blue "$NEW_IMAGE" 3000 new-api "$OLD_IMAGE"
read_blue_green_state
recover_blue_green_cleanup_pending_state
grep -q '^PHASE=committed$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "cleanup-pending recovery did not commit after idempotent cleanup"
[[ "$TEST_DRAIN_CALLS" -ge 2 ]] ||
  fail_test "cleanup-pending recovery did not retry old-container cleanup"

commit_window_case="$test_root/commit-window"
mkdir -p "$commit_window_case"
DEPLOY_DIR="$commit_window_case"
RELEASE_ENV="$commit_window_case/release.env"
BLUE_GREEN_STATE_FILE="$commit_window_case/.blue-green.state"
printf '%s\n' '# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' 'proxy_pass http://127.0.0.1:3001;' > "$commit_window_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$commit_window_case/site.conf")
printf 'NEW_API_IMAGE=%s\n' "$OLD_IMAGE" > "$RELEASE_ENV"
TEST_DRAIN_PHASE=''
drain_blue_green_container() {
  TEST_DRAIN_PHASE="$(sed -n 's/^PHASE=//p' "$BLUE_GREEN_STATE_FILE")"
}
blue_green_commit_active_slot \
  3001 new-api-blue "$NEW_IMAGE" 3000 new-api "$OLD_IMAGE"
[[ "$TEST_DRAIN_PHASE" == 'cleanup-pending' ]] ||
  fail_test "commit path did not persist cleanup-pending before draining"
grep -q '^PHASE=committed$' "$BLUE_GREEN_STATE_FILE" ||
  fail_test "commit path did not persist committed state after cleanup"

mixed_case="$test_root/mixed-ports"
mkdir -p "$mixed_case"
printf '%s\n' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
	'proxy_pass http://127.0.0.1:3001;' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
	'proxy_pass http://127.0.0.1:3000;' > "$mixed_case/site.conf"
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
	'proxy_pass http://127.0.0.1:3000;' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
	'proxy_pass http://127.0.0.1:3001;' > "$bootstrap_mixed_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$bootstrap_mixed_case/site.conf")
BLUE_GREEN_STATE_WAS_PRESENT=0
set +e
bootstrap_output="$(ensure_blue_green_proxy 3000 2>&1)"
bootstrap_status=$?
set -e
[[ "$bootstrap_status" -ne 0 ]] || fail_test "mixed initial proxy ports were normalized"
grep -q 'mixed ports' <<< "$bootstrap_output" ||
  fail_test "mixed initial proxy ports did not fail closed"

partial_case="$test_root/partial-rewrite"
mkdir -p "$partial_case"
printf '%s\n' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
  'proxy_pass http://127.0.0.1:3000;' > "$partial_case/first.conf"
printf '%s\n' 'proxy_pass http://127.0.0.1:8201;' > "$partial_case/second.conf"
BLUE_GREEN_PROXY_FILES=("$partial_case/first.conf" "$partial_case/second.conf")
set +e
partial_output="$(switch_blue_green_proxy 3001 2>&1)"
partial_status=$?
set -e
[[ "$partial_status" -ne 0 ]] || fail_test "partial proxy rewrite unexpectedly succeeded"
grep -q 'proxy_pass http://127.0.0.1:3000;' "$partial_case/first.conf" ||
  fail_test "partial proxy rewrite did not restore the first file"
! grep -q 'proxy_pass http://127.0.0.1:3001;' "$partial_case/first.conf" ||
  fail_test "partial proxy rewrite left the first file on the standby port"

invalid_case="$test_root/invalid"
mkdir -p "$invalid_case"
printf '%s\n' 'server { location / { proxy_pass http://127.0.0.1:8201; } }' > "$invalid_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$invalid_case/site.conf")
set +e
invalid_output="$(blue_green_rewrite_proxy_files 3001 2>&1)"
invalid_status=$?
set -e
[[ "$invalid_status" -ne 0 ]] || fail_test "unsupported proxy backend was accepted"
grep -q 'no managed backend' <<< "$invalid_output" ||
  fail_test "unsupported proxy backend error was not actionable"

reload_case="$test_root/reload-failure"
mkdir -p "$reload_case"
printf '%s\n' \
	'# TOKENESS_BLUE_GREEN_MANAGED host=api.example.test' \
  'proxy_pass http://127.0.0.1:3000;' > "$reload_case/site.conf"
BLUE_GREEN_PROXY_FILES=("$reload_case/site.conf")
BLUE_GREEN_PROXY_BACKUP_DIR=''
TEST_PROXY_RELOAD_STATUS=1
set +e
reload_output="$(switch_blue_green_proxy 3001 2>&1)"
reload_status=$?
set -e
unset TEST_PROXY_RELOAD_STATUS
[[ "$reload_status" -ne 0 ]] || fail_test "reload failure was accepted"
grep -q 'proxy_pass http://127.0.0.1:3000;' "$reload_case/site.conf" ||
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
