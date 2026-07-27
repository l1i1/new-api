#!/usr/bin/env bash
set -Eeuo pipefail

readonly TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly ROLLOUT_SCRIPT="$TEST_DIR/../rollout.sh"
readonly NEW_DIGEST='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
readonly OLD_IMAGE='ghcr.io/l1i1/new-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

run_rollout() {
  local case_dir="$1"
  shift
  mkdir -p "$case_dir/state" "$case_dir/bin"
  cp "$TEST_DIR/fake-bin/ssh" "$TEST_DIR/fake-bin/curl" "$TEST_DIR/fake-bin/sleep" "$case_dir/bin/"
  chmod 0700 "$case_dir/bin/ssh" "$case_dir/bin/curl" "$case_dir/bin/sleep"
  touch "$case_dir/key" "$case_dir/commands.log"
  chmod 0600 "$case_dir/key"
  env PATH="$case_dir/bin:$PATH" \
    TOKENESS_SSH_KEY_PATH="$case_dir/key" \
    TOKENESS_TEST_LOG="$case_dir/commands.log" \
    TOKENESS_TEST_STATE_DIR="$case_dir/state" \
    "$@" bash "$ROLLOUT_SCRIPT" deploy "$NEW_DIGEST"
}

assert_commands() {
  local log_file="$1"
  shift
  local -a actual=("$@") commands
  mapfile -t commands < "$log_file"
  [[ "${#commands[@]}" -eq "${#actual[@]}" ]] ||
    fail "unexpected command count in $log_file: ${#commands[@]}"
  for index in "${!actual[@]}"; do
    [[ "${commands[$index]}" == "${actual[$index]}" ]] ||
      fail "command $index mismatch in $log_file: ${commands[$index]}"
  done
}

disconnect_case="$test_root/disconnect"
if run_rollout "$disconnect_case" \
  TOKENESS_TEST_MODE=disconnect-both \
  TOKENESS_TEST_FAIL_HOST=103.214.68.250; then
  fail "disconnect rollout unexpectedly succeeded"
fi
assert_commands "$disconnect_case/commands.log" \
  '156.246.94.70|verify' \
  '103.214.68.250|verify' \
  '149.13.91.236|verify' \
  '216.73.158.156|verify' \
  "156.246.94.70|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  "103.214.68.250|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  '103.214.68.250|verify' \
  "103.214.68.250|deploy $OLD_IMAGE" \
  '103.214.68.250|verify' \
  '156.246.94.70|verify' \
  "156.246.94.70|deploy $OLD_IMAGE"

noop_case="$test_root/noop"
if run_rollout "$noop_case" \
  TOKENESS_TEST_MODE=noop-result \
  TOKENESS_TEST_FAIL_HOST=156.246.94.70; then
  fail "no-op rollout unexpectedly succeeded"
fi
assert_commands "$noop_case/commands.log" \
  '156.246.94.70|verify' \
  '103.214.68.250|verify' \
  '149.13.91.236|verify' \
  '216.73.158.156|verify' \
  "156.246.94.70|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  '156.246.94.70|verify'

mutable_case="$test_root/mutable"
if run_rollout "$mutable_case" TOKENESS_TEST_MUTABLE_HOST=156.246.94.70; then
  fail "mutable rollback image unexpectedly passed preflight"
fi
assert_commands "$mutable_case/commands.log" '156.246.94.70|verify'

unhealthy_case="$test_root/unhealthy"
if run_rollout "$unhealthy_case" TOKENESS_TEST_UNHEALTHY_TARGET=1; then
  fail "unhealthy deployment result unexpectedly succeeded"
fi
assert_commands "$unhealthy_case/commands.log" \
  '156.246.94.70|verify' \
  '103.214.68.250|verify' \
  '149.13.91.236|verify' \
  '216.73.158.156|verify' \
  "156.246.94.70|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  '156.246.94.70|verify' \
  "156.246.94.70|deploy $OLD_IMAGE"

success_case="$test_root/success"
run_rollout "$success_case"
assert_commands "$success_case/commands.log" \
  '156.246.94.70|verify' \
  '103.214.68.250|verify' \
  '149.13.91.236|verify' \
  '216.73.158.156|verify' \
  "156.246.94.70|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  "103.214.68.250|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  "149.13.91.236|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  "216.73.158.156|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST"
for host in 156.246.94.70 103.214.68.250 149.13.91.236 216.73.158.156; do
  [[ "$(<"$success_case/state/$host")" == "ghcr.io/l1i1/new-api@$NEW_DIGEST" ]] ||
    fail "successful rollout did not commit the target on $host"
done

cdn_failure_case="$test_root/cdn-failure"
if run_rollout "$cdn_failure_case" TOKENESS_TEST_CDN_FAILURE=1; then
  fail "CDN failure rollout unexpectedly succeeded"
fi
assert_commands "$cdn_failure_case/commands.log" \
  '156.246.94.70|verify' \
  '103.214.68.250|verify' \
  '149.13.91.236|verify' \
  '216.73.158.156|verify' \
  "156.246.94.70|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  "103.214.68.250|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  "149.13.91.236|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  "216.73.158.156|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  '216.73.158.156|verify' \
  "216.73.158.156|deploy $OLD_IMAGE" \
  '149.13.91.236|verify' \
  "149.13.91.236|deploy $OLD_IMAGE" \
  '103.214.68.250|verify' \
  "103.214.68.250|deploy $OLD_IMAGE" \
  '156.246.94.70|verify' \
  "156.246.94.70|deploy $OLD_IMAGE"

lock_case="$test_root/lock-reconcile"
if run_rollout "$lock_case" \
  TOKENESS_TEST_MODE=lock-after-apply \
  TOKENESS_TEST_FAIL_HOST=156.246.94.70; then
  fail "lock contention rollout unexpectedly succeeded"
fi
assert_commands "$lock_case/commands.log" \
  '156.246.94.70|verify' \
  '103.214.68.250|verify' \
  '149.13.91.236|verify' \
  '216.73.158.156|verify' \
  "156.246.94.70|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST" \
  '156.246.94.70|verify' \
  "156.246.94.70|deploy $OLD_IMAGE" \
  '156.246.94.70|verify' \
  "156.246.94.70|deploy $OLD_IMAGE"
[[ "$(<"$lock_case/state/156.246.94.70")" == "$OLD_IMAGE" ]] ||
  fail "lock reconciliation did not restore the preflight image"

printf 'rollout transaction tests passed\n'
