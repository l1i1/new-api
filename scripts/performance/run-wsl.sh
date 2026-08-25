#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
compose_file="$repo_dir/docker-compose.performance.yml"
project_name="${PERF_PROJECT_NAME:-new-api-perf}"
base_url="${PERF_BASE_URL:-http://127.0.0.1:3001}"
internal_base_url="${PERF_INTERNAL_BASE_URL:-http://new-api:3000}"
duration="${PERF_DURATION:-60s}"
concurrency="${PERF_CONCURRENCY:-64}"
rounds="${PERF_ROUNDS:-3}"
warmup_duration="${PERF_WARMUP_DURATION:-15s}"
results_dir="${PERF_RESULTS_DIR:-$repo_dir/private/performance-results}"
db_password="${PERF_DB_PASSWORD:-Perf$(date +%s%N)X9}"
export PERF_DB_PASSWORD="$db_password"

performance_binary="$repo_dir/scripts/performance/.new-api-linux-amd64"
loadgen_binary="$repo_dir/scripts/performance/.loadgen-linux-amd64"
performance_provenance="$performance_binary.provenance"
active_stats_pid=""
active_stats_stop_file=""

cleanup_active_stats() {
  if [[ -z "$active_stats_pid" ]]; then
    return
  fi
  local stats_pid="$active_stats_pid"
  local stop_file="$active_stats_stop_file"
  active_stats_pid=""
  active_stats_stop_file=""
  kill -- "-$stats_pid" 2>/dev/null || true
  wait "$stats_pid" 2>/dev/null || true
  rm -f "$stop_file"
}

trap cleanup_active_stats EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

write_performance_provenance() {
  local go_version="$1"
  local state="clean"
  if [[ -n "$(git -C "$repo_dir" status --porcelain)" ]]; then
    state="dirty"
  fi
  printf 'commit %s\nstate %s\ngo_version %s\ntarget linux/amd64\nnew_api_sha256 %s\nloadgen_sha256 %s\n' \
    "$(git -C "$repo_dir" rev-parse HEAD)" \
    "$state" \
    "$go_version" \
    "$(sha256sum "$performance_binary" | awk '{print $1}')" \
    "$(sha256sum "$loadgen_binary" | awk '{print $1}')" \
    >"$performance_provenance"
}

validate_reusable_binary() {
  if [[ ! -x "$performance_binary" || ! -x "$loadgen_binary" || ! -f "$performance_provenance" ]]; then
    echo "PERF_SKIP_BUILD=1 requires both binaries and their provenance file" >&2
    exit 1
  fi
  local built_commit built_state expected_performance_sha expected_loadgen_sha
  built_commit="$(awk '$1 == "commit" { print $2; exit }' "$performance_provenance")"
  built_state="$(awk '$1 == "state" { print $2; exit }' "$performance_provenance")"
  expected_performance_sha="$(awk '$1 == "new_api_sha256" { print $2; exit }' "$performance_provenance")"
  expected_loadgen_sha="$(awk '$1 == "loadgen_sha256" { print $2; exit }' "$performance_provenance")"
  if [[ "$built_state" != "clean" || -n "$(git -C "$repo_dir" status --porcelain)" || "$built_commit" != "$(git -C "$repo_dir" rev-parse HEAD)" ]]; then
    echo "PERF_SKIP_BUILD=1 may only reuse a binary built from the current clean commit" >&2
    exit 1
  fi
  if [[ -z "$expected_performance_sha" || -z "$expected_loadgen_sha" ]] || \
    [[ "$expected_performance_sha" != "$(sha256sum "$performance_binary" | awk '{print $1}')" ]] || \
    [[ "$expected_loadgen_sha" != "$(sha256sum "$loadgen_binary" | awk '{print $1}')" ]]; then
    echo "PERF_SKIP_BUILD=1 rejected binaries that do not match their provenance hashes" >&2
    exit 1
  fi
}

build_performance_binary() {
  if [[ "${PERF_SKIP_BUILD:-0}" == "1" ]]; then
    validate_reusable_binary
    return
  fi
  local go_command="${PERF_GO_COMMAND:-}"
  if [[ -z "$go_command" ]]; then
    go_command="$(command -v go || command -v go.exe || true)"
  fi
  if [[ -z "$go_command" ]]; then
    echo "A Go toolchain is required to build the performance binaries" >&2
    exit 1
  fi

  if [[ "$go_command" == *.exe ]]; then
    local windows_repo_dir windows_performance_binary windows_loadgen_binary windows_build_env
    windows_repo_dir="$(wslpath -w "$repo_dir")"
    windows_performance_binary="$(wslpath -w "$performance_binary")"
    windows_loadgen_binary="$(wslpath -w "$loadgen_binary")"
    windows_build_env="${WSLENV:+$WSLENV:}GOOS/w:GOARCH/w:CGO_ENABLED/w:GOEXPERIMENT/w"
    env WSLENV="$windows_build_env" GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOEXPERIMENT=greenteagc \
      "$go_command" -C "$windows_repo_dir" build -trimpath \
        -ldflags "-s -w -X github.com/QuantumNous/new-api/common.Version=perf-local" \
        -o "$windows_performance_binary" .
    env WSLENV="$windows_build_env" GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOEXPERIMENT=greenteagc \
      "$go_command" -C "$windows_repo_dir" build -trimpath \
        -o "$windows_loadgen_binary" ./scripts/performance/loadgen
  else
    env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOEXPERIMENT=greenteagc \
      "$go_command" -C "$repo_dir" build -trimpath \
        -ldflags "-s -w -X github.com/QuantumNous/new-api/common.Version=perf-local" \
        -o "$performance_binary" .
    env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOEXPERIMENT=greenteagc \
      "$go_command" -C "$repo_dir" build -trimpath \
        -o "$loadgen_binary" ./scripts/performance/loadgen
  fi
  chmod +x "$performance_binary" "$loadgen_binary"
  write_performance_provenance "$("$go_command" version)"
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
    -v "$loadgen_binary:/loadgen:ro" \
    alpine:3.22 \
    /loadgen \
      -url "$internal_base_url/v1/chat/completions" \
      -token "$token" \
      -duration "$duration" \
      -concurrency "$concurrency" \
      "$@" >"$output_file"
}

start_stats_sampler() {
  local output_file="$1"
  local stop_file="$output_file.stop"
  rm -f "$stop_file"
  setsid bash -c '
    set -euo pipefail
    output_file="$1"
    container_name="$2"
    stop_file="$3"
    : >"$output_file"
    while [[ ! -e "$stop_file" ]]; do
      timeout 5s docker stats --no-stream --format "{{.Name}},{{.CPUPerc}},{{.MemUsage}},{{.MemPerc}}" \
        "$container_name" >>"$output_file"
      sleep 1
    done
  ' _ "$output_file" "${project_name}-new-api-1" "$stop_file" &
  active_stats_pid=$!
  active_stats_stop_file="$stop_file"
}

stop_stats_sampler() {
  local stats_pid="$active_stats_pid"
  local stop_file="$active_stats_stop_file"
  if ! : >"$stop_file"; then
    echo "Failed to signal the resource sampler to stop" >&2
    cleanup_active_stats
    return 1
  fi
  local status=0
  if wait "$stats_pid"; then
    status=0
  else
    status=$?
  fi
  active_stats_pid=""
  active_stats_stop_file=""
  if ! rm -f "$stop_file"; then
    echo "Failed to remove the resource sampler stop file" >&2
    return 1
  fi
  return "$status"
}

assert_valid_stats() {
  local stats_file="$1"
  if [[ ! -s "$stats_file" ]] || ! awk -F',' '
    {
      rows++
      if (!(NF == 4 &&
        $1 != "" &&
        $2 ~ /^[[:space:]]*[0-9]+([.][0-9]+)?%[[:space:]]*$/ &&
        $3 ~ /^[[:space:]]*[0-9]+([.][0-9]+)?[KMGT]?i?B[[:space:]]+\/[[:space:]]+[0-9]+([.][0-9]+)?[KMGT]?i?B[[:space:]]*$/ &&
        $4 ~ /^[[:space:]]*[0-9]+([.][0-9]+)?%[[:space:]]*$/)) {
        invalid = 1
      }
    }
    END { exit rows > 0 && !invalid ? 0 : 1 }
  ' "$stats_file"; then
    echo "Resource sampling produced no valid rows in $stats_file" >&2
    return 1
  fi
}

assert_clean_result() {
  local result_file="$1"
  local label="$2"
  if ! grep -Eq '"errors":[[:space:]]*0([,}])' "$result_file" || grep -Eq '"successes":[[:space:]]*0([,}])' "$result_file"; then
    echo "$label did not complete cleanly; refusing to record official results." >&2
    sed -n '1,80p' "$result_file" >&2
    exit 1
  fi
}

run_measured_loadgen() {
  local token="$1"
  local output_file="$2"
  local stats_file="$3"
  shift 3
  if [[ -n "$stats_file" ]]; then
    start_stats_sampler "$stats_file"
  fi

  local loadgen_status=0
  run_loadgen "$token" "$output_file" "$@" || loadgen_status=$?

  local stats_status=0
  if [[ -n "$stats_file" ]]; then
    stop_stats_sampler || stats_status=$?
  fi

  if (( loadgen_status != 0 )); then
    return "$loadgen_status"
  fi
  if (( stats_status != 0 )); then
    echo "Resource sampler failed during the measured load" >&2
    return 1
  fi
  if [[ -n "$stats_file" ]]; then
    assert_valid_stats "$stats_file"
  fi
}

record_median_result() {
  local run_dir="$1"
  local scenario="$2"
  local ranking_file="$run_dir/.$scenario-ranking"
  : >"$ranking_file"
  for round in $(seq 1 "$rounds"); do
    local result_file="$run_dir/round-$round/$scenario.json"
    local rps
    rps="$(awk -F': ' '/"rps"/ {gsub(/,/, "", $2); print $2; exit}' "$result_file")"
    printf '%s %s\n' "$rps" "$result_file" >>"$ranking_file"
  done
  sort -n -o "$ranking_file" "$ranking_file"
  local median_line median_file median_round
  median_line="$(sed -n "$((rounds / 2 + 1))p" "$ranking_file")"
  median_file="${median_line#* }"
  median_round="$(basename "$(dirname "$median_file")")"
  cp "$median_file" "$run_dir/median-$scenario.json"
  if [[ -f "$(dirname "$median_file")/$scenario-stats.csv" ]]; then
    cp "$(dirname "$median_file")/$scenario-stats.csv" "$run_dir/median-$scenario-stats.csv"
  fi
  printf '%s %s %s\n' "$scenario" "${median_line%% *}" "$median_round" >>"$run_dir/median-summary.txt"
  rm -f "$ranking_file"
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
    if (( rounds < 3 || rounds % 2 == 0 )); then
      echo "PERF_ROUNDS must be an odd number greater than or equal to 3" >&2
      exit 2
    fi
    if (( concurrency < 4 )); then
      echo "PERF_CONCURRENCY must be at least 4" >&2
      exit 2
    fi
    mkdir -p "$results_dir"
    build_performance_binary
    compose down --volumes --remove-orphans
    compose up -d --build
    wait_for_api
    token="$(seed_stack)"
    run_id="$(date +%Y%m%d-%H%M%S)"
    run_dir="$results_dir/$run_id"
    mkdir -p "$run_dir"
    {
      printf 'source_commit %s\nrounds %s\n' "$(git -C "$repo_dir" rev-parse HEAD)" "$rounds"
      printf 'host %s\ncpu_count %s\nmemory_total_kib %s\nkernel %s\n' \
        "$(hostname)" \
        "$(nproc)" \
        "$(awk '/^MemTotal:/ { print $2; exit }' /proc/meminfo)" \
        "$(uname -srmo)"
      printf 'docker_server_version %s\ndocker_compose_version %s\n' \
        "$(docker version --format '{{.Server.Version}}')" \
        "$(docker compose version --short)"
      printf 'duration %s\nauth_duration %s\nconcurrency %s\nwarmup_duration %s\n' \
        "$duration" "${PERF_AUTH_DURATION:-60s}" "$concurrency" "$warmup_duration"
      printf 'gogc %s\nlog_batch_enabled %s\nchannel_observability_async_redis %s\nperf_metrics_async_redis %s\nsql_disable_prepared_statements %s\n' \
        "${PERF_GOGC:-30}" \
        "${PERF_LOG_BATCH_ENABLED:-true}" \
        "${PERF_CHANNEL_OBSERVABILITY_ASYNC_REDIS:-false}" \
        "${PERF_METRICS_ASYNC_REDIS:-false}" \
        "${PERF_SQL_DISABLE_PREPARED_STATEMENTS:-false}"
      cat "$performance_provenance"
    } >"$run_dir/provenance.txt"
    for round in $(seq 1 "$rounds"); do
      round_dir="$run_dir/round-$round"
      mkdir -p "$round_dir"

      run_loadgen "$token" "$round_dir/warmup-auth-cache-hot.json" \
        -url "$internal_base_url/v1/models" -models -duration "$warmup_duration" -concurrency "$concurrency"
      assert_clean_result "$round_dir/warmup-auth-cache-hot.json" "round $round auth-cache-hot warmup"
      run_measured_loadgen "$token" "$round_dir/auth-cache-hot.json" "" \
        -url "$internal_base_url/v1/models" -models -duration "${PERF_AUTH_DURATION:-60s}" -concurrency "$concurrency"
      assert_clean_result "$round_dir/auth-cache-hot.json" "round $round auth-cache-hot"

      run_loadgen "$token" "$round_dir/warmup-nonstream.json" -duration "$warmup_duration" -concurrency "$concurrency" -payload-bytes 128
      assert_clean_result "$round_dir/warmup-nonstream.json" "round $round nonstream warmup"
      run_measured_loadgen "$token" "$round_dir/nonstream.json" "$round_dir/nonstream-stats.csv" -payload-bytes 128
      assert_clean_result "$round_dir/nonstream.json" "round $round nonstream"

      run_loadgen "$token" "$round_dir/warmup-stream.json" -stream -duration "$warmup_duration" -concurrency "$((concurrency / 2))"
      assert_clean_result "$round_dir/warmup-stream.json" "round $round stream warmup"
      run_measured_loadgen "$token" "$round_dir/stream.json" "$round_dir/stream-stats.csv" \
        -stream -duration "$duration" -concurrency "$((concurrency / 2))"
      assert_clean_result "$round_dir/stream.json" "round $round stream"

      run_loadgen "$token" "$round_dir/warmup-body-large.json" -duration "$warmup_duration" -concurrency "$((concurrency / 4))" -payload-bytes 8192
      assert_clean_result "$round_dir/warmup-body-large.json" "round $round body-large warmup"
      run_measured_loadgen "$token" "$round_dir/body-large.json" "$round_dir/body-large-stats.csv" \
        -payload-bytes 8192 -concurrency "$((concurrency / 4))"
      assert_clean_result "$round_dir/body-large.json" "round $round body-large"
    done
    : >"$run_dir/median-summary.txt"
    for scenario in auth-cache-hot nonstream stream body-large; do
      record_median_result "$run_dir" "$scenario"
    done
    echo "Results written to $run_dir"
    ;;
  *)
    echo "Usage: $0 {up|run|reset}" >&2
    exit 2
    ;;
esac
