# DeepSeek Feature Probe

This directory contains the redacted live probe runner for the DeepSeek V4
compatibility matrix. It reads credentials only from environment variables and
prints one JSON record per case; request and response bodies are never written.

Run from the repository root:

```powershell
$env:DEEPSEEK_API_KEY = '...'
$env:DEEPSEEK_BASE_URL = 'https://api.deepseek.com'
$env:NEW_API_KEY = '...'
$env:NEW_API_BASE_URL = 'https://n.tokeness.dev/v1'
$env:NEW_API_BACKUP_URL = 'https://n-cf.tokeness.dev/v1'
go run ./scripts/feature-probe
```

Without credentials the runner performs the offline coverage audit and reports
the live tiers as `inconclusive`; it never invents pass results. The current
runner has live fixtures for 49 of the 85 matrix IDs, including the P0
authentication/model, role replay, thinking multi-turn, parameter-boundary,
streaming-logprobs, tool-round-trip, and Responses API cases. Responses probes
cover non-stream output, semantic event streams, reasoning output, and a
function-call/function_call_output continuation. Multi-turn fixtures keep the
first response only in memory so the output remains structural and redacted.

For a bounded normal-profile run, select only the cases needed for the current
probe. `FEATURE_PROBE_CASES` and `FEATURE_PROBE_CASE_ID` accept comma-,
semicolon-, or whitespace-separated `DS-*` IDs; omitted IDs retain the full
live set. The offline audit still emits the complete matrix so unselected cases
remain visible as `inconclusive`:

```powershell
$env:FEATURE_PROBE_CASES = 'DS-A01,DS-A02,DS-A03,DS-A04,DS-A05,DS-B05,DS-C08,DS-C09,DS-C10,DS-C11,DS-D01,DS-D02,DS-D06,DS-D07,DS-D08,DS-D09,DS-D10,DS-E05,DS-E06,DS-E07,DS-F06,DS-F07,DS-F08,DS-F09'
go run ./scripts/feature-probe
```

Responses cases can be run explicitly with the same selector:

```powershell
$env:FEATURE_PROBE_CASES = 'DS-G01,DS-G02,DS-G03,DS-G04'
go run ./scripts/feature-probe
```

The Responses stream assertion uses semantic terminal events
(`response.completed`, `response.incomplete`, or `response.failed`) and does
not require Chat Completions' `data: [DONE]`. A `response.failed` event is
recorded as a terminal protocol event but cannot pass the successful output
assertion. A missing endpoint is reported as `expected_unsupported` only when
the HTTP error fingerprint identifies the Responses endpoint; ordinary 404s,
validation errors, and malformed responses remain failures, while transport
errors remain `inconclusive`.

`DS-E06` is reported as `expected_unsupported` only when the redacted
`return_logprob` capability fingerprint is observed; a successful request is
reported as `inconclusive` because it did not exercise the DFLASH capability
error path.

For acceptance of the 13 fit cases, use the Go `fit` profile. It sends the exact
K01-K13 request bodies, emits a `pass`, `fail`, or `inconclusive` verdict per
route, and rejects response-shape drift such as non-400 logprobs validation,
stop-sequence leakage, aggregator usage extensions, malformed SSE, usage-only
tail events, or a non-final `[DONE]` marker. Case IDs are `K01` through `K13`;
legacy `DS-Kxx` aliases are also accepted:

```powershell
$env:FEATURE_PROBE_PROFILE = 'fit'
$env:FEATURE_PROBE_CASES = 'K01,K02,K03,K08,K09,K12,K13'
$env:FEATURE_PROBE_INCLUDE_OFFICIAL = 'true'
go run ./scripts/feature-probe
```

Official probing is opt-in with `FEATURE_PROBE_INCLUDE_OFFICIAL=true`; gateway
main and backup default to `https://n.tokeness.dev/v1` and
`https://n-cf.tokeness.dev/v1`. Missing credentials produce `inconclusive`
records and never synthetic passes. A gateway contract match remains
`inconclusive` unless the same case also has a passing official baseline with
the same HTTP status; the record keeps `contract_status=pass` for diagnosis.
K04, K06, and K07 accept the official bounded-reasoning outcome where
`reasoning_content` is present, final `content` is absent, and
`finish_reason=length`; other successful non-stream cases still require usable
content or a valid function tool call.

The PowerShell runner is a diagnostic evidence collector only. It emits status
and redacted structural evidence without a pass/fail contract or paired route
comparison, so its output must not be used alone as acceptance evidence:

```powershell
./scripts/feature-probe/run-fit-probe.ps1 -CaseId K02,K03
./scripts/feature-probe/run-fit-probe.ps1 -IncludeBackup -IncludeOfficial -CaseId K01,K05,K12
```
