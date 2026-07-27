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
trap 'rm -rf "$test_root"' EXIT
touch "$test_root/key" "$test_root/commands.log"
chmod 0600 "$test_root/key"

if PATH="$TEST_DIR/fake-bin:$PATH" \
  TOKENESS_SSH_KEY_PATH="$test_root/key" \
  TOKENESS_TEST_LOG="$test_root/commands.log" \
  TOKENESS_TEST_FAIL_HOST='103.214.68.250' \
  bash "$ROLLOUT_SCRIPT" deploy "$NEW_DIGEST"; then
  fail "rollout unexpectedly succeeded"
fi

mapfile -t commands < "$test_root/commands.log"
expected=(
  '156.246.94.70|verify'
  '103.214.68.250|verify'
  '149.13.91.236|verify'
  '216.73.158.156|verify'
  "156.246.94.70|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST"
  "103.214.68.250|deploy ghcr.io/l1i1/new-api@$NEW_DIGEST"
  "103.214.68.250|deploy $OLD_IMAGE"
  "156.246.94.70|deploy $OLD_IMAGE"
)

[[ "${#commands[@]}" -eq "${#expected[@]}" ]] ||
  fail "unexpected command count: ${#commands[@]}"

for index in "${!expected[@]}"; do
  [[ "${commands[$index]}" == "${expected[$index]}" ]] ||
    fail "command $index mismatch: ${commands[$index]}"
done

printf 'rollout rollback test passed\n'
