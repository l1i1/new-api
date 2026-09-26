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
CONF
}

test_root="$(mktemp -d)"
trap 'rm -rf -- "$test_root"' EXIT

# Stand-in for private/scripts/bootstrap-newapi-host.sh: the real script pulls
# env/image/creds from the scaling config and runs docker (unavailable here).
# The fake counts syncs (host-sync-count, logged in host-bootstrap.log) and
# drops a marker line into aliyun-calls.log so its ordering vs ESS API calls
# is assertable. TOKENESS_TEST_HOST_FAIL_TIMES makes the first N syncs fail
# (master-first abort/recovery paths).
host_bootstrap="$test_root/fake-bootstrap.sh"
cat > "$host_bootstrap" <<'FAKE'
#!/usr/bin/env bash
set -Eeuo pipefail
state_dir="${TOKENESS_TEST_STATE_DIR:?}"
mkdir -p "$state_dir"
count_file="$state_dir/host-sync-count"
count="$(cat "$count_file" 2>/dev/null || echo 0)"
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
printf 'host-bootstrap\n' >> "$state_dir/aliyun-calls.log"
printf 'host-bootstrap invocation %s\n' "$count" >> "$state_dir/host-bootstrap.log"
if [[ "$count" -le "${TOKENESS_TEST_HOST_FAIL_TIMES:-0}" ]]; then
  echo "simulated host bootstrap failure" >&2
  exit 1
fi
FAKE
chmod +x "$host_bootstrap"

bin_dir="$test_root/bin"
mkdir -p "$bin_dir"
cp "$TEST_DIR"/fake-bin/* "$bin_dir/"
chmod 0700 "$bin_dir"/*
touch "$test_root/key"
chmod 0600 "$test_root/key"

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
  # The relay tier is pinned through a marker file that the fake ssh creates
  # locally, so every case needs its own path; and the drain hold is set to 0
  # because a test cannot usefully wait the production 600s. Cases that assert
  # the real timing override these (they come later in env, so they win).
  env \
    PATH="$bin_dir:$PATH" \
    NGINX_CONF="$case_dir/nginx.conf" \
    SWAS_SSH_KEY_PATH="$test_root/key" \
    HOST_BOOTSTRAP_SCRIPT="$host_bootstrap" \
    TOKENESS_TEST_STATE_DIR="$case_dir/state" \
    DRAIN_MARKER_PATH="$case_dir/state/drain-target" \
    TOKENESS_TEST_MLSYNC=1 \
    ML_DRAIN_SECONDS=0 \
    ML_DRAIN_CONVERGE_ATTEMPTS=2 \
    ML_DRAIN_CONVERGE_DELAY_SECONDS=0 \
    CNB_REGISTRY_TOKEN=dummy-test-token \
    "${env_args[@]}" \
    bash "$DEPLOY_SCRIPT" "$@" \
    > "$case_dir/state/stdout.log" 2> "$case_dir/state/output.log" || rc=$?
  # Replay stdout to the caller. It is part of this helper contract: image-ref
  # prints the immutable reference and a test captures it. Keeping it in a file
  # as well lets a case assert on log() lines without racing a tee.
  cat "$case_dir/state/stdout.log"
  return "$rc"
}

invalid_ip_case="$test_root/invalid-ip"
mkdir -p "$invalid_ip_case"
make_conf "$invalid_ip_case/nginx.conf"
if run_deploy "$invalid_ip_case" nginx-update 10.0.0.999; then
  fail "invalid IPv4 address unexpectedly succeeded"
fi

public_failure_case="$test_root/public-failure"
mkdir -p "$public_failure_case"
make_conf "$public_failure_case/nginx.conf"
if run_deploy "$public_failure_case" TOKENESS_TEST_PUBLIC_FAIL=1 verify; then
  fail "public failure unexpectedly passed verification"
fi

direct_failure_case="$test_root/direct-failure"
mkdir -p "$direct_failure_case"
make_conf "$direct_failure_case/nginx.conf"
if run_deploy "$direct_failure_case" TOKENESS_TEST_DIRECT_FAIL=1 verify; then
  fail "direct failure unexpectedly passed verification"
fi

# Non-success body / invalid JSON must be treated as unhealthy.
bad_body_case="$test_root/bad-body"
mkdir -p "$bad_body_case"
make_conf "$bad_body_case/nginx.conf"
if run_deploy "$bad_body_case" TOKENESS_TEST_PUBLIC_BODY='{"success":false}' verify; then
  fail "non-success public body unexpectedly passed verification"
fi
if run_deploy "$bad_body_case" TOKENESS_TEST_PUBLIC_BODY='not-json' verify; then
  fail "invalid public JSON unexpectedly passed verification"
fi
if run_deploy "$bad_body_case" TOKENESS_TEST_DIRECT_BODY='{"success":false}' verify; then
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
if run_deploy "$duplicate_case" verify; then
  fail "duplicate upstream unexpectedly passed verification"
fi

# A parameterized server line (weight=, max_fails=) must be rewritten too.
param_case="$test_root/parameterized"
mkdir -p "$param_case"
make_conf "$param_case/nginx.conf"
sed -i 's|server 10.0.0.207:3000;|server 10.0.0.207:3000 max_fails=3 fail_timeout=30s;|' "$param_case/nginx.conf"
run_deploy "$param_case" nginx-update 10.0.0.209
assert_contains "$param_case/nginx.conf" 'server 10.0.0.209:3000 max_fails=3 fail_timeout=30s;'
assert_not_contains "$param_case/nginx.conf" 'server 10.0.0.207:3000;'

success_case="$test_root/success"
mkdir -p "$success_case"
make_conf "$success_case/nginx.conf"
run_deploy "$success_case" nginx-update 10.0.0.208
assert_contains "$success_case/nginx.conf" 'server 10.0.0.208:3000;'
assert_contains "$success_case/nginx.conf" '# server 10.0.0.99:3000;'
assert_contains "$success_case/nginx.conf" 'server 10.9.9.9:3000;'
assert_not_contains "$success_case/nginx.conf" 'server 10.0.0.207:3000;'
run_deploy "$success_case" nginx-update 10.0.0.208

nginx_failure_case="$test_root/nginx-failure"
mkdir -p "$nginx_failure_case"
make_conf "$nginx_failure_case/nginx.conf"
if run_deploy "$nginx_failure_case" TOKENESS_TEST_NGINX_FAIL=1 nginx-update 10.0.0.208; then
  fail "nginx validation failure unexpectedly succeeded"
fi
assert_contains "$nginx_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$nginx_failure_case/nginx.conf" 'server 10.0.0.208:3000;'

reload_failure_case="$test_root/reload-failure"
mkdir -p "$reload_failure_case"
make_conf "$reload_failure_case/nginx.conf"
if run_deploy "$reload_failure_case" TOKENESS_TEST_RELOAD_FAIL_ONCE=1 nginx-update 10.0.0.208; then
  fail "reload failure unexpectedly succeeded"
fi
assert_contains "$reload_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$reload_failure_case/nginx.conf" 'server 10.0.0.208:3000;'

rollback_reload_failure_case="$test_root/rollback-reload-failure"
mkdir -p "$rollback_reload_failure_case"
make_conf "$rollback_reload_failure_case/nginx.conf"
if run_deploy "$rollback_reload_failure_case" TOKENESS_TEST_RELOAD_FAIL_ALWAYS=1 nginx-update 10.0.0.208; then
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
if run_deploy "$post_verify_case" TOKENESS_TEST_DIRECT_FAIL=1 nginx-update 10.0.0.208; then
  fail "post-update verification failure unexpectedly succeeded"
fi
assert_contains "$post_verify_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$post_verify_case/nginx.conf" 'server 10.0.0.208:3000;'

# Public (EdgeOne) failure after an nginx update must also roll back.
post_public_failure_case="$test_root/post-public-failure"
mkdir -p "$post_public_failure_case"
make_conf "$post_public_failure_case/nginx.conf"
if run_deploy "$post_public_failure_case" TOKENESS_TEST_PUBLIC_FAIL=1 nginx-update 10.0.0.208; then
  fail "post-update public failure unexpectedly succeeded"
fi
assert_contains "$post_public_failure_case/nginx.conf" 'server 10.0.0.207:3000;'
assert_not_contains "$post_public_failure_case/nginx.conf" 'server 10.0.0.208:3000;'

# Rollback must run a post-rollback probe; when even that fails, the script
# must exit nonzero and leave the old config in place.
post_rollback_probe_failure_case="$test_root/post-rollback-probe-failure"
mkdir -p "$post_rollback_probe_failure_case"
make_conf "$post_rollback_probe_failure_case/nginx.conf"
if run_deploy "$post_rollback_probe_failure_case" TOKENESS_TEST_DIRECT_FAIL=1 nginx-update 10.0.0.208; then
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
  if run_deploy "$test_root/ip-$bad_ip" nginx-update "$bad_ip"; then
    fail "invalid IPv4 '$bad_ip' unexpectedly accepted"
  fi
done

# ---- deploy-release / ess_rollout coverage via the fake aliyun CLI ----

TEST_ML_DIGEST="sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
PREV_DIGEST="sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
export TOKENESS_TEST_ML_DIGEST="$TEST_ML_DIGEST"

init_ess_state() {
  local dir="$1" log_content="${2:-ready}"
  jq -n \
    --arg image "docker.cnb.cool/imvhb/new-api-cn@$PREV_DIGEST" \
    --arg logContent "$log_content" \
    '{
      desired: 1,
      instances: [{InstanceId: "eci-old", PrivateIpAddress: "10.0.0.207",
                   HealthStatus: "Healthy", LifecycleState: "InService"}],
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
# The deploy pipeline must converge the SWAS-2 host container to the same
# release: bootstrap runs on the host and the reported version must match.
assert_contains "$release_case/state/host-bootstrap.log" "host-bootstrap invocation 1"
[[ "$(wc -l < "$release_case/state/host-bootstrap.log")" -eq 1 ]] \
  || fail "host bootstrap ran more than once"
# Master-first: the host sync must complete before the ESS group scales out.
first_bootstrap="$(grep -n '^host-bootstrap$' "$release_case/state/aliyun-calls.log" | head -n1 | cut -d: -f1)"
first_scaleout="$(grep -n 'ess ModifyScalingGroup .*--DesiredCapacity 2' "$release_case/state/aliyun-calls.log" | head -n1 | cut -d: -f1)"
[[ -n "$first_bootstrap" && -n "$first_scaleout" && "$first_bootstrap" -lt "$first_scaleout" ]] \
  || fail "master (SWAS-2 host) sync did not run before the ESS scale-out"

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
grep -q "SWAS-2 host version" "$host_mismatch_case/state/output.log" 2>/dev/null \
  || fail "mismatch case did not report the host version problem"
if grep -q "ess ModifyScalingGroup" "$host_mismatch_case/state/aliyun-calls.log"; then
  fail "ESS rollout started even though the master never matched the release"
fi
[[ "$(wc -l < "$host_mismatch_case/state/host-bootstrap.log")" -eq 2 ]] \
  || fail "host was not restored after the version mismatch"
jq -e '.image == "docker.cnb.cool/imvhb/new-api-cn@'"$PREV_DIGEST"'"' "$host_mismatch_case/state/state.json" > /dev/null \
  || fail "scaling configuration was not restored after the host version mismatch"

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
[[ "$(wc -l < "$host_fail_case/state/host-bootstrap.log")" -eq 2 ]] \
  || fail "host was not re-bootstrapped from the restored configuration"

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
# Master-first: the host took the release before the rollout; after the failed
# rollout the master must be re-synced to the restored previous digest.
[[ "$(wc -l < "$app_failure_case/state/host-bootstrap.log")" -eq 2 ]] \
  || fail "master was not re-synced after the failed rollout"

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
[[ "$(wc -l < "$fatal_case/state/host-bootstrap.log")" -eq 2 ]] \
  || fail "master was not re-synced after the FATAL-aborted rollout"

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
  TOKENESS_TEST_MLSYNC=1 \
  ML_DRAIN_SECONDS=1 \
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

printf 'Tokeness China deployment tests passed\n'
