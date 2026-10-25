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
# A single nginx primary still leaves one hole: after the switch the
# still-alive blue is no longer in the config, so a green crash inside the
# quiet window leaves :80 with nothing to fall back to (2026-10-19: an abort
# removed the promoted green and the panel answered 30 502s). The transition
# therefore lists blue as an nginx `backup` server until it is retired; nginx
# consults backup servers only after every primary has failed.
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
#   commit  point this host's marker + its nginx :80 upstream at green, keep
#           blue listed as an nginx `backup` server, give blue a short quiet
#           window, drop that backup, stop+remove blue, rename green to the
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
# Default = the entry ECS that has hosted the master since the 2026-10-09
# migration; the host also pins MASTER_SERVE_IP in /etc/environment (pam_env
# applies it to non-interactive ssh commands), so this default is a fallback for
# a host whose environment was lost, not the primary source.
SERVE_IP="${MASTER_SERVE_IP:-10.0.0.249}"
# Targeted request captures are files, and the panel can only read files on the machine it runs on,
# so the master owns them. Without a bind mount they would live in the container and disappear on the
# next release - a debugging tool that silently loses the evidence it was taken for. Both blue and
# green get the same mount, so a roll neither hides nor destroys what was captured.
readonly CAPTURE_HOST_DIR="${CAPTURE_HOST_DIR:-/var/lib/new-api-captures}"
readonly CAPTURE_CONTAINER_DIR="${CAPTURE_CONTAINER_DIR:-/data/request-captures}"
PORT_A="${MASTER_PORT_A:-3000}"
PORT_B="${MASTER_PORT_B:-3001}"
SERVING_PORT_FILE="${MASTER_SERVING_PORT_FILE:-/etc/tokeness-cn/master-serving-port}"
# A commit phase can outlive its SSH session (the 2026-10-19 outage:
# the client was reset at 300s of silence, the remote commit kept
# running and tore down blue after an abort had already removed
# green). The pidfile lets a second phase see a live commit and
# defer instead of racing it.
COMMIT_LOCK="${MASTER_COMMIT_LOCK:-/run/tokeness-cn/master-commit.pid}"
READY_TIMEOUT="${MASTER_READY_TIMEOUT:-180}"
# The master is not a panel-only process: this host's nginx :80 carries the
# /v1 LAST-RESORT upstream, and when the relay tier is degraded (or a rollout
# has parked every relay member) real customer streams can be running on blue.
# A 15s quiet window plus a 60s stop cuts any /v1 stream longer than ~75s, so
# the budgets must cover the same long-stream profile the relay drains for:
# p99 765s, max 2132s. The commit phase below switches the last-resort
# upstream to green BEFORE the quiet window, so blue receives no new /v1
# requests during the wait; these budgets then only have to outlast the
# streams that already started.
QUIET_SECONDS="${MASTER_V1_QUIET_SECONDS:-765}"
STOP_TIMEOUT="${MASTER_V1_STOP_TIMEOUT:-2132}"
# This host's own nginx terminates :80 (the /v1 last resort) and proxies to
# the master container. Its upstream port must follow the serving one. The
# file is a symlink into sites-available, so the edit must follow it rather
# than replace it (plain `sed -i` would turn sites-enabled into a regular file).
# Two upstreams in it point at the master: newapi_ml (the /v1 last resort) and
# newapi_web (this host's own panel route) - both follow the port.
ECS_NGINX_CONF="${ECS_NGINX_CONF:-/etc/nginx/sites-enabled/tokeness-ml.conf}"
ECS_NGINX_HOST_HEADER="${ECS_NGINX_HOST_HEADER:-tokeness.cn}"
# Backups must live OUTSIDE the directory nginx includes, or the copy itself
# becomes a second loaded config (see ecs_nginx_point_at).
ECS_NGINX_BAK_DIR="${ECS_NGINX_BAK_DIR:-/root/nginx-bg-bak}"

log() { printf 'bootstrap-master-ecs: %s\n' "$*"; }
err() { printf 'bootstrap-master-ecs: %s\n' "$*" >&2; }

command -v docker >/dev/null 2>&1 || { err "docker not found"; exit 3; }

# The nginx rewrite helper lives in lib/master-nginx-upstream.sh. deploy.sh
# prepends that file to this script on stdin because only one stream reaches
# the ECS; a direct run from a checkout sources the sibling file instead.
if ! declare -F master_nginx_rewrite_file >/dev/null 2>&1; then
  _bs_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
  if [ -n "$_bs_dir" ] && [ -r "$_bs_dir/lib/master-nginx-upstream.sh" ]; then
    . "$_bs_dir/lib/master-nginx-upstream.sh"
  fi
  unset _bs_dir
fi

serving_port() { # -> the port currently serving (marker wins, else PORT_A)
  local p=""
  if [ -r "$SERVING_PORT_FILE" ]; then
    p="$(tr -d '[:space:]' < "$SERVING_PORT_FILE" 2>/dev/null || true)"
  fi
  case "$p" in "$PORT_A"|"$PORT_B") printf '%s\n' "$p" ;; *) printf '%s\n' "$PORT_A" ;; esac
}

commit_in_flight() { # -> pid of a live commit phase, nothing otherwise
  local pid
  [ -r "$COMMIT_LOCK" ] || return 1
  pid="$(tr -dc '0-9' < "$COMMIT_LOCK" 2>/dev/null || true)"
  [ -n "$pid" ] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  tr '\0' ' ' < "/proc/$pid/cmdline" 2>/dev/null | grep -q ' commit' || return 1
  printf '%s\n' "$pid"
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

# ecs_nginx_apply <target_port> <from_port> [<add_backup_port>] - move this
# host's :80 panel/last-resort upstream to the given master port. Verified
# (nginx -t + reload + Host-pinned probe) and reverted on failure, exactly like
# ml-sync treats the lightweight hosts. With <add_backup_port> the old port is
# kept as an nginx `backup` server, so the retired-but-alive blue can still
# answer a request if green dies inside the quiet window.
#
# Two traps this function exists to avoid (both hit on 2026-09-30's first
# rehearsal):
#   1. $ECS_NGINX_CONF lives in sites-enabled/, which nginx includes with a
#      wildcard - a backup written next to it becomes a SECOND loaded config
#      (duplicate log_format/upstream) and breaks every later `nginx -t`.
#      Backups therefore go to $ECS_NGINX_BAK_DIR, outside the include path.
#   2. It is a symlink into sites-available/, and `cp -a` on a symlink copies
#      the LINK, not the file - a "backup" that changes whenever the live file
#      does. So: resolve the target once, edit the target, back up a regular
#      copy of it.
# nginx_panel_probe - the panel must answer through this host's :80 after the
# flip. Loopback is tried first on purpose: the panel server blocks listen on
# 0.0.0.0:80, but since 2026-10-20 a gateway block owns the private address
# (`internal-2api.conf: listen 10.0.0.249:80`), and a request to $SERVE_IP:80
# matches that block instead of the wildcard listener, which answers its own
# 404. Probing $SERVE_IP first therefore failed a healthy flip on 2026-10-21
# and aborted the .63 release while green was already serving correctly.
nginx_panel_probe() {
  local host
  for host in 127.0.0.1 "$SERVE_IP"; do
    if curl -fsS --max-time 5 -H "Host: $ECS_NGINX_HOST_HEADER" "http://$host:80/health/ready" >/dev/null 2>&1; then
      return 0
    fi
  done
  return 1
}

ecs_nginx_apply() {
  local port="$1" old_port="$2" add_backup="${3:-}" bak target
  target="$(readlink -f "$ECS_NGINX_CONF" 2>/dev/null || printf '%s' "$ECS_NGINX_CONF")"
  [ -f "$target" ] || { err "ECS nginx conf not found (resolved: $target)"; return 1; }
  if ! declare -F master_nginx_rewrite_file >/dev/null 2>&1; then
    err "nginx rewrite helper missing (deploy.sh prepends lib/master-nginx-upstream.sh; direct runs need the sibling lib)"
    return 1
  fi
  install -d -m 0700 "$ECS_NGINX_BAK_DIR"
  bak="$ECS_NGINX_BAK_DIR/$(basename "$target").$(date +%s)"
  cp -a "$target" "$bak" || { err "could not back up $target"; return 1; }
  if ! master_nginx_rewrite_file "$target" "$SERVE_IP" "$port" "$old_port" "$add_backup"; then
    err "could not rewrite the :80 upstream in $target; restoring $bak"
    cp -a "$bak" "$target"
    return 1
  fi
  if cmp -s "$bak" "$target"; then
    log "ECS nginx already points at :$port (idempotent)"
    return 0
  fi
  if ! nginx -t >/dev/null 2>&1; then
    err "nginx -t failed after :80 upstream flip; restoring $bak"
    cp -a "$bak" "$target"
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    return 1
  fi
  systemctl reload nginx || { err "nginx reload failed; restoring $bak"; cp -a "$bak" "$target"; systemctl reload nginx >/dev/null 2>&1 || true; return 1; }
  if ! nginx_panel_probe; then
    err ":80 post-flip probe failed; restoring $bak"
    cp -a "$bak" "$target"
    nginx -t >/dev/null 2>&1 && systemctl reload nginx >/dev/null 2>&1 || true
    return 1
  fi
  if [ -n "$add_backup" ]; then
    log "ECS nginx :80 panel upstream: primary $SERVE_IP:$port, backup $SERVE_IP:$add_backup (probe 200; backup $bak)"
  else
    log "ECS nginx :80 last-resort now upstreams $SERVE_IP:$port (probe 200 via :80; backup $bak)"
  fi
}

# ecs_nginx_point_at <port> <old_port> - single-target rewrite. Also removes any
# transition-backup line left by a flip, which is what abort/settle need.
ecs_nginx_point_at() { ecs_nginx_apply "$1" "$2"; }

# ecs_nginx_flip_with_backup <new_port> <old_port> - flip the panel to green and
# keep blue as the nginx backup for the quiet window.
ecs_nginx_flip_with_backup() { ecs_nginx_apply "$1" "$2" "$2"; }

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
  if pid="$(commit_in_flight)"; then
    log "abort deferred: a commit phase is still running (pid $pid); leaving serving state untouched"
    printf 'SERVING_PORT=%s\n' "$sp"
    printf 'COMMIT_IN_FLIGHT=1\n'
    exit 0
  fi
  rm -f "$COMMIT_LOCK" 2>/dev/null || true
  blue="$(container_on_port "$sp")"
  # The commit flips nginx and the marker BEFORE its quiet window, and renames
  # green only at the end. If we are aborted inside that window the serving
  # port already carries $GREEN; removing it (the pre-commit branch below)
  # leaves :80 pointing at a dead port, which is exactly what happened on
  # 2026-10-19. Finalize it instead: the commit had effectively landed.
  if [ -n "$blue" ] && [ "$blue" = "$GREEN" ]; then
    write_serving_marker "$sp"
    if [ "$blue" != "$CONTAINER" ]; then
      if docker inspect "$CONTAINER" >/dev/null 2>&1; then
        # The retired blue still owns the canonical name; it is not serving
        # (:80 points at green), so dropping it cannot break the panel.
        docker rm -f "$CONTAINER" >/dev/null 2>&1 \
          || err "WARN: could not remove retired $CONTAINER"
      fi
      docker rename "$blue" "$CONTAINER" >/dev/null 2>&1 \
        || err "WARN: could not rename $blue to $CONTAINER"
    fi
    ecs_nginx_point_at "$sp" "$gp" \
      || err "WARN: could not drop the :$gp transition backup; check $ECS_NGINX_CONF"
    log "abort finalized the commit that had already flipped: green serving on :$sp"
    printf 'SERVING_PORT=%s\n' "$sp"
    exit 0
  fi
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
if pid="$(commit_in_flight)"; then
  err "a master commit phase is still running (pid $pid); refusing to start another roll"
  exit 6
fi
rm -f "$COMMIT_LOCK" 2>/dev/null || true
# master-state-reconcile:begin
# A serving container can lose its canonical name when a previous release
# flipped nginx + the serving marker and then died before renaming green (the
# 2026-10-24 .67 roll left exactly this: marker/serving port 3001 held by
# new-api-master--green, while new-api-master named the already-retired blue on
# 3000). Do not let the stale-green cleanup below delete the container that is
# actually serving, and free the other port before starting.
serve_c="$(container_on_port "$sp")"
[ -n "$serve_c" ] || { err "no running container publishes $SERVE_IP:$sp; refusing to invent a master"; exit 3; }
if [ "$serve_c" != "$CONTAINER" ]; then
  if docker inspect "$CONTAINER" >/dev/null 2>&1; then
    stale_port="$(docker inspect -f '{{range $p, $conf := .HostConfig.PortBindings}}{{range $conf}}{{.HostPort}}{{end}}{{end}}' "$CONTAINER" 2>/dev/null || true)"
    if [ "$stale_port" = "$sp" ]; then
      err "canonical $CONTAINER publishes serving port :$sp but is not the serving container; refusing to start"
      exit 3
    fi
    log "canonical $CONTAINER is a non-serving leftover on port ${stale_port:-unknown}; removing it before adopting $serve_c"
    docker rm -f "$CONTAINER" >/dev/null 2>&1 \
      || { err "could not remove stale canonical container $CONTAINER; refusing to start"; exit 3; }
  fi
  docker rename "$serve_c" "$CONTAINER" \
    || { err "could not adopt serving container $serve_c as $CONTAINER; refusing to start"; exit 3; }
  log "adopted serving container $serve_c as $CONTAINER (port :$sp); the old canonical name was stale"
fi
docker inspect "$CONTAINER" >/dev/null 2>&1 \
  || { err "container $CONTAINER does not exist; refusing to invent a master"; exit 3; }
# master-state-reconcile:end
old_image="$(docker inspect -f '{{.Config.Image}}' "$CONTAINER")"

# ---- phase: commit ---------------------------------------------------------
if [ "$PHASE" = commit ]; then
  install -d -m 0755 "$(dirname "$COMMIT_LOCK")" 2>/dev/null || true
  printf '%s\n' "$$" > "$COMMIT_LOCK" 2>/dev/null \
    || err "WARN: could not write $COMMIT_LOCK; an abort may race this commit"
  trap 'rm -f "$COMMIT_LOCK"' EXIT
  green="$(container_on_port "$gp")"
  [ -n "$green" ] || { err "commit found no green container on :$gp"; exit 3; }
  if ! port_ready "$gp"; then
    err "commit gate: green on :$gp is not ready; refusing to retire blue"
    exit 5
  fi
  ecs_nginx_flip_with_backup "$gp" "$sp" || exit 3
  write_serving_marker "$gp"
  log "panel is on green (:$gp); blue (:$sp) stays an nginx backup while it finishes in-flight work (${QUIET_SECONDS}s)"
  # Print progress every <=30s: a silent 765s session is what middleboxes reap
  # first (2026-10-19 the commit was reset after 300s of silence).
  slept=0
  while [ "$slept" -lt "$QUIET_SECONDS" ]; do
    chunk=$(( QUIET_SECONDS - slept ))
    [ "$chunk" -gt 30 ] && chunk=30
    sleep "$chunk"
    slept=$(( slept + chunk ))
    log "draining blue: ${slept}/${QUIET_SECONDS}s"
  done
  # The quiet window is over, so no new connection should reach blue any more:
  # drop it from nginx BEFORE stopping it (a backup entry pointing at a dead
  # container would only add a misleading fallback target).
  ecs_nginx_point_at "$gp" "$sp" \
    || err "WARN: could not drop the :$sp transition backup; check $ECS_NGINX_CONF"
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

# Binds are preserved rather than restated: this script already refuses to guess about ports and
# environment, and silently dropping a mount a previous release added would be the same class of
# mistake. The capture mount is then ensured on top of whatever was there.
binds=()
while IFS= read -r b; do
  [ -n "$b" ] || continue
  case "$b" in
    *":$CAPTURE_CONTAINER_DIR") continue ;;  # re-added below, so the host path is the configured one
  esac
  binds+=(-v "$b")
done < <(docker inspect -f '{{range .HostConfig.Binds}}{{println .}}{{end}}' "$CONTAINER" 2>/dev/null || true)

if mkdir -p "$CAPTURE_HOST_DIR" 2>/dev/null; then
  chmod 700 "$CAPTURE_HOST_DIR" 2>/dev/null || true
  binds+=(-v "$CAPTURE_HOST_DIR:$CAPTURE_CONTAINER_DIR")
else
  err "cannot create the capture directory $CAPTURE_HOST_DIR; refusing to start green without it"
  exit 3
fi

envs=()
while IFS= read -r kv; do
  [ -n "$kv" ] || continue
  # docker injects these itself; re-passing the captured copies would freeze
  # values that belong to the new container.
  case "$kv" in PATH=*|HOSTNAME=*|HOME=*) continue ;; esac
  envs+=(-e "$kv")
done < <(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$CONTAINER")

# B10 fix: the Docker stop budget is an UPPER BOUND on how long the process is
# ALLOWED to live; it does not make the app WAIT. main.go drains in-flight
# requests for SHUTDOWN_TIMEOUT_SECONDS (default 120s) and then exits on its
# own - so a blue master carrying /v1 last-resort streams still cut them at
# ~120s no matter what the stop timeout says. Force the app-level budget to
# the same /v1-aware number the stop timeout uses (the commit phase already
# moved nginx off blue first, so the budget only has to outlast streams that
# already started). Operators can still override it via
# MASTER_ENV_OVERRIDES, which is applied after this default.
# Replace any captured SHUTDOWN_TIMEOUT_SECONDS in place rather than append a
# second -e: docker takes the LAST duplicate, but the overrides loop below
# replaces only the FIRST match - an appended duplicate silently defeats any
# operator override (review reproduced override 3000 losing to 2132).
# envs is a FLAT array: "-e" and "KEY=VALUE" are separate elements. Match
# the PAIR (envs[i]=="-e" and envs[i+1] starts with the key) and replace the
# value element; otherwise append the pair as two elements.
new_shutdown_val="SHUTDOWN_TIMEOUT_SECONDS=${MASTER_V1_STOP_TIMEOUT:-2132}"
replaced_shutdown=0
for _i in "${!envs[@]}"; do
  if [[ "${envs[$_i]}" == "-e" && "${envs[$(( _i + 1 ))]}" == "SHUTDOWN_TIMEOUT_SECONDS="* ]]; then
    envs[$(( _i + 1 ))]="$new_shutdown_val"
    replaced_shutdown=1
  fi
done
(( replaced_shutdown == 1 )) || { envs+=("-e"); envs+=("$new_shutdown_val"); }

# Optional env overrides, applied ON TOP of the env captured from the running
# container. The captured env is the master's source of truth (this script
# re-passes it verbatim), which made a value change require hand-editing the
# live container - impossible without a recreate. With this file a change is
# declarative and survives every later re-roll, because each roll copies the
# (already updated) running container.
# Absent file => behaviour is identical to before this hook existed.
ENV_OVERRIDES_FILE="${MASTER_ENV_OVERRIDES:-/etc/tokeness-cn/master-env-overrides}"
if [ -r "$ENV_OVERRIDES_FILE" ]; then
  overrides_applied=0
  while IFS= read -r line; do
    line="${line%$'\r'}"
    case "$line" in ''|'#'*) continue ;; esac
    key="${line%%=*}"
    if [ -z "$key" ] || [ "$key" = "$line" ] || printf '%s' "$key" | grep -qE '[^A-Za-z0-9_]'; then
      log "WARN: ignoring malformed env override '$line' in $ENV_OVERRIDES_FILE"
      continue
    fi
    val="${line#*=}"
    replaced=0
    for i in "${!envs[@]}"; do
      if [ "${envs[$i]}" = "-e" ] && [ "${envs[$((i + 1))]%%=*}" = "$key" ]; then
        envs[$((i + 1))]="$key=$val"
        replaced=1
        break
      fi
    done
    [ "$replaced" = "1" ] || envs+=(-e "$key=$val")
    overrides_applied=$((overrides_applied + 1))
  done < "$ENV_OVERRIDES_FILE"
  log "applied $overrides_applied env override(s) from $ENV_OVERRIDES_FILE"
fi

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
log "preserving $(( ${#envs[@]} / 2 )) env vars, $(( ${#binds[@]} / 2 )) bind(s), restart=$restart, log_driver=$log_driver"

# Adopt a leftover green when it is healthy on the right port with the right
# image (an earlier run died after start); otherwise clear it and start fresh.
if docker inspect "$GREEN" >/dev/null 2>&1; then
  gimg="$(docker inspect -f '{{.Config.Image}}' "$GREEN")"
  if [ "$gimg" = "$IMAGE_REF" ] && [ -n "$(container_on_port "$gp" | grep -x "$GREEN" || true)" ] && curl -fsS --max-time 3 "http://$SERVE_IP:$gp/health/ready" >/dev/null 2>&1; then
    log "adopting existing healthy green ($GREEN on :$gp)"
    printf 'SERVING_PORT=%s\nPREVIOUS_PORT=%s\n' "$gp" "$sp"
    exit 0
  fi
  # stale-green-guard:begin
  # Defense in depth after the reconcile block above: never remove a container
  # that publishes the serving port, whatever name it currently carries.
  if [ "$(container_on_port "$sp")" = "$GREEN" ]; then
    err "refusing to remove $GREEN: it publishes the serving port $SERVE_IP:$sp; a state reconcile is required"
    exit 3
  fi
  # stale-green-guard:end
  log "stale green ($GREEN) found; removing before start"
  docker rm -f "$GREEN" >/dev/null 2>&1 || true
fi

if ! docker pull "$IMAGE_REF"; then
  err "docker pull failed for $IMAGE_REF (the serving master is untouched)"
  exit 4
fi

if ! docker run -d --name "$GREEN" --restart "$restart" \
  "${ports[@]}" "${binds[@]}" --log-driver "$log_driver" "${log_opts[@]}" "${envs[@]}" "$IMAGE_REF" >/dev/null; then
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
