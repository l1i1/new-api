#!/usr/bin/env bash
set -Eeuo pipefail

# ecs-shrink-guard tests - the coverage deploy-test.sh never had: the guard and
# the release share the drain marker file, the hold, and the tier, so every
# interaction between the two scripts is where a silent customer-visible bug
# hides. These cases drive the guard against the same fake ESS the release
# tests use.

readonly TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly GUARD_SCRIPT="$TEST_DIR/../ecs-shrink-guard"

fail() {
  printf 'ERROR (guard): %s\n' "$*" >&2
  exit 1
}

test_root="$(mktemp -d)"
# Background fleet-sync loops are tracked globally and reaped (kill + wait) by
# the EXIT trap: a loop inherits this script's stdout otherwise, and a pipeline
# consumer (| tail) never sees EOF; a fail() mid-case would leak the loop too.
FLEET_SYNC_PIDS=()
trap 'for __p in "${FLEET_SYNC_PIDS[@]:-}"; do kill "$__p" 2>/dev/null || true; done; for __p in "${FLEET_SYNC_PIDS[@]:-}"; do wait "$__p" 2>/dev/null || true; done; rm -rf -- "$test_root"' EXIT
bin_dir="$test_root/bin"
mkdir -p "$bin_dir"
cp "$TEST_DIR"/fake-bin/* "$bin_dir/"
chmod 0700 "$bin_dir"/*

init_guard_ess() {
  # Three in-service instances with distinct join times: the oldest is always
  # eci-old, which is what both the guard's victim selection and the fake's
  # OldestInstance removal must agree on.
  jq -n '{desired: 3, instances: [
    {InstanceId: "eci-old",  PrivateIpAddress: "10.0.0.207", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-01T00:00:00Z", ZoneId: "cn-shanghai-l"},
    {InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"},
    {InstanceId: "eci-run",  PrivateIpAddress: "10.0.0.205", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-03T00:00:00Z", ZoneId: "cn-shanghai-l"}
  ], image: "docker.cnb.cool/imvhb/new-api-cn@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}' \
    > "$1/state.json"
}

# fleet-sync emulator (#16): rewrites the relay upstream from the fake ESS
# state minus the drain marker's live-leased IPs, on a short cadence - the
# guard's consumption checks then observe REAL convergence (initial service,
# consumption, stuck) instead of a file that excluded the victim from the
# start. Cases that want a stuck consumer simply never start it.
fleet_sync_once() {
  if [[ -f "$1/state/state.json" ]]; then
    local parked="" lease_line
    lease_line="$(sed -n '2p' "$1/shrink-drain" 2>/dev/null | tr -d ' \r' || true)"
    if [[ "$lease_line" =~ ^[0-9]+$ ]] && (( lease_line > $(date +%s) )); then
      parked="$(grep -E '^10\.0\.0\.[0-9]+$' "$1/shrink-drain" 2>/dev/null || true)"
    fi
    jq -r '[.instances[]? | select(.LifecycleState=="InService") | .PrivateIpAddress] | sort | .[]' \
      "$1/state/state.json" 2>/dev/null | while IFS= read -r fs_ip; do
      [ -n "$fs_ip" ] || continue
      if [ -n "$parked" ] && printf '%s\n' "$parked" | grep -qxF "$fs_ip"; then continue; fi
      printf 'server %s:3000;\n' "$fs_ip"
    done > "$1/upstream.conf.new" 2>/dev/null && mv -f -- "$1/upstream.conf.new" "$1/upstream.conf"
  fi
}

fleet_sync_loop() (
  cd / 2>/dev/null || true
  while :; do
    fleet_sync_once "$1"
    sleep 0.3
  done
)

start_fleet_sync() {
  mkdir -p "$1" "$1/state"
  # One synchronous pass first: the guard reads the upstream immediately, and
  # a loop that has not written yet looks like a stuck consumer.
  fleet_sync_once "$1"
  fleet_sync_loop "$1" >/dev/null 2>&1 &
  echo $! > "$1/fleet-sync.pid"
  FLEET_SYNC_PIDS+=("$!")
}

stop_fleet_sync() {
  if [[ -f "$1/fleet-sync.pid" ]]; then
    kill "$(cat "$1/fleet-sync.pid")" 2>/dev/null || true
    rm -f "$1/fleet-sync.pid"
  fi
}

run_guard() {
  local case_dir="$1"; shift
  mkdir -p "$case_dir/state"
  if [[ ! -f "$case_dir/state/state.json" ]]; then
    init_guard_ess "$case_dir/state"
  fi
  env PATH="$bin_dir:$PATH" \
    SHRINK_PATH="$bin_dir:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
    SHRINK_STATE_FILE="$case_dir/shrink-state.json" \
    SHRINK_DRAIN_FILE="$case_dir/shrink-drain" \
    SHRINK_HOLD_FILE="$case_dir/shrink-hold" \
    SHRINK_LOG="$case_dir/guard.log" \
    SHRINK_LOCK_FILE="$case_dir/guard.lock" \
    SHRINK_MARKER_LOCK="$case_dir/marker.lock" \
    SHRINK_UPSTREAM_FILE="$case_dir/upstream.conf" \
    ECS_UPSTREAM_CONF="$case_dir/upstream.conf" \
    SHRINK_DRAIN_PATH="$case_dir/shrink-drain" \
    SHRINK_FLEET_APPLY_POLLS=2 \
    SHRINK_FLEET_APPLY_POLL_SECONDS=1 \
    SHRINK_REMOVAL_POLLS="${SHRINK_REMOVAL_POLLS:-3}" \
    SHRINK_ACCESS_LOGS="$case_dir/access.log" \
    SHRINK_MIN_KEEP=2 \
    SHRINK_RATE_WINDOW=900 \
    TOKENESS_TEST_STATE_DIR="$case_dir/state" \
    "$@" bash "$GUARD_SCRIPT"
}

guard_log() { cat "$1/guard.log" 2>/dev/null || true; }

# (1) Idle parking: the oldest instance is parked with the v2 marker - owner
# line, lease line, then the IP. A bare IP list would let any writer delete the
# marker, and no lease would leave it parked forever after a SIGKILL.
park_case="$test_root/park"
start_fleet_sync "$park_case"
run_guard "$park_case" SHRINK_IDLE_MINUTES=0 >/dev/null
stop_fleet_sync "$park_case"
[[ -f "$park_case/shrink-drain" ]] || fail "an idle tier above the floor did not park a drain marker"
owner="$(sed -n '1p' "$park_case/shrink-drain")"
lease="$(sed -n '2p' "$park_case/shrink-drain")"
ip="$(sed -n '3p' "$park_case/shrink-drain")"
[[ "$owner" == "shrink-guard:"* ]] || fail "the drain marker records no owner (got '$owner')"
[[ "$lease" =~ ^[0-9]+$ ]] || fail "the drain marker records no lease epoch (got '$lease')"
(( lease > $(date +%s) )) || fail "the drain marker's lease is already expired"
[[ "$ip" == "10.0.0.207" ]] || fail "the guard parked '$ip' instead of the oldest instance 10.0.0.207"
[[ "$(jq -r '.draining' "$park_case/shrink-state.json")" == "10.0.0.207" ]] || fail "state did not record the parked IP"
[[ "$(jq -r '.draining_id' "$park_case/shrink-state.json")" == "eci-old" ]] || fail "state did not record the victim id"

# (2) A release hold appearing mid-drain releases OUR marker but never touches a
# foreign one: the release parks its own retire set in the same file.
hold_case="$test_root/hold"
cp -r "$park_case" "$hold_case"
date -u +%Y-%m-%dT%H:%M:%SZ > "$hold_case/shrink-hold"
echo "release:other-1" > "$hold_case/foreign-marker"
printf 'release:other-1\n%s\n10.0.0.205\n' "$(( $(date +%s) + 600 ))" > "$hold_case/shrink-drain"
run_guard "$hold_case" >/dev/null
[[ -f "$hold_case/shrink-drain" ]] || fail "a live foreign drain marker was deleted while a release held the tier"
[[ "$(sed -n '1p' "$hold_case/shrink-drain")" == "release:other-1" ]] || fail "the foreign marker was overwritten"
guard_log "$hold_case" | grep -q "release hold present" || fail "the guard did not stand down for the release hold"
# The guard's own state was dropped without shrinking.
[[ "$(jq -r '.draining // empty' "$hold_case/shrink-state.json")" == "" ]] || fail "the guard kept drain state while a release owned the tier"
grep -q "ModifyScalingGroup" "$hold_case/state/aliyun-calls.log" 2>/dev/null && fail "the guard shrank a tier a release owned"

# (3) Janitor: a foreign marker whose lease has lapsed is the leftover of a
# SIGKILLed release; the tier at the floor has nothing else to do, so the tick
# must clear it instead of leaving a healthy member parked forever.
janitor_case="$test_root/janitor"
mkdir -p "$janitor_case/state"
jq -n '{desired: 2, instances: [
    {InstanceId: "eci-old", PrivateIpAddress: "10.0.0.207", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-01T00:00:00Z", ZoneId: "cn-shanghai-l"},
    {InstanceId: "eci-run", PrivateIpAddress: "10.0.0.205", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-03T00:00:00Z", ZoneId: "cn-shanghai-l"}
  ], image: "x"}' > "$janitor_case/state/state.json"
printf 'release:dead-1\n%s\n10.0.0.207\n' "$(( $(date +%s) - 3600 ))" > "$janitor_case/shrink-drain"
run_guard "$janitor_case" >/dev/null
[[ -f "$janitor_case/shrink-drain" ]] && fail "an abandoned (lease-lapsed) foreign marker survived the tick"
guard_log "$janitor_case" | grep -q "abandoned" || fail "the janitor did not log the abandoned marker"

# (4) Resume validation: the window expired but the victim is no longer the
# healthy in-service instance we parked (it was replaced externally). Shrinking
# now would let ESS remove a never-parked instance; the drain must end without
# a removal.
victim_case="$test_root/victim-changed"
mkdir -p "$victim_case/state"
# The parked victim (eci-old / 10.0.0.207) is GONE from the group: something
# else removed or replaced it during the window.
jq -n '{desired: 3, instances: [
    {InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"},
    {InstanceId: "eci-run", PrivateIpAddress: "10.0.0.205", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-03T00:00:00Z", ZoneId: "cn-shanghai-l"},
    {InstanceId: "eci-ext-1", PrivateIpAddress: "10.0.0.201", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-04T00:00:00Z", ZoneId: "cn-shanghai-l"}
  ], image: "x"}' > "$victim_case/state/state.json"
jq -n '{draining: "10.0.0.207", draining_id: "eci-old", drain_until: "'$(( $(date +%s) - 60 ))'", idle_streak: "0"}' > "$victim_case/shrink-state.json"
printf 'shrink-guard:%s\n%s\n10.0.0.207\n' "$(hostname)" "$(( $(date +%s) + 600 ))" > "$victim_case/shrink-drain"
start_fleet_sync "$victim_case"
run_guard "$victim_case" >/dev/null
stop_fleet_sync "$victim_case"
guard_log "$victim_case" | grep -q "no longer the healthy in-service instance we parked" || fail "a changed victim still triggered a shrink path"
grep -q "ModifyScalingGroup" "$victim_case/state/aliyun-calls.log" 2>/dev/null && fail "the guard shrank although the parked victim changed"

# (5) Full shrink path: window expired, victim unchanged, still idle, no hold.
# The marker must stay parked until the victim leaves the group ENTIRELY - the
# fake keeps reporting it in Removing during the lag window.
shrink_case="$test_root/shrink"
mkdir -p "$shrink_case/state"
# The ESS state must exist BEFORE the fleet-sync emulator starts: the guard's
# resume path reads the upstream immediately, and a sync that has not written
# yet is indistinguishable from a stuck consumer.
jq -n '{desired: 3, instances: [
    {InstanceId: "eci-old",  PrivateIpAddress: "10.0.0.207", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-01T00:00:00Z", ZoneId: "cn-shanghai-l"},
    {InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"},
    {InstanceId: "eci-run",  PrivateIpAddress: "10.0.0.205", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-03T00:00:00Z", ZoneId: "cn-shanghai-l"}
  ], image: "x"}' > "$shrink_case/state/state.json"
jq -n '{draining: "10.0.0.207", draining_id: "eci-old", drain_until: "'$(( $(date +%s) - 60 ))'", idle_streak: "0"}' > "$shrink_case/shrink-state.json"
printf 'shrink-guard:%s\n%s\n10.0.0.207\n' "$(hostname)" "$(( $(date +%s) + 600 ))" > "$shrink_case/shrink-drain"
start_fleet_sync "$shrink_case"
run_guard "$shrink_case" SHRINK_REMOVAL_POLL_SECONDS=1 TOKENESS_TEST_SCALE_IN_LAG_SECONDS=3 >/dev/null
stop_fleet_sync "$shrink_case"
guard_log "$shrink_case" | grep -q "DesiredCapacity set to 2" || { cat "$shrink_case/guard.log" >&2; fail "the guard did not shrink an idle drained tier by exactly one"; }
guard_log "$shrink_case" | grep -q "eci-old.*left the group" || fail "the guard did not wait for the victim's full departure"
[[ -f "$shrink_case/shrink-drain" ]] && fail "the marker stayed parked after the victim left the group"
[[ "$(jq -r '.draining // empty' "$shrink_case/shrink-state.json")" == "" ]] || fail "drain state survived the completed shrink"
[[ "$(jq -r '.retiring // empty' "$shrink_case/shrink-state.json")" == "" ]] || fail "retiring state survived the completed shrink (the next drain would never start)"
# The Modify-time snapshot shows the marker still parked on the victim and the
# upstream already excluding it - the shrink only ever happens through the
# parking, not around it.
guard_snap="$shrink_case/state/modify-snapshots/modify-1.log"
[[ -f "$guard_snap" ]] || fail "the guard's Modify was not snapshotted"
grep -qxF "10.0.0.207" "$guard_snap" || fail "at the guard's shrink, the marker did not park the victim"
if sed -n '/--- relay upstream ---/,$p' "$guard_snap" | grep -q "10.0.0.207"; then
  fail "at the guard's shrink, the relay upstream still served the victim"
fi

# (5b) Stuck consumer: fleet-sync is not running, so the upstream keeps
# serving the victim. A blind countdown would remove an instance that never
# stopped receiving new requests - the guard must refuse and clear.
stuck_case="$test_root/stuck-sync"
run_guard "$stuck_case" SHRINK_IDLE_MINUTES=0 >/dev/null
guard_log "$stuck_case" | grep -q "fleet-sync is not consuming the drain marker" \
  || fail "the guard shrank although the upstream never dropped the victim"
grep -q "ess ModifyScalingGroup" "$stuck_case/state/aliyun-calls.log" 2>/dev/null \
  && fail "the guard shrank with a stuck fleet-sync"
[[ -f "$stuck_case/shrink-drain" ]] && fail "the marker stayed parked after a refused stuck-consumer round"

# (6) Policy refusal: when the group's removal policy is not OldestInstance, the
# guard cannot know which instance a shrink removes, so it must not park a guess.
policy_case="$test_root/policy"
run_guard "$policy_case" SHRINK_IDLE_MINUTES=0 TOKENESS_TEST_REMOVAL_POLICY=NewestInstance >/dev/null
[[ -f "$policy_case/shrink-drain" ]] && fail "the guard parked a victim it could not predict"
guard_log "$policy_case" | grep -q "not OldestInstance" || fail "the policy refusal did not explain itself"

printf 'ecs-shrink-guard tests passed\n'
