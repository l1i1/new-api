#!/usr/bin/env bash
# Regression tests for the master-state reconcile block in bootstrap-master-ecs.sh.
#
# The 2026-10-24 .67 roll flipped the master's nginx + serving marker and then
# died before renaming green: for a day the serving container was still named
# new-api-master--green, while new-api-master named the retired .66 blue. The
# .68 deploy's start phase removed the serving green as "stale" and then failed
# to bind its green port. These tests extract the real blocks out of the script
# (not copies) and drive them with a stub docker, so an edit to the real script
# is what gets tested.
set -Eeuo pipefail

TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TARGET="$TEST_DIR/../bootstrap-master-ecs.sh"
[ -r "$TARGET" ] || { echo "FAIL: cannot read $TARGET" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
fail=0
ok() { printf 'ok   %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=1; }

extract_block() { # <start-marker> <end-marker> <out>
  awk -v s="$1" -v e="$2" '$0 ~ s {f=1} f {print} f && $0 ~ e {exit}' "$TARGET" > "$3"
  [ -s "$3" ] || { bad "could not extract '$1' (markers moved?)"; return 1; }
}
reconcile="$work/reconcile.sh"
guard="$work/guard.sh"
extract_block 'master-state-reconcile:begin' 'master-state-reconcile:end' "$reconcile" || exit 1
extract_block 'stale-green-guard:begin' 'stale-green-guard:end' "$guard" || exit 1

# Stub environment shared by both blocks. State lives in files so a subshell's
# docker calls can change what the next inspect sees.
run_reconcile() { # <fake-ps> <canonical-exists:0|1> <canonical-port> <log>
  (
    state="$work/state"
    logfile="${4:-/dev/null}"
    rm -rf "$state"; mkdir -p "$state"
    printf '%s\n' "$1" > "$state/ps"
    [ "$2" = 1 ] && : > "$state/canonical-exists"
    printf '%s\n' "$3" > "$state/canonical-port"
    printf '%s\n' "new-api-master--green" > "$state/serving-name"
    CONTAINER=new-api-master GREEN=new-api-master--green SERVE_IP=10.0.0.249 sp=3001 gp=3000
    err() { printf 'err: %s\n' "$*" >&2; }
    log() { printf 'log: %s\n' "$*" >> "$logfile"; }
    container_on_port() {
      awk -v needle="10.0.0.249:$1->" '$0 ~ needle {print $1; exit}' "$state/ps"
    }
    docker() {
      local sub="$1"; shift
      case "$sub" in
        inspect)
          if [ "${1:-}" = "-f" ]; then
            local name="${@: -1}"
            [ "$name" = "$CONTAINER" ] && [ -e "$state/canonical-exists" ] || return 1
            cat "$state/canonical-port"
            return 0
          fi
          local name="${@: -1}"
          if [ "$name" = "$CONTAINER" ]; then [ -e "$state/canonical-exists" ]; return; fi
          [ "$name" = "$(cat "$state/serving-name")" ] && return 0
          return 1
          ;;
        rm)
          printf 'rm %s\n' "$*" >> "$logfile"
          rm -f "$state/canonical-exists"
          return 0
          ;;
        rename)
          printf 'rename %s\n' "$*" >> "$logfile"
          printf '%s\n' "$2" > "$state/serving-name"
          : > "$state/canonical-exists"
          return 0
          ;;
        *) return 2 ;;
      esac
    }
    source "$reconcile"
    printf 'END\n'
  )
}

# 1. Normal state: canonical container is serving, nothing to reconcile.
log1="$work/log1"; : > "$log1"
out="$(run_reconcile "new-api-master 10.0.0.249:3001->3000/tcp" 1 3001 "$log1" 2>&1)" || true
grep -q '^END$' <<<"$out" && ok "normal state proceeds" || bad "normal state did not proceed: $out"
grep -q '^rm ' "$log1" && bad "normal state removed a container" || ok "normal state removes nothing"
grep -q '^rename ' "$log1" && bad "normal state renamed a container" || ok "normal state renames nothing"

# 2. Serving green + stale canonical blue: adopt green, remove stale canonical.
log2="$work/log2"; : > "$log2"
out="$(run_reconcile "new-api-master--green 10.0.0.249:3001->3000/tcp" 1 3000 "$log2" 2>&1)" || true
grep -q '^END$' <<<"$out" && ok "stale-canonical state proceeds" || bad "stale-canonical state did not proceed: $out"
grep -q 'adopted serving container' "$log2" && ok "serving container adopted" || bad "serving container not adopted"
grep -q '^rm .*new-api-master$' "$log2" && ok "stale canonical removed" || bad "stale canonical not removed"
grep -q '^rename new-api-master--green new-api-master$' "$log2" && ok "serving container renamed" || bad "serving container not renamed"

# 3. Marker points at a port no container publishes: refuse instead of inventing one.
log3="$work/log3"; : > "$log3"
out="$(run_reconcile "new-api-master 10.0.0.249:3000->3000/tcp" 1 3000 "$log3" 2>&1)" || true
grep -q '^END$' <<<"$out" && bad "missing-serving-container state proceeded" || ok "missing serving container aborts"
grep -q 'no running container publishes' <<<"$out" && ok "missing serving container reports the reason" || bad "missing serving container reason missing: $out"
[ -s "$log3" ] && bad "missing serving container performed docker mutations" || ok "missing serving container mutates nothing"

# 4. Defensive guard: a green publishing the serving port is never removed.
(
  state="$work/state"
  printf '%s\n' "new-api-master--green 10.0.0.249:3001->3000/tcp" > "$state/ps"
  CONTAINER=new-api-master GREEN=new-api-master--green SERVE_IP=10.0.0.249 sp=3001
  err() { printf 'err: %s\n' "$*" >&2; }
  log() { :; }
  container_on_port() {
    awk -v needle="10.0.0.249:$1->" '$0 ~ needle {print $1; exit}' "$state/ps"
  }
  docker() { printf 'UNEXPECTED %s\n' "$*" >&2; return 2; }
  source "$guard"
  echo END
) > "$work/guard.out" 2>&1 || true
grep -q 'refusing to remove' "$work/guard.out" && ok "serving green is protected from stale cleanup" || bad "serving green guard did not fire: $(cat "$work/guard.out")"
grep -q '^UNEXPECTED' "$work/guard.out" && bad "guard performed a docker mutation" || ok "guard mutates nothing"

exit "$fail"
