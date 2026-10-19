#!/usr/bin/env bash
# Tests for the master nginx transition-backup rewrite
# (lib/master-nginx-upstream.sh + its call sites in bootstrap-master-ecs.sh and
# deploy.sh). No docker, nginx or network needed: the rewrite is a pure file
# transformation.
#
# Run: bash deployment/tokeness-cn/tests/master-nginx-upstream-test.sh
set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
LIB="$DEPLOY_DIR/lib/master-nginx-upstream.sh"
BOOTSTRAP="$DEPLOY_DIR/bootstrap-master-ecs.sh"
DEPLOY="$DEPLOY_DIR/deploy.sh"

. "$LIB"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
fail=0

ok() { printf 'ok   %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1" >&2; fail=1; }
expect() { # <label> <file> <pattern>
  if grep -Fq -- "$3" "$2"; then ok "$1"; else bad "$1 (missing: $3)"; fi
}
reject() { # <label> <file> <pattern>
  if grep -Fq -- "$3" "$2"; then bad "$1 (unexpected: $3)"; else ok "$1"; fi
}
count() { grep -Fc -- "$2" "$1" || true; }
expect_count() { # <label> <file> <pattern> <n>
  local got; got="$(grep -Fc -- "$3" "$2" || true)"
  if [[ "$got" == "$4" ]]; then ok "$1"; else bad "$1 (count $got != $4: $3)"; fi
}

# A fixture shaped like the real /etc/nginx/sites-available/tokeness-ml.conf:
# two managed upstreams (one primary master reference, one permanent `backup`
# last-resort reference), an unrelated upstream that must not be touched, and a
# vhost block whose `server {` line must not be mistaken for an upstream server.
write_fixture() { # <file>
  cat > "$1" <<'CONF'
# tokeness-ml.conf fixture
upstream newapi_ml {
    server 10.0.0.249:3000 backup max_fails=2 fail_timeout=5s;
    include /etc/nginx/fleet/newapi_ml_servers.conf;
}
upstream newapi_web {
    server 10.0.0.249:3000 max_fails=2 fail_timeout=10s;
}
upstream unrelated {
    # server 10.0.0.99:3000;  different host, must stay untouched
    server 10.9.9.9:8080;
}
server {
    listen 80;
    location / { proxy_pass http://newapi_web; }
}
CONF
}

# ---- 1. flip: green primary + blue transition backup ----------------------
f="$tmpdir/flip.conf"; write_fixture "$f"
master_nginx_rewrite_file "$f" 10.0.0.249 3001 3000 3000
expect "flip: newapi_web primary moves to 3001" "$f" "server 10.0.0.249:3001 max_fails=2 fail_timeout=10s;"
expect "flip: parameters are preserved" "$f" "max_fails=2 fail_timeout=10s;"
expect "flip: transition backup points at blue 3000" "$f" "server 10.0.0.249:3000 backup max_fails=2 fail_timeout=5s; # blue-green-transition-backup"
expect "flip: permanent last-resort backup follows to 3001" "$f" "server 10.0.0.249:3001 backup max_fails=2 fail_timeout=5s;"
expect_count "flip: only one reference to old port remains (the backup line)" "$f" "10.0.0.249:3000" 1
reject "flip: unrelated upstream untouched" "$f" "10.0.0.249:8080"
expect "flip: unrelated upstream preserved" "$f" "server 10.9.9.9:8080;"
expect "flip: commented server line preserved" "$f" "# server 10.0.0.99:3000;"
expect "flip: vhost block preserved" "$f" "location / { proxy_pass http://newapi_web; }"

# ---- 2. flip is idempotent (no duplicate backup lines) ---------------------
f2="$tmpdir/flip-twice.conf"; write_fixture "$f2"
master_nginx_rewrite_file "$f2" 10.0.0.249 3001 3000 3000
cp "$f2" "$tmpdir/flip-once.conf"
master_nginx_rewrite_file "$f2" 10.0.0.249 3001 3000 3000
if cmp -s "$f2" "$tmpdir/flip-once.conf"; then ok "flip twice: identical output"; else bad "flip twice: output drifted"; fi
expect_count "flip twice: single transition backup" "$f2" "blue-green-transition-backup" 1

# ---- 3. settle: drop the backup once blue is retired ----------------------
f="$tmpdir/settle.conf"; write_fixture "$f"
master_nginx_rewrite_file "$f" 10.0.0.249 3001 3000 3000
master_nginx_rewrite_file "$f" 10.0.0.249 3001 3000
reject "settle: transition backup removed" "$f" "blue-green-transition-backup"
reject "settle: no old-port reference remains" "$f" "10.0.0.249:3000"
expect "settle: primary stays on 3001" "$f" "server 10.0.0.249:3001 max_fails=2 fail_timeout=10s;"
expect "settle: last-resort backup stays on 3001" "$f" "server 10.0.0.249:3001 backup"

# ---- 4. settle is idempotent ----------------------------------------------
cp "$f" "$tmpdir/settle-once.conf"
master_nginx_rewrite_file "$f" 10.0.0.249 3001 3000
if cmp -s "$f" "$tmpdir/settle-once.conf"; then ok "settle twice: identical output"; else bad "settle twice: output drifted"; fi

# ---- 5. abort: restore the blue primary from a flipped config -------------
f="$tmpdir/abort.conf"; write_fixture "$f"
master_nginx_rewrite_file "$f" 10.0.0.249 3001 3000 3000
master_nginx_rewrite_file "$f" 10.0.0.249 3000 3001
reject "abort: transition backup removed" "$f" "blue-green-transition-backup"
expect "abort: primary restored to 3000" "$f" "server 10.0.0.249:3000 max_fails=2 fail_timeout=10s;"
expect "abort: last-resort backup restored to 3000" "$f" "server 10.0.0.249:3000 backup"

# ---- 6. a third master port is refused, file untouched --------------------
f="$tmpdir/unknown.conf"; write_fixture "$f"
printf 'upstream stranger {\n    server 10.0.0.249:3002;\n}\n' >> "$f"
cp "$f" "$tmpdir/unknown-orig.conf"
if master_nginx_rewrite_file "$f" 10.0.0.249 3001 3000 3000 2>"$tmpdir/unknown.err"; then
  bad "unknown port: rewrite must fail"
else
  ok "unknown port: rewrite refused"
fi
if cmp -s "$f" "$tmpdir/unknown-orig.conf"; then ok "unknown port: original file untouched"; else bad "unknown port: file was modified"; fi
expect "unknown port: diagnostic names the port" "$tmpdir/unknown.err" "10.0.0.249:3002"

# ---- 7. missing file is an error ----------------------------------------
if master_nginx_rewrite_file "$tmpdir/does-not-exist.conf" 10.0.0.249 3001 3000; then
  bad "missing file: must fail"
else
  ok "missing file: refused"
fi

# ---- 8. call sites: the commit flips with a backup, then settles ----------
if grep -Fq 'ecs_nginx_flip_with_backup "$gp" "$sp" || exit 3' "$BOOTSTRAP"; then
  ok "bootstrap: commit flips with the transition backup"
else
  bad "bootstrap: commit does not use the transition backup"
fi
if grep -Fq 'ecs_nginx_point_at "$gp" "$sp"' "$BOOTSTRAP"; then
  ok "bootstrap: commit settles (drops) the backup before retiring blue"
else
  bad "bootstrap: commit never drops the transition backup"
fi
if grep -Fq 'ecs_nginx_point_at "$sp" "$gp"' "$BOOTSTRAP"; then
  ok "bootstrap: abort finalizes by settling the backup"
else
  bad "bootstrap: abort-finalize path does not settle the backup"
fi
if grep -Fq 'lib/master-nginx-upstream.sh' "$BOOTSTRAP"; then
  ok "bootstrap: documents/prepares the helper library"
else
  bad "bootstrap: helper library not prepared"
fi

# ---- 9. deploy.sh streams the library with the script ---------------------
if grep -Fq 'master_bootstrap_stdin' "$DEPLOY"; then ok "deploy: combined stdin helper present"; else bad "deploy: combined stdin helper missing"; fi
if grep -Fq '< "$HOST_BOOTSTRAP_SCRIPT"' "$DEPLOY"; then
  bad "deploy: a call site still streams only the bootstrap script"
else
  ok "deploy: every master phase streams the combined script"
fi
if grep -Fq 'MASTER_NGINX_LIB' "$DEPLOY"; then ok "deploy: library path is declared"; else bad "deploy: library path missing"; fi

if [[ "$fail" -ne 0 ]]; then
  printf '\nmaster-nginx-upstream-test: FAILED\n' >&2
  exit 1
fi
printf '\nmaster-nginx-upstream-test: PASS\n'
