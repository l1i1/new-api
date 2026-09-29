#!/usr/bin/env bash
set -Eeuo pipefail
# bootstrap-master-ecs <image-ref> - re-roll the master container on the backup
# entry ECS onto <image-ref>, preserving everything else about it.
#
# Why this exists: the master (NODE_TYPE=master) serves the panel and is the
# only node that runs DB migrations and the system tasks, so a release must
# update it FIRST and prove it healthy before the ECI relay tier rolls
# ("master-first"). Until 2026-09-29 that master was a container on SWAS-2 and
# this job was done by bootstrap-newapi-host.sh; that container is gone and the
# master now lives here, on the backup ECS.
#
# Why it carries no secrets and needs no cloud credential: the master's whole
# environment (SQL_DSN, REDIS_CONN_STRING, CRYPTO_SECRET, SESSION_SECRET, …) is
# read off the container that is already on this box and copied verbatim into
# its replacement. Nothing secret is transported by the deploy pipeline, stored
# in the repository, or read from the ESS API — so the ECS still needs no
# Aliyun AK (that was deliberately removed on 2026-09-29). The CNB registry at
# docker.cnb.cool serves this image anonymously, so no registry credential is
# needed either.
#
# The previous image is deliberately left in the local docker store: rollback is
# "rerun this script with the previous digest", not a re-pull.
#
# Exit codes: 2 usage, 3 docker/definition failure, 4 image pull failure,
#             5 readiness gate failure (the previous image was restored first).
: "${1:?usage: bootstrap-master-ecs.sh <image-ref>}"
IMAGE_REF="$1"

CONTAINER="${MASTER_CONTAINER:-new-api-master}"
HEALTH_URL="${MASTER_HEALTH_URL:-http://10.1.0.43:3000/health/ready}"
READY_TIMEOUT="${MASTER_READY_TIMEOUT:-90}"

command -v docker >/dev/null 2>&1 || { echo "bootstrap-master-ecs: docker not found" >&2; exit 3; }
docker inspect "$CONTAINER" >/dev/null 2>&1 \
  || { echo "bootstrap-master-ecs: container $CONTAINER does not exist; refusing to invent a master" >&2; exit 3; }
case "$IMAGE_REF" in
  *@sha256:[0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
  *) echo "bootstrap-master-ecs: image ref must be pinned by @sha256:<digest>, got '$IMAGE_REF'" >&2; exit 2 ;;
esac

# ---- capture the existing definition -------------------------------------
old_image="$(docker inspect -f '{{.Config.Image}}' "$CONTAINER")"
restart="$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$CONTAINER")"
log_driver="$(docker inspect -f '{{.HostConfig.LogConfig.Type}}' "$CONTAINER")"

envs=()
while IFS= read -r kv; do
  [ -n "$kv" ] || continue
  # docker injects these itself; re-passing the captured copies would freeze
  # values that belong to the new container.
  case "$kv" in PATH=*|HOSTNAME=*|HOME=*) continue ;; esac
  envs+=(-e "$kv")
done < <(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$CONTAINER")

ports=()
while IFS=' ' read -r host_ip host_port proto_port; do
  [ -n "$host_port" ] || continue
  container_port="${proto_port%%/*}"
  if [ -n "$host_ip" ]; then
    ports+=(-p "$host_ip:$host_port:$container_port")
  else
    ports+=(-p "$host_port:$container_port")
  fi
done < <(docker inspect -f '{{range $p, $conf := .HostConfig.PortBindings}}{{range $conf}}{{.HostIp}} {{.HostPort}} {{$p}}{{end}}{{end}}' "$CONTAINER")

log_opts=()
while IFS='=' read -r k v; do
  [ -n "$k" ] || continue
  log_opts+=(--log-opt "$k=$v")
done < <(docker inspect -f '{{range $k, $v := .HostConfig.LogConfig.Config}}{{$k}}={{$v}}{{println}}{{end}}' "$CONTAINER")

echo "bootstrap-master-ecs: $CONTAINER  $old_image -> $IMAGE_REF"
# ${#envs[@]} and ${#ports[@]} count the flattened run arguments (a flag plus its
# value), so report the item counts instead of the array lengths.
echo "bootstrap-master-ecs: preserving $(( ${#envs[@]} / 2 )) env vars, $(( ${#ports[@]} / 2 )) port binding(s), restart=$restart, log_driver=$log_driver"

create() { # create <image> -> container id
  docker run -d --name "$CONTAINER" --restart "$restart" \
    "${ports[@]}" --log-driver "$log_driver" "${log_opts[@]}" "${envs[@]}" "$1"
}

wait_ready() { # 1 when /health/ready answers 200 while the container stays up
  local i
  for ((i = 0; i < READY_TIMEOUT; i++)); do
    if curl -fsS --max-time 3 "$HEALTH_URL" >/dev/null 2>&1; then return 0; fi
    if [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null)" != "true" ]; then
      echo "bootstrap-master-ecs: container exited while waiting for readiness" >&2
      docker logs --tail 20 "$CONTAINER" 2>&1 | sed 's/^/  | /' >&2 || true
      return 1
    fi
    sleep 1
  done
  echo "bootstrap-master-ecs: not ready after ${READY_TIMEOUT}s" >&2
  docker logs --tail 20 "$CONTAINER" 2>&1 | sed 's/^/  | /' >&2 || true
  return 1
}

# ---- pull, then swap ------------------------------------------------------
if ! docker pull "$IMAGE_REF"; then
  echo "bootstrap-master-ecs: docker pull failed for $IMAGE_REF (the running master is untouched)" >&2
  exit 4
fi

docker rm -f "$CONTAINER" >/dev/null
if ! create "$IMAGE_REF" >/dev/null; then
  echo "bootstrap-master-ecs: could not start the new container; restoring $old_image" >&2
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  create "$old_image" >/dev/null || { echo "bootstrap-master-ecs: restore failed; manual intervention required" >&2; exit 3; }
  wait_ready || true
  exit 3
fi

if wait_ready; then
  echo "bootstrap-master-ecs: master ready on $IMAGE_REF"
  exit 0
fi

# A master that does not come up must not be left in place: the panel and the
# /v1 last-resort both live on this container.
echo "bootstrap-master-ecs: new image did not become ready; rolling back to $old_image" >&2
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
if create "$old_image" >/dev/null && wait_ready; then
  echo "bootstrap-master-ecs: previous image restored and ready" >&2
else
  echo "bootstrap-master-ecs: previous image also failed to become ready; manual intervention required" >&2
fi
exit 5
