#!/usr/bin/env bash
set -Eeuo pipefail
# bootstrap-master-ecs <image-ref> [start|commit|abort|probe] - blue-green
# re-roll of the master container on the backup entry ECS onto <image-ref>,
# preserving everything else about it.
#
# Why blue-green: the master serves the panel from a single container, so a
# plain recreate (rm -f -> run) blacked the panel out for the container's
# start time (seconds) on every release, and nginx's passive failover
# (max_fails=2 fail_timeout=10s) stretched that further. Blue-green keeps the
# serving container ("blue") up while its replacement ("green") starts on the
# OTHER host port, passes a readiness gate, and gets version-checked; only
# then does deploy.sh pin the lightweight hosts' nginx onto the green port
# (via ml-sync's web-primary marker) and come back here to "commit" (retire
# blue). The panel never sees a dead primary: the switch is an nginx upstream
# port change against an already-healthy container, not a container restart.
#
# Port ping-pong: the serving port alternates 3000 <-> 3001 per release. The
# source of truth for "which port serves" on this host is $SERVING_PORT_FILE
# (absent = 3000, the pre-blue-green bootstrap value). deploy.sh pins the same
# port into /etc/ml-sync/web-primary-port on both lightweight hosts and
# ml-sync rewrites their nginx primary from it (health-gated); this host's own
# nginx :80 /v1 last-resort upstream follows in the commit phase.
#
# Why the brief multi-master overlap is acceptable (explicitly authorized
# 2026-09-29): blue and green coexist only for the gate window (minutes).
# Migrations run at container START, so only green ever migrates - two
# migrators never run at once. Blue keeps serving old code against the
# migrated schema, which the release train already requires anyway (relay
# slaves on the previous image run against the migrated DB through every
# master-first rollout). System tasks fire on both masters during the
# overlap - duplicate bookkeeping for minutes, bounded, deploy-time only.
#
# Why it carries no secrets and needs no cloud credential: the master's whole
# environment (SQL_DSN, REDIS_CONN_STRING, CRYPTO_SECRET, SESSION_SECRET, ...)
# is read off the serving container and copied verbatim into its replacement.
# Nothing secret is transported by the deploy pipeline, stored in the
# repository, or read from the ESS API - so the ECS still needs no Aliyun AK
# (removed deliberately on 2026-09-29). The CNB registry at docker.cnb.cool
# serves this image anonymously, so no registry credential is needed either.
#
# The previous image stays in the local docker store: rollback is "rerun this
# script with the previous digest", not a re-pull.
#
# Phases (idempotent; deploy.sh orchestrates):
#   start   create/adopt green on the free port, pull, readiness-gate it.
#           Blue is NEVER touched. Prints SERVING_PORT=<green> and
#           PREVIOUS_PORT=<blue> as the last two stdout lines.
#   commit  point this host's marker + its nginx :80 upstream at green, give
#           blue a short quiet window, stop+remove blue, rename green to the
#           canonical name. Prints SERVING_PORT=<green>.
#   abort   reconcile after a failed start/gate/pin/commit: if blue is still
#           alive, restore the :80 upstream to blue (when flipped) and remove
#           green; if blue is already gone (commit got that far), finish the
#           commit instead (a dead blue must never be pointed at). Prints
#           SERVING_PORT=<port actually serving afterwards>.
#   probe   report state, change nothing: SERVING_PORT / BLUE_OK / GREEN_PORT
#           / GREEN_OK lines.
#
# Exit codes: 2 usage, 3 docker/definition failure, 4 image pull failure,
#             5 readiness gate failure (green removed; blue untouched).

: "${1:?usage: bootstrap-master-ecs.sh <image-ref> [start|commit|abort|probe]}"
IMAGE_REF="$1"
PHASE="${2:-start}"
case "$PHASE" in start|commit|abort|probe) ;; *)
  echo "bootstrap-master-ecs: unknown phase '$PHASE'" >&2; exit 2 ;;
esac
case "$IMAGE_REF" in
  *@sha256:[0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) ;;
  *) echo "bootstrap-master-ecs: image ref must be pinned by @sha256:<digest>, got '$IMAGE_REF'" >&2; exit 2 ;;
esac

CONTAINER="${MASTER_CONTAINER:-new-api-master}"
GREEN="${MASTER_GREEN_NAME:-new-api-master--green}"
SERVE_IP="${MASTER_SERVE_IP:-10.1.0.43}"
PORT_A="${MASTER_PORT_A:-3000}"
PORT_B="${MASTER_PORT_B:-3001}"
SERVING_PORT_FILE="${MASTER_SERVING_PORT_FILE:-/etc/tokeness-cn/master-serving-port}"
READY_TIMEOUT="${MASTER_READY_TIMEOUT:-180}"
# In-flight panel requests on blue are short (no SSE on the panel tier), and
# docker stop's SIGTERM lets the app drain; the quiet window covers keepalive
# connections that nginx on the lightweight hosts has already moved off.
QUIET_SECONDS="${MASTER_QUIET_SECONDS:-15}"
STOP_TIMEOUT="${MASTER_STOP_TIMEOUT:-60}"
# This host's own nginx terminates :80 (the /v1 last resort) and proxies to
# the master container. Its upstream port must follow the serving one. The
# file is a symlink into sites-available, so the edit must follow it rather
# than replace it (plain `sed -i` would turn sites-enabled into a regular file).
# Two upstreams in it point at the master: newapi_ml (the /v1 last resort) and
# newapi_web (this host's own panel route) - both follow the port.
ECS_NGINX_CONF="${ECS_NGINX_CONF:-/etc/nginx/sites-enabled/tokeness-ml.conf}"
ECS_NGINX_HOST_HEADER="${ECS_NGINX_HOST_HEADER:-tokeness.cn}"

log() { printf 'bootstrap-master-ecs: %s\n' "$*"; }
err() { printf 'bootstrap-master-ecs: %s\n' "$*" >&2; }

command -v docker >/dev/null 2>&1 || { err "docker not found"; exit 3; }

serving_port() { # -> the port currently serving (marker wins, else PORT_A)
  local p=""
  if [ -r "$SERVING_PORT_FILE" ]; then
    p="$(tr -d '[:space:]' < "$SERVING_PORT_FILE" 2>/dev/null || true)"
  fi
  case "$p" in "$PORT_A"|"$PORT_B") printf '%s\n' "$p" ;; *) printf '%s\n' "$PORT_A" ;; esac
}

other_port() { # <port> -> the other port of the pair
  case "$1" in "$PORT_A") printf '%s\n' "$PORT_B" ;; "$PORT_B") printf '%s\n' "$PORT_A" ;; *)
    err "port '$1' is outside the managed pair $PORT_A/$PORT_B"; return 3 ;;
  esac
}

container_on_port() { # <port> -> name of the RUNNING container publishing SERVE_IP:port
  docker ps --format '{{.Names}} {{.Ports}}' \
    | awk -v needle="$SERVE_IP:$1->" '$0 ~ needle {print $1; exit}'
}

port_ready() { # <port> - 1s-granularity readiness gate on the app port
  local port="$1" i
  for ((i = 0; i < READY_TIMEOUT; i++)); do
    if curl -fsS --max-time 3 "http://$SERVE_IP:$port/health/ready" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  return 1
}

write_serving_marker() { # <port> - atomic (tmp + rename): ml-sync/deploy never read a partial line
  local tmp
  install -d -m 0755 "$(dirname "$SERVING_PORT_FILE")"
  tmp="$(mktemp "${SERVING_PORT_FILE}.tmp.XXXXXX")"
  printf '%s\n' "$1" > "$tmp"
  chmod 0644 "$tmp"
  mv -f -- "$tmp" "$SERVING_PORT_FILE"
}

# ecs_nginx_point_at <port> - move this host's :80 last-resort upstream to the
# given master port. Verified (nginx -t + reload + Host-pinned probe) and
# reverted on failure, exactly like ml-sync treats the lightweight hosts.
ecs_nginx_point_at() {
  local port="$1" old_port="$2" bak
  [ -f "$ECS_NGINX_CONF" ] || { err "ECS nginx conf not found at $ECS_NGINX_CONF"; return 1; }
  if ! grep -q "$SERVE_IP:$old_port" "$ECS_NGINX_CONF"; then
    if grep -q "$SERVE_IP:$port" "$ECS_NGINX_CONF"; then
      log "ECS nginx already points at :$port (idempotent)"
      return 0
    fi
    err "ECS nginx conf references neither :$old_port nor :$port; refusing to guess"
    return 1
  fi
  bak="$ECS_NGINX_CONF.bg-bak-$(date +%s)"
  cp -a "$ECS_NGINX_CONF" "$bak"
  # Only upstream/proxy lines are rewritten; a comment mentioning the port is
  # cosmetic and harmless, but the substitution is anchored to the serving
  # address to keep the blast radius at exactly the master pointers.
  sed -i --follow-symlinks "s/${SERVE_IP//./\\.}:${old_port}\b/${SERVE_IP}:${port}/g" "$ECS_NGINX_CONF"
  if ! nginx -t >/dev/null 2>&1; then
    err "nginx -t failed after :80 upstream flip; restoring $bak"
    cp -a "$bak" "$ECS_NGINX_CONF"
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    return 1
  fi
  systemctl reload nginx || { err "nginx reload failed; restoring $bak"; cp -a "$bak" "$ECS_NGINX_CONF"; systemctl reload nginx >/dev/null 2>&1 || true; return 1; }
  if ! curl -fsS --max-time 5 -H "Host: $ECS_NGINX_HOST_HEADER" "http://$SERVE_IP:80/health/ready" >/dev/null 2>&1; then
    err ":80 post-flip probe failed; restoring $bak"
    cp -a "$bak" "$ECS_NGINX_CONF"
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    return 1
  fi
  log "ECS nginx :80 last-resort now upstreams $SERVE_IP:$port (probe 200 via :80)"
}

# ---- phase: probe ----------------------------------------------------------
if [ "$PHASE" = probe ]; then
  sp="$(serving_port)"; gp="$(other_port "$sp")" || exit 3
  blue="$(container_on_port "$sp")"
  green="$(container_on_port "$gp")"
  printf 'SERVING_PORT=%s\n' "$sp"
  printf 'BLUE_OK=%s\n' "$([ -n "$blue" ] && echo 1 || echo 0)"
  printf 'GREEN_PORT=%s\n' "$gp"
  printf 'GREEN_OK=%s\n' "$([ -n "$green" ] && curl -fsS --max-time 3 "http://$SERVE_IP:$gp/health/ready" >/dev/null 2>&1 && echo 1 || echo 0)"
  exit 0
fi

sp="$(serving_port)"
gp="$(other_port "$sp")" || exit 3

# ---- phase: abort ----------------------------------------------------------
if [ "$PHASE" = abort ]; then
  blue="$(container_on_port "$sp")"
  if [ -n "$blue" ]; then
    # Pre-commit (or commit failed before blue was retired): blue serves.
    ecs_nginx_point_at "$sp" "$gp" || err "WARN: :80 upstream restore to :$sp failed; check $ECS_NGINX_CONF"
    write_serving_marker "$sp"
    if docker inspect "$GREEN" >/dev/null 2>&1; then
      docker rm -f "$GREEN" >/dev/null 2>&1 || err "WARN: could not remove $GREEN"
    fi
    log "aborted: blue ($blue) still serving on :$sp, green removed"
    printf 'SERVING_PORT=%s\n' "$sp"
    exit 0
  fi
  # Blue is gone: a previous commit passed the point of no return. Finishing
  # it is the only safe move - never point anything at a dead blue.
  green="$(container_on_port "$gp")"
  [ -n "$green" ] || { err "abort found NEITHER blue (:$sp) nor green (:$gp) running; manual intervention required"; exit 3; }
  ecs_nginx_point_at "$gp" "$sp" || { err "could not point :80 at green :$gp"; exit 3; }
  write_serving_marker "$gp"
  if [ "$green" != "$CONTAINER" ]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker rename "$green" "$CONTAINER" >/dev/null
  fi
  log "abort finalized the in-flight commit: green serving on :$gp (blue was already gone)"
  printf 'SERVING_PORT=%s\n' "$gp"
  exit 0
fi

# ---- shared: capture the serving container's definition --------------------
serve_c="$(container_on_port "$sp")"
[ -n "$serve_c" ] || { err "no running container publishes $SERVE_IP:$sp; refusing to invent a master"; exit 3; }
docker inspect "$CONTAINER" >/dev/null 2>&1 || docker rename "$serve_c" "$CONTAINER" >/dev/null 2>&1 || true
docker inspect "$CONTAINER" >/dev/null 2>&1 \
  || { err "container $CONTAINER does not exist; refusing to invent a master"; exit 3; }
old_image="$(docker inspect -f '{{.Config.Image}}' "$CONTAINER")"

# ---- phase: commit ---------------------------------------------------------
if [ "$PHASE" = commit ]; then
  green="$(container_on_port "$gp")"
  [ -n "$green" ] || { err "commit found no green container on :$gp"; exit 3; }
  if ! port_ready "$gp"; then
    err "commit gate: green on :$gp is not ready; refusing to retire blue"
    exit 5
  fi
  ecs_nginx_point_at "$gp" "$sp" || exit 3
  write_serving_marker "$gp"
  log "panel is on green (:$gp); giving blue ${QUIET_SECONDS}s to finish in-flight work"
  sleep "$QUIET_SECONDS"
  docker stop -t "$STOP_TIMEOUT" "$CONTAINER" >/dev/null 2>&1 || true
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  if [ "$green" != "$CONTAINER" ]; then
    docker rename "$green" "$CONTAINER" >/dev/null
  fi
  log "commit done: $CONTAINER serves on :$gp (was :$sp); old image $old_image kept for rollback"
  printf 'SERVING_PORT=%s\n' "$gp"
  exit 0
fi

# ---- phase: start ----------------------------------------------------------
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
  # Blue-green: the replacement publishes the SAME container port on the free
  # host port of the pair. The serving port is never re-used while blue lives.
  ports+=(-p "$host_ip:$gp:$container_port")
done < <(docker inspect -f '{{range $p, $conf := .HostConfig.PortBindings}}{{range $conf}}{{.HostIp}} {{.HostPort}} {{$p}}{{end}}{{end}}' "$CONTAINER")
[ "${#ports[@]}" -eq 2 ] || { err "expected exactly one published port on $CONTAINER, got $(( ${#ports[@]} / 2 )); refusing to guess"; exit 3; }

log_opts=()
while IFS='=' read -r k v; do
  [ -n "$k" ] || continue
  log_opts+=(--log-opt "$k=$v")
done < <(docker inspect -f '{{range $k, $v := .HostConfig.LogConfig.Config}}{{$k}}={{$v}}{{println}}{{end}}' "$CONTAINER")

log "$CONTAINER (serving :$sp) -> green on :$gp with $IMAGE_REF"
log "preserving $(( ${#envs[@]} / 2 )) env vars, restart=$restart, log_driver=$log_driver"

# Adopt a leftover green when it is healthy on the right port with the right
# image (an earlier run died after start); otherwise clear it and start fresh.
if docker inspect "$GREEN" >/dev/null 2>&1; then
  gimg="$(docker inspect -f '{{.Config.Image}}' "$GREEN")"
  if [ "$gimg" = "$IMAGE_REF" ] && [ -n "$(container_on_port "$gp" | grep -x "$GREEN" || true)" ] && curl -fsS --max-time 3 "http://$SERVE_IP:$gp/health/ready" >/dev/null 2>&1; then
    log "adopting existing healthy green ($GREEN on :$gp)"
    printf 'SERVING_PORT=%s\nPREVIOUS_PORT=%s\n' "$gp" "$sp"
    exit 0
  fi
  log "stale green ($GREEN) found; removing before start"
  docker rm -f "$GREEN" >/dev/null 2>&1 || true
fi

if ! docker pull "$IMAGE_REF"; then
  err "docker pull failed for $IMAGE_REF (the serving master is untouched)"
  exit 4
fi

if ! docker run -d --name "$GREEN" --restart "$restart" \
  "${ports[@]}" --log-driver "$log_driver" "${log_opts[@]}" "${envs[@]}" "$IMAGE_REF" >/dev/null; then
  err "could not start the green container; blue keeps serving"
  docker rm -f "$GREEN" >/dev/null 2>&1 || true
  exit 3
fi

if port_ready "$gp"; then
  log "green ready on :$gp (blue still serving on :$sp; nothing was switched)"
  printf 'SERVING_PORT=%s\nPREVIOUS_PORT=%s\n' "$gp" "$sp"
  exit 0
fi

# A green that does not come up is removed; blue was never touched.
err "green did not become ready after ${READY_TIMEOUT}s; removing it (blue untouched)"
docker logs --tail 20 "$GREEN" 2>&1 | sed 's/^/  | /' >&2 || true
docker rm -f "$GREEN" >/dev/null 2>&1 || true
exit 5
