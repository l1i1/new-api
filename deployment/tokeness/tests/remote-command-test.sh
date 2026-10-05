#!/usr/bin/env bash
set -Eeuo pipefail

readonly TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REMOTE_SCRIPT="$TEST_DIR/../remote-command.sh"
readonly OLD_IMAGE='ghcr.io/l1i1/new-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
readonly NEW_IMAGE='ghcr.io/l1i1/new-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
readonly MUTABLE_IMAGE='ghcr.io/l1i1/new-api:legacy'

fail_test() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

# Sourcing is intentionally side-effect free; the forced-command entry point
# calls main only when the production script is executed directly.
source "$REMOTE_SCRIPT"

is_trusted_image "$NEW_IMAGE" || fail_test "immutable trusted digest was rejected"
if is_trusted_image 'ghcr.io/l1i1/new-api:latest; id'; then
  fail_test "mutable command-like image reference was accepted"
fi

prepare_case() {
  local case_dir="$1"
  mkdir -p "$case_dir"
  DEPLOY_DIR="$case_dir"
  RELEASE_ENV="$case_dir/release.env"
  BASE_COMPOSE_FILE="$case_dir/docker-compose.yml"
  OVERRIDE_COMPOSE_FILE="$case_dir/docker-compose.tokeness.yml"
  SERVICE_NAME='new-api'
  PREVIOUS_IMAGE="$OLD_IMAGE"
  DEPLOY_IN_PROGRESS=0
  printf 'NEW_API_IMAGE=%s\n' "$OLD_IMAGE" > "$RELEASE_ENV"
  printf '%s\n' "$OLD_IMAGE" > "$case_dir/runtime-image"
  printf 'running\n' > "$case_dir/state"
  printf 'healthy\n' > "$case_dir/health"
  printf '2026-07-27T00:00:00Z\n' > "$case_dir/started-at"
  printf '{"success":true,"data":{"version":"v-test"}}\n' > "$case_dir/status.json"
  printf 'running\n' > "$case_dir/epay-state"
  printf 'healthy\n' > "$case_dir/epay-health"
  printf '%s\n' "$EPAY_RECONCILIATION_IMAGE" > "$case_dir/epay-image"
  printf '65534:65534\n' > "$case_dir/epay-user"
  printf '%s\n' "$EPAY_RECONCILIATION_IP" > "$case_dir/epay-ip"
  printf 'true\n' > "$case_dir/epay-read-only"
  printf '{}\n' > "$case_dir/epay-port-bindings"
  printf 'ALL\n' > "$case_dir/epay-cap-drop"
  printf 'no-new-privileges:true\n' > "$case_dir/epay-security-opt"
  printf 'EPAY_RECONCILIATION_QUERY_URL=%s\n' "$EPAY_RECONCILIATION_URL" > "$case_dir/new-api-env"
  printf '{"code":-3}\n' > "$case_dir/epay-probe.json"
  : > "$case_dir/compose.log"
  : > "$case_dir/prune.log"
}

container_id() {
  printf 'new-api-container\n'
}

run_timed() {
  local seconds="$1"
  shift
  if [[ "$1" == docker && "$2" == image && "$3" == prune ]]; then
    printf '%s\n' "$*" >> "$DEPLOY_DIR/prune.log"
    printf 'Total reclaimed space: 0B\n'
    return 0
  fi
  if [[ "$1" == docker && "$2" == image && "$3" == inspect ]]; then
    return 1
  fi
  if [[ "$1" == docker && "$2" == inspect ]]; then
    if [[ "${*: -1}" == "$EPAY_RECONCILIATION_CONTAINER" ]]; then
      printf 'epay-container\n'
      return 0
    fi
    if [[ "${*: -1}" == 'epay-container' ]]; then
      case "$*" in
        *'{{.Config.Image}}'*)
          cat "$DEPLOY_DIR/epay-image"
          ;;
        *'{{.Config.User}}'*)
          cat "$DEPLOY_DIR/epay-user"
          ;;
        *'{{.State.Status}}'*)
          cat "$DEPLOY_DIR/epay-state"
          ;;
        *'{{if .State.Health}}'*)
          cat "$DEPLOY_DIR/epay-health"
          ;;
        *'.NetworkSettings.Networks'*)
          cat "$DEPLOY_DIR/epay-ip"
          ;;
        *'{{.HostConfig.ReadonlyRootfs}}'*)
          cat "$DEPLOY_DIR/epay-read-only"
          ;;
        *'{{json .HostConfig.PortBindings}}'*)
          cat "$DEPLOY_DIR/epay-port-bindings"
          ;;
        *'{{range .HostConfig.CapDrop}}'*)
          cat "$DEPLOY_DIR/epay-cap-drop"
          ;;
        *'{{range .HostConfig.SecurityOpt}}'*)
          cat "$DEPLOY_DIR/epay-security-opt"
          ;;
        *)
          fail_test "unexpected EPay docker inspect format: $*"
          ;;
      esac
      return 0
    fi
    case "$*" in
      *'{{.Config.Image}}'*)
        cat "$DEPLOY_DIR/runtime-image"
        ;;
      *'{{.State.Status}}'*)
        cat "$DEPLOY_DIR/state"
        ;;
      *'{{if .State.Health}}'*)
        cat "$DEPLOY_DIR/health"
        ;;
      *'{{.State.StartedAt}}'*)
        cat "$DEPLOY_DIR/started-at"
        ;;
      *'{{range .Config.Env}}'*)
        cat "$DEPLOY_DIR/new-api-env"
        ;;
      *)
        fail_test "unexpected docker inspect format: $*"
        ;;
    esac
    return 0
  fi
  if [[ "$1" == docker && "$2" == exec && "$3" == 'new-api-container' ]]; then
    cat "$DEPLOY_DIR/epay-probe.json"
    return 0
  fi
  fail_test "unexpected timed command after ${seconds}s: $*"
}

wait_for_ready() {
  local expected_image="${1:-}"
  [[ "$(<"$DEPLOY_DIR/runtime-image")" == "$expected_image" ]]
}

status_json() {
  cat "$DEPLOY_DIR/status.json"
}

# Free space is a host fact the cases below must control; TEST_AVAILABLE_DISK_KB
# defaults to a comfortable value and is lowered for the headroom gate case.
available_disk_kb() {
  printf '%s\n' "${TEST_AVAILABLE_DISK_KB:-99999999}"
}

compose_timed() {
  local seconds="$1" operation
  shift
  operation="$1"
  printf '%s\n' "$operation" >> "$DEPLOY_DIR/compose.log"
  case "$operation" in
    pull)
      return 0
      ;;
    up)
      if [[ "${TEST_ROLLBACK_STAYS_ON_TARGET:-0}" -eq 1 && "$(read_release_image)" == "$OLD_IMAGE" ]]; then
        return 1
      fi
      printf '%s\n' "$(read_release_image)" > "$DEPLOY_DIR/runtime-image"
      if [[ "$(read_release_image)" == "$NEW_IMAGE" ]]; then
        printf '%s\n' "${TEST_TARGET_FINAL_HEALTH:-unhealthy}" > "$DEPLOY_DIR/health"
      else
        printf 'healthy\n' > "$DEPLOY_DIR/health"
      fi
      return 0
      ;;
    *)
      fail_test "unexpected compose operation after ${seconds}s: $operation"
      ;;
  esac
}

commit_case="$test_root/successful-commit"
prepare_case "$commit_case"
export TEST_TARGET_FINAL_HEALTH=healthy
commit_output="$(deploy_release "$NEW_IMAGE")"
unset TEST_TARGET_FINAL_HEALTH
[[ "$(read_release_image)" == "$NEW_IMAGE" ]] || fail_test "successful deploy did not commit the target selection"
[[ "$(<"$commit_case/runtime-image")" == "$NEW_IMAGE" ]] || fail_test "successful deploy did not start the target runtime"
[[ "$(<"$commit_case/compose.log")" == $'pull\nup' ]] || fail_test "successful deploy did not pull and recreate exactly once"
[[ "$(wc -l < "$commit_case/prune.log")" -eq 2 ]] ||
  fail_test "successful deploy did not reclaim unused images before the pull and after the switch"
grep -q 'image prune -af' "$commit_case/prune.log" ||
  fail_test "image cleanup must also collect untagged images the containerd store does not report dangling"
grep -q $'TOKENESS_RESULT\t.*\trunning\thealthy\t' <<< "$commit_output" ||
  fail_test "successful deploy did not emit a healthy result"

idempotent_case="$test_root/idempotent-target"
prepare_case "$idempotent_case"
printf 'NEW_API_IMAGE=%s\n' "$NEW_IMAGE" > "$RELEASE_ENV"
printf '%s\n' "$NEW_IMAGE" > "$idempotent_case/runtime-image"
printf 'healthy\n' > "$idempotent_case/health"
idempotent_output="$(deploy_release "$NEW_IMAGE")"
[[ ! -s "$idempotent_case/compose.log" ]] || fail_test "same-digest deploy unnecessarily invoked Compose"
grep -q 'target image is already selected and healthy' <<< "$idempotent_output" ||
  fail_test "same-digest deploy did not report the idempotent path"
grep -q $'TOKENESS_RESULT\t.*\trunning\thealthy\t' <<< "$idempotent_output" ||
  fail_test "same-digest deploy did not emit the current healthy result"

successful_case="$test_root/successful-rollback"
prepare_case "$successful_case"
set +e
successful_output="$({
  set -e
  trap rollback_interrupted_deploy EXIT
  deploy_release "$NEW_IMAGE"
} 2>&1)"
successful_status=$?
set -e
[[ "$successful_status" -ne 0 ]] || fail_test "final unhealthy state unexpectedly succeeded"
[[ "$(read_release_image)" == "$OLD_IMAGE" ]] || fail_test "release selection was not restored"
[[ "$(<"$successful_case/runtime-image")" == "$OLD_IMAGE" ]] || fail_test "runtime image was not restored"
grep -q 'previous image restored and verified' <<< "$successful_output" ||
  fail_test "successful rollback was not verified"

failed_case="$test_root/failed-rollback"
prepare_case "$failed_case"
set +e
failed_output="$({
  set -e
  export TEST_ROLLBACK_STAYS_ON_TARGET=1
  trap rollback_interrupted_deploy EXIT
  deploy_release "$NEW_IMAGE"
} 2>&1)"
failed_status=$?
set -e
[[ "$failed_status" -ne 0 ]] || fail_test "failed rollback unexpectedly succeeded"
[[ "$(read_release_image)" == "$OLD_IMAGE" ]] || fail_test "failed rollback did not restore release selection"
[[ "$(<"$failed_case/runtime-image")" == "$NEW_IMAGE" ]] || fail_test "failed rollback test did not retain target runtime"
grep -q 'rollback did not restore the previous runtime image' <<< "$failed_output" ||
  fail_test "runtime mismatch was incorrectly reported as restored"
if grep -q 'previous image restored and verified' <<< "$failed_output"; then
  fail_test "failed rollback emitted a false success message"
fi

# A deploy that cannot hold a pull plus extraction must stop before it writes a
# release selection, so a full disk cannot leave the node without one.
headroom_case="$test_root/insufficient-disk"
prepare_case "$headroom_case"
set +e
headroom_output="$({
  export TEST_AVAILABLE_DISK_KB=1024
  deploy_release "$NEW_IMAGE"
} 2>&1)"
headroom_status=$?
set -e
[[ "$headroom_status" -ne 0 ]] || fail_test "deploy proceeded without free disk headroom"
[[ "$(read_release_image)" == "$OLD_IMAGE" ]] || fail_test "low-disk deploy changed the committed release selection"
[[ ! -s "$headroom_case/compose.log" ]] || fail_test "low-disk deploy reached Compose"
[[ "$(wc -l < "$headroom_case/prune.log")" -eq 1 ]] ||
  fail_test "low-disk deploy did not attempt dangling image cleanup before failing"
grep -q 'insufficient free disk space' <<< "$headroom_output" ||
  fail_test "low-disk deploy did not report the missing headroom"

# The release selection must survive a failed write: the rollback path reaches
# write_release_image from inside an `if`, where `set -e` does not apply.
write_guard_case="$test_root/release-write-guard"
prepare_case "$write_guard_case"
set +e
write_guard_output="$({
  printf() { return 1; }
  if write_release_image "$NEW_IMAGE"; then
    echo committed
  else
    echo rejected
  fi
} 2>&1)"
write_guard_status=$?
set -e
[[ "$write_guard_status" -ne 0 ]] || fail_test "a failed release-selection write unexpectedly succeeded"
[[ "$(read_release_image)" == "$OLD_IMAGE" ]] ||
  fail_test "a failed release-selection write replaced the committed selection"
if compgen -G "$write_guard_case/.release.env.*" >/dev/null; then
  fail_test "a failed release-selection write left its temporary file behind"
fi

none_healthy_case="$test_root/status-healthy"
prepare_case "$none_healthy_case"
printf 'none\n' > "$none_healthy_case/health"
none_healthy_output="$(emit_result "$OLD_IMAGE")"
grep -q $'\trunning\tnone\t' <<< "$none_healthy_output" ||
  fail_test "health=none with a healthy status endpoint was rejected"

none_failed_case="$test_root/status-false"
prepare_case "$none_failed_case"
printf 'none\n' > "$none_failed_case/health"
printf '{"success":false,"data":{"version":"v-test"}}\n' > "$none_failed_case/status.json"
set +e
none_output="$(emit_result "$OLD_IMAGE" 2>&1)"
none_status=$?
set -e
[[ "$none_status" -ne 0 ]] || fail_test "health=none with status failure was accepted"
grep -q 'status endpoint is not healthy' <<< "$none_output" ||
  fail_test "health=none failure did not report the status endpoint"

mutable_verify_case="$test_root/mutable-verify"
prepare_case "$mutable_verify_case"
printf 'NEW_API_IMAGE=%s\n' "$MUTABLE_IMAGE" > "$RELEASE_ENV"
printf '%s\n' "$MUTABLE_IMAGE" > "$mutable_verify_case/runtime-image"
set +e
mutable_verify_output="$(verify_release 2>&1)"
mutable_verify_status=$?
set -e
[[ "$mutable_verify_status" -ne 0 ]] || fail_test "direct verify accepted a mutable selected image"
grep -q 'trusted immutable image' <<< "$mutable_verify_output" ||
  fail_test "direct verify did not identify the mutable selected image"

mutable_deploy_case="$test_root/mutable-deploy"
prepare_case "$mutable_deploy_case"
printf 'NEW_API_IMAGE=%s\n' "$MUTABLE_IMAGE" > "$RELEASE_ENV"
printf '%s\n' "$MUTABLE_IMAGE" > "$mutable_deploy_case/runtime-image"
set +e
mutable_deploy_output="$(deploy_release "$NEW_IMAGE" 2>&1)"
mutable_deploy_status=$?
set -e
[[ "$mutable_deploy_status" -ne 0 ]] || fail_test "direct deploy accepted a mutable previous image"
[[ ! -s "$mutable_deploy_case/compose.log" ]] || fail_test "mutable previous image reached Compose"
[[ "$(read_release_image)" == "$MUTABLE_IMAGE" ]] || fail_test "mutable previous image was rewritten before rejection"
grep -q 'trusted immutable image' <<< "$mutable_deploy_output" ||
  fail_test "direct deploy did not identify the mutable previous image"

for epay_case in \
  'epay-image|1panel/openresty:latest|reviewed digest' \
  'epay-user|0:0|restricted user' \
  'epay-state|exited|sidecar state' \
  'epay-health|unhealthy|sidecar health' \
  'epay-ip|172.18.0.251|private address' \
  'epay-read-only|false|root filesystem' \
  'epay-port-bindings|{"18080/tcp":[{"HostPort":"18080"}]}|publishes host ports' \
  'epay-cap-drop|NET_RAW|drop all capabilities' \
  'epay-security-opt|seccomp=unconfined|privilege escalation' \
  'new-api-env|UNRELATED=true|does not select the private' \
  'epay-probe.json|{"code":-1}|health probe'; do
  IFS='|' read -r fixture value expected_error <<< "$epay_case"
  gate_case="$test_root/gate-$fixture"
  prepare_case "$gate_case"
  printf '%s\n' "$value" > "$gate_case/$fixture"
  set +e
  gate_output="$(emit_result "$OLD_IMAGE" 2>&1)"
  gate_status=$?
  set -e
  [[ "$gate_status" -ne 0 ]] || fail_test "$fixture sidecar gate unexpectedly succeeded"
  grep -q "$expected_error" <<< "$gate_output" ||
    fail_test "$fixture sidecar gate did not report the failed invariant"
done

replica_case="$test_root/replica-skips-epay"
prepare_case "$replica_case"
SERVICE_NAME='new-api-slave'
printf 'unhealthy\n' > "$replica_case/epay-health"
emit_result "$OLD_IMAGE" >/dev/null || fail_test "replica verification required the JP-M EPay sidecar"

printf 'remote command transaction tests passed\n'
