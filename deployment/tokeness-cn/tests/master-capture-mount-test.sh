#!/usr/bin/env bash
# Verifies the capture bind mount the master bootstrap assembles.
#
# The deploy suite deliberately stands in for bootstrap-master-ecs.sh, so nothing else covers the real
# script's docker arguments. This extracts the bind-assembly block out of the script itself - not a
# copy of it - and runs that text against a stub docker, so an edit to the real script is what gets
# tested. The script cannot be sourced: deploy.sh pipes it to the host over stdin, so it has to stay
# self-contained and running it directly would need a whole container lifecycle.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
target="$script_dir/../bootstrap-master-ecs.sh"
[ -r "$target" ] || { echo "FAIL: cannot read $target" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
cat > "$work/bin/docker" <<'STUB'
#!/usr/bin/env bash
# Only the inspect call this block makes is answered; anything else is a test bug.
if [[ "$1" == "inspect" ]]; then
  printf '%s\n' "${FAKE_BINDS:-}"
  exit 0
fi
echo "unexpected docker invocation: $*" >&2
exit 2
STUB
chmod +x "$work/bin/docker"

# The block is delimited by its own comment and the closing "fi" of the mkdir guard.
awk '/^# Binds are preserved rather than restated/{f=1} f{print} f&&/^fi$/{exit}' "$target" > "$work/block.sh"
[ -s "$work/block.sh" ] || { echo "FAIL: could not extract the bind block (markers moved?)" >&2; exit 1; }

run_block() {
  local fake_binds="$1" host_dir="$2"
  PATH="$work/bin:$PATH" FAKE_BINDS="$fake_binds" \
  CAPTURE_HOST_DIR="$host_dir" CAPTURE_CONTAINER_DIR="/data/request-captures" \
  CONTAINER="new-api-master" \
  bash -c '
    err() { echo "err: $*" >&2; }
    source "$1"
    printf "%s\n" "${binds[@]}"
  ' _ "$work/block.sh"
}

fail=0
check() { # label expected actual
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: expected [$2] got [$3]"; fail=1; fi
}

# 1. No pre-existing binds: the capture mount is added and the host directory is created.
host="$work/captures"
out="$(run_block "" "$host")"
check "capture mount added" "-v
$host:/data/request-captures" "$out"
[ -d "$host" ] && echo "ok   host directory created" || { echo "FAIL host directory not created"; fail=1; }
perms="$(stat -c '%a' "$host")"
check "host directory is 0700" "700" "$perms"

# 2. Other mounts survive; a stale capture mount is replaced rather than duplicated.
out="$(run_block "/srv/data:/data
/var/lib/old-captures:/data/request-captures" "$host")"
check "existing bind preserved, stale capture bind replaced" "-v
/srv/data:/data
-v
$host:/data/request-captures" "$out"

# 3. An unwritable host directory fails closed instead of starting green without the mount.
out="$(PATH="$work/bin:$PATH" CAPTURE_HOST_DIR="$work/nope/captures" CAPTURE_CONTAINER_DIR="/data/request-captures" \
  CONTAINER="new-api-master" bash -c '
    err() { echo "err: $*" >&2; }
    mkdir() { return 1; }   # simulate a host where the directory cannot be created
    source "$1"
    echo "REACHED-THE-END"
  ' _ "$work/block.sh" 2>&1 || true)"
echo "$out" | grep -q "REACHED-THE-END" && { echo "FAIL: did not abort when the directory was unusable"; fail=1; } || echo "ok   aborts when the capture directory cannot be created"

exit "$fail"
