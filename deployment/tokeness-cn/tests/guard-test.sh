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
trap 'rm -rf -- "$test_root"' EXIT
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

run_guard() {
  local case_dir="$1"; shift
  mkdir -p "$case_dir/state"
  # The relay upstream fleet-sync would maintain: empty by default (nobody
  # parked), so "victim not served" holds; cases that need the stuck-consumer
  # direction write their own.
  [[ -f "$case_dir/upstream.conf" ]] || printf 'server 10.0.0.199:3000;\n' > "$case_dir/upstream.conf"
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
run_guard "$park_case" SHRINK_IDLE_MINUTES=0 >/dev/null
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
run_guard "$victim_case" >/dev/null
guard_log "$victim_case" | grep -q "no longer the healthy in-service instance we parked" || fail "a changed victim still triggered a shrink path"
grep -q "ModifyScalingGroup" "$victim_case/state/aliyun-calls.log" 2>/dev/null && fail "the guard shrank although the parked victim changed"

# (5) Full shrink path: window expired, victim unchanged, still idle, no hold.
# The marker must stay parked until the victim leaves the group ENTIRELY - the
# fake keeps reporting it in Removing during the lag window.
shrink_case="$test_root/shrink"
mkdir -p "$shrink_case"
jq -n '{draining: "10.0.0.207", draining_id: "eci-old", drain_until: "'$(( $(date +%s) - 60 ))'", idle_streak: "0"}' > "$shrink_case/shrink-state.json"
printf 'shrink-guard:%s\n%s\n10.0.0.207\n' "$(hostname)" "$(( $(date +%s) + 600 ))" > "$shrink_case/shrink-drain"
run_guard "$shrink_case" SHRINK_REMOVAL_POLL_SECONDS=1 TOKENESS_TEST_SCALE_IN_LAG_SECONDS=3 >/dev/null
guard_log "$shrink_case" | grep -q "DesiredCapacity set to 2" || fail "the guard did not shrink an idle drained tier by exactly one"
guard_log "$shrink_case" | grep -q "eci-old.*left the group" || fail "the guard did not wait for the victim's full departure"
[[ -f "$shrink_case/shrink-drain" ]] && fail "the marker stayed parked after the victim left the group"
[[ "$(jq -r '.draining // empty' "$shrink_case/shrink-state.json")" == "" ]] || fail "drain state survived the completed shrink"

# (6) Policy refusal: when the group's removal policy is not OldestInstance, the
# guard cannot know which instance a shrink removes, so it must not park a guess.
policy_case="$test_root/policy"
run_guard "$policy_case" SHRINK_IDLE_MINUTES=0 TOKENESS_TEST_REMOVAL_POLICY=NewestInstance >/dev/null
[[ -f "$policy_case/shrink-drain" ]] && fail "the guard parked a victim it could not predict"
guard_log "$policy_case" | grep -q "not OldestInstance" || fail "the policy refusal did not explain itself"

printf 'ecs-shrink-guard tests passed\n'
