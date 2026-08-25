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
runner has live fixtures for 45 of the 85 matrix IDs, including the P0
authentication/model, role replay, thinking multi-turn, parameter-boundary,
streaming-logprobs, and tool-round-trip cases. Multi-turn fixtures keep the
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

`DS-E06` is reported as `expected_unsupported` only when the redacted
`return_logprob` capability fingerprint is observed; a successful request is
reported as `inconclusive` because it did not exercise the DFLASH capability
error path.

For the 13 fit cases, use the PowerShell runner. Case IDs are `K01` through
`K13`; multiple IDs may be comma- or space-separated. The runner emits only
status and structural evidence (including redacted error fingerprints), never
request or response bodies:

```powershell
./scripts/feature-probe/run-fit-probe.ps1 -CaseId K02,K03
./scripts/feature-probe/run-fit-probe.ps1 -IncludeBackup -IncludeOfficial -CaseId K01,K05,K12
```

The Go fit profile accepts the same canonical IDs (and legacy `DS-Kxx` aliases)
through `FEATURE_PROBE_CASES` or `FEATURE_PROBE_CASE_ID`. Official probing is
opt-in with `FEATURE_PROBE_INCLUDE_OFFICIAL=true`; gateway main and backup
default to `https://n.tokeness.dev/v1` and `https://n-cf.tokeness.dev/v1`.
