#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
compose_file="$repo_dir/docker-compose.performance.yml"
project_name="${PERF_PROJECT_NAME:-new-api-perf}"
base_url="${PERF_BASE_URL:-http://127.0.0.1:3001}"
internal_base_url="${PERF_INTERNAL_BASE_URL:-http://new-api:3000}"
duration="${PERF_DURATION:-60s}"
concurrency="${PERF_CONCURRENCY:-64}"
results_dir="${PERF_RESULTS_DIR:-$repo_dir/private/performance-results}"
db_password="${PERF_DB_PASSWORD:-Perf$(date +%s%N)X9}"
export PERF_DB_PASSWORD="$db_password"

performance_binary="$repo_dir/scripts/performance/.new-api-linux-amd64"

build_performance_binary() {
  if [[ "${PERF_SKIP_BUILD:-0}" == "1" && -x "$performance_binary" ]]; then
    return
  fi
  local go_command="${PERF_GO_COMMAND:-}"
  if [[ -z "$go_command" ]]; then
    go_command="$(command -v go.exe || command -v go || true)"
  fi
  if [[ -z "$go_command" ]]; then
    echo "A Go toolchain is required to build $performance_binary" >&2
    exit 1
  fi

  local build_dir="$repo_dir"
  local output_path="$performance_binary"
  if [[ "$go_command" == *.exe ]]; then
    local windows_go_command="$(wslpath -w "$go_command")"
    build_dir="$(wslpath -w "$repo_dir")"
    output_path="$(wslpath -w "$performance_binary")"
    cmd.exe /D /S /C "set GOOS=linux&&set GOARCH=amd64&&set CGO_ENABLED=0&&set GOEXPERIMENT=greenteagc&&$windows_go_command -C $build_dir build -trimpath -o $output_path ."
    return
  fi

  env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOEXPERIMENT=greenteagc \
    "$go_command" -C "$build_dir" build -trimpath \
      -ldflags "-s -w -X github.com/QuantumNous/new-api/common.Version=perf-local" \
      -o "$output_path" .
}

compose() {
  docker compose -p "$project_name" -f "$compose_file" "$@"
}

wait_for_api() {
  for _ in $(seq 1 90); do
    if curl --fail --silent "$base_url/api/status" >/dev/null; then
      return 0
    fi
    sleep 2
  done
  echo "New API did not become ready at $base_url" >&2
  compose logs --tail=100 new-api >&2 || true
  exit 1
}

seed_stack() {
  local password="${PERF_ADMIN_PASSWORD:-}"
  if [[ -z "$password" ]]; then
    password="Perf$(date +%s)X9"
  fi
  docker run --rm --network "${project_name}_performance" \
    -e BASE_URL="$internal_base_url" \
    -e ADMIN_USER="${PERF_ADMIN_USER:-perf-admin}" \
    -e ADMIN_PASSWORD="$password" \
    -e UPSTREAM_URL="${PERF_UPSTREAM_URL:-http://mock-upstream:8080}" \
    -v "$repo_dir:/src" \
    -w /src \
    golang:1.26.1-alpine \
    /usr/local/go/bin/go run ./scripts/performance/seed/main.go
}

run_loadgen() {
  local token="$1"
  local output_file="$2"
  shift 2
  docker run --rm --network "${project_name}_performance" \
    -v "$repo_dir:/src" \
    -w /src \
    golang:1.26.1-alpine \
    /usr/local/go/bin/go run ./scripts/performance/loadgen/main.go \
      -url "$internal_base_url/v1/chat/completions" \
      -token "$token" \
      -duration "$duration" \
      -concurrency "$concurrency" \
      "$@" >"$output_file"
}

sample_stats() {
  local output_file="$1"
  : >"$output_file"
  while :; do
    docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.MemPerc}}' \
      "${project_name}-new-api-1" 2>/dev/null >>"$output_file" || true
    sleep 1
  done
}

case "${1:-run}" in
  reset)
    compose down --volumes --remove-orphans
    ;;
  up)
    build_performance_binary
    compose down --volumes --remove-orphans
    compose up -d --build
    wait_for_api
    echo "Performance stack is ready at $base_url"
    ;;
  run)
    mkdir -p "$results_dir"
    build_performance_binary
    compose down --volumes --remove-orphans
    compose up -d --build
    wait_for_api
    token="$(seed_stack)"
    run_id="$(date +%Y%m%d-%H%M%S)"
    run_dir="$results_dir/$run_id"
    mkdir -p "$run_dir"
    run_loadgen "$token" "$run_dir/warmup-nonstream.json" -duration 15s -concurrency "$concurrency"
    if ! grep -q '"errors": 0,' "$run_dir/warmup-nonstream.json" || grep -q '"successes": 0,' "$run_dir/warmup-nonstream.json"; then
      echo "Warmup did not complete cleanly; refusing to record official results." >&2
      cat "$run_dir/warmup-nonstream.json" >&2
      exit 1
    fi
    run_loadgen "$token" "$run_dir/warmup-auth-cache-hot.json" \
      -url "$internal_base_url/v1/models" -models -duration 15s -concurrency "$concurrency"
    run_loadgen "$token" "$run_dir/auth-cache-hot.json" \
      -url "$internal_base_url/v1/models" -models \
      -duration "${PERF_AUTH_DURATION:-60s}" -concurrency "$concurrency"
    stats_file="$run_dir/nonstream-stats.csv"
    stats_pid=""
    sample_stats "$stats_file" & stats_pid=$!
    run_loadgen "$token" "$run_dir/nonstream.json" -payload-bytes 128
    kill "$stats_pid" 2>/dev/null || true
    wait "$stats_pid" 2>/dev/null || true

    run_loadgen "$token" "$run_dir/stream.json" -stream -duration "$duration" -concurrency "$((concurrency / 2))"
    run_loadgen "$token" "$run_dir/body-large.json" -payload-bytes 8192 -concurrency "$((concurrency / 4))"
    for scenario in auth-cache-hot nonstream stream body-large; do
      if ! grep -q '"errors": 0,' "$run_dir/$scenario.json"; then
        echo "WARNING: $scenario recorded failed requests; results are not comparable." >&2
      fi
    done
    echo "Results written to $run_dir"
    ;;
  *)
    echo "Usage: $0 {up|run|reset}" >&2
    exit 2
    ;;
esac
