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

`run` builds the candidate image, waits for `/api/status`, creates a local
admin/user/token/channel fixture, runs warm-up and auth-cache/non-stream/stream/large-body
cases, and writes raw JSON plus container resource samples under
`private/performance-results` by default. The generated token is kept in memory
by the shell process and is never written to the repository.

Useful overrides are `PERF_DURATION`, `PERF_CONCURRENCY`, `PERF_RESULTS_DIR`,
`PERF_AUTH_DURATION`, `PERF_BASE_URL`, `PERF_ADMIN_USER`, and
`PERF_ADMIN_PASSWORD`. The password is
required only for the local fixture; when omitted, the runner generates an
ephemeral value for the current process.

The runner does not claim the project-level 2x/-30%/-30% gates by itself. Save
baseline and candidate result directories and compare them under the same host,
commit, workload, and fresh-data procedure.
