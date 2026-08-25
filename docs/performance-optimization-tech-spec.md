# New API Performance Optimization Technical Specification

## Design principles

1. Measure before changing code and compare only runs with identical inputs.
2. Keep billing, authorization, channel retry, and provider protocol semantics
   unchanged.
3. Prefer immutable precomputed metadata and existing caches over new global
   state or new dependencies.
4. Keep benchmark tooling outside the production binary and use standard
   library code where practical.

## Reference environments

The preferred target is `JP-N4-EV`. The fallback is the local Debian WSL2
instance. The current JP-N4-EV SSH check is not authorized, so initial runs
must use WSL2 until an operator supplies an approved access path. The benchmark
must record host name, CPU count, memory, kernel, container/runtime versions,
commit, Go version, and environment variables that affect performance.

## Isolated topology

```text
load generator -> new-api -> PostgreSQL
                      |\
                      | Redis
                      `-> deterministic mock upstream
```

The performance compose project uses its own project name and named volumes.
It must not reuse the development or production compose project, database,
Redis instance, or credentials. The mock upstream accepts only local test
traffic and returns fixed non-stream and SSE responses. Compliance GeoIP lookup
is disabled in this isolated stack so a fresh run neither downloads external
data nor measures unrelated cold-start latency.

## Measurement protocol

1. Build one image from the candidate commit with production compiler flags.
2. Start PostgreSQL, Redis, mock upstream, and New API from a clean performance
   project.
3. Seed one enabled user, one relay token, the required abilities/channels, and
   deterministic options through the local setup path.
4. Warm the process and caches before every measured scenario. Do not include
   warm-up in the result.
5. Run the precompiled load generator for the configured duration at a fixed
   concurrency. Start container resource sampling immediately before the client
   process and stop it immediately after completion. Missing, malformed, or
   interrupted resource samples invalidate the run.
6. Repeat each case three times after a fresh warm-up. Select the complete
   median-RPS run, retain every raw result, and retain the matching resource
   samples for non-stream, stream, and large-body cases.
7. Capture a CPU profile and heap profile for the steady-state `relay-nonstream`
   case. Profiles are local artifacts and must not contain request bodies,
   credentials, or production data.

## Metrics

- Throughput: completed successful requests / actual wall-clock measurement
  seconds, including completion time for requests admitted before the deadline.
- Latency: client-observed p50, p95, and p99.
- CPU: container CPU time converted to core-seconds / successful request.
- Memory: maximum container RSS and Go heap `inuse_space` during the measured
  interval.
- Go runtime: allocations, allocation bytes, GC cycles, and pause time.
- Dependencies: PostgreSQL statement count/time and Redis command count/time.
- Correctness: HTTP status, response body contract, quota delta, log row count,
  and retry/channel distribution.

## Planned optimization batches

### Batch 1: request-completion overhead

- Stop serializing the complete consume-log parameter structure for an INFO log
  on every successful request when debug logging is disabled.
- Keep structured consume logs and data export unchanged.
- Verify that debug mode still emits the diagnostic record.

### Batch 2: channel selection metadata

- Build immutable priority and effective-weight metadata during channel-cache
  refresh.
- Use it only for the unfiltered fast path. Requests with a request-path
  filter or blocked channels retain the existing filtering path until equivalent
  metadata is available.
- Preserve priority ordering, zero-weight smoothing, random selection, missing
  channel errors, and retry clamping.

### Batch 3: database and Redis round trips

- Use query/command counters to identify actual repeated round trips before
  changing them.
- Consider batching only independent reads. Do not batch or reorder quota
  writes when that can weaken atomic reservation or settlement.
- The optional quota updater stores each user's quota, used-quota, and request
  count deltas as one in-memory tuple. Snapshotting cannot split one settlement
  across batches. Graceful shutdown stops new queue admission, drains and
  retries pending tuples, and makes later callers use the synchronous SQL path.
- The optional log batcher uses an explicit transaction on SQLite, MySQL, and
  PostgreSQL. Begin or insert failures retain the accepted buffer for retry;
  only a commit error has unknown outcome and is dropped without replay to
  avoid duplicate audit rows. ClickHouse keeps synchronous log inserts because
  it cannot provide the same transaction boundary. Shutdown passes its context
  into database work and does not return before the worker exits.

### Batch 4: body and relay allocations

- Use allocation profiles and representative payloads to reduce copies only
  where ownership and retry behavior remain explicit.
- Keep request-body replay and multipart limits unchanged.

### Batch 5: channel-observation Redis writes

- Keep the in-process `hotBuckets` update synchronous because request-local
  dashboards depend on it immediately.
- When explicitly enabled with `CHANNEL_OBSERVABILITY_ASYNC_REDIS=true`, put
  Redis observation events on a bounded queue and merge `HINCRBY` deltas per
  Redis hash in short worker batches before one transaction pipeline.
- Keep the default disabled. A full queue falls back to the existing
  synchronous write so the opt-in path does not silently discard observations
  under backpressure. A failed batch is logged and dropped without replay
  once; residual Redis failures are logged and remain non-fatal to the request.
- Active-dashboard reads enqueue a worker barrier before reading Redis. The
  barrier flushes all observations accepted before the query, preventing the
  current bucket from lagging without merging and double-counting `hotBuckets`.
  If that flush fails, the query falls back to the synchronous local bucket.
- Preserve all counter, histogram, error-class, usage, expiry, and index
  fields, then compare request errors and active-dashboard counts before
  retaining any measured performance gain.

### Experimental batch: model-performance Redis writes

- Keep the in-process `hotBuckets` update synchronous because model-square
  queries use it for the current bucket.
- When explicitly enabled with `PERF_METRICS_ASYNC_REDIS=true`, put the
  per-request Redis aggregate on a bounded queue and merge field deltas per
  Redis hash in short worker batches before one transaction pipeline.
- Keep the default disabled. A full queue falls back to the existing
  synchronous write, and graceful shutdown drains accepted records. A failed
  batch is logged and dropped without replay; Redis failures remain
  non-fatal to the request.
- An initial 60-second screening suggested higher candidate RPS, but control
  throughput varied materially. A later fair recheck used two order-reversed
  120-second pairs with the same loadgen and measured combined deltas of -1.4%
  non-stream, +0.5% stream, and +0.9% large-body; CPU per request was slightly
  worse overall and peak memory was unchanged. Keep the default disabled and
  do not count this as a retained performance improvement.

### Rejected Batch 6: wallet quota-warning read fusion

- The candidate extended the authoritative wallet balance read in
  `NewBillingSession` with `quota_warning_sent` and reused that request-local
  snapshot to skip the later healthy-balance claim read.
- The snapshot is not authoritative when the asynchronous claim runs. A low
  balance can be claimed and then topped up after the snapshot; skipping the
  current database read would leave `quota_warning_sent=true` even though the
  balance recovered, suppressing a later notification episode.
- The candidate was rejected before A/B testing. `ClaimQuotaWarning` keeps its
  current-state database read and rearm semantics.

### Batch 7: content-moderation enabled cache fast path (rejected)

- The candidate read only `Enabled` when the option value matched a fresh
  in-process cache, avoiding the full config clone on that request path.
- Allocation profiles removed nearly all of the targeted threshold-clone
  hotspot, but total allocation fell by only about 0.5 KiB per request and CPU
  profiles showed no clear improvement.
- A reverse-order 60-second A/B run regressed all measured relay scenarios:
  non-stream -5.2%, stream -4.1%, and large-body -11.1%, with zero errors on
  both variants. The candidate was reverted because it provided no repeatable
  throughput benefit.

### Batch 8: remaining relay allocation micro-hotspots (rejected)

- `common.DeepCopy(textReq)` remains the retry-isolation boundary introduced
  after adaptors corrupted the reusable request across attempts. Provider
  conversion mutates model, parameter, message, and nested request state, so a
  partial typed clone would make retry correctness depend on every current and
  future adaptor remembering the clone contract. The measured 16-18 MiB per
  45-second profile does not justify that maintenance and protocol risk.
- `gin.Context.Set` accounted for 13.5 MiB in the same profile, about 3% of
  total allocation. A global fixed-capacity `Keys` map would shift memory cost
  onto lightweight routes, while a relay-only resize would allocate and copy
  an already-created map. Neither path has enough expected leverage to justify
  production code or another end-to-end A/B candidate.
- No Batch 8 production code was written.

### Batch 11: user auth-cache Redis pipeline (rejected)

- The candidate pipelined the user-cache `HGETALL` and auth-fence/version
  `MGET` while preserving schema checks, database fallback, and the monotonic
  authorization floor.
- A fair 120-second WSL2 candidate/control run used identical response-validating
  loadgen code, concurrency 64, `GOGC=30`, and zero-error HTTP 200 responses.
  Candidate RPS regressed auth-hot by 53.2%, non-stream by 14.7%, stream by
  54.3%, and large-body by 34.7%; peak RSS was effectively unchanged.
- The production pipeline code was reverted. The `/v1/models` auth-hot workload
  remains in the local performance harness so future authentication changes can
  be measured directly.

## Verification commands

From the repository root:

```text
GOWORK=off go test ./...
GOWORK=off go vet ./...
git diff --check
```

For the independent module:

```text
cd relaykit
GOWORK=off go test ./...
GOWORK=off go build ./...
```

The WSL runner is `scripts/performance/run-wsl.sh run`. It enforces an odd
`PERF_ROUNDS` value of at least three, rejects any warm-up or measured errors,
captures validated resource samples for every relay scenario, and records
commit plus server/loadgen binary hashes. `PERF_SKIP_BUILD=1` only reuses both
binaries when they match provenance from the current clean commit.
