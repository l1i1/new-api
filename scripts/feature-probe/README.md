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
the live tiers as `inconclusive`; it never invents pass results.

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
