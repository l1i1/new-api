# New API Performance Optimization PRD

## Objective

Improve the gateway's sustained request throughput while preserving relay,
authentication, retry, quota, logging, and database behavior.

The target is measured against a reproducible baseline on the same machine,
build, dataset, and workload:

- Throughput: at least 2.0x baseline requests per second.
- CPU: at least 30% lower CPU time per successful request.
- Memory: at least 30% lower peak resident memory under the same workload.

These are release gates for the complete optimization program. An individual
optimization must not be described as meeting them until the complete workload
has been measured.

## Scope

### In scope

- Authentication and token/user cache lookup on relay requests.
- Channel selection and retry bookkeeping.
- Request-body buffering and JSON/multipart allocations.
- Quota pre-consume and settlement database/Redis access.
- Consume-log and data-export work on the request completion path.
- HTTP client reuse, connection pooling, and upstream relay overhead.
- Repeatable local and JP-N4-EV benchmark execution and profiling.

### Out of scope

- Changes to billing semantics, quota atomicity, retry correctness, or access
  control made only to improve a benchmark score.
- Production database cleanup, schema changes, or configuration changes without
  a separately reviewed migration and rollback procedure.
- Provider-specific behavior changes that are not covered by focused tests.

## Users and impact

The primary users are API clients sending concurrent relay requests. Operators
need predictable resource usage and enough telemetry to distinguish CPU,
database, Redis, and upstream bottlenecks. Billing and authorization must remain
strictly correct when the system is under contention or when a cache is cold.

## Workload matrix

The benchmark must run each scenario with the same request count, concurrency,
payload, database seed, and upstream response.

| Scenario | Purpose | Default load |
| --- | --- | --- |
| `auth-cache-hot` | Isolate token and user authorization | 60s, 64 clients |
| `relay-nonstream` | Measure complete request and quota path | 120s, 64 clients |
| `relay-stream` | Measure SSE, logging, and settlement path | 120s, 32 clients |
| `channel-retry` | Measure selection and retry overhead | 60s, 64 clients |
| `body-large` | Measure body buffering and peak RSS | 60s, 16 clients |

The simulated upstream must return deterministic OpenAI-compatible responses;
it must not call a real provider. The benchmark token, user, channel, and
database data are generated only inside the isolated test environment.

## Acceptance criteria

- All existing backend tests pass, including database compatibility-sensitive
  tests and the independent `relaykit` module tests.
- Every optimization has a focused regression or contract test when it changes
  observable behavior or concurrency boundaries.
- The benchmark reports RPS, p50/p95/p99 latency, error rate, CPU time per
  request, peak RSS, heap allocation, GC count, PostgreSQL query count, and
  Redis command count.
- No benchmark run uses production endpoints, credentials, database DSNs, or
  Redis keys.
- The final release candidate meets the 2.0x throughput, 30% CPU, and 30%
  memory gates on the same reference environment. If the gates are not met,
  the result is reported as incomplete with the remaining bottleneck named.

## Rollout and rollback

Optimizations are merged in small batches. Each batch must have a before/after
benchmark record and can be reverted independently. Production rollout starts
with one canary node after correctness and resource limits are reviewed; the
operator can roll back to the previous image without changing persistent data.
