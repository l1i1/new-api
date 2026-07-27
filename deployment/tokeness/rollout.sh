#!/usr/bin/env bash
set -Eeuo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly NODES_FILE="$SCRIPT_DIR/nodes.json"
readonly OPERATION="${1:-verify}"
readonly INPUT_DIGEST="${2:-}"
readonly SSH_KEY_PATH="${TOKENESS_SSH_KEY_PATH:-$HOME/.ssh/tokeness-deploy}"

log() {
  printf '[%s] %s\n' "$(date --iso-8601=seconds)" "$*"
}

fail() {
  log "ERROR: $*"
  exit 1
}

command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v ssh >/dev/null 2>&1 || fail "ssh is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"
[[ -r "$NODES_FILE" ]] || fail "missing $NODES_FILE"
[[ -r "$SSH_KEY_PATH" ]] || fail "missing SSH key at $SSH_KEY_PATH"
[[ "$OPERATION" == "verify" || "$OPERATION" == "deploy" ]] || fail "operation must be verify or deploy"

jq -e '
  .schema_version == 1 and
  (.registry_image | type == "string") and
  (.nodes | type == "array" and length == 4) and
  ([.nodes[].name] | unique | length == 4) and
  (.public_endpoints | type == "array" and length > 0)
' "$NODES_FILE" >/dev/null || fail "invalid nodes.json"

registry_image="$(jq -r '.registry_image' "$NODES_FILE")"
target_image=''
if [[ "$OPERATION" == "deploy" ]]; then
  digest="$INPUT_DIGEST"
  [[ "$digest" == sha256:* ]] || digest="sha256:$digest"
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || fail "deploy requires a sha256 image digest"
  target_image="$registry_image@$digest"
fi

ssh_args=(
  -T
  -i "$SSH_KEY_PATH"
  -o BatchMode=yes
  -o ConnectTimeout=20
  -o IdentitiesOnly=yes
  -o ServerAliveInterval=15
  -o ServerAliveCountMax=4
  -o StrictHostKeyChecking=yes
)

declare -A previous_images=()
declare -A current_versions=()
declare -a completed_nodes=()
ROLLBACK_ARMED=0
ROLLOUT_SUCCEEDED=0

parse_result() {
  local output="$1" line marker selected runtime state health started version
  line="$(grep '^TOKENESS_RESULT' <<< "$output" | tail -n 1)"
  [[ -n "$line" ]] || return 1
  IFS=$'\t' read -r marker selected runtime state health started version <<< "$line"
  [[ "$marker" == "TOKENESS_RESULT" && -n "$selected" && "$selected" == "$runtime" ]] || return 1
  [[ "$state" == "running" && -n "$version" ]] || return 1
  PARSED_SELECTED="$selected"
  PARSED_VERSION="$version"
  PARSED_STARTED="$started"
  PARSED_HEALTH="$health"
}

run_node() {
  local node="$1" command="$2" host port user output
  host="$(jq -r --arg node "$node" '.nodes[] | select(.name == $node) | .host' "$NODES_FILE")"
  port="$(jq -r --arg node "$node" '.nodes[] | select(.name == $node) | .port' "$NODES_FILE")"
  user="$(jq -r --arg node "$node" '.nodes[] | select(.name == $node) | .user' "$NODES_FILE")"
  [[ "$host" =~ ^[0-9A-Fa-f:.]+$ && "$port" =~ ^[0-9]+$ && "$user" =~ ^[A-Za-z_][A-Za-z0-9_-]*$ ]] ||
    fail "invalid SSH metadata for $node"

  log "$node: $command"
  if ! output="$(ssh "${ssh_args[@]}" -p "$port" "$user@$host" -- "$command" 2>&1)"; then
    printf '%s\n' "$output"
    return 1
  fi
  printf '%s\n' "$output"
  if ! parse_result "$output"; then
    log "ERROR: $node returned an invalid deployment result"
    return 1
  fi
}

append_summary_row() {
  local node="$1" role="$2" image="$3" version="$4" started="$5" health="$6"
  [[ -n "${GITHUB_STEP_SUMMARY:-}" ]] || return 0
  printf '| %s | %s | `%s` | `%s` | `%s` | `%s` |\n' \
    "$node" "$role" "${image#*@}" "$version" "$started" "$health" >> "$GITHUB_STEP_SUMMARY"
}

verify_public_endpoint() {
  local endpoint="$1" expected_version="$2" headers body status actual_version probe_url
  headers="$(mktemp)"
  body="$(mktemp)"
  probe_url="${endpoint%/}/v1/models?tokeness_deploy_check=$(date +%s%N)"
  status="$(curl -sS --connect-timeout 15 --max-time 45 \
    -H 'Cache-Control: no-cache' -D "$headers" -o "$body" -w '%{http_code}' "$probe_url")" || {
      rm -f "$headers" "$body"
      return 1
    }
  actual_version="$(awk -F ': *' 'tolower($1) == "x-new-api-version" {gsub(/\r/, "", $2); print $2}' "$headers" | tail -n 1)"
  rm -f "$headers" "$body"
  [[ "$status" == "401" || "$status" == "200" ]] || return 1
  [[ "$actual_version" == "$expected_version" ]] || return 1
  log "$endpoint: CDN route healthy version=$actual_version status=$status"
}

verify_public_routes() {
  local expected_version="$1" endpoint
  while IFS= read -r endpoint; do
    if ! verify_public_endpoint "$endpoint" "$expected_version"; then
      log "ERROR: $endpoint did not expose expected version $expected_version through the CDN"
      return 1
    fi
  done < <(jq -r '.public_endpoints[]' "$NODES_FILE")
}

rollback_completed() {
  local index node previous
  [[ "${#completed_nodes[@]}" -gt 0 ]] || return 0
  log "rolling back ${#completed_nodes[@]} updated node(s)"
  for ((index=${#completed_nodes[@]} - 1; index>=0; index--)); do
    node="${completed_nodes[$index]}"
    previous="${previous_images[$node]}"
    if run_node "$node" "deploy $previous"; then
      log "$node: rollback healthy"
    else
      log "ERROR: $node rollback failed and requires manual recovery"
    fi
  done
}

handle_exit() {
  local exit_code=$?
  trap - EXIT HUP INT TERM
  if [[ "$OPERATION" == "deploy" && "$ROLLBACK_ARMED" -eq 1 && "$ROLLOUT_SUCCEEDED" -eq 0 ]]; then
    rollback_completed
  fi
  exit "$exit_code"
}

trap handle_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mapfile -t node_names < <(jq -r '.nodes[].name' "$NODES_FILE")

if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo '### Tokeness New API rollout'
    echo
    echo '| Node | Role | Image digest | Version | Started at | Health |'
    echo '| --- | --- | --- | --- | --- | --- |'
  } >> "$GITHUB_STEP_SUMMARY"
fi

log "preflight: verifying all nodes"
for node in "${node_names[@]}"; do
  run_node "$node" verify || fail "$node preflight verification failed"
  previous_images[$node]="$PARSED_SELECTED"
  current_versions[$node]="$PARSED_VERSION"
  if [[ "$OPERATION" == "deploy" ]]; then
    previous_digest="${PARSED_SELECTED#"$registry_image"@}"
    if [[ "$PARSED_SELECTED" != "$registry_image@$previous_digest" || ! "$previous_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      fail "$node does not select a trusted rollback digest"
    fi
  fi
  if [[ "$OPERATION" == "verify" ]]; then
    role="$(jq -r --arg node "$node" '.nodes[] | select(.name == $node) | .role' "$NODES_FILE")"
    append_summary_row "$node" "$role" "$PARSED_SELECTED" "$PARSED_VERSION" "$PARSED_STARTED" "$PARSED_HEALTH"
  fi
done

if [[ "$OPERATION" == "verify" ]]; then
  baseline_image="${previous_images[${node_names[0]}]}"
  baseline_version="${current_versions[${node_names[0]}]}"
  for node in "${node_names[@]}"; do
    [[ "${previous_images[$node]}" == "$baseline_image" ]] || fail "fleet image drift detected at $node"
    [[ "${current_versions[$node]}" == "$baseline_version" ]] || fail "fleet version drift detected at $node"
  done
  origin_version="${current_versions[EV-JP2]}"
  verify_public_routes "$origin_version"
  log "verification complete: four nodes and CDN routes are consistent"
  exit 0
fi

log "deploying $target_image"
ROLLBACK_ARMED=1
for node in "${node_names[@]}"; do
  # Include the current node before SSH so an ambiguous connection loss still
  # triggers an idempotent restore of its preflight digest.
  completed_nodes+=("$node")
  if ! run_node "$node" "deploy $target_image"; then
    fail "$node deployment failed; fleet rollback will run"
  fi
  role="$(jq -r --arg node "$node" '.nodes[] | select(.name == $node) | .role' "$NODES_FILE")"
  append_summary_row "$node" "$role" "$PARSED_SELECTED" "$PARSED_VERSION" "$PARSED_STARTED" "$PARSED_HEALTH"
done

origin_version="$PARSED_VERSION"
if ! verify_public_routes "$origin_version"; then
  fail "public validation failed; fleet rollback will run"
fi

ROLLOUT_SUCCEEDED=1
ROLLBACK_ARMED=0
log "deployment complete: all nodes and CDN routes expose version $origin_version"
