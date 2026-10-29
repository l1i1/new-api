#!/usr/bin/env python3
"""Derive the continuation whitelist from real continuation telemetry.

The gateway records, per channel, how many continuation rounds produced a usable
answer and how many produced nothing. This tool prints that evidence and, with
--apply, merges the channels it supports into the continuation whitelist
(relay_chat_continuation_channels).

Read-only by default. Applying it widens how customer answers are stitched, so the
change is explicit and verifiable: the tool prints the value it wrote and reads it
back.

Usage:
  MAINLAND_ACCESS_TOKEN=... python3 continuation-whitelist.py            # report only
  MAINLAND_ACCESS_TOKEN=... python3 continuation-whitelist.py --apply    # update option
  MAINLAND_ACCESS_TOKEN=... python3 continuation-whitelist.py --apply --keep 106
"""
from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.request

BASE = os.environ.get("TOKENESS_BASE_URL", "https://tokeness.cn").rstrip("/")
TOKEN = os.environ.get("MAINLAND_ACCESS_TOKEN", "")
HEADERS = {
    "Authorization": "Bearer " + TOKEN,
    "New-Api-User": "1",
    "Accept": "application/json",
    "Content-Type": "application/json",
}


def call(path: str, method: str = "GET", payload: dict | None = None):
    data = json.dumps(payload).encode() if payload is not None else None
    req = urllib.request.Request(BASE + path, data=data, headers=HEADERS, method=method)
    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.loads(resp.read().decode())


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="write the recommended whitelist")
    parser.add_argument("--min-attempts", type=int, default=3)
    parser.add_argument("--min-success-rate", type=float, default=0.6)
    parser.add_argument(
        "--keep",
        default="",
        help="comma separated channel ids to keep regardless of telemetry (e.g. a freshly enabled channel)",
    )
    args = parser.parse_args()

    if not TOKEN:
        print("MAINLAND_ACCESS_TOKEN is required (use the secrets broker)", file=sys.stderr)
        return 2

    result = call("/api/continuation_capabilities").get("data") or {}
    channels = result.get("channels") or {}
    current = [int(x) for x in str(result.get("current") or "").split(",") if x.strip()]
    keep = [int(x) for x in args.keep.split(",") if x.strip()]

    print(f"current whitelist: {','.join(str(i) for i in current) or '(empty)'}")
    print(f"{'channel':>8}  {'name':<28} {'attempts':>8} {'success':>8} {'rate':>7}  verdict")
    recommended: list[int] = []
    for channel_id, entry in sorted(channels.items(), key=lambda kv: int(kv[0])):
        attempts = int(entry.get("attempts") or 0)
        successes = int(entry.get("successes") or 0)
        rate = (successes / attempts) if attempts else 0.0
        verdict = "enable" if entry.get("recommended") else "-"
        if entry.get("recommended"):
            recommended.append(int(channel_id))
        print(
            f"{int(channel_id):>8}  {str(entry.get('name') or '')[:28]:<28} {attempts:>8} {successes:>8} {rate:>6.0%}  {verdict}"
        )

    target = sorted(set(current) | set(recommended) | set(keep))
    print(f"recommended whitelist: {','.join(str(i) for i in target)}")
    if not args.apply:
        print("report only; re-run with --apply to write it")
        return 0
    if target == sorted(set(current)):
        print("whitelist already covers every recommended channel; nothing to write")
        return 0

    call(
        "/api/option/",
        "PUT",
        {"key": "relay_chat_continuation_channels", "value": ",".join(str(i) for i in target)},
    )
    readback = ""
    for option in call("/api/option/").get("data") or []:
        if option.get("key") == "relay_chat_continuation_channels":
            readback = str(option.get("value") or "")
    print(f"wrote: {','.join(str(i) for i in target)}")
    print(f"readback: {readback}")
    return 0 if readback == ",".join(str(i) for i in target) else 1


if __name__ == "__main__":
    raise SystemExit(main())
