# Performance Harness

This directory contains local-only performance tooling. It is not part of the
production binary and must use only the isolated performance Compose project.

The preferred test host is `JP-N4-EV`; the current fallback is Debian WSL2.
Run the commands below from WSL in the repository checkout:

```sh
chmod +x scripts/performance/run-wsl.sh
scripts/performance/run-wsl.sh up
scripts/performance/run-wsl.sh run
scripts/performance/run-wsl.sh reset
```

`run` builds the candidate server and load-generator binaries, waits for
`/api/status`, creates a local admin/user/token/channel fixture, then runs three
measured rounds by default. Every scenario gets a fresh warm-up; any warm-up,
measured request, or resource-sampling error stops the run. The precompiled
load generator runs directly in a small container, so its Go compilation is
excluded from the CPU/RSS sampling window. Raw JSON is retained per round,
non-stream/stream/large-body runs each capture validated container resource
samples, and the complete median-RPS run for every scenario is copied to the
run root with `median-summary.txt`. The generated token is kept in memory by
the shell process and is never written to the repository.

Useful overrides are `PERF_DURATION`, `PERF_CONCURRENCY`, `PERF_RESULTS_DIR`,
`PERF_AUTH_DURATION`, `PERF_ROUNDS`, `PERF_WARMUP_DURATION`, `PERF_BASE_URL`, `PERF_ADMIN_USER`, and
`PERF_ADMIN_PASSWORD`. The password is
required only for the local fixture; when omitted, the runner generates an
ephemeral value for the current process.

`PERF_SKIP_BUILD=1` only accepts server and load-generator binaries whose
provenance identifies the current clean commit and whose SHA-256 hashes still
match. Dirty worktrees and stale, modified, or provenance-less binaries are
rejected instead of being silently reused.

Each result directory records the WSL host, CPU and memory size, kernel, Docker
and Compose versions, Go compiler, binary hashes, and the non-sensitive
performance settings needed to compare runs.

Throughput uses completed successful requests divided by actual wall-clock
elapsed time. Requests admitted before the configured deadline are allowed to
finish, and that drain time is included in the denominator.

The runner does not claim the project-level 2x/-30%/-30% gates by itself. Save
baseline and candidate result directories and compare them under the same host,
commit, workload, and fresh-data procedure.

The isolated stack disables compliance GeoIP lookup. This prevents a fresh
benchmark container from downloading an external database or including that
cold-start delay in the local mock workload.
