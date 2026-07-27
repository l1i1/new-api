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

readonly DEPLOY_DIR="$(detect_deploy_dir)"
readonly RELEASE_ENV="$DEPLOY_DIR/release.env"
readonly BASE_COMPOSE_FILE="$DEPLOY_DIR/docker-compose.yml"
readonly OVERRIDE_COMPOSE_FILE="$DEPLOY_DIR/docker-compose.tokeness.yml"
DEPLOY_IN_PROGRESS=0
PREVIOUS_IMAGE=''

if docker compose version >/dev/null 2>&1; then
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

compose() {
  "${COMPOSE[@]}" "${COMPOSE_ARGS[@]}" "$@"
}

if compose config --services | grep -qx 'new-api'; then
  readonly SERVICE_NAME="new-api"
elif compose config --services | grep -qx 'new-api-slave'; then
  readonly SERVICE_NAME="new-api-slave"
else
  fail "compose project does not define new-api or new-api-slave"
fi

install -d -m 0755 /run/lock
exec 9>/run/lock/tokeness-new-api-deploy.lock
flock -n 9 || fail "another New API deployment is running"

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

rollback_interrupted_deploy() {
  local exit_code=$?
  trap - EXIT HUP INT TERM
  if [[ "$DEPLOY_IN_PROGRESS" -eq 1 && -n "$PREVIOUS_IMAGE" ]]; then
    log "deployment interrupted; restoring previous image"
    write_release_image "$PREVIOUS_IMAGE" || true
    compose up -d --no-deps --force-recreate "$SERVICE_NAME" || true
    if wait_for_ready; then
      log "previous image restored and healthy"
    else
      log "ERROR: interrupted deployment rollback requires manual recovery"
    fi
  fi
  exit "$exit_code"
}

trap rollback_interrupted_deploy EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

container_id() {
  compose ps -q "$SERVICE_NAME" | head -n 1
}

status_json() {
  local id="$1"
  docker exec "$id" wget -q -O - http://127.0.0.1:3000/api/status 2>/dev/null || true
}

wait_for_ready() {
  local attempt id state health body
  for attempt in {1..40}; do
    id="$(container_id)"
    if [[ -n "$id" ]]; then
      state="$(docker inspect --format '{{.State.Status}}' "$id" 2>/dev/null || true)"
      health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id" 2>/dev/null || true)"
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
  local selected id runtime state health started body version
  selected="$(read_release_image)"
  id="$(container_id)"
  [[ -n "$selected" ]] || fail "release.env does not select an image"
  [[ -n "$id" ]] || fail "New API container is not running"
  runtime="$(docker inspect --format '{{.Config.Image}}' "$id")"
  state="$(docker inspect --format '{{.State.Status}}' "$id")"
  health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id")"
  started="$(docker inspect --format '{{.State.StartedAt}}' "$id")"
  body="$(status_json "$id")"
  version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' <<< "$body")"

  [[ "$runtime" == "$selected" ]] || fail "runtime image does not match release.env"
  [[ "$state" == "running" ]] || fail "container state is $state"
  [[ -n "$version" ]] || fail "could not read New API version"

  printf 'TOKENESS_RESULT\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$selected" "$runtime" "$state" "$health" "$started" "$version"
}

verify_release() {
  wait_for_ready || fail "New API did not become ready"
  emit_result
}

deploy_release() {
  local target="$1" previous id runtime
  [[ "$target" =~ ^ghcr\.io/l1i1/new-api@sha256:[0-9a-f]{64}$ ]] ||
    fail "refusing mutable or untrusted image reference"

  previous="$(read_release_image)"
  [[ -n "$previous" ]] || fail "release.env does not select an image"

  if [[ "$previous" == "$target" ]] && wait_for_ready; then
    id="$(container_id)"
    runtime="$(docker inspect --format '{{.Config.Image}}' "$id" 2>/dev/null || true)"
    if [[ "$runtime" == "$target" ]]; then
      log "target image is already selected and healthy"
      emit_result
      return 0
    fi
    log "release.env selects the target but the runtime differs; recreating"
  fi

  PREVIOUS_IMAGE="$previous"
  DEPLOY_IN_PROGRESS=1
  log "pulling reviewed image digest"
  write_release_image "$target"
  if ! compose pull "$SERVICE_NAME"; then
    write_release_image "$previous"
    DEPLOY_IN_PROGRESS=0
    fail "pull failed; release selection restored"
  fi

  log "starting $SERVICE_NAME"
  if ! compose up -d --no-deps --force-recreate "$SERVICE_NAME" || ! wait_for_ready; then
    log "deployment failed; rolling back node"
    write_release_image "$previous"
    compose up -d --no-deps --force-recreate "$SERVICE_NAME" || true
    if wait_for_ready; then
      DEPLOY_IN_PROGRESS=0
      fail "deployment failed; previous image restored and healthy"
    fi
    DEPLOY_IN_PROGRESS=0
    fail "deployment and node rollback health checks both failed"
  fi

  DEPLOY_IN_PROGRESS=0
  emit_result
}

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
