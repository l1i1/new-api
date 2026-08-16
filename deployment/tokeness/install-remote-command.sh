#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly SOURCE_SCRIPT="${1:-$SCRIPT_DIR/remote-command.sh}"
readonly TARGET_SCRIPT="${TOKENESS_REMOTE_COMMAND_TARGET:-/usr/local/sbin/tokeness-new-api-deploy}"
readonly EXPECTED_VERSION='2026-08-17.2'

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

[[ "$(id -u)" -eq 0 ]] || fail "installer must run as root"
[[ -f "$SOURCE_SCRIPT" ]] || fail "source deployment command was not found: $SOURCE_SCRIPT"
command -v install >/dev/null 2>&1 || fail "install is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"

source_version="$(bash "$SOURCE_SCRIPT" --version)"
[[ "$source_version" == "$EXPECTED_VERSION" ]] ||
  fail "source deployment command version is $source_version; expected $EXPECTED_VERSION"

target_dir="$(dirname -- "$TARGET_SCRIPT")"
install -d -o root -g root -m 0755 "$target_dir"
staged_script="$(mktemp "$target_dir/.tokeness-new-api-deploy.XXXXXX")"
trap 'rm -f -- "$staged_script"' EXIT
install -o root -g root -m 0755 "$SOURCE_SCRIPT" "$staged_script"

source_hash="$(sha256sum "$SOURCE_SCRIPT" | awk '{print $1}')"
staged_hash="$(sha256sum "$staged_script" | awk '{print $1}')"
[[ "$source_hash" == "$staged_hash" ]] || fail "staged deployment command hash mismatch"
[[ "$("$staged_script" --version)" == "$EXPECTED_VERSION" ]] ||
  fail "staged deployment command failed its version handshake"

mv -f -- "$staged_script" "$TARGET_SCRIPT"
trap - EXIT
chown root:root "$TARGET_SCRIPT"
chmod 0755 "$TARGET_SCRIPT"
[[ "$("$TARGET_SCRIPT" --version)" == "$EXPECTED_VERSION" ]] ||
  fail "installed deployment command failed its version handshake"
[[ "$(sha256sum "$TARGET_SCRIPT" | awk '{print $1}')" == "$source_hash" ]] ||
  fail "installed deployment command hash mismatch"

printf 'installed %s version=%s sha256=%s\n' \
  "$TARGET_SCRIPT" "$EXPECTED_VERSION" "$source_hash"
