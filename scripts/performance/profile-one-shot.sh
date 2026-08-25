#!/usr/bin/env bash
# One-shot profiling run: must execute inside a single wsl.exe session because
# the WSL2 VM is recycled between invocations, wiping /tmp and stopping Docker.
set -uo pipefail

repo_dir="/mnt/e/Ai/Platforms/Tokeness/apps/new-api"
out_dir="$repo_dir/private/performance-results/profiling"
mkdir -p "$out_dir"

export PERF_DB_PASSWORD="${PERF_DB_PASSWORD:-Perf$(date +%s%N)X9}"
admin_password="${PERF_ADMIN_PASSWORD:-Perf$(date +%s%N)Y8}"

cd "$repo_dir" || exit 1
docker compose -p new-api-perf -f docker-compose.performance.yml down --volumes --remove-orphans >/dev/null 2>&1
docker compose -p new-api-perf -f docker-compose.performance.yml up -d >/dev/null 2>&1

code=""
for _ in $(seq 1 40); do
  code=$(curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:3001/api/status 2>/dev/null)
  [ "$code" = "200" ] && break
  sleep 2
done
echo "stack_ready=$code"
[ "$code" = "200" ] || exit 1
sleep 3

token=$(docker run --rm --network new-api-perf_performance \
  -e BASE_URL=http://new-api:3000 \
  -e ADMIN_USER=perf-admin \
  -e ADMIN_PASSWORD="$admin_password" \
  -e UPSTREAM_URL=http://mock-upstream:8080 \
  -v "$repo_dir:/src" -w /src \
  golang:1.26.1-alpine /usr/local/go/bin/go run ./scripts/performance/seed/main.go)
echo "token_len=${#token}"
[ -n "$token" ] || exit 1

# Pre-build the loadgen as a Linux binary inside a golang container (native WSL
# cross-build is unavailable in this VM).
docker run --rm --network new-api-perf_performance \
  -v "$repo_dir:/src" -w /src \
  golang:1.26.1-alpine \
  /usr/local/go/bin/go build -o /tmp/loadgen ./scripts/performance/loadgen

# Start load in a detached container on the shared network, sample pprof from
# the host side, then collect the loadgen stdout.
docker run -d --name perf-loadgen --network new-api-perf_performance \
  -v "$repo_dir:/src" -w /src \
  golang:1.26.1-alpine \
  /usr/local/go/bin/go run ./scripts/performance/loadgen/main.go \
  -url http://new-api:3000/v1/chat/completions -token "$token" \
  -duration 45s -concurrency 64 -payload-bytes 128 >/dev/null

sleep 8
curl -s "http://127.0.0.1:8006/debug/pprof/profile?seconds=25" -o "$out_dir/cpu.pb.gz"
curl -s "http://127.0.0.1:8006/debug/pprof/heap" -o "$out_dir/heap.pb.gz"

docker wait perf-loadgen >/dev/null 2>&1
docker logs perf-loadgen > "$out_dir/load.json" 2>&1 || true
docker rm -f perf-loadgen >/dev/null 2>&1 || true
echo "=== load result ==="
cat "$out_dir/load.json"
echo "=== artifacts ==="
ls -la "$out_dir"
