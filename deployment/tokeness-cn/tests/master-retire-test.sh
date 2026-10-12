#!/usr/bin/env bash
set -Eeuo pipefail
# Direct state-machine test of the real bootstrap-master-ecs.sh flip/retire
# phases against a stateful fake docker/nginx - the coverage gap the 2026-10-13
# review called out: the deploy-test stub has no state, so the retire-port P0
# (rsp/rgp swapped, every parallel-retire release failing at the tail) passed
# CI unnoticed. Cases: pre-flip retire refuses; post-flip retire promotes
# green and retires blue; retire is idempotent; commit runs flip+retire whole.
TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
root=$(mktemp -d); trap 'rm -rf -- "$root"' EXIT
mkdir -p "$root/bin" "$root/state" "$root/nginx-bak"
# fake docker: containers tracked in state, name -> port
cat > "$root/bin/docker" <<'D'
#!/usr/bin/env bash
S="$DOCKER_STATE/state.json"
cmd="$1"; shift
case "$cmd" in
  ps) jq -r '.containers | to_entries[] | "\(.key) 10.0.0.249:\(.value)->3000/tcp"' "$S" ;;
  inspect)
    name="${@: -1}"
    case "$name" in
      new-api-master) jq -e '.containers["new-api-master"]' "$S" >/dev/null && exit 0 || exit 1 ;;
      new-api-master--green) jq -e '.containers["new-api-master--green"]' "$S" >/dev/null && exit 0 || exit 1 ;;
      *) exit 1 ;;
    esac ;;
  stop)  jq --arg n "${@: -1}" 'del(.containers[$n])' "$S" > "$S.t" && mv "$S.t" "$S" ;;
  rm)    jq --arg n "$2" 'del(.containers[$n])' "$S" > "$S.t" && mv "$S.t" "$S" ;;
  rm-f)  jq --arg n "${@: -1}" 'del(.containers[$n])' "$S" > "$S.t" && mv "$S.t" "$S" ;;
  rename)
    jq --arg f "$1" --arg t "$2" '.containers[$t] = .containers[$f] | del(.containers[$f])' "$S" > "$S.t" && mv "$S.t" "$S" ;;
  *) exit 0 ;;
esac
D
chmod +x "$root/bin/docker"
# docker ps format: '{{.Names}} {{.Ports}}' with '10.0.0.249:3000->' needles
cat > "$root/bin/nginx" <<'N'
#!/usr/bin/env bash
echo "nginx ok"
N
chmod +x "$root/bin/nginx"
cat > "$root/bin/systemctl" <<'S'
#!/usr/bin/env bash
exit 0
S
chmod +x "$root/bin/systemctl"
# fake curl: ready on whichever ports containers hold
cat > "$root/bin/curl" <<'C'
#!/usr/bin/env bash
url=""; for a in "$@"; do case "$a" in http*) url="$a"; break;; esac; done
port="${url##*:}"; port="${port%%/*}"
# The :80 probe (ecs_nginx_point_at) goes through nginx -> treat as ready when
# the port-80 upstream target (the serving container) is up.
if [ "$port" = "80" ]; then
  m="$(cat "$DOCKER_STATE/serving-port" 2>/dev/null || echo 3000)"
  port="$m"
fi
if jq -e --argjson p "$port" '([.containers[]] | index($p)) != null' "$DOCKER_STATE/state.json" >/dev/null 2>&1; then
  printf 'ok\n'
else
  exit 22
fi
C
chmod +x "$root/bin/curl"
# fake conf with the serving IP
conf="$root/tokeness-ml.conf"
printf 'server 10.0.0.249:3000;\n' > "$conf"

state="$root/dstate/state.json"
mkdir -p "$root/dstate"
mkstate() {
  printf '%s\n' "$1" > "$root/dstate/serving-port"
  printf '%s' "$2" | jq '.' > "$state"
}

run_phase() {
  env PATH="$root/bin:/usr/bin:/bin" \
    MASTER_SERVING_PORT_FILE="$root/dstate/serving-port" \
    ECS_NGINX_CONF="$conf" ECS_NGINX_BAK_DIR="$root/nginx-bak" \
    MASTER_READY_TIMEOUT=3 MASTER_V1_QUIET_SECONDS=0 MASTER_V1_STOP_TIMEOUT=1 \
    DOCKER_STATE="$root/dstate" \
    bash "$TEST_DIR/bootstrap-master-ecs.sh" docker.cnb.cool/x@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "$1" 2>&1
}

fail() { echo "FAIL: $*" >&2; exit 1; }

echo "== case 1: retire before flip must REFUSE (exit 3)"
mkstate "3000" '{"containers": {"new-api-master": 3000, "new-api-master--green": 3001}}'
out="$(run_phase retire)" && fail "retire before flip unexpectedly succeeded: $out"
grep -q "refusing to stop the serving container" <<<"$out" || fail "refusal message missing: $out"
jq -e '.containers["new-api-master"] == 3000' "$state" >/dev/null || fail "blue was touched despite refusal"

echo "== case 2: retire after flip completes (rename green into canonical)"
mkstate "3001" '{"containers": {"new-api-master": 3000, "new-api-master--green": 3001}}'
out="$(run_phase retire)" || fail "retire after flip failed: $out"
grep -q "retire done" <<<"$out" || fail "no completion message: $out"
jq -e '.containers["new-api-master"] == 3001 and (.containers | has("new-api-master--green") | not)' "$state" >/dev/null \
  || fail "green was not promoted: $(cat "$state")"

echo "== case 3: retire twice is a no-op (idempotent)"
out="$(run_phase retire)" || fail "second retire failed: $out"
grep -q "nothing to do" <<<"$out" || fail "idempotent message missing: $out"

echo "== case 4: commit end-to-end (flip + retire with marker moved)"
mkstate "3000" '{"containers": {"new-api-master": 3000}}'
# start creates green on the other port
jq '.containers["new-api-master--green"] = 3001' "$state" > "$state.t" && mv "$state.t" "$state"
out="$(run_phase commit)" || fail "commit failed: $out"
grep -q "retire done" <<<"$out" || fail "commit did not retire: $out"
grep -q "3001" "$root/dstate/serving-port" || fail "marker not flipped"

echo "ALL RETIRE STATE TESTS PASSED"
