#!/usr/bin/env bash
set -Eeuo pipefail

readonly TEST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly DEPLOY_SCRIPT="$TEST_DIR/../deploy.sh"
readonly VALID_DIGEST="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

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
  env \
    PATH="$bin_dir:$PATH" \
    NGINX_CONF="$case_dir/nginx.conf" \
    SWAS_SSH_KEY_PATH="$test_root/key" \
    TOKENESS_TEST_STATE_DIR="$case_dir/state" \
    "${env_args[@]}" \
    bash "$DEPLOY_SCRIPT" "$@"
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

printf 'Tokeness China deployment tests passed\n'
