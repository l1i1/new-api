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
