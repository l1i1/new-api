# shellcheck shell=bash
# master-nginx-upstream.sh - rewrite this host's :80 panel upstream so it
# follows the serving master container.
#
# Why this is a separate file: bootstrap-master-ecs.sh is streamed to the ECS
# over ssh stdin (`ssh ... -- bash -s`), so it cannot source a sibling file on
# the remote host. deploy.sh prepends this library to that stream instead. The
# rewrite stays a pure file transformation here so the deploy tests can run it
# against fixture configs with no docker or nginx in sight.
#
# What the rewrite is for (2026-10-19, user request). During the blue-green
# quiet window the retired blue container is still alive but no longer listed
# in nginx at all: if green died in that window the panel answered 502 until
# the abort finished. The fix is to keep blue as an nginx BACKUP server while
# both containers exist:
#
#   upstream newapi_web {
#       server 10.0.0.249:3001 max_fails=2 fail_timeout=10s;      # green
#       server 10.0.0.249:3000 backup max_fails=2 fail_timeout=5s; # transition backup
#   }
#
# nginx only consults a backup server when all primary servers fail, and it
# never adds a backup server to the peer list while a primary is healthy - so
# the failover is one-directional, which is exactly the transition we want.
#
# The marker comment matters: a backup line added by the transition can be
# removed mechanically when the old container is retired, and it can never be
# mistaken for the /v1 last-resort fallback that nginx owns permanently.

# master_nginx_rewrite_file <file> <ip> <target_port> <from_port> [<add_backup_port>]
#
# Every `server <ip>:<port>` line in <file> is rewritten to <target_port>;
# `backup` flags and the rest of the line (parameters) are preserved because
# the line is rebuilt from the original address suffix. With
# <add_backup_port>, each PRIMARY master reference also gains a sibling
# `backup` line pointing at <add_backup_port>.
#
# Transition-backup lines from an earlier run (marked below) are dropped
# first, so flip -> settle -> flip is idempotent and a half-finished previous
# release cannot leave two backup lines behind.
#
# Ports outside {<from_port>, <target_port>} are refused (exit 3): the only
# master references in that config are the pair being ping-ponged, and a third
# value means the file is not what we think it is.
master_nginx_rewrite_file() {
  local file="${1:-}" ip="${2:-}" target="${3:-}" from="${4:-}" add_backup="${5:-}"
  local tmp marker

  if [[ -z "$file" || -z "$ip" || -z "$target" || -z "$from" ]]; then
    printf 'master-nginx-upstream: usage: master_nginx_rewrite_file <file> <ip> <target_port> <from_port> [<add_backup_port>]\n' >&2
    return 1
  fi
  if [[ -n "$add_backup" && "$add_backup" == "$target" ]]; then
    printf 'master-nginx-upstream: backup port equals target port (%s)\n' "$target" >&2
    return 1
  fi
  if [[ ! -f "$file" || ! -r "$file" || ! -w "$file" ]]; then
    printf 'master-nginx-upstream: %s is not a readable/writable file\n' "$file" >&2
    return 1
  fi

  marker='# blue-green-transition-backup'
  tmp="$(mktemp "${file}.rewrite.XXXXXX")" || return 1

  if ! awk -v ip="$ip" -v target="$target" -v froms="$from $target" \
      -v add_backup="$add_backup" -v marker="$marker" '
    BEGIN {
      n = split(froms, ports, " ")
      for (i = 1; i <= n; i++) managed[ports[i]] = 1
    }
    {
      # A line from an earlier transition (or its marker-only variant) goes away
      # unconditionally: the rewrite below re-adds it when it is still needed.
      if (index($0, marker) > 0) next

      # Only upstream server lines that carry an address: `server {` starts a
      # vhost, and `server_name` is a different directive.
      if ($0 ~ /^[ \t]*server[ \t]+[0-9]/) {
        indent = ""
        line = $0
        if (match(line, /^[ \t]+/)) {
          indent = substr(line, 1, RLENGTH)
          line = substr(line, RLENGTH + 1)
        }
        sub(/^server[ \t]+/, "", line)          # line begins with the address
        addr = line
        sub(/[ \t;].*$/, "", addr)
        rest = substr(line, length(addr) + 1)   # parameters + ";"

        if (substr(addr, 1, length(ip) + 1) == ip ":") {
          port = substr(addr, length(ip) + 2)
          if (!(port in managed)) {
            printf "master-nginx-upstream: %s references %s:%s, outside the managed pair %s\n", \
              FILENAME, ip, port, froms > "/dev/stderr"
            exit 3
          }
          is_backup = (rest ~ /(^|[ \t;])backup([ \t;]|$)/)
          print indent "server " ip ":" target rest
          if (add_backup != "" && !is_backup) {
            print indent "server " ip ":" add_backup \
              " backup max_fails=2 fail_timeout=5s; " marker
          }
          next
        }
      }
      print
    }
  ' "$file" > "$tmp"; then
    rm -f "$tmp"
    printf 'master-nginx-upstream: refusing to rewrite %s (unmanaged master port or read error)\n' "$file" >&2
    return 1
  fi

  # mktemp creates 0600; nginx.conf is normally 0644 and the deploy path should
  # not silently tighten it.
  chmod --reference="$file" "$tmp" 2>/dev/null || true
  if cmp -s "$file" "$tmp"; then
    rm -f "$tmp"
    return 0
  fi
  mv -f -- "$tmp" "$file"
}
