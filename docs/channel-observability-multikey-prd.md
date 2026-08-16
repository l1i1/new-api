# Channel Observability and Multi-Key Credential Pool

## Problem

Administrators cannot reliably compare a channel and model by request volume, attempt success rate, cache behavior, p95 latency, or time to first token. Multi-key channels also lack stable credential identity, per-key proxy settings, key-level health probes, and safe batch operations.

## Goals

- Separate user-request outcomes from every upstream channel attempt, including retries.
- Report channel/model request count, attempt count, success rates, cache hit/token rates, p95 latency, TTFT, upstream first-byte latency, and coverage.
- Give every multi-key credential a stable identity, independent status, proxy mode, and test history.
- Test selected/all keys asynchronously without charging quota or creating consume logs.
- Enable or disable selected/all keys transactionally and keep channel status, abilities, and caches consistent.
- Preserve legacy single-key and legacy newline/JSON key configurations.

## Non-goals

- No raw prompt, response, API key, proxy password, or user IP in metrics.
- No exact historical p95 backfill from old average/second-resolution data.
- No automatic disabling on transient 429, timeout, proxy, or 5xx errors by default.

## User experience

The channel list exposes an observability entry. A channel detail view groups metrics by requested and upstream model. The multi-key view shows only a fingerprint, status, masked proxy, last test result, latency, recent error, and 24-hour metrics. Administrators can test, enable, disable, and configure proxies for selected keys or the whole pool. Tests run as durable system tasks and can be observed until completion.

## Success criteria

1. A retry from channel A to B produces an attempt sample for both channels and one final request outcome.
2. p95 values are calculated from mergeable latency histograms, never from averages.
3. Cache rates exclude requests without reliable cache usage fields.
4. Key deletion/reordering cannot attach a historical test or metric to another key.
5. All keys disabled makes the channel unavailable; re-enabling a key restores channel selection and abilities.
6. Per-key inherit/direct/custom proxy behavior is deterministic for HTTP, SSE, WebSocket, and async polling paths.
7. Key tests never alter billing or user consume logs and never expose credentials.
