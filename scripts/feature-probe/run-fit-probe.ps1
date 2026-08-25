param(
    [switch]$IncludeOfficial,
    [switch]$IncludeBackup,
    [Alias('Case', 'Cases')]
    [string[]]$CaseId
)

$ErrorActionPreference = "Stop"

$mainBase = if ($env:NEW_API_BASE_URL) { $env:NEW_API_BASE_URL } else { 'https://n.tokeness.dev/v1' }
$backupBase = if ($env:NEW_API_BACKUP_URL) { $env:NEW_API_BACKUP_URL } else { 'https://n-cf.tokeness.dev/v1' }
$routes = @(
    [pscustomobject]@{ Name = "main"; BaseUrl = $mainBase; Key = $env:NEW_API_KEY }
)
if ($IncludeBackup -or $env:NEW_API_BACKUP_URL) {
    $routes += [pscustomobject]@{ Name = "backup"; BaseUrl = $backupBase; Key = $env:NEW_API_KEY }
}
if ($IncludeOfficial) {
    $routes = @([pscustomobject]@{ Name = "official"; BaseUrl = $(if ($env:DEEPSEEK_BASE_URL) { $env:DEEPSEEK_BASE_URL } else { 'https://api.deepseek.com' }); Key = $env:DEEPSEEK_API_KEY }) + $routes
}

function Join-ChatCompletionsUrl([string]$BaseUrl) {
    $trimmed = $BaseUrl.TrimEnd('/')
    if ($trimmed -match '/chat/completions$') {
        return $trimmed
    }
    return "$trimmed/chat/completions"
}

foreach ($route in $routes) {
    $route | Add-Member -NotePropertyName Url -NotePropertyValue (Join-ChatCompletionsUrl $route.BaseUrl)
}

$cases = @(
    [pscustomobject]@{ Id = "K01"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "依次说出：苹果、香蕉、橙子、西瓜。" }); stop = "香蕉"; max_tokens = 256; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K02"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "1+1=?" }); top_logprobs = 5; max_tokens = 64; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K03"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "1+1=?" }); logprobs = $true; top_logprobs = 21; max_tokens = 64; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K04"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "1+1=?" }); max_tokens = 393216; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K05"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "用一个词描述春天。" }); temperature = 0; max_tokens = 256; thinking = [ordered]@{ type = "disabled" }; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K06"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "你好" }); frequency_penalty = 2; presence_penalty = 2; max_tokens = 64; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K07"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "system"; name = "teacher"; content = "你是一位数学老师。" }, [ordered]@{ role = "user"; name = "student_a"; content = "1+1=?" }); max_tokens = 256; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K08"; Body = [ordered]@{ messages = @([ordered]@{ role = "user"; content = "用一个词描述秋天。" }); temperature = 2; top_p = 0.1; presence_penalty = 1.5; frequency_penalty = 1.5; max_tokens = 1024; reasoning_effort = "low"; model = "deepseek-v4-flash"; stream = $true; stream_options = [ordered]@{ include_usage = $true } } },
    [pscustomobject]@{ Id = "K09"; Body = [ordered]@{ messages = @([ordered]@{ role = "user"; content = "1+1=?" }); max_tokens = 1024; reasoning_effort = "low"; model = "deepseek-v4-flash"; stream = $true; stream_options = [ordered]@{ include_usage = $true } } },
    [pscustomobject]@{ Id = "K10"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "北京今天天气怎么样？" }); tools = @([ordered]@{ type = "function"; function = [ordered]@{ name = "get_weather"; description = "查询指定城市的当前天气"; parameters = [ordered]@{ type = "object"; properties = [ordered]@{ city = [ordered]@{ type = "string"; description = "城市名，例如 北京" } }; required = @("city") } } }); tool_choice = "auto"; max_tokens = 1024; thinking = [ordered]@{ type = "disabled" }; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K11"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "依次说出：苹果、香蕉、橙子、西瓜。" }); stop = "香蕉"; max_tokens = 256; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K12"; Body = [ordered]@{ stream = $false; messages = @([ordered]@{ role = "user"; content = "1+1=?" }); logprobs = $true; top_logprobs = 5; max_tokens = 1024; reasoning_effort = "low"; model = "deepseek-v4-flash" } },
    [pscustomobject]@{ Id = "K13"; Body = [ordered]@{ messages = @([ordered]@{ role = "user"; content = "1+1=?" }); max_tokens = 1024; reasoning_effort = "low"; model = "deepseek-v4-flash"; stream = $true; stream_options = [ordered]@{ include_usage = $true } } }
)

function Get-MaxTopLogprobs([object[]]$Entries) {
    $max = 0
    foreach ($entry in @($Entries)) {
        if ($null -ne $entry.top_logprobs -and $entry.top_logprobs.Count -gt $max) {
            $max = $entry.top_logprobs.Count
        }
    }
    return $max
}

function Get-MessageFingerprint([string]$Message) {
    if ([string]::IsNullOrWhiteSpace($Message)) {
        return $null
    }
    $normalized = ($Message -replace '\s+', ' ').Trim().ToLowerInvariant()
    $normalized = [regex]::Replace($normalized, '(?i)(sk-[a-z0-9_-]+|bearer\s+[a-z0-9._-]+)', '***')
    if ($normalized.Length -gt 160) {
        return $normalized.Substring(0, 160)
    }
    return $normalized
}

function Add-ErrorEvidence {
    param(
        [object]$ErrorValue,
        [hashtable]$State
    )
    if ($null -eq $ErrorValue) {
        return
    }
    $State.has_error = $true
    $paramProperty = $ErrorValue.PSObject.Properties['param']
    $State.error_param_present = $null -ne $paramProperty
    if ($null -ne $paramProperty) {
        $State.error_param_null = $null -eq $paramProperty.Value
    }
    foreach ($field in @('type', 'code')) {
        $property = $ErrorValue.PSObject.Properties[$field]
        if ($null -ne $property -and -not [string]::IsNullOrWhiteSpace([string]$property.Value)) {
            $State["error_$field"] = [string]$property.Value
        }
    }
    $messageProperty = $ErrorValue.PSObject.Properties['message']
    if ($null -ne $messageProperty) {
        $State.error_message_fingerprint = Get-MessageFingerprint ([string]$messageProperty.Value)
    }
}

function Normalize-CaseId([string]$Id) {
    $key = $Id.Trim().ToUpperInvariant()
    $key = $key -replace '^DS-', ''
    if ($key -match '^K(?:0[1-9]|1[0-3])$') {
        return $key
    }
    switch ($key) {
        'K01-STOP-LOW' { return 'K01' }
        'K02-TOP-WITHOUT-LOGPROBS' { return 'K02' }
        'K03-TOP-21' { return 'K03' }
        'K04-MAX-TOKENS' { return 'K04' }
        'K05-THINKING-DISABLED' { return 'K05' }
        'K06-PENALTIES' { return 'K06' }
        'K07-MESSAGE-NAME' { return 'K07' }
        'K08-STREAM-PARAMS' { return 'K08' }
        'K09-STREAM-BASIC' { return 'K09' }
        'K10-TOOLS-DISABLED' { return 'K10' }
        'K11-STOP-LOW-2' { return 'K11' }
        'K12-THINKING-LOGPROBS' { return 'K12' }
        'K13-STREAM-BASIC-2' { return 'K13' }
    }
    return $null
}

function Get-Result($Route, $Case) {
    $body = $Case.Body | ConvertTo-Json -Depth 20 -Compress
    $headers = @{ Authorization = "Bearer $($Route.Key)"; "Content-Type" = "application/json"; Accept = "application/json" }
    try {
        $response = Invoke-WebRequest -Uri $Route.Url -Method Post -Headers $headers -Body $body -SkipHttpErrorCheck -TimeoutSec 60
    } catch {
        return [pscustomobject]@{
            case_id = $Case.Id
            route = $Route.Name
            status = "transport_error"
            http_status = 0
            has_error = $true
            error_type = $_.Exception.GetType().Name
            error_code = "transport_error"
            error_param_present = $false
            error_param_null = $null
        }
    }

    $status = [int]$response.StatusCode
    $raw = if ($response.Content -is [byte[]]) {
        [Text.Encoding]::UTF8.GetString($response.Content)
    } else {
        [string]$response.Content
    }
    $isSse = [string]$response.Headers["Content-Type"] -match "text/event-stream"
    $json = $null
    if (-not $isSse) {
        try { $json = $raw | ConvertFrom-Json } catch { }
    }

    $state = @{
        has_error = $false
        error_param_present = $false
        error_param_null = $null
        error_type = $null
        error_code = $null
        error_message_fingerprint = $null
        has_content = $false
        has_reasoning_content = $false
        has_tool_calls = $false
        tool_arguments_json = $false
        finish_reason = $null
        usage = $false
        usage_events = 0
        sse_events = 0
        content_chunks = 0
        reasoning_chunks = 0
        done = $false
        logprobs_content = 0
        logprobs_reasoning_content = 0
        max_top_logprobs = 0
        max_reasoning_top_logprobs = 0
    }

    if ($null -ne $json) {
        $errorProperty = $json.PSObject.Properties['error']
        if ($null -ne $errorProperty) {
            Add-ErrorEvidence $errorProperty.Value $state
        }
        if ($null -ne $json.choices -and $json.choices.Count -gt 0) {
            $choice = $json.choices[0]
            $state.finish_reason = [string]$choice.finish_reason
            if ($null -ne $choice.message) {
                $state.has_content = -not [string]::IsNullOrWhiteSpace([string]$choice.message.content)
                $state.has_reasoning_content = -not [string]::IsNullOrWhiteSpace([string]$choice.message.reasoning_content)
                if ($null -ne $choice.message.tool_calls) {
                    $toolCalls = @($choice.message.tool_calls)
                    $state.has_tool_calls = $toolCalls.Count -gt 0
                    $validArguments = $state.has_tool_calls
                    foreach ($toolCall in $toolCalls) {
                        $argumentsValid = $false
                        if ($null -ne $toolCall -and $null -ne $toolCall.function -and -not [string]::IsNullOrWhiteSpace([string]$toolCall.function.name) -and -not [string]::IsNullOrWhiteSpace([string]$toolCall.function.arguments)) {
                            try {
                                $argumentsValid = [string]$toolCall.function.arguments | Test-Json -ErrorAction Stop
                            } catch {
                                $argumentsValid = $false
                            }
                        }
                        if (-not $argumentsValid) {
                            $validArguments = $false
                        }
                    }
                    $state.tool_arguments_json = $validArguments
                }
            }
            if ($null -ne $choice.logprobs) {
                if ($null -ne $choice.logprobs.content) {
                    $state.logprobs_content = $choice.logprobs.content.Count
                    $state.max_top_logprobs = Get-MaxTopLogprobs $choice.logprobs.content
                }
                if ($null -ne $choice.logprobs.reasoning_content) {
                    $state.logprobs_reasoning_content = $choice.logprobs.reasoning_content.Count
                    $state.max_reasoning_top_logprobs = Get-MaxTopLogprobs $choice.logprobs.reasoning_content
                }
            }
        }
        $state.usage = $null -ne $json.usage
    }

    if ($isSse) {
        foreach ($line in ($raw -split "`n")) {
            if (-not $line.StartsWith("data:")) { continue }
            $state.sse_events++
            $payload = $line.Substring(5).Trim()
            if ($payload -eq "[DONE]") { $state.done = $true; continue }
            try { $event = $payload | ConvertFrom-Json } catch { continue }
            $errorProperty = $event.PSObject.Properties['error']
            if ($null -ne $errorProperty) {
                Add-ErrorEvidence $errorProperty.Value $state
            }
            if ($null -ne $event.usage) { $state.usage = $true; $state.usage_events++ }
            foreach ($choice in @($event.choices)) {
                if ($choice.finish_reason) { $state.finish_reason = [string]$choice.finish_reason }
                if ($null -ne $choice.delta) {
                    if (-not [string]::IsNullOrWhiteSpace([string]$choice.delta.content)) { $state.has_content = $true; $state.content_chunks++ }
                    if (-not [string]::IsNullOrWhiteSpace([string]$choice.delta.reasoning_content)) { $state.has_reasoning_content = $true; $state.reasoning_chunks++ }
                }
            }
        }
    }

    $protocolAccepted = $status -ge 200 -and $status -lt 300 -and -not $state.has_error
    $effectiveSuccess = $protocolAccepted -and ($state.has_content -or ($state.has_tool_calls -and $state.tool_arguments_json))

    return [pscustomobject]@{
        case_id = $Case.Id
        route = $Route.Name
        status = $status
        http_status = $status
        has_error = $state.has_error
        protocol_accepted = $protocolAccepted
        effective_success = $effectiveSuccess
        failure_reason = if ($protocolAccepted -and -not $effectiveSuccess) { "empty_final_content" } else { $null }
        error_type = $state.error_type
        error_code = $state.error_code
        error_param_present = $state.error_param_present
        error_param_null = $state.error_param_null
        param_null = $state.error_param_null
        error_message_fingerprint = $state.error_message_fingerprint
        content = $state.has_content
        reasoning = $state.has_reasoning_content
        has_tool_calls = $state.has_tool_calls
        tool_arguments_json = $state.tool_arguments_json
        finish = $state.finish_reason
        finish_reason = $state.finish_reason
        stream = $isSse
        usage = $state.usage
        usage_events = $state.usage_events
        sse_events = $state.sse_events
        content_chunks = $state.content_chunks
        reasoning_chunks = $state.reasoning_chunks
        done = $state.done
        lp_content = $state.logprobs_content
        lp_reasoning = $state.logprobs_reasoning_content
        lp_max = $state.max_top_logprobs
        lp_reason_max = $state.max_reasoning_top_logprobs
    }
}

$requestedIds = @($CaseId | ForEach-Object { $_ -split '[,;\s]+' } | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
$selectedIds = New-Object System.Collections.Generic.List[string]
$unknownIds = New-Object System.Collections.Generic.List[string]
foreach ($requestedId in $requestedIds) {
    $normalizedId = Normalize-CaseId $requestedId
    if ($null -eq $normalizedId) {
        [void]$unknownIds.Add($requestedId)
    } elseif (-not $selectedIds.Contains($normalizedId)) {
        [void]$selectedIds.Add($normalizedId)
    }
}
if ($unknownIds.Count -gt 0) {
    throw "Unknown fit case id(s): $($unknownIds -join ', ')"
}
$selectedCases = if ($selectedIds.Count -gt 0) {
    @($cases | Where-Object { $selectedIds.Contains($_.Id) })
} else {
    $cases
}
if ($selectedCases.Count -eq 0) {
    throw "No matching fit cases"
}

$results = foreach ($route in $routes) {
    if ([string]::IsNullOrWhiteSpace($route.Key)) {
        foreach ($case in $selectedCases) {
            [pscustomobject]@{
                case_id = $case.Id
                route = $route.Name
                status = "configuration_error"
                http_status = 0
                has_error = $true
                error_type = "configuration_error"
                error_code = "missing_api_key"
                error_param_present = $false
                error_param_null = $null
            }
        }
    } else {
        foreach ($case in $selectedCases) {
            Get-Result $route $case
        }
    }
}
$results | ConvertTo-Json -Depth 5
