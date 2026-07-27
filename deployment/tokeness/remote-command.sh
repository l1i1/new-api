#!/usr/bin/env bash
set -Eeuo pipefail

export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:$PATH"

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
  compose_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" ps -q "$SERVICE_NAME" | head -n 1
}

status_json() {
  local id="$1"
  run_timed "$STATUS_TIMEOUT_SECONDS" docker exec "$id" \
    wget -q -T 10 -O - http://127.0.0.1:3000/api/status 2>/dev/null || true
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

  printf 'TOKENESS_RESULT\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$selected" "$runtime" "$state" "$health" "$started" "$version"
}

verify_release() {
  local selected
  selected="$(read_release_image)"
  [[ -n "$selected" ]] || fail "release.env does not select an image"
  is_trusted_image "$selected" || fail "release.env does not select a trusted immutable image"
  wait_for_ready "$selected" || fail "New API did not become ready on the selected image"
  emit_result "$selected"
}

restore_previous_image() {
  local up_status=0 selected id runtime

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

  previous="$(read_release_image)"
  [[ -n "$previous" ]] || fail "release.env does not select an image"
  is_trusted_image "$previous" || fail "release.env does not select a trusted immutable image"

  if [[ "$previous" == "$target" ]] && wait_for_ready "$target"; then
    id="$(container_id)"
    runtime="$(run_timed "$DOCKER_COMMAND_TIMEOUT_SECONDS" docker inspect \
      --format '{{.Config.Image}}' "$id" 2>/dev/null || true)"
    if [[ "$runtime" == "$target" ]]; then
      log "target image is already selected and healthy"
      emit_result "$target"
      return 0
    fi
    log "release.env selects the target but the runtime differs; recreating"
  fi

  PREVIOUS_IMAGE="$previous"
  DEPLOY_IN_PROGRESS=1
  write_release_image "$target"
  if image_available_locally "$target"; then
    log "reviewed image digest is already available locally"
  else
    log "pulling reviewed image digest"
    compose_timed "$COMPOSE_PULL_TIMEOUT_SECONDS" pull "$SERVICE_NAME" || fail "image pull failed or timed out"
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

  install -d -m 0755 /run/lock
  exec 9>/run/lock/tokeness-new-api-deploy.lock
  flock -n 9 || fail "another New API deployment is running"
}

main() {
  local -a command_parts
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
