#!/usr/bin/env bash
set -Eeuo pipefail

export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$PATH"

readonly TOKENESS_DEPLOY_COMMAND_VERSION='2026-08-17.3'

log() {
  printf '[%s] %s\n' "$(date --iso-8601=seconds)" "$*"
}

fail() {
  log "ERROR: $*"
  exit 1
}

detect_deploy_dir() {
  local candidate match=''
  for candidate in /home/ai/new-api /opt/new-api-slave; do
    if [[ -f "$candidate/docker-compose.yml" && -f "$candidate/release.env" ]]; then
      [[ -z "$match" ]] || fail "multiple New API deployment directories detected"
      match="$candidate"
    fi
  done
  [[ -n "$match" ]] || fail "New API deployment directory was not found"
  printf '%s\n' "$match"
}

DEPLOY_DIR=''
RELEASE_ENV=''
BASE_COMPOSE_FILE=''
OVERRIDE_COMPOSE_FILE=''
SERVICE_NAME=''
COMPOSE=()
COMPOSE_ARGS=()
DEPLOY_IN_PROGRESS=0
PREVIOUS_IMAGE=''

readonly COMPOSE_PULL_TIMEOUT_SECONDS=240
readonly COMPOSE_UP_TIMEOUT_SECONDS=120
readonly DOCKER_COMMAND_TIMEOUT_SECONDS=10
readonly STATUS_TIMEOUT_SECONDS=15
readonly READY_TIMEOUT_SECONDS=120
readonly BLUE_GREEN_DRAIN_TIMEOUT_SECONDS=120
readonly EPAY_RECONCILIATION_CONTAINER='epay-reconciliation'
readonly EPAY_RECONCILIATION_IMAGE='1panel/openresty@sha256:ee8c5117c291c7384a381c32068e1d9a50adc8bf392f9157c42d14bedbbe018b'
readonly EPAY_RECONCILIATION_IP='172.18.0.250'
readonly EPAY_RECONCILIATION_URL='http://172.18.0.250:18080/api.php'
readonly BLUE_GREEN_STATE_BASENAME='.blue-green.state'
readonly BLUE_GREEN_PROXY_MARKER='TOKENESS_BLUE_GREEN_MANAGED'
readonly BLUE_GREEN_PRIMARY_PORT="${TOKENESS_BLUE_GREEN_PRIMARY_PORT:-8201}"
readonly BLUE_GREEN_SECONDARY_PORT="${TOKENESS_BLUE_GREEN_SECONDARY_PORT:-8202}"
readonly BLUE_GREEN_PROXY_ROOT="${TOKENESS_BLUE_GREEN_PROXY_ROOT:-/opt/1panel/www}"

BLUE_GREEN_MODE=0
BLUE_GREEN_STATE_FILE=''
BLUE_GREEN_PROXY_CONTAINER=''
declare -a BLUE_GREEN_PROXY_FILES=()
BLUE_GREEN_ACTIVE_PORT="$BLUE_GREEN_PRIMARY_PORT"
BLUE_GREEN_ACTIVE_CONTAINER=''
BLUE_GREEN_STATE_WAS_PRESENT=0
BLUE_GREEN_STATE_BASELINE_PRESENT=0
BLUE_GREEN_STATE_PHASE='absent'
BLUE_GREEN_ACTIVE_IMAGE=''
BLUE_GREEN_PENDING_PORT=''
BLUE_GREEN_PENDING_CONTAINER=''
BLUE_GREEN_PENDING_IMAGE=''
BLUE_GREEN_CLEANUP_OLD_PORT=''
BLUE_GREEN_CLEANUP_OLD_CONTAINER=''
BLUE_GREEN_CLEANUP_OLD_IMAGE=''
BLUE_GREEN_OLD_PORT="$BLUE_GREEN_PRIMARY_PORT"
BLUE_GREEN_OLD_CONTAINER=''
BLUE_GREEN_NEW_PORT="$BLUE_GREEN_SECONDARY_PORT"
BLUE_GREEN_NEW_CONTAINER=''
BLUE_GREEN_NEW_INSTANCE_ID=''
BLUE_GREEN_SWITCHED=0
BLUE_GREEN_PROXY_BACKUP_DIR=''
BLUE_GREEN_PROC_ROOT='/proc'

run_timed() {
  local seconds="$1"
  shift
  timeout --signal=TERM --kill-after=10s "${seconds}s" "$@"
}

compose_timed() {
  local seconds="$1"
  shift
  run_timed "$seconds" "${COMPOSE[@]}" "${COMPOSE_ARGS[@]}" "$@"
}

blue_green_state_value() {
  local key="$1"
  sed -n "s/^${key}=//p" "$BLUE_GREEN_STATE_FILE" | tail -n 1
}

validate_blue_green_container_name() {
  [[ "$1" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$ ]]
}

is_blue_green_slot_port() {
  [[ "$1" == "$BLUE_GREEN_PRIMARY_PORT" || "$1" == "$BLUE_GREEN_SECONDARY_PORT" ]]
}

validate_blue_green_configuration() {
  [[ "$BLUE_GREEN_PRIMARY_PORT" =~ ^[0-9]+$ &&
    "$BLUE_GREEN_PRIMARY_PORT" -ge 1 && "$BLUE_GREEN_PRIMARY_PORT" -le 65535 ]] ||
    fail "blue-green primary port is invalid"
  [[ "$BLUE_GREEN_SECONDARY_PORT" =~ ^[0-9]+$ &&
    "$BLUE_GREEN_SECONDARY_PORT" -ge 1 && "$BLUE_GREEN_SECONDARY_PORT" -le 65535 ]] ||
    fail "blue-green secondary port is invalid"
  [[ "$BLUE_GREEN_PRIMARY_PORT" != "$BLUE_GREEN_SECONDARY_PORT" ]] ||
    fail "blue-green ports must be distinct"
  [[ "$BLUE_GREEN_PROXY_ROOT" == /* && "$BLUE_GREEN_PROXY_ROOT" != / ]] ||
    fail "blue-green proxy root must be an absolute non-root path"
}

read_blue_green_state() {
  BLUE_GREEN_ACTIVE_PORT="$BLUE_GREEN_PRIMARY_PORT"
  BLUE_GREEN_ACTIVE_CONTAINER="$SERVICE_NAME"
  BLUE_GREEN_STATE_WAS_PRESENT=0
  BLUE_GREEN_STATE_BASELINE_PRESENT=0
  BLUE_GREEN_STATE_PHASE='absent'
  BLUE_GREEN_ACTIVE_IMAGE=''
  BLUE_GREEN_PENDING_PORT=''
  BLUE_GREEN_PENDING_CONTAINER=''
  BLUE_GREEN_PENDING_IMAGE=''
  BLUE_GREEN_CLEANUP_OLD_PORT=''
  BLUE_GREEN_CLEANUP_OLD_CONTAINER=''
  BLUE_GREEN_CLEANUP_OLD_IMAGE=''
  [[ -f "$BLUE_GREEN_STATE_FILE" ]] || return 0
  BLUE_GREEN_STATE_WAS_PRESENT=1

  local phase baseline next_port next_container next_image port container image
  phase="$(blue_green_state_value PHASE)"
  [[ -z "$phase" ]] && phase='committed'
  [[ "$phase" == 'committed' || "$phase" == 'pending' || "$phase" == 'cleanup-pending' ]] ||
    fail "blue-green state has an invalid phase"
  baseline="$(blue_green_state_value BASELINE_PRESENT)"
  if [[ "$phase" == 'pending' ]]; then
    [[ "$baseline" == 0 || "$baseline" == 1 ]] ||
      fail "blue-green pending state has an invalid baseline marker"
    next_port="$(blue_green_state_value NEXT_PORT)"
    next_container="$(blue_green_state_value NEXT_CONTAINER)"
    next_image="$(blue_green_state_value NEXT_IMAGE)"
    is_blue_green_slot_port "$next_port" ||
      fail "blue-green pending state has an invalid next port"
    validate_blue_green_container_name "$next_container" ||
      fail "blue-green pending state has an invalid next container"
    is_trusted_image "$next_image" ||
      fail "blue-green pending state has an invalid next image"
    BLUE_GREEN_STATE_BASELINE_PRESENT="$baseline"
    BLUE_GREEN_PENDING_PORT="$next_port"
    BLUE_GREEN_PENDING_CONTAINER="$next_container"
    BLUE_GREEN_PENDING_IMAGE="$next_image"
  elif [[ "$phase" == 'cleanup-pending' ]]; then
    old_port="$(blue_green_state_value OLD_PORT)"
    old_container="$(blue_green_state_value OLD_CONTAINER)"
    old_image="$(blue_green_state_value OLD_IMAGE)"
    is_blue_green_slot_port "$old_port" ||
      fail "blue-green cleanup state has an invalid old port"
    validate_blue_green_container_name "$old_container" ||
      fail "blue-green cleanup state has an invalid old container"
    is_trusted_image "$old_image" ||
      fail "blue-green cleanup state has an invalid old image"
    BLUE_GREEN_STATE_BASELINE_PRESENT=1
    BLUE_GREEN_CLEANUP_OLD_PORT="$old_port"
    BLUE_GREEN_CLEANUP_OLD_CONTAINER="$old_container"
    BLUE_GREEN_CLEANUP_OLD_IMAGE="$old_image"
  else
    BLUE_GREEN_STATE_BASELINE_PRESENT=1
  fi

  port="$(blue_green_state_value PORT)"
  container="$(blue_green_state_value CONTAINER)"
  image="$(blue_green_state_value IMAGE)"
  is_blue_green_slot_port "$port" ||
    fail "blue-green state has an invalid active port"
  validate_blue_green_container_name "$container" ||
    fail "blue-green state has an invalid active container"
  is_trusted_image "$image" ||
    fail "blue-green state has an invalid active image"
  BLUE_GREEN_ACTIVE_PORT="$port"
  BLUE_GREEN_ACTIVE_CONTAINER="$container"
  BLUE_GREEN_ACTIVE_IMAGE="$image"
  BLUE_GREEN_STATE_PHASE="$phase"
}

write_blue_green_state() {
  local port="$1" container="$2" image="$3" next_file
  is_blue_green_slot_port "$port" || fail "invalid blue-green state port"
  validate_blue_green_container_name "$container" || fail "invalid blue-green state container"
  is_trusted_image "$image" || fail "invalid blue-green state image"
  next_file="$(mktemp "$DEPLOY_DIR/.blue-green.state.XXXXXX")"
  printf 'PHASE=committed\nPORT=%s\nCONTAINER=%s\nIMAGE=%s\n' \
    "$port" "$container" "$image" > "$next_file"
  chmod 0644 "$next_file"
  mv -f "$next_file" "$BLUE_GREEN_STATE_FILE"
}

write_blue_green_pending_state() {
  local old_port="$1" old_container="$2" old_image="$3" baseline="$4"
  local next_port="$5" next_container="$6" next_image="$7" next_file
  is_blue_green_slot_port "$old_port" || fail "invalid pending old port"
  validate_blue_green_container_name "$old_container" || fail "invalid pending old container"
  is_trusted_image "$old_image" || fail "invalid pending old image"
  [[ "$baseline" == 0 || "$baseline" == 1 ]] || fail "invalid pending baseline marker"
  is_blue_green_slot_port "$next_port" || fail "invalid pending next port"
  validate_blue_green_container_name "$next_container" || fail "invalid pending next container"
  is_trusted_image "$next_image" || fail "invalid pending next image"
  next_file="$(mktemp "$DEPLOY_DIR/.blue-green.state.XXXXXX")"
  printf 'PHASE=pending\nBASELINE_PRESENT=%s\nPORT=%s\nCONTAINER=%s\nIMAGE=%s\nNEXT_PORT=%s\nNEXT_CONTAINER=%s\nNEXT_IMAGE=%s\n' \
    "$baseline" "$old_port" "$old_container" "$old_image" \
    "$next_port" "$next_container" "$next_image" > "$next_file"
  chmod 0644 "$next_file"
  mv -f "$next_file" "$BLUE_GREEN_STATE_FILE"
}

write_blue_green_cleanup_pending_state() {
  local active_port="$1" active_container="$2" active_image="$3"
  local old_port="$4" old_container="$5" old_image="$6" next_file
  is_blue_green_slot_port "$active_port" ||
    fail "invalid cleanup active port"
  validate_blue_green_container_name "$active_container" ||
    fail "invalid cleanup active container"
  is_trusted_image "$active_image" ||
    fail "invalid cleanup active image"
  is_blue_green_slot_port "$old_port" ||
    fail "invalid cleanup old port"
  validate_blue_green_container_name "$old_container" ||
    fail "invalid cleanup old container"
  is_trusted_image "$old_image" ||
    fail "invalid cleanup old image"
  next_file="$(mktemp "$DEPLOY_DIR/.blue-green.state.XXXXXX")"
  printf 'PHASE=cleanup-pending\nPORT=%s\nCONTAINER=%s\nIMAGE=%s\nOLD_PORT=%s\nOLD_CONTAINER=%s\nOLD_IMAGE=%s\n' \
    "$active_port" "$active_container" "$active_image" \
    "$old_port" "$old_container" "$old_image" > "$next_file"
  chmod 0644 "$next_file"
  mv -f "$next_file" "$BLUE_GREEN_STATE_FILE"
}

blue_green_container_exists() {
	local container="$1" output status
	if output="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
		--format '{{.Id}}' "$container" 2>&1)"; then
		return 0
	else
		status=$?
	fi
	if grep -Eqi 'no such (object|container)' <<< "$output"; then
		return 1
	fi
	log "ERROR: docker inspect failed for $container with status $status: $output"
	return 2
}

blue_green_remove_container() {
	local container="$1" exists_status
	if blue_green_container_exists "$container"; then
		:
	else
		exists_status=$?
		[[ "$exists_status" -eq 1 ]] && return 0
		return 1
	fi
	if ! run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker rm -f "$container" >/dev/null 2>&1; then
		log "ERROR: could not remove blue-green container $container"
		return 1
	fi
	if blue_green_container_exists "$container"; then
		log "ERROR: blue-green container still exists after removal: $container"
		return 1
	else
		exists_status=$?
		[[ "$exists_status" -eq 1 ]] && return 0
		return 1
	fi
}

discover_blue_green_proxy_container() {
  [[ -n "$BLUE_GREEN_PROXY_CONTAINER" ]] && return 0
  local -a candidates=() matches=()
  local candidate mounts listed
  listed="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker ps \
    --format '{{.Names}}\t{{.Image}}' |
    awk -F '\t' 'tolower($2) ~ /openresty|nginx/ {print $1}')" ||
    fail "could not list OpenResty proxy containers"
  if [[ -n "$listed" ]]; then
    mapfile -t candidates <<< "$listed"
  fi
  for candidate in "${candidates[@]}"; do
    [[ "$candidate" == "$EPAY_RECONCILIATION_CONTAINER" ]] && continue
    mounts="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
			--format '{{range .Mounts}}{{printf "%s\t%s\n" .Source .Destination}}{{end}}' \
			"$candidate")" || fail "could not inspect OpenResty proxy container $candidate"
    if awk -F '\t' -v root="$BLUE_GREEN_PROXY_ROOT" '$1 == root { found = 1 } END { exit(found ? 0 : 1) }' <<< "$mounts"; then
      matches+=("$candidate")
    fi
  done
  if ((${#matches[@]} != 1)); then
    fail "expected exactly one OpenResty proxy container mounted at $BLUE_GREEN_PROXY_ROOT; found ${#matches[@]}"
  fi
  BLUE_GREEN_PROXY_CONTAINER="${matches[0]}"
}

discover_blue_green_proxy_files() {
	local -a files=()
  [[ -d "$BLUE_GREEN_PROXY_ROOT" ]] || fail "JP-M OpenResty site root was not found"
  mapfile -d '' files < <(find "$BLUE_GREEN_PROXY_ROOT" -type f -path '*/proxy/*.conf' -print0)
  ((${#files[@]} > 0)) || fail "JP-M OpenResty proxy configuration was not found"
	BLUE_GREEN_PROXY_FILES=()
	local file entries
	for file in "${files[@]}"; do
		entries="$(blue_green_managed_proxy_entries "$file")" ||
			fail "OpenResty proxy file contains an invalid or unpaired blue-green marker: $file"
		if [[ -n "$entries" ]]; then
			BLUE_GREEN_PROXY_FILES+=("$file")
		fi
  done
  ((${#BLUE_GREEN_PROXY_FILES[@]} > 0)) ||
    fail "JP-M OpenResty proxy configuration has no supported New API backend"
}

blue_green_managed_proxy_entries() {
	local file="$1"
	awk -v marker="$BLUE_GREEN_PROXY_MARKER" \
		-v primary="$BLUE_GREEN_PRIMARY_PORT" -v secondary="$BLUE_GREEN_SECONDARY_PORT" '
		function managed_host(line, value) {
			if (line !~ "^[[:space:]]*#[[:space:]]*" marker "[[:space:]]+host=[^[:space:]]+[[:space:]]*$") {
				return ""
			}
			value = line
			sub("^[[:space:]]*#[[:space:]]*" marker "[[:space:]]+host=", "", value)
			sub("[[:space:]]*$", "", value)
			return value
		}
		function is_backend(line, pattern) {
			pattern = "^[[:space:]]*proxy_pass[[:space:]]+http://127\\.0\\.0\\.1:(" primary "|" secondary ");[[:space:]]*$"
			return line ~ pattern
		}
		function has_marker_token(line) {
			return line ~ "^[[:space:]]*#[[:space:]]*" marker "([[:space:]]|$)"
		}
		{
			if (pending_host != "") {
				if (!is_backend($0)) {
					exit 3
				}
				port = $0
				sub(/^.*:/, "", port)
				sub(/;.*/, "", port)
				print pending_host "\t" port
				pending_host = ""
				next
			}
			host = managed_host($0)
			if (host != "") {
				pending_host = host
				next
			}
			if (has_marker_token($0)) {
				exit 2
			}
		}
		END { if (pending_host != "") exit 3 }
	' "$file"
}

blue_green_proxy_port() {
	local file host port expected=''
	discover_blue_green_proxy_files
	for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
		while IFS=$'\t' read -r host port; do
			[[ -z "$port" ]] && continue
      if [[ -z "$expected" ]]; then
        expected="$port"
      elif [[ "$expected" != "$port" ]]; then
        fail "OpenResty proxy backends use mixed ports: $file"
      fi
		done < <(blue_green_managed_proxy_entries "$file")
  done
  [[ -n "$expected" ]] || fail "OpenResty proxy active port could not be determined"
  printf '%s\n' "$expected"
}

blue_green_proxy_exec() {
  discover_blue_green_proxy_container
  run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker exec "$BLUE_GREEN_PROXY_CONTAINER" \
    sh -c '
      if command -v openresty >/dev/null 2>&1; then
        exec openresty "$@"
      elif command -v nginx >/dev/null 2>&1; then
        exec nginx "$@"
      else
        exec /usr/local/openresty/nginx/sbin/nginx "$@"
      fi
    ' sh "$@" >/dev/null
}

blue_green_proxy_reload() {
	local before after attempt pid
	before="$(blue_green_proxy_worker_pids)" || return 1
	[[ -n "$before" ]] || return 1
	blue_green_proxy_exec -t || return 1
	blue_green_proxy_exec -s reload || return 1
	for attempt in $(seq 1 10); do
		after="$(blue_green_proxy_worker_pids 2>/dev/null || true)"
		for pid in $after; do
			if [[ " $before " != *" $pid "* ]]; then
				return 0
			fi
		done
		sleep 1
	done
	log "ERROR: OpenResty did not start a new worker generation after reload"
	return 1
}

blue_green_proxy_worker_pids() {
	discover_blue_green_proxy_container
	run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker exec "$BLUE_GREEN_PROXY_CONTAINER" sh -c '
		master=""
		for pid_file in /var/run/nginx.pid /run/nginx.pid /usr/local/openresty/nginx/logs/nginx.pid; do
			if [ -r "$pid_file" ]; then
				master="$(cat "$pid_file")"
				break
			fi
		done
		[ -n "$master" ] || exit 1
		children="/proc/$master/task/$master/children"
		[ -r "$children" ] || exit 1
		cat "$children"
	'
}

blue_green_proxy_route_hosts() {
	local file host port
	declare -A seen_hosts=()
	discover_blue_green_proxy_files
	for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
		while IFS=$'\t' read -r host port; do
			[[ -n "$host" ]] || continue
			[[ "$host" =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$ ]] ||
				fail "OpenResty New API marker contains an invalid probe hostname: $host"
			if [[ -z "${seen_hosts[$host]:-}" ]]; then
				printf '%s\n' "$host"
				seen_hosts[$host]=1
			fi
		done < <(blue_green_managed_proxy_entries "$file")
	done
  ((${#BLUE_GREEN_PROXY_FILES[@]} > 0)) ||
    fail "OpenResty New API proxy site hostname could not be determined"
}

blue_green_proxy_route_host() {
  local host
  while IFS= read -r host; do
    printf '%s\n' "$host"
    return 0
  done < <(blue_green_proxy_route_hosts)
  fail "OpenResty New API proxy site hostname could not be determined"
}

blue_green_proxy_route_status() {
  local host="${1:-}"
  local probe
  [[ -n "$host" ]] || host="$(blue_green_proxy_route_host)"
  probe="$(date +%s%N)"
  discover_blue_green_proxy_container
  run_timed "$STATUS_TIMEOUT_SECONDS" docker exec "$BLUE_GREEN_PROXY_CONTAINER" sh -c '
    host="$1"
    probe="$2"
    command -v curl >/dev/null 2>&1 || exit 127
    body="$(curl -kfsS --max-time 10 --max-redirs 0 \
      --resolve "$host:443:127.0.0.1" -H "Host: $host" \
      "https://$host/api/status?tokeness_blue_green_probe=$probe" 2>/dev/null || true)"
    if printf "%s\n" "$body" | grep -Eq "\"success\"[[:space:]]*:[[:space:]]*true"; then
      printf "%s\n" "$body"
      exit 0
    fi
    body="$(curl -fsS --max-time 10 --max-redirs 0 \
      --resolve "$host:80:127.0.0.1" -H "Host: $host" \
      "http://$host/api/status?tokeness_blue_green_probe=$probe" 2>/dev/null || true)"
    printf "%s\n" "$body" | grep -Eq "\"success\"[[:space:]]*:[[:space:]]*true" || exit 1
    printf "%s\n" "$body"
  ' sh "$host" "$probe"
}

blue_green_status_version() {
  sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"\\]*\)".*/\1/p' <<< "$1" | head -n 1
}

blue_green_status_identity() {
  local body="$1" instance_id start_time
  instance_id="$(sed -n 's/.*"instance_id"[[:space:]]*:[[:space:]]*"\([^"\\]*\)".*/\1/p' <<< "$body" | head -n 1)"
  if [[ -n "$instance_id" ]]; then
    printf 'instance:%s\n' "$instance_id"
    return 0
  fi

  # Older images do not expose instance_id. Their process start time remains
  # a useful bootstrap identity until the first blue-green image is active.
  start_time="$(sed -n 's/.*"start_time"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' <<< "$body" | head -n 1)"
  [[ "$start_time" =~ ^[1-9][0-9]*$ ]] || return 1
  printf 'start:%s\n' "$start_time"
}

blue_green_verify_container_binding() {
	local container="$1" expected_image="$2" expected_port="$3"
	local identity id name runtime bindings host_ip host_port
	local -a binding_lines=()
	identity="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
		--format '{{printf "%s\t%s\t%s" .Id .Name .Config.Image}}' "$container")" ||
		fail "could not inspect blue-green target container $container"
	IFS=$'\t' read -r id name runtime <<< "$identity"
	[[ -n "$id" && ( "$name" == "/$container" || "$id" == "$container" ) ]] ||
		fail "blue-green target container identity is invalid: $container"
	[[ "$runtime" == "$expected_image" ]] ||
		fail "blue-green target container does not run the expected image: $container"

	bindings="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
		--format '{{range index .NetworkSettings.Ports "3000/tcp"}}{{printf "%s\t%s\n" .HostIp .HostPort}}{{end}}' \
		"$container")" || fail "could not inspect blue-green target port binding: $container"
	mapfile -t binding_lines < <(sed '/^[[:space:]]*$/d' <<< "$bindings")
	((${#binding_lines[@]} == 1)) ||
		fail "blue-green target must publish exactly one 3000/tcp binding: $container"
	IFS=$'\t' read -r host_ip host_port <<< "${binding_lines[0]}"
	[[ "$host_ip" == '127.0.0.1' && "$host_port" == "$expected_port" ]] ||
		fail "blue-green target binding is $host_ip:$host_port; expected 127.0.0.1:$expected_port"
}

blue_green_verify_live_route() {
  local expected_port="$1" expected_container="$2" expected_image="$3"
  local route_body target_body route_version target_version route_identity target_identity
  local attempt host hosts all_hosts_ok
	blue_green_verify_container_binding "$expected_container" "$expected_image" "$expected_port"
  [[ "$(blue_green_proxy_port)" == "$expected_port" ]] ||
    fail "OpenResty proxy does not select the expected port $expected_port"
	# A disk edit is not evidence that workers loaded it. Reload waits for a new
	# worker generation before this probe can justify draining the old container.
  blue_green_proxy_reload || fail "OpenResty reload failed while validating the active slot"
  hosts="$(blue_green_proxy_route_hosts)"
  for attempt in $(seq 1 10); do
    target_body="$(status_json "$expected_container" 2>/dev/null || true)"
    target_version="$(blue_green_status_version "$target_body")"
    target_identity="$(blue_green_status_identity "$target_body" 2>/dev/null || true)"
    all_hosts_ok=1
    while IFS= read -r host; do
      [[ -n "$host" ]] || continue
      route_body="$(blue_green_proxy_route_status "$host" 2>/dev/null || true)"
      route_version="$(blue_green_status_version "$route_body")"
      route_identity="$(blue_green_status_identity "$route_body" 2>/dev/null || true)"
      if ! grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<< "$route_body" ||
        ! grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<< "$target_body" ||
        [[ -z "$route_version" || "$route_version" != "$target_version" ||
          -z "$target_identity" || "$route_identity" != "$target_identity" ]]; then
        all_hosts_ok=0
        break
      fi
    done <<< "$hosts"
    if [[ "$all_hosts_ok" -eq 1 && -n "$target_version" ]]; then
      return 0
    fi
    sleep 1
  done
  fail "OpenResty route did not reach $expected_container/$expected_image"
}

blue_green_verify_selected_route() {
	local expected_image="$1" id
	read_blue_green_state
	id="$(container_id)"
	[[ -n "$id" ]] || fail "active New API container was not found for route verification"
	blue_green_verify_live_route "$BLUE_GREEN_ACTIVE_PORT" "$id" "$expected_image"
}

blue_green_restore_proxy_backup() {
  local file backup index=0
  [[ -n "$BLUE_GREEN_PROXY_BACKUP_DIR" && -d "$BLUE_GREEN_PROXY_BACKUP_DIR" ]] || return 1
  for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
    index=$((index + 1))
    backup="$BLUE_GREEN_PROXY_BACKUP_DIR/$index.conf"
    [[ -f "$backup" ]] || {
      log "ERROR: missing OpenResty proxy backup: $backup"
      return 1
    }
    cp -a "$backup" "$file" || {
      log "ERROR: failed to restore OpenResty proxy file: $file"
      return 1
    }
  done
  if ! blue_green_proxy_reload; then
    log "ERROR: failed to reload the restored OpenResty configuration"
    return 1
  fi
}

blue_green_rewrite_proxy_files() {
  local port="$1" file temp
  local changed=0
  for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
    temp="$(mktemp "$file.blue-green.XXXXXX")"
	awk -v marker="$BLUE_GREEN_PROXY_MARKER" -v port="$port" \
		-v primary="$BLUE_GREEN_PRIMARY_PORT" -v secondary="$BLUE_GREEN_SECONDARY_PORT" '
		function is_marker(line) {
			return line ~ "^[[:space:]]*#[[:space:]]*" marker "[[:space:]]+host=[^[:space:]]+[[:space:]]*$"
		}
		function is_backend(line, pattern) {
			pattern = "^[[:space:]]*proxy_pass[[:space:]]+http://127\\.0\\.0\\.1:(" primary "|" secondary ");[[:space:]]*$"
			return line ~ pattern
      }
      function indent(line, value) {
        value = line
        sub(/[^[:space:]].*$/, "", value)
        return value
      }
      {
			if (managed && is_backend($0)) {
				prefix = indent($0)
				print prefix "proxy_pass http://127.0.0.1:" port ";"
          managed = 0
          changed = 1
          next
        }
        print $0
			managed = is_marker($0)
      }
      END { exit(changed ? 0 : 1) }
    ' "$file" > "$temp" || {
      rm -f "$temp"
      log "ERROR: OpenResty proxy file has no managed backend: $file"
      return 1
    }
    mv -f "$temp" "$file" || {
      rm -f "$temp"
      log "ERROR: failed to replace OpenResty proxy file: $file"
      return 1
    }
    changed=1
  done
  ((changed == 1)) || {
    log "ERROR: OpenResty proxy configuration was not changed"
    return 1
  }
}

ensure_blue_green_proxy() {
  local expected_port="$1" active_port
  discover_blue_green_proxy_container
  discover_blue_green_proxy_files
	active_port="$(blue_green_proxy_port)"
  [[ "$active_port" == "$expected_port" ]] ||
    fail "OpenResty marked New API backend is on port $active_port; expected $expected_port"
  if [[ "$BLUE_GREEN_STATE_WAS_PRESENT" -eq 1 ]]; then
    verify_blue_green_proxy
  fi
}

verify_blue_green_proxy() {
	local expected_port file host port managed_count
  read_blue_green_state
  expected_port="$BLUE_GREEN_ACTIVE_PORT"
  discover_blue_green_proxy_container
	discover_blue_green_proxy_files
	for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
		managed_count=0
		while IFS=$'\t' read -r host port; do
			[[ -z "$port" || "$port" == "$expected_port" ]] ||
				fail "OpenResty proxy backends use mixed ports; expected $expected_port: $file"
			managed_count=$((managed_count + 1))
		done < <(blue_green_managed_proxy_entries "$file")
		((managed_count > 0)) || fail "OpenResty proxy file has no managed backend: $file"
	done
}

switch_blue_green_proxy() {
  local port="$1" file backup index=0 backup_dir
  discover_blue_green_proxy_container
  discover_blue_green_proxy_files
  backup_dir="$(mktemp -d "$DEPLOY_DIR/.blue-green-proxy-switch.XXXXXX")"
  BLUE_GREEN_PROXY_BACKUP_DIR="$backup_dir"
  for file in "${BLUE_GREEN_PROXY_FILES[@]}"; do
    index=$((index + 1))
    backup="$backup_dir/$index.conf"
    cp -a "$file" "$backup" ||
      fail "could not back up OpenResty proxy file: $file"
  done

  # From this point on, a failed reload may have applied the new workers. Keep
  # the rollback marker and backup until the old route is live again.
  BLUE_GREEN_SWITCHED=1
  if ! blue_green_rewrite_proxy_files "$port"; then
    if blue_green_restore_proxy_backup; then
      BLUE_GREEN_SWITCHED=0
      rm -rf -- "$backup_dir"
      BLUE_GREEN_PROXY_BACKUP_DIR=''
    else
      log "ERROR: OpenResty proxy rollback is pending; preserving switch backup"
    fi
    fail "OpenResty blue-green proxy switch failed"
  fi
  if ! blue_green_proxy_reload; then
    if blue_green_restore_proxy_backup; then
      BLUE_GREEN_SWITCHED=0
      rm -rf -- "$backup_dir"
      BLUE_GREEN_PROXY_BACKUP_DIR=''
    else
      log "ERROR: OpenResty proxy rollback is pending; preserving switch backup"
    fi
    fail "OpenResty blue-green proxy switch failed"
  fi
}

is_trusted_image() {
  [[ "$1" =~ ^ghcr\.io/l1i1/new-api@sha256:[0-9a-f]{64}$ ]]
}

image_available_locally() {
  run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker image inspect "$1" >/dev/null 2>&1
}

read_release_image() {
  sed -n 's/^NEW_API_IMAGE=//p' "$RELEASE_ENV" | tail -n 1
}

write_release_image() {
  local image="$1" next_file
  next_file="$(mktemp "$DEPLOY_DIR/.release.env.XXXXXX")"
  printf 'NEW_API_IMAGE=%s\n' "$image" > "$next_file"
  chmod 0644 "$next_file"
  mv -f "$next_file" "$RELEASE_ENV"
}

container_id() {
  local container id
  if [[ "$BLUE_GREEN_MODE" -eq 1 ]]; then
    read_blue_green_state
    if [[ "$BLUE_GREEN_STATE_WAS_PRESENT" -eq 1 ]]; then
      container="$BLUE_GREEN_ACTIVE_CONTAINER"
    else
      compose_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" ps -q "$SERVICE_NAME" |
        head -n 1
      return 0
    fi
  else
    id="$(compose_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" ps -q "$SERVICE_NAME" |
      head -n 1)"
    [[ -n "$id" ]] && { printf '%s\n' "$id"; return 0; }
    container="$SERVICE_NAME"
  fi
  run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect --format '{{.Id}}' "$container" 2>/dev/null || true
}

blue_green_standby_details() {
  read_blue_green_state
  BLUE_GREEN_OLD_PORT="$BLUE_GREEN_ACTIVE_PORT"
  BLUE_GREEN_OLD_CONTAINER="$BLUE_GREEN_ACTIVE_CONTAINER"
  if [[ "$BLUE_GREEN_STATE_WAS_PRESENT" -eq 0 ]]; then
    BLUE_GREEN_OLD_CONTAINER="$(compose_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" ps -q "$SERVICE_NAME" |
      head -n 1)"
    [[ -n "$BLUE_GREEN_OLD_CONTAINER" ]] || fail "active New API container was not found"
  fi
  if [[ "$BLUE_GREEN_OLD_PORT" == "$BLUE_GREEN_PRIMARY_PORT" ]]; then
		BLUE_GREEN_NEW_PORT="$BLUE_GREEN_SECONDARY_PORT"
    BLUE_GREEN_NEW_CONTAINER='new-api-blue'
  else
		BLUE_GREEN_NEW_PORT="$BLUE_GREEN_PRIMARY_PORT"
    BLUE_GREEN_NEW_CONTAINER='new-api-green'
  fi
}

blue_green_commit_active_slot() {
  local active_port="$1" active_container="$2" active_image="$3"
  local old_port="$4" old_container="$5" old_image="$6"
  write_release_image "$active_image" || return 1
  # Keep the old container durable until cleanup succeeds. A process killed
  # after this write can retry the same cleanup without guessing from Docker.
  write_blue_green_cleanup_pending_state \
    "$active_port" "$active_container" "$active_image" \
    "$old_port" "$old_container" "$old_image" || return 1
  DEPLOY_IN_PROGRESS=0
	if ! drain_blue_green_container "$old_port" "$old_container" "$active_container"; then
    log "WARNING: old blue-green container cleanup is pending: $old_container"
		return 2
  fi
	if ! write_blue_green_state "$active_port" "$active_container" "$active_image"; then
		log "WARNING: blue-green cleanup completed but committed state persistence is pending"
		return 2
	fi
}

recover_blue_green_cleanup_pending_state() {
  [[ "$BLUE_GREEN_STATE_PHASE" == 'cleanup-pending' ]] || return 0

  local commit_status=0
  local active_port="$BLUE_GREEN_ACTIVE_PORT"
  local active_container="$BLUE_GREEN_ACTIVE_CONTAINER"
  local active_image="$BLUE_GREEN_ACTIVE_IMAGE"
  local old_port="$BLUE_GREEN_CLEANUP_OLD_PORT"
  local old_container="$BLUE_GREEN_CLEANUP_OLD_CONTAINER"
  local old_image="$BLUE_GREEN_CLEANUP_OLD_IMAGE"
  BLUE_GREEN_NEW_CONTAINER="$active_container"
  if wait_for_blue_green_container "$active_container" "$active_image" 1 &&
    (blue_green_verify_live_route "$active_port" "$active_container" "$active_image"); then
    blue_green_commit_active_slot \
      "$active_port" "$active_container" "$active_image" \
      "$old_port" "$old_container" "$old_image" || commit_status=$?
    if [[ "$commit_status" -eq 2 ]]; then
      log "blue-green active slot is healthy; old-slot cleanup remains pending"
      return 0
    fi
    [[ "$commit_status" -eq 0 ]] ||
      fail "cleanup-pending active slot could not be persisted"
    return 0
  fi

  log "WARNING: cleanup-pending active slot or live route is unhealthy; restoring the previous slot"
  wait_for_blue_green_container "$old_container" "$old_image" ||
    fail "cleanup-pending previous New API slot is not healthy"
  switch_blue_green_proxy "$old_port"
  blue_green_verify_live_route "$old_port" "$old_container" "$old_image"
  BLUE_GREEN_SWITCHED=0
  rm -rf -- "$BLUE_GREEN_PROXY_BACKUP_DIR" ||
    log "WARNING: could not remove rollback proxy backup directory"
  BLUE_GREEN_PROXY_BACKUP_DIR=''

  commit_status=0
  blue_green_commit_active_slot \
    "$old_port" "$old_container" "$old_image" \
    "$active_port" "$active_container" "$active_image" || commit_status=$?
  if [[ "$commit_status" -eq 2 ]]; then
    log "blue-green rollback committed; failed-slot cleanup remains pending"
    return 0
  fi
  [[ "$commit_status" -eq 0 ]] ||
    fail "cleanup-pending rollback state could not be persisted"
}

recover_blue_green_pending_state() {
  [[ "$BLUE_GREEN_STATE_PHASE" == 'pending' ]] || return 0

  local active_port="$BLUE_GREEN_ACTIVE_PORT" commit_status=0
  local next_port="$BLUE_GREEN_PENDING_PORT" next_container="$BLUE_GREEN_PENDING_CONTAINER"
  local next_image="$BLUE_GREEN_PENDING_IMAGE"
  active_port="$(blue_green_proxy_port)"
  BLUE_GREEN_OLD_PORT="$BLUE_GREEN_ACTIVE_PORT"
  BLUE_GREEN_OLD_CONTAINER="$BLUE_GREEN_ACTIVE_CONTAINER"
  BLUE_GREEN_NEW_PORT="$next_port"
  BLUE_GREEN_NEW_CONTAINER="$next_container"

  if [[ "$active_port" == "$next_port" ]]; then
    if wait_for_blue_green_container "$next_container" "$next_image" 1 &&
      (blue_green_verify_live_route "$next_port" "$next_container" "$next_image"); then
      # The proxy file can say "next" while the old OpenResty workers are
      # still serving the old config. Reload and compare the live status route
      # before allowing the old container to be drained.
      blue_green_commit_active_slot \
        "$next_port" "$next_container" "$next_image" \
        "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$BLUE_GREEN_ACTIVE_IMAGE" ||
        commit_status=$?
      if [[ "$commit_status" -eq 2 ]]; then
        log "pending blue-green recovery committed; old-slot cleanup remains pending"
      elif [[ "$commit_status" -ne 0 ]]; then
        fail "pending blue-green cleanup could not be completed"
      fi
      rm -rf -- "$BLUE_GREEN_PROXY_BACKUP_DIR" 2>/dev/null || true
      BLUE_GREEN_PROXY_BACKUP_DIR=''
      read_blue_green_state
      return 0
    fi
    log "WARNING: pending blue-green standby or live route is unhealthy; restoring old proxy slot"
    switch_blue_green_proxy "$BLUE_GREEN_OLD_PORT"
    blue_green_verify_live_route \
      "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$BLUE_GREEN_ACTIVE_IMAGE"
    rm -rf -- "$BLUE_GREEN_PROXY_BACKUP_DIR" 2>/dev/null || true
    BLUE_GREEN_PROXY_BACKUP_DIR=''
  elif [[ "$active_port" == "$BLUE_GREEN_OLD_PORT" ]]; then
    blue_green_verify_live_route \
      "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$BLUE_GREEN_ACTIVE_IMAGE"
  else
    fail "pending blue-green state does not match the active OpenResty port"
  fi

	blue_green_remove_container "$next_container" ||
		fail "pending blue-green standby could not be removed"
  write_release_image "$BLUE_GREEN_ACTIVE_IMAGE"
  if [[ "$BLUE_GREEN_STATE_BASELINE_PRESENT" -eq 1 ]]; then
    write_blue_green_state "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$BLUE_GREEN_ACTIVE_IMAGE"
  else
    rm -f -- "$BLUE_GREEN_STATE_FILE"
  fi
  wait_for_ready "$BLUE_GREEN_ACTIVE_IMAGE" ||
    fail "old New API slot was not healthy during pending blue-green recovery"
  read_blue_green_state
}

wait_for_blue_green_container() {
  local container="$1" expected_image="$2" fail_fast="${3:-0}" attempt runtime state health body
  for attempt in $(seq 1 $((READY_TIMEOUT_SECONDS / 3))); do
    runtime="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
      --format '{{.Config.Image}}' "$container" 2>/dev/null || true)"
    state="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
      --format '{{.State.Status}}' "$container" 2>/dev/null || true)"
    health="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
      --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container" 2>/dev/null || true)"
    if [[ "$runtime" != "$expected_image" ]]; then
      [[ "$fail_fast" -eq 0 ]] || return 1
      sleep 3
      continue
    fi
    if [[ "$state" == running && "$health" == healthy ]]; then
      return 0
    fi
    if [[ "$state" == running && "$health" == none ]]; then
      body="$(run_timed "$STATUS_TIMEOUT_SECONDS" docker exec "$container" \
        wget -q -T 10 -O - http://127.0.0.1:3000/api/status 2>/dev/null || true)"
      if grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<< "$body"; then
        return 0
      fi
      [[ "$fail_fast" -eq 0 ]] || return 1
    fi
    if [[ "$health" == unhealthy || "$state" == dead || "$state" == exited ]]; then
      return 1
    fi
    [[ "$fail_fast" -eq 0 ]] || return 1
    sleep 3
  done
  return 1
}

start_blue_green_standby() {
	local image="$1" published_port="$BLUE_GREEN_NEW_PORT"
	blue_green_remove_container "$BLUE_GREEN_NEW_CONTAINER" ||
		fail "could not clear the inactive blue-green container"
  BLUE_GREEN_NEW_INSTANCE_ID="${BLUE_GREEN_NEW_CONTAINER}-$(date +%s%N)-$RANDOM"
  log "starting standby container=$BLUE_GREEN_NEW_CONTAINER port=$published_port"
  compose_timed "$COMPOSE_UP_TIMEOUT_SECONDS" run -d --no-deps \
    --name "$BLUE_GREEN_NEW_CONTAINER" \
    -e "TOKENESS_INSTANCE_ID=$BLUE_GREEN_NEW_INSTANCE_ID" \
    -p "127.0.0.1:$published_port:3000" "$SERVICE_NAME" >/dev/null
  wait_for_blue_green_container "$BLUE_GREEN_NEW_CONTAINER" "$image" ||
    fail "standby container did not become ready"
  run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker update \
    --restart always "$BLUE_GREEN_NEW_CONTAINER" >/dev/null ||
    fail "could not enable restart policy on standby container"
}

blue_green_active_connection_count() {
	local container="$1" pid file count current readable=0
	pid="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
		--format '{{.State.Pid}}' "$container" 2>/dev/null)" || return 1
	[[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
	count=0
	for file in "$BLUE_GREEN_PROC_ROOT/$pid/net/tcp" "$BLUE_GREEN_PROC_ROOT/$pid/net/tcp6"; do
		[[ -r "$file" ]] || continue
		readable=1
		current="$(awk '
			NR > 1 {
				split($2, endpoint, ":")
				if (toupper(endpoint[2]) == "0BB8" && $4 == "01") count++
			}
			END { print count + 0 }
		' "$file")" || return 1
		count=$((count + current))
	done
	((readable == 1)) || return 1
	printf '%s\n' "$count"
}

wait_for_blue_green_connections_to_drain() {
	local port="$1" container="$2" deadline count waiting_logged=0
	deadline=$((SECONDS + BLUE_GREEN_DRAIN_TIMEOUT_SECONDS))
	while ((SECONDS < deadline)); do
		count="$(blue_green_active_connection_count "$container")" || {
			log "WARNING: could not inspect active connections in old container $container"
			return 1
		}
		if ((count == 0)); then
			return 0
		fi
		if ((waiting_logged == 0)); then
			log "waiting for $count active connection(s) on old port $port to drain"
			waiting_logged=1
		fi
		sleep 2
	done
	log "WARNING: active connections on old port $port did not drain within ${BLUE_GREEN_DRAIN_TIMEOUT_SECONDS}s"
	return 1
}

drain_blue_green_container() {
	local port="$1" container="$2" active_container="${3:-$BLUE_GREEN_NEW_CONTAINER}" exists_status state
	if [[ -n "$active_container" && "$container" == "$active_container" ]]; then
		log "ERROR: refusing to drain the active blue-green container $container"
		return 1
	fi
	if blue_green_container_exists "$container"; then
		:
	else
		exists_status=$?
		[[ "$exists_status" -eq 1 ]] && return 0
		return 1
	fi
	state="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
		--format '{{.State.Status}}' "$container" 2>/dev/null)" || return 1
	if [[ "$state" == exited || "$state" == dead || "$state" == created ]]; then
		blue_green_remove_container "$container"
		return
	fi
	[[ "$state" == running ]] || {
		log "WARNING: old blue-green container has unexpected state $state: $container"
		return 1
	}
	# OpenResty stops creating connections to the old slot after reload. Wait for
	# existing HTTP/SSE connections to close before signaling the application.
	wait_for_blue_green_connections_to_drain "$port" "$container" || return 1
	run_timed "$BLUE_GREEN_DRAIN_TIMEOUT_SECONDS" docker stop \
    --time "$BLUE_GREEN_DRAIN_TIMEOUT_SECONDS" "$container" >/dev/null 2>&1 ||
    log "WARNING: graceful stop timed out for old container $container"
	blue_green_remove_container "$container"
}

status_json() {
  local id="$1"
  run_timed "$STATUS_TIMEOUT_SECONDS" docker exec "$id" \
    wget -q -T 10 -O - http://127.0.0.1:3000/api/status 2>/dev/null || true
}

verify_epay_reconciliation_sidecar() {
  local new_api_id="$1" sidecar_id image user state health ip read_only port_bindings cap_drop security_opt probe
  [[ "$SERVICE_NAME" == 'new-api' ]] || return 0

  sidecar_id="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{.Id}}' "$EPAY_RECONCILIATION_CONTAINER" 2>/dev/null || true)"
  [[ -n "$sidecar_id" ]] || fail "EPay reconciliation sidecar is not running"

  image="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{.Config.Image}}' "$sidecar_id")"
  user="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{.Config.User}}' "$sidecar_id")"
  state="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{.State.Status}}' "$sidecar_id")"
  health="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$sidecar_id")"
  ip="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{with index .NetworkSettings.Networks "1panel-network"}}{{.IPAddress}}{{end}}' "$sidecar_id")"
  read_only="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{.HostConfig.ReadonlyRootfs}}' "$sidecar_id")"
  port_bindings="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{json .HostConfig.PortBindings}}' "$sidecar_id")"
  cap_drop="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{range .HostConfig.CapDrop}}{{println .}}{{end}}' "$sidecar_id")"
  security_opt="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{range .HostConfig.SecurityOpt}}{{println .}}{{end}}' "$sidecar_id")"

  [[ "$image" == "$EPAY_RECONCILIATION_IMAGE" ]] || fail "EPay reconciliation sidecar image is not the reviewed digest"
  [[ "$user" == '65534:65534' ]] || fail "EPay reconciliation sidecar does not run as the restricted user"
  [[ "$state" == 'running' ]] || fail "EPay reconciliation sidecar state is $state"
  [[ "$health" == 'healthy' ]] || fail "EPay reconciliation sidecar health is $health"
  [[ "$ip" == "$EPAY_RECONCILIATION_IP" ]] || fail "EPay reconciliation sidecar private address is invalid"
  [[ "$read_only" == 'true' ]] || fail "EPay reconciliation sidecar root filesystem is writable"
  [[ "$port_bindings" == '{}' || "$port_bindings" == 'null' ]] ||
    fail "EPay reconciliation sidecar publishes host ports"
  grep -qx 'ALL' <<< "$cap_drop" || fail "EPay reconciliation sidecar does not drop all capabilities"
  grep -qx 'no-new-privileges:true' <<< "$security_opt" ||
    fail "EPay reconciliation sidecar permits privilege escalation"
  run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{range .Config.Env}}{{println .}}{{end}}' "$new_api_id" |
    grep -qx "EPAY_RECONCILIATION_QUERY_URL=$EPAY_RECONCILIATION_URL" ||
    fail "New API does not select the private EPay reconciliation origin"

  probe="$(run_timed "$STATUS_TIMEOUT_SECONDS" docker exec "$new_api_id" \
    wget -q -T 5 -O - \
    "$EPAY_RECONCILIATION_URL?act=order&key=healthcheck&out_trade_no=healthcheck&pid=0" \
    2>/dev/null || true)"
  grep -Eq '"code"[[:space:]]*:[[:space:]]*-3' <<< "$probe" ||
    fail "New API cannot complete the private EPay reconciliation health probe"
}

wait_for_ready() {
  local expected_image="${1:-}" deadline id runtime state health body
  deadline=$((SECONDS + READY_TIMEOUT_SECONDS))
  while ((SECONDS < deadline)); do
    id="$(container_id 2>/dev/null || true)"
    if [[ -n "$id" ]]; then
      runtime="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
        --format '{{.Config.Image}}' "$id" 2>/dev/null || true)"
      state="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
        --format '{{.State.Status}}' "$id" 2>/dev/null || true)"
      health="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
        --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id" 2>/dev/null || true)"
      if [[ -n "$expected_image" && "$runtime" != "$expected_image" ]]; then
        sleep 3
        continue
      fi
      if [[ "$state" == "running" && "$health" == "healthy" ]]; then
        return 0
      fi
      if [[ "$state" == "running" && "$health" == "none" ]]; then
        body="$(status_json "$id")"
        if grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<< "$body"; then
          return 0
        fi
      fi
      if [[ "$state" == "dead" || "$state" == "exited" ]]; then
        return 1
      fi
    fi
    sleep 3
  done
  return 1
}

emit_result() {
  local expected_image="${1:-}" selected id runtime state health started body version
  selected="$(read_release_image)"
  id="$(container_id)"
  [[ -n "$selected" ]] || fail "release.env does not select an image"
  [[ -n "$id" ]] || fail "New API container is not running"
  runtime="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect --format '{{.Config.Image}}' "$id")"
  state="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect --format '{{.State.Status}}' "$id")"
  health="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id")"
  started="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect --format '{{.State.StartedAt}}' "$id")"
  body="$(status_json "$id")"
  version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' <<< "$body")"

  is_trusted_image "$selected" || fail "release.env does not select a trusted immutable image"
  [[ "$runtime" == "$selected" ]] || fail "runtime image does not match release.env"
  [[ -z "$expected_image" || "$selected" == "$expected_image" ]] ||
    fail "deployment result does not match the requested image"
  [[ "$state" == "running" ]] || fail "container state is $state"
  if [[ "$health" == "none" ]]; then
    grep -Eq '"success"[[:space:]]*:[[:space:]]*true' <<< "$body" ||
      fail "container has no healthcheck and the status endpoint is not healthy"
  elif [[ "$health" != "healthy" ]]; then
    fail "container health is $health"
  fi
  [[ -n "$version" ]] || fail "could not read New API version"
  verify_epay_reconciliation_sidecar "$id"

  printf 'TOKENESS_RESULT\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$selected" "$runtime" "$state" "$health" "$started" "$version" \
    "$TOKENESS_DEPLOY_COMMAND_VERSION"
}

verify_release() {
  local selected
  selected="$(read_release_image)"
  [[ -n "$selected" ]] || fail "release.env does not select an image"
  is_trusted_image "$selected" || fail "release.env does not select a trusted immutable image"
	wait_for_ready "$selected" || fail "New API did not become ready on the selected image"
	if [[ "$BLUE_GREEN_MODE" -eq 1 ]]; then
		blue_green_verify_selected_route "$selected"
	fi
  emit_result "$selected"
}

restore_blue_green() {
  local selected id had_switched="$BLUE_GREEN_SWITCHED"
  if [[ "$had_switched" -eq 1 ]]; then
    if ! blue_green_restore_proxy_backup; then
      log "ERROR: blue-green proxy rollback failed"
      return 1
    fi
    BLUE_GREEN_SWITCHED=0
    rm -rf -- "$BLUE_GREEN_PROXY_BACKUP_DIR"
    BLUE_GREEN_PROXY_BACKUP_DIR=''
  fi

	if [[ -n "$BLUE_GREEN_NEW_CONTAINER" ]]; then
		blue_green_remove_container "$BLUE_GREEN_NEW_CONTAINER" || return 1
	fi
  write_release_image "$PREVIOUS_IMAGE" || return 1
  if [[ ("$had_switched" -eq 1 || "$BLUE_GREEN_STATE_BASELINE_PRESENT" -eq 1) &&
    -n "$BLUE_GREEN_OLD_CONTAINER" ]]; then
    write_blue_green_state "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$PREVIOUS_IMAGE" || return 1
  elif [[ "$had_switched" -eq 0 && "$BLUE_GREEN_STATE_BASELINE_PRESENT" -eq 0 ]]; then
    rm -f -- "$BLUE_GREEN_STATE_FILE"
  fi
  wait_for_ready "$PREVIOUS_IMAGE" || return 1

  selected="$(read_release_image)"
  id="$(container_id 2>/dev/null || true)"
  [[ -n "$id" ]] || return 1
  [[ "$selected" == "$PREVIOUS_IMAGE" ]] || return 1
  [[ "$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{.Config.Image}}' "$id" 2>/dev/null || true)" == "$PREVIOUS_IMAGE" ]]
}

deploy_blue_green() {
  local target="$1" commit_status=0
  blue_green_standby_details
  ensure_blue_green_proxy "$BLUE_GREEN_OLD_PORT"
  start_blue_green_standby "$target"
  verify_epay_reconciliation_sidecar "$BLUE_GREEN_NEW_CONTAINER"
  if [[ "$BLUE_GREEN_STATE_PHASE" != 'pending' ]]; then
    write_blue_green_pending_state \
      "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$PREVIOUS_IMAGE" \
      "$BLUE_GREEN_STATE_BASELINE_PRESENT" \
      "$BLUE_GREEN_NEW_PORT" "$BLUE_GREEN_NEW_CONTAINER" "$target"
  fi

  log "switching OpenResty backend to port $BLUE_GREEN_NEW_PORT"
  switch_blue_green_proxy "$BLUE_GREEN_NEW_PORT"
  blue_green_verify_live_route \
    "$BLUE_GREEN_NEW_PORT" "$BLUE_GREEN_NEW_CONTAINER" "$target"
  blue_green_commit_active_slot \
    "$BLUE_GREEN_NEW_PORT" "$BLUE_GREEN_NEW_CONTAINER" "$target" \
    "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$PREVIOUS_IMAGE" || commit_status=$?
	if [[ "$commit_status" -eq 2 ]]; then
		log "blue-green deployment committed; old-slot cleanup remains pending"
	elif [[ "$commit_status" -ne 0 ]]; then
		fail "blue-green deployment could not persist the active slot"
	fi
  emit_result "$target"

  rm -rf -- "$BLUE_GREEN_PROXY_BACKUP_DIR" ||
    log "WARNING: could not remove OpenResty proxy backup directory"
  BLUE_GREEN_PROXY_BACKUP_DIR=''
}

restore_previous_image() {
  local up_status=0 selected id runtime

  if [[ "$BLUE_GREEN_MODE" -eq 1 ]]; then
    restore_blue_green || return 1
    return 0
  fi

  write_release_image "$PREVIOUS_IMAGE" || return 1
  compose_timed "$COMPOSE_UP_TIMEOUT_SECONDS" up -d --no-deps --force-recreate "$SERVICE_NAME" || up_status=$?
  if [[ "$up_status" -ne 0 ]]; then
    log "ERROR: rollback compose up exited with status $up_status; verifying runtime state"
  fi
  wait_for_ready "$PREVIOUS_IMAGE" || return 1

  selected="$(read_release_image)"
  id="$(container_id 2>/dev/null || true)"
  [[ -n "$id" ]] || return 1
  runtime="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
    --format '{{.Config.Image}}' "$id" 2>/dev/null || true)"
  [[ "$selected" == "$PREVIOUS_IMAGE" && "$runtime" == "$PREVIOUS_IMAGE" ]]
}

rollback_interrupted_deploy() {
  local exit_code=$?
  trap - EXIT HUP INT TERM
  set +e
  if [[ "$DEPLOY_IN_PROGRESS" -eq 1 && -n "$PREVIOUS_IMAGE" ]]; then
    [[ "$exit_code" -ne 0 ]] || exit_code=1
    log "deployment did not commit; restoring previous image"
    if restore_previous_image; then
      log "previous image restored and verified"
    else
      log "ERROR: deployment rollback did not restore the previous runtime image"
    fi
    DEPLOY_IN_PROGRESS=0
  fi
  exit "$exit_code"
}

deploy_release() {
  local target="$1" previous id runtime
  is_trusted_image "$target" || fail "refusing mutable or untrusted image reference"
  if [[ "$BLUE_GREEN_MODE" -eq 1 && "$BLUE_GREEN_STATE_PHASE" == 'cleanup-pending' ]]; then
    fail "previous blue-green slot cleanup is still pending; refusing a new deployment"
  fi

  previous="$(read_release_image)"
  [[ -n "$previous" ]] || fail "release.env does not select an image"
  is_trusted_image "$previous" || fail "release.env does not select a trusted immutable image"

	if [[ "$previous" == "$target" ]] && wait_for_ready "$target"; then
		id="$(container_id)"
    runtime="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
      --format '{{.Config.Image}}' "$id" 2>/dev/null || true)"
		if [[ "$runtime" == "$target" ]]; then
			if [[ "$BLUE_GREEN_MODE" -eq 1 ]]; then
				blue_green_verify_selected_route "$target"
			fi
      log "target image is already selected and healthy"
      emit_result "$target"
      return 0
    fi
    log "release.env selects the target but the runtime differs; recreating"
  fi

  PREVIOUS_IMAGE="$previous"
  DEPLOY_IN_PROGRESS=1
  if [[ "$BLUE_GREEN_MODE" -eq 1 ]]; then
    # Persist the complete recovery target before changing release.env. This
    # closes the SIGKILL window between selecting the image and starting the
    # standby container.
    blue_green_standby_details
    write_blue_green_pending_state \
      "$BLUE_GREEN_OLD_PORT" "$BLUE_GREEN_OLD_CONTAINER" "$PREVIOUS_IMAGE" \
      "$BLUE_GREEN_STATE_BASELINE_PRESENT" \
      "$BLUE_GREEN_NEW_PORT" "$BLUE_GREEN_NEW_CONTAINER" "$target"
  fi
  write_release_image "$target"
  if image_available_locally "$target"; then
    log "reviewed image digest is already available locally"
  else
    log "pulling reviewed image digest"
    compose_timed "$COMPOSE_PULL_TIMEOUT_SECONDS" pull "$SERVICE_NAME" || fail "image pull failed or timed out"
  fi

  if [[ "$BLUE_GREEN_MODE" -eq 1 ]]; then
    deploy_blue_green "$target"
    return 0
  fi

  log "starting $SERVICE_NAME"
  compose_timed "$COMPOSE_UP_TIMEOUT_SECONDS" up -d --no-deps --force-recreate "$SERVICE_NAME" ||
    fail "compose up failed or timed out"
  wait_for_ready "$target" || fail "target image did not become ready"

  emit_result "$target"
  DEPLOY_IN_PROGRESS=0
}

initialize_runtime() {
  command -v timeout >/dev/null 2>&1 || fail "timeout is required"
  DEPLOY_DIR="$(detect_deploy_dir)"
  RELEASE_ENV="$DEPLOY_DIR/release.env"
  BASE_COMPOSE_FILE="$DEPLOY_DIR/docker-compose.yml"
  OVERRIDE_COMPOSE_FILE="$DEPLOY_DIR/docker-compose.tokeness.yml"

  if run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker compose version >/dev/null 2>&1; then
    COMPOSE=(docker compose)
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE=(docker-compose)
  else
    fail "Docker Compose is not installed"
  fi

  COMPOSE_ARGS=(--env-file "$RELEASE_ENV" -f "$BASE_COMPOSE_FILE")
  if [[ -f "$OVERRIDE_COMPOSE_FILE" ]]; then
    COMPOSE_ARGS+=(-f "$OVERRIDE_COMPOSE_FILE")
  fi

  if compose_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" config --services | grep -qx 'new-api'; then
    SERVICE_NAME='new-api'
  elif compose_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" config --services | grep -qx 'new-api-slave'; then
    SERVICE_NAME='new-api-slave'
  else
    fail "compose project does not define new-api or new-api-slave"
  fi

	if [[ "$SERVICE_NAME" == 'new-api' ]]; then
		BLUE_GREEN_MODE=1
		validate_blue_green_configuration
		BLUE_GREEN_STATE_FILE="$DEPLOY_DIR/$BLUE_GREEN_STATE_BASENAME"
	fi

  install -d -m 0755 /run/lock
  exec 9>/run/lock/tokeness-new-api-deploy.lock
  flock -n 9 || fail "another New API deployment is running"

  if [[ "$BLUE_GREEN_MODE" -eq 1 ]]; then
    read_blue_green_state
    recover_blue_green_cleanup_pending_state
    read_blue_green_state
    recover_blue_green_pending_state
    read_blue_green_state
  fi
}

main() {
  local -a command_parts
  if [[ "${1:-}" == '--version' && "$#" -eq 1 ]]; then
    printf '%s\n' "$TOKENESS_DEPLOY_COMMAND_VERSION"
    return 0
  fi
  [[ "$#" -eq 0 ]] || fail "direct arguments are not accepted"
  initialize_runtime
  trap rollback_interrupted_deploy EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  read -r -a command_parts <<< "${SSH_ORIGINAL_COMMAND:-verify}"
  case "${command_parts[0]:-}" in
    verify)
      [[ "${#command_parts[@]}" -eq 1 ]] || fail "verify does not accept arguments"
      verify_release
      ;;
    deploy)
      [[ "${#command_parts[@]}" -eq 2 ]] || fail "deploy requires exactly one image digest"
      deploy_release "${command_parts[1]}"
      ;;
    *)
      fail "allowed commands: verify, deploy <trusted-image-digest>"
      ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
