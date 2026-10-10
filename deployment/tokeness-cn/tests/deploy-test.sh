#!/usr/bin/env bash
set -Eeuo pipefail

readonly TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly DEPLOY_SCRIPT="$TEST_DIR/../deploy.sh"
readonly VALID_DIGEST="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

# deploy-release cases go through resolve_ml_digest + config_args.py; without
# python3 they would die with the error captured into the per-case output.log
# and the harness would exit silently. Check up front instead.
command -v python3 >/dev/null 2>&1 \
  || fail "python3 is required (CNB default-build-env lacks it; pipelines must use deployment/tokeness-cn/ci/Dockerfile.cnb)"

assert_contains() {
  local file="$1" text="$2"
  grep -Fq -- "$text" "$file" || fail "expected $file to contain: $text"
}

assert_not_contains() {
  local file="$1" text="$2"
  if grep -Fq -- "$text" "$file"; then
    fail "expected $file not to contain: $text"
  fi
}

# aliyun CLI 3.x silently ignores the camelCase --RegionId on eci/vpc/ess and
# then dies with "region can't be empty", which broke EIP->bandwidth-package
# convergence without failing the rollout. Only the lowercase flag works.
if grep -nE 'aliyun_cmd [a-z]+ [A-Za-z]+ --RegionId' "$DEPLOY_SCRIPT" >/dev/null; then
  fail "deploy.sh passes --RegionId to aliyun (CLI 3.x needs lowercase --region)"
fi

make_conf() {
  local path="$1"
  cat > "$path" <<'CONF'
# server 10.0.0.99:3000;
upstream unrelated {
    server 10.9.9.9:3000;
}
upstream newapi_ml {
    server 10.0.0.207:3000;
}
upstream newapi_web {
    server 10.1.0.43:3000 max_fails=2 fail_timeout=10s;
    server 10.0.0.207:3000 backup max_fails=2 fail_timeout=5s;
    keepalive 8;
}
CONF
}

test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

# Stand-in for deployment/tokeness-cn/bootstrap-master-ecs.sh: the real script
# copies the env off the serving master container, pulls the pinned image and
# starts a green container on the other port of the 3000/3001 pair (docker is
# unavailable here). The fake emulates the blue-green phases with a state file
# ("serving-port", default 3000): start swaps to the other port, probe reports
# it, commit keeps it, abort rolls it back. TOKENESS_TEST_HOST_FAIL_TIMES makes
# the first N starts fail (master-first abort/recovery paths).
host_bootstrap="$test_root/fake-bootstrap.sh"
cat > "$host_bootstrap" <<'FAKE'
#!/usr/bin/env bash
set -Eeuo pipefail
state_dir="${TOKENESS_TEST_STATE_DIR:?}"
mkdir -p "$state_dir"
phase="${2:-start}"
count_file="$state_dir/host-sync-count"
count="$(cat "$count_file" 2>/dev/null || echo 0)"
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
printf 'host-bootstrap\n' >> "$state_dir/aliyun-calls.log"
printf 'host-bootstrap invocation %s phase=%s\n' "$count" "$phase" >> "$state_dir/host-bootstrap.log"
port_file="$state_dir/serving-port"
serving="$(cat "$port_file" 2>/dev/null || echo 3000)"
case "$serving" in 3000) green=3001 ;; 3001) green=3000 ;; *) green=3001 ;; esac
case "$phase" in
  probe)
    printf 'SERVING_PORT=%s\nBLUE_OK=1\nGREEN_PORT=%s\nGREEN_OK=0\n' "$serving" "$green" ;;
  commit)
    printf '%s\n' "$green" > "$port_file"
    printf 'SERVING_PORT=%s\n' "$green" ;;
  abort)
    printf 'SERVING_PORT=%s\n' "$serving" ;;
  start)
    if [[ "$count" -le "${TOKENESS_TEST_HOST_FAIL_TIMES:-0}" ]]; then
      echo "simulated host bootstrap failure" >&2
      exit 1
    fi
    printf 'SERVING_PORT=%s\nPREVIOUS_PORT=%s\n' "$green" "$serving" ;;
esac
FAKE
chmod +x "$host_bootstrap"

bin_dir="$test_root/bin"
mkdir -p "$bin_dir"
cp "$TEST_DIR"/fake-bin/* "$bin_dir/"
chmod 0700 "$bin_dir"/*
touch "$test_root/key"
chmod 0600 "$test_root/key"

run_deploy_legacy() {
  SWAS_PANEL_TIER=1 run_deploy "$@"
}

run_deploy() {
  local case_dir="$1"
  shift
  local -a env_args=()
  while [[ "${1:-}" == *=* ]]; do
    env_args+=("$1")
    shift
  done
  mkdir -p "$case_dir/state"
  local rc=0
  # The ECS is the relay and panel entry now (SWAS_PANEL_TIER defaults to 0), so
  # the release reads ITS upstream file instead of the retired lightweight
  # hosts' nginx. Production fills that file from the local ESS view, so the
  # default here names the instance the fake ESS promotes; a case that exercises
  # the gate writes its own copy before calling run_deploy.
  if [[ ! -e "$case_dir/ecs-upstream.conf" ]]; then
    printf 'server 10.0.0.241:3000;\n' > "$case_dir/ecs-upstream.conf"
  fi
  # The relay tier is pinned through a marker file that the fake ssh creates
  # locally, so every case needs its own path; and the drain hold is set to 0
  # because a test cannot usefully wait the production 600s. Cases that assert
  # the real timing override these (they come later in env, so they win).
  env \
    PATH="$bin_dir:$PATH" \
    NGINX_CONF="$case_dir/nginx.conf" \
    ECS_UPSTREAM_CONF="$case_dir/ecs-upstream.conf" \
    SWAS_SSH_KEY_PATH="$test_root/key" \
    HOST_BOOTSTRAP_SCRIPT="$host_bootstrap" \
    TOKENESS_TEST_STATE_DIR="$case_dir/state" \
    DRAIN_MARKER_PATH="$case_dir/state/drain-target" \
    WEB_PRIMARY_MARKER_PATH="$case_dir/state/web-primary-port" \
    WEB_PRIMARY_HOST=10.1.0.43 \
    WEB_PRIMARY_CONVERGE_ATTEMPTS=3 \
    WEB_PRIMARY_CONVERGE_DELAY_SECONDS=0 \
    TOKENESS_TEST_MLSYNC=1 \
    ML_DRAIN_SECONDS=0 \
    RELAY_MAX_MEMBERS=10 \
    SHRINK_HOLD_PATH="$case_dir/shrink-hold" \
    SHRINK_DRAIN_PATH="$case_dir/shrink-drain" \
    MARKER_LOCK_PATH="$case_dir/marker.lock" \
    ML_DRAIN_CONVERGE_ATTEMPTS=2 \
    ML_DRAIN_CONVERGE_DELAY_SECONDS=0 \
    CNB_REGISTRY_TOKEN=dummy-test-token \
    "${env_args[@]}" \
    bash "$DEPLOY_SCRIPT" "$@" \
    > "$case_dir/state/stdout.log" 2> "$case_dir/state/output.log" || rc=$?
  # Replay stdout to the caller. It is part of this helper contract: image-ref
  # prints the immutable reference and a test captures it. Keeping it in a file
  # ... and on failure also replay stderr: a deploy that died under set -e
  # writes its only explanation to output.log, and a suite that dies silently
  # (bare non-zero under its own set -e) loses it completely.
  if (( rc != 0 )); then
    cat "$case_dir/state/output.log" 2>/dev/null || true
  fi
  # as well lets a case assert on log() lines without racing a tee.
  cat "$case_dir/state/stdout.log"
  return "$rc"
}

invalid_ip_case="$test_root/invalid-ip"
mkdir -p "$invalid_ip_case"
make_conf "$invalid_ip_case/nginx.conf"
if run_deploy_legacy "$invalid_ip_case" nginx-update 10.0.0.999; then
  fail "invalid IPv4 address unexpectedly succeeded"
fi

public_failure_case="$test_root/public-failure"
mkdir -p "$public_failure_case"
make_conf "$public_failure_case/nginx.conf"
if run_deploy_legacy "$public_failure_case" TOKENESS_TEST_PUBLIC_FAIL=1 verify; then
  fail "public failure unexpectedly passed verification"
fi

direct_failure_case="$test_root/direct-failure"
mkdir -p "$direct_failure_case"
make_conf "$direct_failure_case/nginx.conf"
if run_deploy_legacy "$direct_failure_case" TOKENESS_TEST_DIRECT_FAIL=1 verify; then
  fail "direct failure unexpectedly passed verification"
fi

# Non-success body / invalid JSON must be treated as unhealthy.
bad_body_case="$test_root/bad-body"
mkdir -p "$bad_body_case"
make_conf "$bad_body_case/nginx.conf"
if run_deploy_legacy "$bad_body_case" TOKENESS_TEST_PUBLIC_BODY='{"success":false}' verify; then
  fail "non-success public body unexpectedly passed verification"
fi
if run_deploy_legacy "$bad_body_case" TOKENESS_TEST_PUBLIC_BODY='not-json' verify; then
  fail "invalid public JSON unexpectedly passed verification"
fi
if run_deploy_legacy "$bad_body_case" TOKENESS_TEST_DIRECT_BODY='{"success":false}' verify; then
  fail "non-success direct body unexpectedly passed verification"
fi

duplicate_case="$test_root/duplicate"
mkdir -p "$duplicate_case"
make_conf "$duplicate_case/nginx.conf"
tmp_conf="$duplicate_case/nginx.conf.tmp"
awk '
  { print }
  $0 == "upstream newapi_ml {" { print "    server 10.0.0.208:3000;" }
' "$duplicate_case/nginx.conf" > "$tmp_conf"
mv -- "$tmp_conf" "$duplicate_case/nginx.conf"
if run_deploy_legacy "$duplicate_case" verify; then
  fail "duplicate upstream unexpectedly passed verification"
fi

# A parameterized server line (weight=, max_fails=) must be rewritten too.
param_case="$test_root/parameterized"
mkdir -p "$param_case"
make_conf "$param_case/nginx.conf"
sed -i 's|server 10.0.0.207:3000;|server 10.0.0.207:3000 max_fails=3 fail_timeout=30s;|' "$param_case/nginx.conf"
run_deploy_legacy "$param_case" nginx-update 10.0.0.209
assert_contains "$param_case/nginx.conf" 'server 10.0.0.209:3000 max_fails=3 fail_timeout=30s;'
assert_not_contains "$param_case/nginx.conf" 'server 10.0.0.207:3000;'

success_case="$test_root/success"
mkdir -p "$success_case"
make_conf "$success_case/nginx.conf"
run_deploy_legacy "$success_case" nginx-update 10.0.0.208
assert_contains "$success_case/nginx.conf" 'server 10.0.0.208:3000;'
assert_contains "$success_case/nginx.conf" '# server 10.0.0.99:3000;'
assert_contains "$success_case/nginx.conf" 'server 10.9.9.9:3000;'
assert_not_contains "$success_case/nginx.conf" 'server 10.0.0.207:3000;'
run_deploy_legacy "$success_case" nginx-update 10.0.0.208

nginx_failure_case="$test_root/nginx-failure"
mkdir -p "$nginx_failure_case"
make_conf "$nginx_failure_case/nginx.conf"
if run_deploy_legacy "$nginx_failure_case" TOKENESS_TEST_NGINX_FAIL=1 nginx-update 10.0.0.208; then
  fail "nginx validation failure unexpectedly succeeded"
fi
assert_contains "$nginx_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$nginx_failure_case/nginx.conf" 'server 10.0.0.208:3000;'

reload_failure_case="$test_root/reload-failure"
mkdir -p "$reload_failure_case"
make_conf "$reload_failure_case/nginx.conf"
if run_deploy_legacy "$reload_failure_case" TOKENESS_TEST_RELOAD_FAIL_ONCE=1 nginx-update 10.0.0.208; then
  fail "reload failure unexpectedly succeeded"
fi
assert_contains "$reload_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$reload_failure_case/nginx.conf" 'server 10.0.0.208:3000;'

rollback_reload_failure_case="$test_root/rollback-reload-failure"
mkdir -p "$rollback_reload_failure_case"
make_conf "$rollback_reload_failure_case/nginx.conf"
if run_deploy_legacy "$rollback_reload_failure_case" TOKENESS_TEST_RELOAD_FAIL_ALWAYS=1 nginx-update 10.0.0.208; then
  fail "unverified rollback reload unexpectedly succeeded"
fi
assert_contains "$rollback_reload_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$rollback_reload_failure_case/nginx.conf" 'server 10.0.0.208:3000;'
if ! find "$rollback_reload_failure_case" -name 'nginx.conf.tokeness-backup.*' -print -quit | grep -q .; then
  fail "unverified rollback did not retain the backup"
fi

post_verify_case="$test_root/post-verify"
mkdir -p "$post_verify_case"
make_conf "$post_verify_case/nginx.conf"
if run_deploy_legacy "$post_verify_case" TOKENESS_TEST_DIRECT_FAIL=1 nginx-update 10.0.0.208; then
  fail "post-update verification failure unexpectedly succeeded"
fi
assert_contains "$post_verify_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$post_verify_case/nginx.conf" 'server 10.0.0.208:3000;'

# Public (EdgeOne) failure after an nginx update must also roll back.
post_public_failure_case="$test_root/post-public-failure"
mkdir -p "$post_public_failure_case"
make_conf "$post_public_failure_case/nginx.conf"
if run_deploy_legacy "$post_public_failure_case" TOKENESS_TEST_PUBLIC_FAIL=1 nginx-update 10.0.0.208; then
  fail "post-update public failure unexpectedly succeeded"
fi
assert_contains "$post_public_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$post_public_failure_case/nginx.conf" 'server 10.0.0.208:3000;'

# Rollback must run a post-rollback probe; when even that fails, the script
# must exit nonzero and leave the old config in place.
post_rollback_probe_failure_case="$test_root/post-rollback-probe-failure"
mkdir -p "$post_rollback_probe_failure_case"
make_conf "$post_rollback_probe_failure_case/nginx.conf"
if run_deploy_legacy "$post_rollback_probe_failure_case" TOKENESS_TEST_DIRECT_FAIL=1 nginx-update 10.0.0.208; then
  fail "post-rollback probe failure unexpectedly succeeded"
fi
assert_contains "$post_rollback_probe_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$post_rollback_probe_failure_case/nginx.conf" 'server 10.0.0.208:3000;'

image_case="$test_root/image"
mkdir -p "$image_case"
make_conf "$image_case/nginx.conf"
if run_deploy "$image_case" image-ref latest; then
  fail "mutable image tag unexpectedly passed digest validation"
fi
if run_deploy "$image_case" TOKENESS_TEST_DOCKER_FAIL=1 image-ref "$VALID_DIGEST"; then
  fail "missing registry digest unexpectedly passed validation"
fi
image_ref="$(run_deploy "$image_case" image-ref "$VALID_DIGEST")"
[[ "$image_ref" == "docker.cnb.cool/imvhb/new-api-cn@$VALID_DIGEST" ]] ||
  fail "unexpected immutable image reference: $image_ref"

# IPv4 boundary checks: leading-zero and out-of-range octets must be rejected.
for bad_ip in 10.0.0.008 10.0.0.256 10.0.0 10.0.0.0.1 10.0.0.a; do
  mkdir -p "$test_root/ip-$bad_ip"
  make_conf "$test_root/ip-$bad_ip/nginx.conf"
  if run_deploy_legacy "$test_root/ip-$bad_ip" nginx-update "$bad_ip"; then
    fail "invalid IPv4 '$bad_ip' unexpectedly accepted"
  fi
done

# ---- deploy-release / ess_rollout coverage via the fake aliyun CLI ----

TEST_ML_DIGEST="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
PREV_DIGEST="sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
export TOKENESS_TEST_ML_DIGEST="$TEST_ML_DIGEST"

init_ess_state() {
  local dir="$1" log_content="${2:-ready}"
  # CreatedTime/ZoneId mirror the real DescribeScalingInstances fields the
  # release's oldest-instance selection sorts and verifies on: second-precision
  # join time and a single zone. The fixture instance predates every instance
  # the fake scale-out creates, so it is always the oldest member.
  jq -n \
    --arg image "docker.cnb.cool/imvhb/new-api-cn@$PREV_DIGEST" \
    --arg logContent "$log_content" \
    --arg created "2026-09-01T00:00:00Z" \
    '{
      desired: 1,
      instances: [{InstanceId: "eci-old", PrivateIpAddress: "10.0.0.207",
                   HealthStatus: "Healthy", LifecycleState: "InService",
                   CreatedTime: $created, ZoneId: "cn-shanghai-l"}],
      image: $image,
      envs: [{Key: "SQL_DSN", Value: "postgresql://u:p@db:5432/newapi?sslmode=disable"},
             {Key: "TZ", Value: "Asia/Shanghai"},
             {Key: "NODE_NAME", Value: "test-node"}],
      logContent: $logContent,
      config: {
        ScalingConfigurationId: "asc-test",
        ActiveDeadlineSeconds: 120,
        AutoCreateEip: true,
        AutoMatchImageCache: false,
        ContainerGroupName: "tokeness-cn-test",
        ContainersUpdateType: "RollingUpdate",
        CostOptimization: false,
        Cpu: 2.0,
        CpuOptionsCore: 2,
        CpuOptionsThreadsPerCore: 1,
        DataCacheBucket: "test-bucket",
        DataCacheBurstingEnabled: false,
        DataCachePL: "PL1",
        DataCacheProvisionedIops: 100,
        Description: "deployment test",
        DnsPolicy: "ClusterFirst",
        EgressBandwidth: 100,
        EipBandwidth: 200,
        EphemeralStorage: 1,
        GpuDriverVersion: "535",
        HostName: "tokeness-test",
        ImageSnapshotId: "snapshot-test",
        IngressBandwidth: 200,
        InstanceFamilyLevel: "EnterpriseLevel",
        Ipv6AddressCount: 0,
        LoadBalancerWeight: 0,
        Memory: 4.0,
        Override: false,
        RamRoleName: "ram-role-test",
        ResourceGroupId: "rg-test",
        RestartPolicy: "Always",
        ScalingConfigurationName: "tokeness-test-config",
        SecurityGroupId: "sg-test",
        SpotPriceLimit: 0.1,
        SpotStrategy: "NoSpot",
        TerminationGracePeriodSeconds: 30,
        InstanceType: ["ecs.g6.large", "ecs.g6.xlarge"],
        NtpServer: ["ntp.aliyun.com"],
        DnsConfig: {
          NameServer: ["8.8.8.8", "1.1.1.1"],
          Search: ["svc.cluster.local"],
          Option: [{Name: "ndots", Value: "2"}]
        },
        ImageRegistryCredential: [{Server: "docker.cnb.cool", UserName: "cnb", Password: "test-registry-password"}],
        AcrRegistryInfo: [{Domain: "registry.cn-shanghai.aliyuncs.com", InstanceId: "acr-test", InstanceName: "test", RegionId: "cn-shanghai"}],
        HostAliase: [{Hostname: "db.internal", Ip: "10.0.0.10"}],
        SecurityContextSysctl: [{Name: "net.ipv4.ip_local_port_range", Value: "1024 65535"}],
        Tag: [{Key: "env", Value: "test"}],
        Volume: [{Name: "cache", Type: "EmptyDirVolume", EmptyDirVolume: {Medium: "Memory", SizeLimit: "1Gi"}}],
        InitContainers: [{
          Name: "init",
          Image: "docker.cnb.cool/tools/init:1",
          ImagePullPolicy: "IfNotPresent",
          Cpu: 0.5,
          Memory: 0.5,
          WorkingDir: "/work",
          Arg: ["--prepare", "literal\\narg"],
          Command: ["/bin/sh", "-c"],
          EnvironmentVars: [{Key: "INIT_FLAG", Value: "false"}],
          VolumeMount: [{Name: "cache", MountPath: "/work", ReadOnly: false}]
        }],
        Containers: [{
          Name: "newapi",
          Image: $image,
          ImagePullPolicy: "IfNotPresent",
          Cpu: 1.5,
          Gpu: 0,
          Memory: 3.0,
          Stdin: false,
          StdinOnce: false,
          Tty: false,
          WorkingDir: "/app",
          Arg: ["--config", "C:\\\\tokeness\\\\config", "literal\\narg"],
          Command: ["/bin/sh", "-c", "printf 'ready\\\\n'"],
          Port: [3000],
          EnvironmentVars: [
            {Key: "SQL_DSN", Value: "postgresql://u:p@db:5432/newapi?sslmode=disable"},
            {Key: "TZ", Value: "Asia/Shanghai"},
            {Key: "NODE_NAME", Value: "test-node"},
            {Key: "FALSE_VALUE", Value: "false"},
            {Key: "BACKSLASH_VALUE", Value: "C:\\\\data\\\\literal\\\\n"},
            {Key: "TAB_VALUE", Value: "literal\\\\tvalue"}
          ],
          VolumeMount: [{Name: "cache", MountPath: "/cache", SubPath: "state", ReadOnly: false}],
          LivenessProbe: {HttpGet: {Path: "/legacy/live", Port: 3000, Scheme: "HTTP"}, InitialDelaySeconds: 5, PeriodSeconds: 7, TimeoutSeconds: 4, FailureThreshold: 2},
          ReadinessProbe: {HttpGet: {Path: "/legacy/ready", Port: 3000, Scheme: "HTTP"}, InitialDelaySeconds: 5, PeriodSeconds: 7, TimeoutSeconds: 4, FailureThreshold: 2},
          SecurityContext: {Capability: {Add: ["NET_ADMIN"]}, ReadOnlyRootFilesystem: false, RunAsUser: 1000},
          LifecyclePostStartHandler: {Exec: {Command: ["/bin/sh", "-c"]}},
          LifecyclePreStopHandler: {HttpGet: {Host: "127.0.0.1", Path: "/shutdown", Port: 3000, Scheme: "HTTP"}}
        }]
      }
    }' > "$dir/state.json"
}

# Happy path: the new instance answers /health/ready, the rollout scales 2 -> 1,
# the config is pinned to the target digest, and the liveness probe is re-sent.
release_case="$test_root/release"
mkdir -p "$release_case"
make_conf "$release_case/nginx.conf"
mkdir -p "$release_case/state"
init_ess_state "$release_case/state"
run_deploy "$release_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
assert_contains "$release_case/state/modify-args.txt" "--Container.1.Image docker.cnb.cool/imvhb/new-api-cn@$TEST_ML_DIGEST"
assert_contains "$release_case/state/modify-args.txt" "--Container.1.LivenessProbe.TcpSocket.Port 3000"
assert_contains "$release_case/state/modify-args.txt" "--Container.1.ReadinessProbe.HttpGet.Path /health/ready"
# The shutdown budget is managed declaratively, so it must be present in the
# actual API call - the readback check compares against the same desired
# document, which is what makes a silent revert to the platform default fail
# verification instead of passing unnoticed.
assert_contains "$release_case/state/modify-args.txt" "--TerminationGracePeriodSeconds 240"
# The env Value is redacted in this log by design, so presence of the KEY is
# what proves injection here; the value itself is covered by the readback
# verification, which compares the live configuration against the same desired
# document and aborts the release on any drift.
grep -Eq -- '--Container\.1\.EnvironmentVar\.[0-9]+\.Key SHUTDOWN_TIMEOUT_SECONDS' \
  "$release_case/state/modify-args.txt" \
  || fail "the application drain budget did not reach the scaling configuration"
assert_contains "$release_case/state/modify-args.txt" "--Container.1.EnvironmentVar.8.Value [REDACTED]"
# Rollback stays byte-faithful. Restore mode returns a snapshot exactly as it
# was: a grace period the snapshot carried survives, and one it never carried is
# NOT injected - otherwise a rollback would quietly re-apply hardening it was
# supposed to undo. Target mode is the only place the managed value appears.
python3 - "$TEST_DIR/../config_args.py" <<'PYEOF' || fail "restore mode is not lossless for the managed fields"
import json, subprocess, sys

serializer = sys.argv[1]

def emit(mode, document, extra=()):
    result = subprocess.run(
        [sys.executable, serializer, "--mode", mode, *extra, "--emit"],
        input=json.dumps(document), capture_output=True, text=True, check=True,
    )
    return result.stdout.split("\0")

def document(grace=None):
    config = {"ScalingConfigurationId": "asc-test",
              "Containers": [{"Name": "newapi", "Image": "x"}]}
    if grace is not None:
        config["TerminationGracePeriodSeconds"] = grace
    return {"ScalingConfigurations": [config]}

restored = emit("restore", document(30))
if "--TerminationGracePeriodSeconds" not in restored or "30" not in restored:
    sys.exit("restore dropped a grace period the snapshot carried")
if "--TerminationGracePeriodSeconds" in emit("restore", document()):
    sys.exit("restore injected a managed field the snapshot did not carry")
targeted = emit("target", document(), (
    "--digest", "sha256:" + "a" * 64, "--target-name", "newapi",
    "--termination-grace-seconds", "240",
))
if "240" not in targeted:
    sys.exit("target mode did not apply the managed grace period")
PYEOF
assert_contains "$release_case/state/modify-args.txt" "--Container.1.ReadinessProbe.HttpGet.Port 3000"
assert_contains "$release_case/state/modify-args.txt" "--Container.1.Name newapi"
assert_contains "$release_case/state/modify-args.txt" "--Container.1.EnvironmentVar.3.Key NODE_NAME"
assert_contains "$release_case/state/modify-args.txt" "--Container.1.EnvironmentVar.7.Key NODE_TYPE"
assert_contains "$release_case/state/modify-args.txt" "--Cpu 2"
assert_contains "$release_case/state/modify-args.txt" "--Memory 4"
assert_contains "$release_case/state/modify-args.txt" "--SecurityGroupId sg-test"
assert_contains "$release_case/state/modify-args.txt" "--AutoCreateEip true"
assert_contains "$release_case/state/modify-args.txt" "--EipBandwidth 200"
# The fake redacts every registry credential value, including the server name.
assert_contains "$release_case/state/modify-args.txt" "--ImageRegistryCredential.1.Server [REDACTED]"
assert_not_contains "$release_case/state/modify-args.txt" "test-registry-password"
assert_not_contains "$release_case/state/modify-args.txt" "postgresql://u:p@db:5432/newapi?sslmode=disable"
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$TEST_ML_DIGEST"'"' "$release_case/state/state.json" > /dev/null \
  || fail "scaling configuration image was not pinned to the release digest"
local_image_digest="$(jq -r '.image' "$release_case/state/state.json" | sed 's/.*@//')"
[[ "$local_image_digest" == "$TEST_ML_DIGEST" ]] || fail "unexpected pinned digest"
grep -q "ess ModifyScalingGroup .*--DesiredCapacity 2" "$release_case/state/aliyun-calls.log" \
  || fail "rollout never scaled out to 2"
grep -q "ess ModifyScalingGroup .*--DesiredCapacity 1" "$release_case/state/aliyun-calls.log" \
  || fail "rollout never scaled back to 1"
grep -q "eci DescribeContainerLog" "$release_case/state/aliyun-calls.log" \
  || fail "rollout never inspected the container log"
# EIP → shared-bandwidth convergence: after the rollout the surviving (new)
# instance's auto-created EIP must be bound to the shared bandwidth package.
grep -q "vpc AddCommonBandwidthPackageIp .*--IpInstanceId eip-eci-new-1" "$release_case/state/aliyun-calls.log" \
  || fail "rollout never bound the instance EIP to the shared bandwidth package"
# The deploy pipeline must converge the master container to the same release,
# blue-green: start gates the green container, the commit flips the ECS's own
# nginx - the panel tier, now that the lightweight hosts retired - and the
# post-commit probe must confirm the ECS serves the green port before the ESS
# group scales out.
assert_contains "$release_case/state/host-bootstrap.log" "host-bootstrap invocation 1 phase=start"
assert_contains "$release_case/state/host-bootstrap.log" "host-bootstrap invocation 2 phase=commit"
# The post-commit probe is the gate that replaced the retired marker
# convergence: it reads the live serving port rather than our intent.
assert_contains "$release_case/state/host-bootstrap.log" "host-bootstrap invocation 3 phase=probe"
[[ "$(wc -l < "$release_case/state/host-bootstrap.log")" -eq 3 ]] \
  || fail "blue-green master roll did not run exactly start+commit+probe"
# The retired lightweight hosts are not touched at all: the panel tier is this
# ECS, the commit flips its nginx, and the post-commit probe proves it.
grep -q 'panel tier is this ECS' "$release_case/state/stdout.log" \
  || fail "the release did not take the ECS panel-tier path"
grep -q 'ECS serves green, blue retired' "$release_case/state/stdout.log" \
  || fail "the post-commit probe did not confirm the ECS serving green"
[[ "$(grep -c 'web-port-pin' "$release_case/state/aliyun-calls.log" || true)" -eq 0 ]] \
  || fail "the release pinned a retired lightweight host"
[[ ! -e "$release_case/state/web-primary-port" ]] \
  || fail "a web-primary marker was written although the lightweight tier retired"
# The relay entry is the ECS too: its upstream file is what the rollout gates on
# and what verification reads, so it must name the promoted instance.
assert_contains "$release_case/ecs-upstream.conf" "server 10.0.0.241:3000;"
# No step of a release may reach a retired lightweight host. This is the guard
# the app-readiness probe lacked: it kept SSHing to 8.133.172.195 to read the new
# instance's health, timed out for its whole window once the hosts were deleted,
# and rolled back a healthy instance - the failure .30 died of.
for retired in 8.133.172.195 101.133.234.135; do
  if grep -q "^ssh $retired" "$release_case/state/aliyun-calls.log"; then
    fail "the release contacted the retired lightweight host $retired"
  fi
done
# Master-first: the whole blue-green cycle (start..commit) must complete
# before the ESS group scales out.
first_bootstrap="$(grep -n '^host-bootstrap$' "$release_case/state/aliyun-calls.log" | head -n1 | cut -d: -f1)"
last_bootstrap="$(grep -n '^host-bootstrap$' "$release_case/state/aliyun-calls.log" | tail -n1 | cut -d: -f1)"
first_scaleout="$(grep -n 'ess ModifyScalingGroup .*--DesiredCapacity 2' "$release_case/state/aliyun-calls.log" | head -n1 | cut -d: -f1)"
[[ -n "$first_bootstrap" && -n "$first_scaleout" && "$first_bootstrap" -lt "$first_scaleout" ]] \
  || fail "master container roll did not run before the ESS scale-out"
[[ -n "$last_bootstrap" && "$last_bootstrap" -lt "$first_scaleout" ]] \
  || fail "blue-green commit did not complete before the ESS scale-out"

# CI-certified digest: passing the digest explicitly must pin exactly that
# digest even though the registry would resolve a different one for the tag.
CERTIFIED_DIGEST="sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
certified_case="$test_root/release-certified-digest"
mkdir -p "$certified_case"
make_conf "$certified_case/nginx.conf"
mkdir -p "$certified_case/state"
init_ess_state "$certified_case/state"
run_deploy "$certified_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9 "$CERTIFIED_DIGEST"
[[ "$(jq -r '.image' "$certified_case/state/state.json" | sed 's/.*@//')" == "$CERTIFIED_DIGEST" ]] \
  || fail "certified digest was not used; the release fell back to the registry lookup"
jq -e '.image != "docker.cnb.cool/imvhb/new-api-cn@'"$TEST_ML_DIGEST"'"' \
  "$certified_case/state/state.json" > /dev/null \
  || fail "certified-digest case unexpectedly pinned the registry-resolved digest"

# Host version mismatch must fail the release BEFORE the ECI tier moves.
host_mismatch_case="$test_root/host-mismatch"
mkdir -p "$host_mismatch_case"
make_conf "$host_mismatch_case/nginx.conf"
mkdir -p "$host_mismatch_case/state"
init_ess_state "$host_mismatch_case/state"
if run_deploy "$host_mismatch_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v0.0.0-wrong-host \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "host version mismatch unexpectedly passed the release"
fi
grep -q "master version" "$host_mismatch_case/state/output.log" 2>/dev/null \
  || fail "mismatch case did not report the master version problem"
if grep -q "ess ModifyScalingGroup" "$host_mismatch_case/state/aliyun-calls.log"; then
  fail "ESS rollout started even though the master never matched the release"
fi
# The failed green roll reconciles (abort), then the recovery path re-rolls
# blue-green from the restored digest: start+abort, then start+commit+probe (the
# post-commit probe is the gate that replaced the retired marker convergence).
grep -q "phase=abort" "$host_mismatch_case/state/host-bootstrap.log" \
  || fail "master roll did not reconcile via abort after the version mismatch"
[[ "$(wc -l < "$host_mismatch_case/state/host-bootstrap.log")" -eq 6 ]] \
  || fail "unexpected bootstrap invocation count after the version mismatch"
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$PREV_DIGEST"'"' "$host_mismatch_case/state/state.json" > /dev/null \
  || fail "scaling configuration was not restored after the master version mismatch"

# Blue-green panel flip fails closed: when the web-primary pin cannot be
# written on a lightweight host, the release aborts before commit and before
# the ESS rollout - blue keeps serving, the abort reconciles green away.
# SWAS_PANEL_TIER=1 keeps the retired panel path covered: its ml-sync was
# stopped rather than deleted, so it is one systemctl away from mattering.
web_flip_fail_case="$test_root/web-flip-fail"
mkdir -p "$web_flip_fail_case"
make_conf "$web_flip_fail_case/nginx.conf"
mkdir -p "$web_flip_fail_case/state"
init_ess_state "$web_flip_fail_case/state"
if run_deploy "$web_flip_fail_case" \
  SWAS_PANEL_TIER=1 \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  TOKENESS_TEST_SSH_FAIL_ON='web_port_marker' \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "release unexpectedly succeeded while the web-primary pin was refused"
fi
grep -q 'web-port-pin' "$web_flip_fail_case/state/aliyun-calls.log" \
  || fail "the retired panel-tier path stopped pinning the lightweight hosts"
if grep -q "ess ModifyScalingGroup" "$web_flip_fail_case/state/aliyun-calls.log"; then
  fail "ESS rollout started even though the panel tier was never switched"
fi
if grep -q "phase=commit" "$web_flip_fail_case/state/host-bootstrap.log"; then
  fail "blue was retired even though the panel tier never moved to green"
fi
grep -q "phase=abort" "$web_flip_fail_case/state/host-bootstrap.log" \
  || fail "the failed panel switch was not reconciled via abort"
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$PREV_DIGEST"'"' "$web_flip_fail_case/state/state.json" > /dev/null \
  || fail "scaling configuration was not restored after the failed panel switch"

# Fail closed on the ECS path too: if the ECS relay entry never picks the new
# instance up, the rollout must abort while the old instance still serves rather
# than scale it away. A stuck fleet-sync is modeled with the sync disabled: the
# upstream stays on the retired member, exactly what a broken ecs-fleet-sync
# would leave behind (the upstream is derived, so the fixture no longer
# hand-writes it).
ecs_gate_fail_case="$test_root/ecs-gate-fail"
mkdir -p "$ecs_gate_fail_case"
make_conf "$ecs_gate_fail_case/nginx.conf"
mkdir -p "$ecs_gate_fail_case/state"
printf 'server 10.0.0.207:3000;\n' > "$ecs_gate_fail_case/ecs-upstream.conf"
init_ess_state "$ecs_gate_fail_case/state"
if run_deploy "$ecs_gate_fail_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  TOKENESS_TEST_FLEET_SYNC_OFF=1 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "release unexpectedly succeeded although the ECS relay entry never served the new instance"
fi
grep -q "relay tier did not start serving" "$ecs_gate_fail_case/state/output.log" \
  || fail "the ECS relay gate failure did not explain itself"
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$PREV_DIGEST"'"' "$ecs_gate_fail_case/state/state.json" > /dev/null \
  || fail "the scaling configuration was not restored after the ECS relay gate failed"
# By IDENTITY, not count: "1 instance" would also pass if the old one was
# deleted and the failed new one kept serving on the old image.
jq -e '([.instances[].InstanceId] | index("eci-old")) != null' \
  "$ecs_gate_fail_case/state/state.json" > /dev/null \
  || fail "the failed ECS relay gate removed the pre-existing instance"

# Master-first abort: a failing host bootstrap aborts the release before the
# ESS group is touched, restores the previous scaling configuration, and
# re-bootstraps the host from the restored (previous) digest.
host_fail_case="$test_root/host-fail"
mkdir -p "$host_fail_case"
make_conf "$host_fail_case/nginx.conf"
mkdir -p "$host_fail_case/state"
init_ess_state "$host_fail_case/state"
if run_deploy "$host_fail_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_FAIL_TIMES=1 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "release with a failing master sync unexpectedly succeeded"
fi
if grep -q "ess ModifyScalingGroup" "$host_fail_case/state/aliyun-calls.log"; then
  fail "ESS rollout started even though the master bootstrap failed"
fi
assert_contains "$host_fail_case/state/modify-args.txt" "--Container.1.Image docker.cnb.cool/imvhb/new-api-cn@$PREV_DIGEST"
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$PREV_DIGEST"'"' "$host_fail_case/state/state.json" > /dev/null \
  || fail "scaling configuration was not restored after the failed master sync"
# The green start failed (blue untouched, so no abort), then the recovery path
# re-rolls blue-green from the restored digest: failed start, then start+commit.
[[ "$(wc -l < "$host_fail_case/state/host-bootstrap.log")" -eq 4 ]] \
  || fail "host was not re-rolled blue-green from the restored configuration"
grep -q "phase=commit" "$host_fail_case/state/host-bootstrap.log" \
  || fail "recovery roll did not complete with a commit"

# App never becomes ready: the rollout must fail BEFORE scaling down (the old
# instance keeps serving), re-pin the previous digest, and delete the failed
# container so ESS replaces it.
app_failure_case="$test_root/app-failure"
mkdir -p "$app_failure_case"
make_conf "$app_failure_case/nginx.conf"
mkdir -p "$app_failure_case/state"
init_ess_state "$app_failure_case/state"
if run_deploy "$app_failure_case" \
  APP_READY_TIMEOUT_SECONDS=6 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  TOKENESS_TEST_DIRECT_FAIL=1 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "rollout with a never-ready app unexpectedly succeeded"
fi
assert_contains "$app_failure_case/state/modify-args.txt" "--Container.1.Image docker.cnb.cool/imvhb/new-api-cn@$PREV_DIGEST"
grep -q "eci DeleteContainerGroup" "$app_failure_case/state/aliyun-calls.log" \
  || fail "failed rollout did not delete the broken container"
jq -e '.desired == 1 and ([.instances[].InstanceId] | index("eci-old") != null)' \
  "$app_failure_case/state/state.json" > /dev/null \
  || fail "rollback did not converge back to a single old instance"
# Master-first: the host took the release before the rollout
# (start+commit+probe); after the failed rollout the master must be re-synced to
# the restored previous digest (another start+commit+probe, so six invocations).
[[ "$(wc -l < "$app_failure_case/state/host-bootstrap.log")" -eq 6 ]] \
  || fail "master was not re-synced after the failed rollout"
grep -q "phase=commit" "$app_failure_case/state/host-bootstrap.log" \
  || fail "master re-sync after the failed rollout did not commit"

# A FATAL line in the container log must abort the rollout fast (fail-fast
# instead of waiting out the whole readiness timeout).
fatal_case="$test_root/fatal"
mkdir -p "$fatal_case/state"
make_conf "$fatal_case/nginx.conf"
init_ess_state "$fatal_case/state" '[FATAL] 2026/09/06 | [cannot parse postgresql://...: invalid control character in URL]'
if run_deploy "$fatal_case" \
  APP_READY_TIMEOUT_SECONDS=30 APP_READY_POLL_SECONDS=5 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "rollout with a FATAL container log unexpectedly succeeded"
fi
grep -q "eci DeleteContainerGroup" "$fatal_case/state/aliyun-calls.log" \
  || fail "FATAL log did not trigger container cleanup"
# Same accounting as the app-failure case: the release roll
# (start+commit+probe) plus the post-failure re-sync (start+commit+probe).
[[ "$(wc -l < "$fatal_case/state/host-bootstrap.log")" -eq 6 ]] \
  || fail "master was not re-synced after the FATAL-aborted rollout"

# ---- rollout-all coverage ---------------------------------------------------
# ess_rollout replaces exactly one instance per release, so a six-instance tier
# needs six releases before it serves one image. rollout-all batches the same
# tested steps under the scaling group's MaxSize instead of adding a new path
# beside them; these cases pin that contract.

# (1) Full batch: MaxSize=6 with a steady state of 3 lets ONE round replace
# THREE instances at once - the case the command exists for. The three
# pre-existing members must all be parked in the same drain window (asserted
# from the fake's modify-time snapshot: the marker content and the relay
# upstream AT the shrink), not merely "gone afterwards".
batch_case="$test_root/rollout-all-batch"
mkdir -p "$batch_case/state"
make_conf "$batch_case/nginx.conf"
init_ess_state "$batch_case/state"
jq '.max_size = 6 | .desired = 3 | .instances += [
      {InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"},
      {InstanceId: "eci-old3", PrivateIpAddress: "10.0.0.205", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-03T00:00:00Z", ZoneId: "cn-shanghai-l"}]' \
  "$batch_case/state/state.json" > "$batch_case/state/tmp.json" \
  && mv "$batch_case/state/tmp.json" "$batch_case/state/state.json"
run_deploy "$batch_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  rollout-all
grep -q "batch rollout: 3 instance(s) to retire, up to 3 per round" "$batch_case/state/stdout.log" \
  || fail "rollout-all did not size the batch from MaxSize"
grep -q "retired all 3 pre-existing instance(s) in 1 round(s)" "$batch_case/state/stdout.log" \
  || fail "rollout-all did not converge the tier in a single round"
# Two ESS calls: up to stable+batch, then back to steady state. More would mean it
# silently degraded to one-at-a-time.
[[ "$(grep -c 'ess ModifyScalingGroup' "$batch_case/state/aliyun-calls.log")" -eq 2 ]] \
  || fail "rollout-all did not batch the replacements into one drain window"
# At the shrink call itself, the marker parked ALL THREE identities (one comma
# argument, not a newline list that the remote shell would split) and the relay
# upstream carried none of them.
snap_shrink="$batch_case/state/modify-snapshots/modify-2.log"
[[ -f "$snap_shrink" ]] || fail "the fake recorded no snapshot of the shrink call"
for want_ip in 10.0.0.207 10.0.0.206 10.0.0.205; do
  grep -qxF "$want_ip" "$snap_shrink" \
    || fail "at the shrink, the drain marker did not park $want_ip"
  if sed -n '/--- relay upstream ---/,$p' "$snap_shrink" | grep -q "$want_ip"; then
    fail "at the shrink, the relay upstream still carried the parked $want_ip"
  fi
done
jq -e '(.instances | length) == 3
       and ([.instances[].InstanceId] | index("eci-old") == null)
       and ([.instances[].InstanceId] | index("eci-old2") == null)
       and ([.instances[].InstanceId] | index("eci-old3") == null)' \
  "$batch_case/state/state.json" > /dev/null \
  || fail "rollout-all left a pre-existing instance serving"
jq -e '.desired == 3' "$batch_case/state/state.json" > /dev/null \
  || fail "rollout-all did not return the group to its steady-state capacity"

# (2) No headroom: MaxSize == DesiredCapacity. The command must refuse up front
# rather than half-rolling, and must not touch the scaling group at all.
no_headroom_case="$test_root/rollout-all-no-headroom"
mkdir -p "$no_headroom_case/state"
make_conf "$no_headroom_case/nginx.conf"
init_ess_state "$no_headroom_case/state"
jq '.max_size = 1' "$no_headroom_case/state/state.json" > "$no_headroom_case/state/tmp.json" \
  && mv "$no_headroom_case/state/tmp.json" "$no_headroom_case/state/state.json"
if run_deploy "$no_headroom_case" rollout-all; then
  fail "rollout-all without headroom unexpectedly succeeded"
fi
grep -q "no headroom to batch" "$no_headroom_case/state/output.log" \
  || fail "rollout-all did not explain the missing headroom"
if grep -q "ess ModifyScalingGroup" "$no_headroom_case/state/aliyun-calls.log"; then
  fail "rollout-all touched the scaling group despite having no headroom"
fi
jq -e '(.instances | length) == 1 and ([.instances[].InstanceId] | index("eci-old") != null)' \
  "$no_headroom_case/state/state.json" > /dev/null \
  || fail "the refused rollout still churned instances"

# (3) Multi-round: MaxSize=3 with a steady state of 2 allows only one
# replacement per round, so two pre-existing instances need two rounds. This is
# the convergence bound - each round must retire at least one, and the loop must
# stop on its own once none is left.
multi_case="$test_root/rollout-all-multi"
mkdir -p "$multi_case/state"
make_conf "$multi_case/nginx.conf"
init_ess_state "$multi_case/state"
jq '.desired = 2 | .max_size = 3 | .instances += [{
      InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206",
      HealthStatus: "Healthy", LifecycleState: "InService",
      CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"}]' \
  "$multi_case/state/state.json" > "$multi_case/state/tmp.json" \
  && mv "$multi_case/state/tmp.json" "$multi_case/state/state.json"
printf 'server 10.0.0.241:3000;\nserver 10.0.0.242:3000;\n' > "$multi_case/ecs-upstream.conf"
run_deploy "$multi_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  rollout-all
grep -q "retired all 2 pre-existing instance(s) in 2 round(s)" "$multi_case/state/stdout.log" \
  || fail "rollout-all did not converge in one round per replaceable instance"
# Four calls: (3 -> 2) twice.
[[ "$(grep -c 'ess ModifyScalingGroup' "$multi_case/state/aliyun-calls.log")" -eq 4 ]] \
  || fail "rollout-all issued an unexpected number of scaling calls"
jq -e '(.instances | length) == 2
       and ([.instances[].InstanceId] | index("eci-old") == null)
       and ([.instances[].InstanceId] | index("eci-old2") == null)' \
  "$multi_case/state/state.json" > /dev/null \
  || fail "rollout-all did not retire every pre-existing instance"
jq -e '.desired == 2' "$multi_case/state/state.json" > /dev/null \
  || fail "rollout-all did not restore the steady-state capacity after the last round"

# (4) A release converges the WHOLE tier, not just the instance ess_rollout
# replaced. Three pre-existing instances with MaxSize=4 give the batch one slot
# per round, so convergence takes two rounds after the release's own roll - and
# the instance that roll already replaced (eci-new-1) must survive untouched,
# which is why the retire set is snapshotted BEFORE the roll.
converge_case="$test_root/release-converges-tier"
mkdir -p "$converge_case/state"
make_conf "$converge_case/nginx.conf"
init_ess_state "$converge_case/state"
jq '.desired = 3 | .max_size = 4 | .instances += [
      {InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"},
      {InstanceId: "eci-old3", PrivateIpAddress: "10.0.0.205", HealthStatus: "Healthy", LifecycleState: "InService", CreatedTime: "2026-09-03T00:00:00Z", ZoneId: "cn-shanghai-l"}]' \
  "$converge_case/state/state.json" > "$converge_case/state/tmp.json" \
  && mv "$converge_case/state/tmp.json" "$converge_case/state/state.json"
printf 'server 10.0.0.241:3000;\nserver 10.0.0.242:3000;\nserver 10.0.0.243:3000;\n' \
  > "$converge_case/ecs-upstream.conf"
run_deploy "$converge_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
grep -q "batch rollout: 3 instance(s) to retire, up to 1 per round" "$converge_case/state/stdout.log" \
  || fail "the release did not hand the batch rollout the pre-release instance set"
grep -q "retired all 3 pre-existing instance(s) in 2 round(s)" "$converge_case/state/stdout.log" \
  || fail "the release did not converge the rest of the tier"
grep -q "deployed to the whole ECI tier" "$converge_case/state/stdout.log" \
  || fail "the release did not report whole-tier convergence"
# Six calls: the release's own roll (4 then 3) plus two batch rounds (4 then 3).
[[ "$(grep -c 'ess ModifyScalingGroup' "$converge_case/state/aliyun-calls.log")" -eq 6 ]] \
  || fail "the converging release issued an unexpected number of scaling calls"
jq -e '(.instances | length) == 3
       and ([.instances[].InstanceId] | index("eci-old") == null)
       and ([.instances[].InstanceId] | index("eci-old2") == null)
       and ([.instances[].InstanceId] | index("eci-old3") == null)
       and ([.instances[].InstanceId] | index("eci-new-1") != null)' \
  "$converge_case/state/state.json" > /dev/null \
  || fail "the release left a pre-existing instance serving, or re-replaced the one it had just rolled"
jq -e '.desired == 3' "$converge_case/state/state.json" > /dev/null \
  || fail "the converging release did not restore the steady-state capacity"

# (5) ROLLOUT_BATCH=0 keeps the old one-at-a-time behaviour for a release that
# only touches master-served paths, and an invalid value is refused rather than
# silently treated as "on".
optout_case="$test_root/release-rollout-all-optout"
mkdir -p "$optout_case/state"
make_conf "$optout_case/nginx.conf"
init_ess_state "$optout_case/state"
run_deploy "$optout_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  ROLLOUT_BATCH=0 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
grep -q "deployed to one instance (batch convergence skipped" "$optout_case/state/stdout.log" \
  || fail "ROLLOUT_BATCH=0 did not skip the batch convergence"
if grep -q "batch rollout" "$optout_case/state/stdout.log"; then
  fail "ROLLOUT_BATCH=0 still ran the batch convergence"
fi
# Two calls: the release's own single-instance roll, and nothing else.
[[ "$(grep -c 'ess ModifyScalingGroup' "$optout_case/state/aliyun-calls.log")" -eq 2 ]] \
  || fail "ROLLOUT_BATCH=0 did not keep the one-at-a-time roll"

invalid_rollout_case="$test_root/release-rollout-all-invalid"
mkdir -p "$invalid_rollout_case/state"
make_conf "$invalid_rollout_case/nginx.conf"
init_ess_state "$invalid_rollout_case/state"
if run_deploy "$invalid_rollout_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  ROLLOUT_BATCH=maybe \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "an invalid ROLLOUT_BATCH value was accepted"
fi
grep -q "ROLLOUT_BATCH must be a non-negative integer" "$invalid_rollout_case/state/output.log" \
  || fail "an invalid ROLLOUT_BATCH value did not explain itself"
# The value is validated before any tier mutation: the release's own roll must
# not have started.
if grep -q "ess ModifyScalingGroup" "$invalid_rollout_case/state/aliyun-calls.log"; then
  fail "an invalid ROLLOUT_BATCH value was rejected only after the tier had moved"
fi

# (6) An EXPLICIT batch size is a demand, not a preference: asking for more than
# the group's headroom must be refused before anything mutates, while the
# built-in default is clamped instead (case (4) relies on exactly that clamp).
overbatch_case="$test_root/release-batch-over-headroom"
mkdir -p "$overbatch_case/state"
make_conf "$overbatch_case/nginx.conf"
init_ess_state "$overbatch_case/state"
# MaxSize=3 against a steady state of 1 leaves room for 2, so asking for 3 is the
# impossible demand this case is about.
jq '.max_size = 3' "$overbatch_case/state/state.json" > "$overbatch_case/state/tmp.json" \
  && mv "$overbatch_case/state/tmp.json" "$overbatch_case/state/state.json"
if run_deploy "$overbatch_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  ROLLOUT_BATCH=3 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "an explicit batch size above the group's headroom was accepted"
fi
grep -q "exceeds the growth ceiling" "$overbatch_case/state/output.log" \
  || fail "an over-large explicit batch did not explain the headroom limit"
if grep -q "ess ModifyScalingGroup" "$overbatch_case/state/aliyun-calls.log"; then
  fail "an over-large explicit batch was rejected only after the tier had moved"
fi
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$PREV_DIGEST"'"' "$overbatch_case/state/state.json" > /dev/null \
  || fail "the refused release still re-pinned the scaling configuration"

# (6b) Two releases started at the same time: exactly one may own the tier.
# The check-and-acquire happens under the entry host's marker lock, so the
# second release must refuse (a fresh foreign hold), not interleave with the
# first. Atomic writes alone were not mutual exclusion: both could write and
# each read back its own owner.
mutex_case="$test_root/release-mutex"
first="$mutex_case/first"; second="$mutex_case/second"
for d in "$first" "$second"; do
  mkdir -p "$d/state"
  make_conf "$d/nginx.conf"
  init_ess_state "$d/state"
done
shared_hold="$mutex_case/shared-hold"
shared_drain="$mutex_case/shared-drain"
shared_lock="$mutex_case/shared-marker.lock"
# Rendezvous on the lock itself: an external holder keeps the marker lock
# busy while BOTH releases start and reach their acquire; releasing it wakes
# the two contenders together. The winner is enforced by the lock-protected
# check-and-acquire AND the read-back (either alone catches the loser: the
# read-back rejects a foreign owner even without the flock, the flock removes
# the window where both could write). Both logs below must show the acquire
# attempt, proving both actually raced rather than one starting late.
(
  exec 200>"$shared_lock"
  flock -x 200
  # Hold until BOTH contenders have logged their acquire attempt, then a short
  # beat so both are actually blocked on the lock: a fixed sleep was flaky on
  # slow machines (the gate could open before either arrived).
  for _ in $(seq 1 100); do
    if grep -qs "acquiring the entry shrink guard hold" "$first/state/stdout.log" \
       && grep -qs "acquiring the entry shrink guard hold" "$second/state/stdout.log"; then
      break
    fi
    sleep 0.2
  done
  sleep 0.4
) &
mutex_gate_pid=$!
run_deploy "$first" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  SHRINK_HOLD_PATH="$shared_hold" \
  SHRINK_DRAIN_PATH="$shared_drain" \
  MARKER_LOCK_PATH="$shared_lock" \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9 &
mutex_first_pid=$!
run_deploy "$second" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  SHRINK_HOLD_PATH="$shared_hold" \
  SHRINK_DRAIN_PATH="$shared_drain" \
  MARKER_LOCK_PATH="$shared_lock" \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9 &
mutex_second_pid=$!
mutex_rc1=0; mutex_rc2=0
wait "$mutex_gate_pid" 2>/dev/null || true
wait "$mutex_first_pid" || mutex_rc1=$?
wait "$mutex_second_pid" || mutex_rc2=$?
if (( mutex_rc1 == 0 && mutex_rc2 == 0 )); then
  fail "two releases ran the tier concurrently"
fi
if (( mutex_rc1 != 0 && mutex_rc2 != 0 )); then
  fail "both concurrent releases failed (expected exactly one winner)"
fi
loser="$first"; winner="$second"
if (( mutex_rc2 != 0 )); then loser="$second"; winner="$first"; fi
grep -qE "refusing to release|refusing to run two at once|owned by|is owned by" "$loser/state/output.log" \
  || fail "the losing release did not explain its refusal"
grep -q "master container blue-green complete" "$winner/state/stdout.log" \
  || fail "the winning release did not complete its roll"
# Both contenders reached the acquire while the gate held the lock: the race
# was real, not one release starting after the other finished.
for d in "$first" "$second"; do
  grep -q "acquiring the entry shrink guard hold" "$d/state/stdout.log" \
    || fail "a contender never reached the hold acquire ($d)"
done

# (7) A group with no headroom cannot be released at all: even the
# one-at-a-time leg scales the tier to stable+1 while the old instance still
# serves, and ESS rejects a DesiredCapacity above MaxSize. The fake now
# enforces that boundary (the real API always did), so the release must refuse
# UP FRONT - before any scale call, with the reason - instead of "succeeding"
# on a fake that ignored MaxSize.
noheadroom_release_case="$test_root/release-no-headroom"
mkdir -p "$noheadroom_release_case/state"
make_conf "$noheadroom_release_case/nginx.conf"
init_ess_state "$noheadroom_release_case/state"
jq '.max_size = 1' "$noheadroom_release_case/state/state.json" > "$noheadroom_release_case/state/tmp.json" \
  && mv "$noheadroom_release_case/state/tmp.json" "$noheadroom_release_case/state/state.json"
if run_deploy "$noheadroom_release_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "a release succeeded on a group pinned at its MaxSize"
fi
grep -q "no headroom to roll" "$noheadroom_release_case/state/output.log" \
  || fail "a headroom-less release did not refuse with the reason"
if grep -q "ess ModifyScalingGroup" "$noheadroom_release_case/state/aliyun-calls.log"; then
  fail "a headroom-less release still touched the scaling group"
fi
jq -e '([.instances[].InstanceId] | index("eci-old")) != null' \
  "$noheadroom_release_case/state/state.json" > /dev/null \
  || fail "the refused release disturbed the serving instance"

# (8) REGRESSION: scaling in is asynchronous, so the group keeps REPORTING the
# detached instances for a while. A gate that accepts "at least N" therefore
# passes the instant it is asked, the next round scales out while the removal is
# still pending, the ESS supersedes that removal, no new instance appears - and
# the rollout aborts after hours. That is precisely how
# v1.0.0-rc.40-tokeness-mainland.40 died on 2026-10-09:
#
#   [18:00:47] scaling group desired capacity set to 6
#   [18:00:48] healthy instances: 8 (want 6)      <- "at least" gate, passed
#   [18:00:49] ERROR: round 3: ESS reported no new instance after scaling to 8
#
# The fake now emulates the window (TOKENESS_TEST_SCALE_IN_LAG_SECONDS) and the
# supersede, so this case fails on the old gate and passes on the exact one.
race_case="$test_root/rollout-all-scale-in-race"
mkdir -p "$race_case/state"
make_conf "$race_case/nginx.conf"
init_ess_state "$race_case/state"
jq '.desired = 2 | .max_size = 3 | .instances += [{
      InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206",
      HealthStatus: "Healthy", LifecycleState: "InService",
      CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"}]' \
  "$race_case/state/state.json" > "$race_case/state/tmp.json" \
  && mv "$race_case/state/tmp.json" "$race_case/state/state.json"
printf 'server 10.0.0.241:3000;\nserver 10.0.0.242:3000;\n' > "$race_case/ecs-upstream.conf"
run_deploy "$race_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  HEALTH_POLL_SECONDS=1 \
  TOKENESS_TEST_SCALE_IN_LAG_SECONDS=3 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  rollout-all
# The parking must actually happen: without this the drain marker is never written
# and the assertion below (that none is left behind) passes for the wrong reason.
grep -q "parked the retiring instance" "$race_case/state/stdout.log" \
  || fail "the round did not park its retiring instance in the relay drain file"
grep -q "scale-in complete:" "$race_case/state/stdout.log" \
  || fail "the rollout did not wait for the asynchronous scale-in to settle"
grep -q "retired all 2 pre-existing instance(s) in 2 round(s)" "$race_case/state/stdout.log" \
  || fail "the rollout did not converge once the scale-in settled"
jq -e '(.instances | length) == 2
       and ([.instances[].InstanceId] | index("eci-old") == null)
       and ([.instances[].InstanceId] | index("eci-old2") == null)' \
  "$race_case/state/state.json" > /dev/null \
  || fail "the rollout did not retire every pre-existing instance after the race"
# At each shrink call itself the drain marker was still parked on that round's
# victim and the relay upstream had already dropped it (#12): end-state
# assertions cannot prove the parking ever happened - a relay_drain_write that
# silently no-ops leaves the same final state.
for r in '2 10.0.0.207' '4 10.0.0.206'; do
  set -- $r
  snap="$race_case/state/modify-snapshots/modify-$1.log"
  [[ -f "$snap" ]] || fail "the fake recorded no snapshot of shrink #$1"
  grep -qxF "$2" "$snap" || fail "at shrink #$1, the marker did not park $2"
  if sed -n '/--- relay upstream ---/,$p' "$snap" | grep -q "$2"; then
    fail "at shrink #$1, the relay upstream still carried the parked $2"
  fi
done

# (8b) H1 guard: when the group's removal policy is not exactly OldestInstance,
# age order does not predict which instance a scale-in removes, so parking the
# "oldest" would drain instance A while ESS releases B. The batch round must
# refuse and say why, not park a guess. (deploy-release's first leg does not
# park yet - that is the ess_rollout gap - so this aims the batch path where
# parking actually runs.)
h1_policy_case="$test_root/oldest-policy-refused"
mkdir -p "$h1_policy_case/state"
make_conf "$h1_policy_case/nginx.conf"
init_ess_state "$h1_policy_case/state"
if run_deploy "$h1_policy_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  TOKENESS_TEST_REMOVAL_POLICY=NewestInstance \
  rollout-batch 1; then
  fail "a batch rollout proceeded although the removal policy makes the scale-in victim unpredictable"
fi
grep -q "not exactly OldestInstance" "$h1_policy_case/state/output.log" \
  || fail "the policy refusal did not explain itself"
# The refusal must not silently REMOVE anything: the pre-existing instance is
# still in service (the round legitimately grew the tier before balking at the
# unparkable retire set; an extra healthy member is the safe direction).
jq -e '([.instances[].InstanceId] | index("eci-old")) != null' \
  "$h1_policy_case/state/state.json" >/dev/null \
  || fail "a refused release dropped the pre-existing instance"

# (9) The entry's relay list is a SECOND ceiling, and the release must respect
# it: ecs-fleet-sync truncates the list with `head -n MAX_MEMBERS`, so an
# instance past the cap never reaches the relay tier and the gate times out 180s
# later. v1.0.0-rc.40-tokeness-mainland.42 died exactly there - the group allowed
# 9, the cap was 8, and the rollout rolled back after 1h44m with
# "the ECS relay tier did not start serving 10.0.0.22 within 180s".
#
# Explicit request above the cap: refused before anything mutates.
relay_cap_case="$test_root/relay-cap-explicit"
mkdir -p "$relay_cap_case/state"
make_conf "$relay_cap_case/nginx.conf"
init_ess_state "$relay_cap_case/state"
jq '.max_size = 6' "$relay_cap_case/state/state.json" > "$relay_cap_case/state/tmp.json" \
  && mv "$relay_cap_case/state/tmp.json" "$relay_cap_case/state/state.json"
if run_deploy "$relay_cap_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  RELAY_MAX_MEMBERS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  ROLLOUT_BATCH=3 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "a batch size above the relay list cap was accepted"
fi
grep -q "exceeds the growth ceiling" "$relay_cap_case/state/output.log" \
  || fail "an over-cap batch did not explain the relay ceiling"
if grep -q "ess ModifyScalingGroup" "$relay_cap_case/state/aliyun-calls.log"; then
  fail "an over-cap batch was rejected only after the tier had moved"
fi
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$PREV_DIGEST"'"' "$relay_cap_case/state/state.json" > /dev/null \
  || fail "the refused release still re-pinned the scaling configuration"

# Default (no ROLLOUT_BATCH): the batch is clamped to the relay cap instead of
# the MaxSize, so the rollout stays inside what the entry can carry.
relay_clamp_case="$test_root/relay-cap-clamp"
mkdir -p "$relay_clamp_case/state"
make_conf "$relay_clamp_case/nginx.conf"
init_ess_state "$relay_clamp_case/state"
jq '.desired = 2 | .max_size = 8 | .instances += [{
      InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206",
      HealthStatus: "Healthy", LifecycleState: "InService",
      CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"}]' \
  "$relay_clamp_case/state/state.json" > "$relay_clamp_case/state/tmp.json" \
  && mv "$relay_clamp_case/state/tmp.json" "$relay_clamp_case/state/state.json"
printf 'server 10.0.0.241:3000;\nserver 10.0.0.242:3000;\nserver 10.0.0.243:3000;\n' \
  > "$relay_clamp_case/ecs-upstream.conf"
run_deploy "$relay_clamp_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  HEALTH_POLL_SECONDS=1 \
  RELAY_MAX_MEMBERS=4 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
grep -q "relay upstream carries at most 4 member(s), below MaxSize=8" "$relay_clamp_case/state/stdout.log" \
  || fail "the release did not notice the relay ceiling"
grep -q "up to 2 per round" "$relay_clamp_case/state/stdout.log" \
  || fail "the batch was not clamped to the relay ceiling (cap 4 - stable 2 = 2)"
jq -e '.desired == 2' "$relay_clamp_case/state/state.json" > /dev/null \
  || fail "the clamped release did not restore the steady-state capacity"

# (10) A release must hold the tier's scale-in alarm off while it rolls. The
# alarm (cpu-in-25: CPU <= 25% for 30 minutes) trims capacity a release leaves
# behind - but during a rollout the tier is deliberately grown and stepped back
# down, so an alarm firing mid-round would retire instances the round is still
# gating on. Suspended before the first mutation, resumed on exit.
alarm_case="$test_root/scale-in-alarm-guard"
mkdir -p "$alarm_case/state"
make_conf "$alarm_case/nginx.conf"
init_ess_state "$alarm_case/state"
run_deploy "$alarm_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
grep -q "scale-in alarm suspended for the rollout" "$alarm_case/state/stdout.log" \
  || fail "the release did not suspend the scale-in alarm"
grep -q "scale-in alarm resumed" "$alarm_case/state/stdout.log" \
  || fail "the release did not resume the scale-in alarm"
jq -e '.scale_in_alarm.state == "enabled"' "$alarm_case/state/state.json" > /dev/null \
  || fail "the alarm was left suspended after the release"
# A drain marker left behind would keep a healthy member out of the relay list.
[[ ! -e "$alarm_case/shrink-drain" ]] \
  || fail "a relay drain marker was left behind after the release"
# The entry's shrink guard is held off for the same window, and released after.
[[ ! -e "$alarm_case/shrink-hold" ]] \
  || fail "the shrink guard hold was left behind after the release"
# Suspend must precede the FIRST scaling call, resume must FOLLOW THE LAST one,
# and nothing may re-suspend in between. The old check only tested "Disable
# appeared somewhere early and Enable appeared somewhere" - Disable->Enable->
# ModifyScalingGroup passed it too.
# A state machine, not first/last bookkeeping: the release disables ONCE,
# every ModifyScalingGroup happens while the alarm is disabled, and exactly
# one re-enable closes the window. The old check accepted mid-release
# Enable->Disable->Modify sequences.
awk '
  /ess DisableAlarm/ { d_count++; state = "d"; next }
  /ess EnableAlarm/  { e_count++; state = "e"; next }
  /ess ModifyScalingGroup/ { if (state != "d") bad = 1; mods++ }
  END { exit !(mods > 0 && d_count == 1 && e_count == 1 && !bad && state == "e") }' \
  "$alarm_case/state/aliyun-calls.log" \
  || fail "a scaling call happened while the alarm was enabled, the alarm was toggled mid-release, or it never came back"
# An alarm that was already disabled before the release must stay disabled: the
# release records the state it found and restores THAT, not a hardcoded on.
alarm_pre_disabled_case="$test_root/scale-in-alarm-pre-disabled"
mkdir -p "$alarm_pre_disabled_case/state"
make_conf "$alarm_pre_disabled_case/nginx.conf"
init_ess_state "$alarm_pre_disabled_case/state"
jq '.scale_in_alarm = {id: "alarm-test-1", state: "disabled"}' \
  "$alarm_pre_disabled_case/state/state.json" > "$alarm_pre_disabled_case/state/tmp.json" \
  && mv "$alarm_pre_disabled_case/state/tmp.json" "$alarm_pre_disabled_case/state/state.json"
run_deploy "$alarm_pre_disabled_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
grep -q "was already disabled before this run; leaving it disabled" \
  "$alarm_pre_disabled_case/state/stdout.log" \
  || fail "the release re-enabled an alarm that was off before it started"
if grep -q "ess EnableAlarm" "$alarm_pre_disabled_case/state/aliyun-calls.log"; then
  fail "the release re-enabled an alarm that was off before it started (call log)"
fi
jq -e '.scale_in_alarm.state == "disabled"' "$alarm_pre_disabled_case/state/state.json" > /dev/null \
  || fail "the alarm's disabled state did not survive the release"

# (10c) The alarm RESTORE failing must not quietly clear the hold: the hold is
# the only thing keeping the guard from shrinking while the alarm is off. The
# release exits non-zero, the hold stays, and the incident is spelled out.
alarm_restore_fail_case="$test_root/scale-in-alarm-restore-fail"
mkdir -p "$alarm_restore_fail_case/state"
make_conf "$alarm_restore_fail_case/nginx.conf"
init_ess_state "$alarm_restore_fail_case/state"
if run_deploy "$alarm_restore_fail_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_ALARM_ENABLE_FAIL=1 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "a release whose alarm restore failed exited successfully"
fi
grep -q "could not re-enable scale-in alarm" "$alarm_restore_fail_case/state/output.log" \
  || fail "the failed alarm restore did not fail loudly"
[[ -s "$alarm_restore_fail_case/shrink-hold" ]] \
  || fail "the hold was cleared although the alarm restore failed (the tier was left unprotected)"
[[ "$(grep -c 'ess EnableAlarm' "$alarm_restore_fail_case/state/aliyun-calls.log")" -eq 3 ]] \
  || fail "the alarm restore was not retried exactly 3 times"

# (10d) A retire set that never settles (wait_retired exhausts its retries
# while the removal is still Removing) must leave BOTH protections in place:
# the drain marker parked and the hold kept, with the alarm left off. Clearing
# either would let the guard or the alarm remove another instance while the
# first removal is mid-flight.
retire_pending_case="$test_root/retire-pending"
mkdir -p "$retire_pending_case/state"
make_conf "$retire_pending_case/nginx.conf"
init_ess_state "$retire_pending_case/state"
if run_deploy "$retire_pending_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  HEALTH_POLL_SECONDS=0 \
  TOKENESS_TEST_SCALE_IN_LAG_SECONDS=3600 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  rollout-all; then
  fail "a release whose retire set never settled exited successfully"
fi
grep -q "timed out waiting for the retired set" "$retire_pending_case/state/output.log" \
  || fail "the unsettled retire set did not time out"
grep -q "a retire set is still settling" "$retire_pending_case/state/stdout.log" \
  || fail "the pending retirement did not keep the alarm off with an explanation"
[[ -s "$retire_pending_case/shrink-drain" ]] \
  || fail "the drain marker was cleared although the retirement is still in flight"
[[ -s "$retire_pending_case/shrink-hold" ]] \
  || fail "the hold was cleared although the retirement is still in flight"
jq -e '.scale_in_alarm.state == "disabled"' "$retire_pending_case/state/state.json" >/dev/null \
  || fail "the pending retirement re-enabled the scale-in alarm while a removal was still in flight"
# A missing alarm must warn, not abort: the guard is advisory.
no_alarm_case="$test_root/scale-in-alarm-missing"
mkdir -p "$no_alarm_case/state"
make_conf "$no_alarm_case/nginx.conf"
init_ess_state "$no_alarm_case/state"
run_deploy "$no_alarm_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_SCALE_IN_ALARM_NAME=does-not-exist \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
grep -q "no scale-in alarm named 'cpu-in-25'" "$no_alarm_case/state/stdout.log" \
  || fail "a missing scale-in alarm did not warn"
grep -q "master container blue-green complete" "$no_alarm_case/state/stdout.log" \
  || fail "a missing scale-in alarm aborted the release"
[[ ! -e "$no_alarm_case/shrink-hold" ]] \
  || fail "the hold survived a release that suspended no alarm"
# A SECOND release on the same state: the resume path must be repeatable (the
# alarm state file stays consistent, the hold clears again, nothing doubles).
run_deploy "$no_alarm_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_SCALE_IN_ALARM_NAME=does-not-exist \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
[[ ! -e "$no_alarm_case/shrink-hold" ]] \
  || fail "the hold survived a second release on the same state"
[[ "$(grep -c 'ess DisableAlarm' "$no_alarm_case/state/aliyun-calls.log")" -eq 0 ]] \
  || fail "a missing alarm was still 'suspended'"

# (11) The tier's own alarms can add instances during a release (cpu-out-70 now
# adds two at a time on a 2-minute window). The old scale-in gate waited for an
# EXACT instance count, so a downstream ramp mid-release inflated the group
# legitimately and the release timed out and rolled back hours of work. The gate
# now waits for the retiring instances to leave service, which an external
# scale-out does not disturb.
external_case="$test_root/scale-in-external-scaleout"
mkdir -p "$external_case/state"
make_conf "$external_case/nginx.conf"
init_ess_state "$external_case/state"
jq '.desired = 2 | .max_size = 6 | .instances += [{
      InstanceId: "eci-old2", PrivateIpAddress: "10.0.0.206",
      HealthStatus: "Healthy", LifecycleState: "InService",
      CreatedTime: "2026-09-02T00:00:00Z", ZoneId: "cn-shanghai-l"}]' \
  "$external_case/state/state.json" > "$external_case/state/tmp.json" \
  && mv "$external_case/state/tmp.json" "$external_case/state/state.json"
printf 'server 10.0.0.241:3000;\nserver 10.0.0.242:3000;\nserver 10.0.0.201:3000;\nserver 10.0.0.202:3000;\n' \
  > "$external_case/ecs-upstream.conf"
# The growth is injected DURING the 1900s hold (the first poll after a live
# drain marker appears) - that is when a cpu-out firing would actually land -
# and the removal window keeps a Removing lag so the full-departure gate is
# exercised too. The old variant injected only after the shrink had already
# removed members, which tested neither the hold window nor the read-to-Modify
# gap.
run_deploy "$external_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  HEALTH_POLL_SECONDS=1 \
  TOKENESS_TEST_SCALE_IN_LAG_SECONDS=3 \
  TOKENESS_TEST_EXTERNAL_SCALEOUT_HOLD=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
grep -q "hold-window external scale-out injected" "$external_case/state/external-scaleout.log" 2>/dev/null \
  || fail "the hold-window injection never fired (the case no longer tests the window)"
grep -q "scale-in complete:" "$external_case/state/stdout.log" \
  || fail "the release did not gate on the retire set"
grep -qE "retired all [12] pre-existing instance\(s\)" "$external_case/state/stdout.log" \
  || fail "the release did not converge across an external scale-out"
if grep -q "ERROR: release batch rollout failed" "$external_case/state/stdout.log"; then
  fail "an external scale-out made the release roll back"
fi
# The extra capacity the alarm added is left alone BY IDENTITY: "at least 2
# instances" would also pass if the release had deleted both external members
# and kept two of its own. The external instances must still be in service.
jq -e '([.instances[].InstanceId] | index("eci-ext-1")) != null
       and ([.instances[].InstanceId] | index("eci-ext-2")) != null' \
  "$external_case/state/state.json" > /dev/null \
  || fail "the external scale-out's instances were removed by the release"
# The retired set left the group ENTIRELY (not merely InService): the round
# must not open the next one while a removal is still terminating.
grep -q "left the group" "$external_case/state/stdout.log" \
  || fail "the retire gate accepted a removal that was still in flight"
# At the shrink call itself: the parked originals are in the marker, the relay
# upstream already excludes them, and the externally added members are IN the
# upstream (the capacity that arrived during the hold is carried, not undone).
# The LAST resize call is the batch round's shrink (the ess_rollout leg has
# its own scale-out/shrink pair first).
ext_snap="$(ls "$external_case/state/modify-snapshots"/modify-*.log 2>/dev/null | sort -V | tail -1)"
[[ -n "$ext_snap" && -f "$ext_snap" ]] || fail "the shrink call was not snapshotted in the external case"
# .207 was retired by the ess_rollout leg (its shrink is the second resize
# call); .206 by the batch round (the last). Each must be parked at ITS OWN
# shrink, not just gone at the end.
ess_snap="$external_case/state/modify-snapshots/modify-2.log"
[[ -f "$ess_snap" ]] || fail "the ess_rollout shrink was not snapshotted in the external case"
for spec in "10.0.0.207 $ess_snap" "10.0.0.206 $ext_snap"; do
  set -- $spec
  grep -qxF "$1" "$2" || fail "at its shrink, the marker did not park $1"
  if sed -n '/--- relay upstream ---/,$p' "$2" | grep -q "$1"; then
    fail "at its shrink, the upstream still carried parked $1"
  fi
done
for ext_ip in 10.0.0.201 10.0.0.202; do
  sed -n '/--- relay upstream ---/,$p' "$ext_snap" | grep -q "$ext_ip" \
    || fail "at the shrink, the external capacity $ext_ip was not carried by the upstream"
done

# CRLF regression: a Windows-side aliyun CLI (CRLF line endings) and a Windows
# jq (CRLF on stdout) must never leak \r into re-sent container/env data.
crlf_case="$test_root/crlf"
mkdir -p "$crlf_case"
make_conf "$crlf_case/nginx.conf"
mkdir -p "$crlf_case/state"
init_ess_state "$crlf_case/state"
cat > "$bin_dir/jq" <<'WRAPPER'
#!/usr/bin/env bash
# Emulate a Windows jq build: CRLF on stdout.
exec "$(dirname "$0")/jq-real" "$@" | sed 's/$/\r/'
WRAPPER
mv "$bin_dir/jq" "$bin_dir/jq-wrap"
cp "$(command -v jq)" "$bin_dir/jq-real"
mv "$bin_dir/jq-wrap" "$bin_dir/jq"
chmod 0700 "$bin_dir/jq" "$bin_dir/jq-real"
run_deploy "$crlf_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9
if grep -q $'\r' "$crlf_case/state/modify-args.txt"; then
  fail "CR leaked into re-sent scaling configuration data"
fi
assert_contains "$crlf_case/state/modify-args.txt" "--Container.1.Name newapi"
rm -f "$bin_dir/jq" "$bin_dir/jq-real"

# EIP shared-bandwidth convergence paths.
eip_case="$test_root/eip-sync"
mkdir -p "$eip_case/state"
init_ess_state "$eip_case/state"
# Already bound: converge_eip_bandwidth must not issue an add call.
jq '.eip_package_id = "cbwp-uf6cup45a4jmnbnbgth04"' "$eip_case/state/state.json" > "$eip_case/state/state.json.tmp" \
  && mv "$eip_case/state/state.json.tmp" "$eip_case/state/state.json"
run_deploy "$eip_case" eip-sync
if grep -q "vpc AddCommonBandwidthPackageIp" "$eip_case/state/aliyun-calls.log"; then
  fail "already-bound EIP triggered a redundant package add call"
fi
# Unbound: eip-sync must add the current instance's EIP to the package.
jq '.eip_package_id = ""' "$eip_case/state/state.json" > "$eip_case/state/state.json.tmp" \
  && mv "$eip_case/state/state.json.tmp" "$eip_case/state/state.json"
run_deploy "$eip_case" eip-sync
grep -q "vpc AddCommonBandwidthPackageIp .*--BandwidthPackageId cbwp-uf6cup45a4jmnbnbgth04 .*--IpInstanceId eip-eci-old" \
  "$eip_case/state/aliyun-calls.log" \
  || fail "eip-sync did not bind the instance EIP to the shared bandwidth package"
# Unresolvable EIP object: convergence must fail loudly.
if run_deploy "$eip_case" TOKENESS_TEST_EIP_ABSENT=1 eip-sync; then
  fail "eip-sync unexpectedly succeeded without a matching EIP object"
fi
# EIP attach still in flight: one retry, then an advisory skip (no add call).
no_public_ip_case="$test_root/no-public-ip"
mkdir -p "$no_public_ip_case/state"
init_ess_state "$no_public_ip_case/state"
run_deploy "$no_public_ip_case" \
  EIP_ATTACH_GRACE_SECONDS=1 \
  TOKENESS_TEST_NO_PUBLIC_IP=1 \
  eip-sync
[[ "$(grep -c "eci DescribeContainerGroups" "$no_public_ip_case/state/aliyun-calls.log")" -eq 2 ]] \
  || fail "no-public-IP path did not retry the container group lookup"
if grep -q "vpc AddCommonBandwidthPackageIp" "$no_public_ip_case/state/aliyun-calls.log"; then
  fail "no-public-IP path issued a package add call"
fi
# VPC API failure: advisory convergence must WARN inside a release, not abort
# it (egress keeps serving on the standalone EIP peak until eip-sync reruns).
vpc_fail_case="$test_root/vpc-fail"
mkdir -p "$vpc_fail_case"
make_conf "$vpc_fail_case/nginx.conf"
mkdir -p "$vpc_fail_case/state"
init_ess_state "$vpc_fail_case/state"
if ! run_deploy "$vpc_fail_case" \
  APP_READY_TIMEOUT_SECONDS=10 APP_READY_POLL_SECONDS=2 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  TOKENESS_TEST_VPC_FAIL=1 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9; then
  fail "EIP convergence failure must not abort an otherwise converged release"
fi
grep -q "could not add EIP .* to shared bandwidth package" "$vpc_fail_case/state/output.log" 2>/dev/null \
  || fail "release did not report the failed EIP convergence"
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$TEST_ML_DIGEST"'"' "$vpc_fail_case/state/state.json" > /dev/null \
  || fail "EIP convergence failure disturbed the rollout result"

# ---- drain-first rollout ------------------------------------------------------
# The retiring instance must stop receiving new requests BEFORE the group scales
# down, and the pin must outlive the scale-down: clearing it first would let
# ml-sync re-add the old member on its next 30s pass and send traffic back to an
# instance that is about to be deleted.

drain_case="$test_root/drain"
mkdir -p "$drain_case"
make_conf "$drain_case/nginx.conf"
mkdir -p "$drain_case/state"
init_ess_state "$drain_case/state"
if ! run_deploy "$drain_case" \
  SWAS_PANEL_TIER=1 \
  TOKENESS_TEST_MLSYNC=1 \
  ML_DRAIN_SECONDS=2 \
  ROLLOUT_HEARTBEAT_SECONDS=1 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9 "$TEST_ML_DIGEST"; then
  fail "drain-first rollout did not converge"
fi

drain_log="$drain_case/state/aliyun-calls.log"
write_line="$(grep -n '^ssh 8.133.172.195 marker-write$' "$drain_log" | head -n1 | cut -d: -f1)"
write2_line="$(grep -n '^ssh 101.133.234.135 marker-write$' "$drain_log" | head -n1 | cut -d: -f1)"
scaledown_line="$(grep -n 'ess ModifyScalingGroup .*--DesiredCapacity 1' "$drain_log" | head -n1 | cut -d: -f1)"
remove_line="$(grep -n '^ssh 8.133.172.195 marker-remove$' "$drain_log" | head -n1 | cut -d: -f1)"
[[ -n "$write_line" && -n "$write2_line" ]] \
  || fail "the drain marker was not written to both lightweight hosts"
[[ -n "$scaledown_line" ]] || fail "drain-first rollout never scaled the group back to 1"
[[ -n "$remove_line" ]] || fail "the drain marker was not cleared after the rollout"
(( write_line < scaledown_line && write2_line < scaledown_line )) \
  || fail "the relay tier was pinned only after the scale-down (write=$write_line/$write2_line scaledown=$scaledown_line)"
(( remove_line > scaledown_line )) \
  || fail "the drain marker was cleared before the scale-down, so ml-sync could put the retiring instance back in rotation"
grep -q 'drain-first: holding' "$drain_case/state/stdout.log" \
  || fail "the rollout did not hold the relay tier while in-flight streams finished"
grep -q 'rollout drain still active' "$drain_case/state/stdout.log" \
  || fail "the long drain did not emit a CNB watchdog heartbeat"
jq -e '.instances | length == 1' "$drain_case/state/state.json" > /dev/null \
  || fail "drain-first rollout did not settle on a single instance"
jq -e '.instances[0].InstanceId == "eci-new-1"' "$drain_case/state/state.json" > /dev/null \
  || fail "drain-first rollout removed the NEW instance instead of the retiring one"
[[ ! -e "$drain_case/state/drain-target" ]] \
  || fail "the drain marker survived a successful rollout"

# Fail closed: a host that refuses the pin must abort the rollout rather than
# scale down, because half the EdgeOne origins would still be sending new
# requests to the instance about to be deleted.
drain_fail_case="$test_root/drain-fail"
mkdir -p "$drain_fail_case"
make_conf "$drain_fail_case/nginx.conf"
mkdir -p "$drain_fail_case/state"
init_ess_state "$drain_fail_case/state"
if run_deploy "$drain_fail_case" \
  SWAS_PANEL_TIER=1 \
  TOKENESS_TEST_MLSYNC=1 \
  ML_DRAIN_SECONDS=1 \
  TOKENESS_TEST_SSH_FAIL_HOST=101.133.234.135 \
  TOKENESS_TEST_SSH_FAIL_ON='dirname "$marker"' \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9 "$TEST_ML_DIGEST"; then
  fail "a refused drain pin must fail the rollout"
fi
grep -q 'could not write the drain marker on 101.133.234.135' "$drain_fail_case/state/output.log" \
  || fail "the refused drain pin was not reported"
# warn() writes to stdout, error()/die() to stderr, so each assertion reads the
# stream the message actually lands on.
grep -q 'rolling back to previous digest' "$drain_fail_case/state/stdout.log" \
  || fail "a refused drain pin did not trigger the rollback path"
jq -e --arg prev "docker.cnb.cool/imvhb/new-api-cn@$PREV_DIGEST" '.image == $prev' \
  "$drain_fail_case/state/state.json" > /dev/null \
  || fail "a refused drain pin did not restore the previous image"
[[ ! -e "$drain_fail_case/state/drain-target" ]] \
  || fail "rollback left the drain marker in place, pinning the relay tier to a deleted instance"

# The shutdown budget must be refused when the platform grace period cannot
# contain the application drain plus its background flush.
budget_case="$test_root/drain-budget"
mkdir -p "$budget_case"
make_conf "$budget_case/nginx.conf"
mkdir -p "$budget_case/state"
init_ess_state "$budget_case/state"
if run_deploy "$budget_case" ECI_TERMINATION_GRACE_SECONDS=150 \
  APP_SHUTDOWN_TIMEOUT_SECONDS=180 ML_DRAIN_SECONDS=1 \
  TOKENESS_TEST_HOST_VERSION=v1.0.0-rc.33-tokeness-mainland.9 \
  deploy-release v1.0.0-rc.33-tokeness-mainland.9 "$TEST_ML_DIGEST"; then
  fail "a grace period shorter than the drain plus background flush must be refused"
fi
grep -q 'must be at least' "$budget_case/state/output.log" \
  || fail "the refused shutdown budget was not explained"
jq -e '.desired == 1' "$budget_case/state/state.json" > /dev/null \
  || fail "a refused shutdown budget still mutated the scaling group"

# The default host bootstrap must resolve inside the repository: the CNB
# release pipeline has no private/ checkout, so a private/ default makes
# master-first sync fail before the ESS rollout starts.
default_bootstrap="$(cd "$TEST_DIR/.." && pwd)/bootstrap-newapi-host.sh"
[[ -r "$default_bootstrap" ]] \
  || fail "default host bootstrap script is missing at $default_bootstrap"
bash -n "$default_bootstrap" \
  || fail "default host bootstrap script has a syntax error"
if grep -q 'private/scripts/bootstrap-newapi-host.sh' "$DEPLOY_SCRIPT"; then
  fail "deploy.sh still defaults to the private/ bootstrap path"
fi

# The guard shares the drain marker file, the hold and the tier with the
# release, and it runs from cron where nobody watches it fail - the coverage
# gap the 2026-10-10 review called out. Run it as part of this suite so a
# guard regression fails the same gate as a release regression.
bash "$TEST_DIR/guard-test.sh" || fail "ecs-shrink-guard tests failed"

printf 'Tokeness China deployment tests passed\n'
