# DeepSeek V4 Feature Probe Test Specification

Status: Partially implemented (49/85 live fixture IDs; live tiers require runtime credentials)
Owner: Tokeness New API relay
Last reviewed: 2026-08-25

## 1. Purpose

This specification defines the compatibility probe for `deepseek-v4-flash` and
`deepseek-v4-pro`. It separates three contracts that must not be conflated:

1. **Provider contract**: behavior documented by DeepSeek and observed at the
   official API.
2. **OpenAI compatibility contract**: request and response shape expected by
   OpenAI-compatible clients.
3. **Gateway contract**: Tokeness conversion, retry/failover, billing,
   observability, and primary/backup route behavior.

The probe must assert deterministic protocol behavior. It must not fail because
the model selected a different valid natural-language answer.

## 2. Sources of Truth

The provider baseline is the DeepSeek documentation current on 2026-08-24:

- [Thinking Mode](https://api-docs.deepseek.com/guides/thinking_mode)
- [Chat Completions API](https://api-docs.deepseek.com/api/create-chat-completion)
- [Multi-round Conversation](https://api-docs.deepseek.com/guides/multi_round_chat)
- [Tool Calls](https://api-docs.deepseek.com/guides/tool_calls)
- [JSON Output](https://api-docs.deepseek.com/guides/json_mode)
- [Responses API](https://api-docs.deepseek.com/guides/responses_api)
- [Token Usage](https://api-docs.deepseek.com/quick_start/token_usage)
- [Error Codes](https://api-docs.deepseek.com/quick_start/error_codes)
- [Context Caching](https://api-docs.deepseek.com/guides/kv_cache)

The current provider documentation states that Chat Completions thinking mode
uses `thinking.type` or `reasoning_effort`, returns `reasoning_content` beside
`content`, and silently ignores `temperature`, `top_p`,
`presence_penalty`, and `frequency_penalty` in thinking mode. It documents
`low/high/max` effort and maps `medium/xhigh` to `high`. The live endpoint has
also returned an `extreme` validation error with a broader valid-level list;
that difference is a compatibility regression case, not a new provider fact.

## 2a. Observed Baseline (2026-08-25)

The authorized small-sample probe compared `https://api.deepseek.com` with
Tokeness primary (`n.tokeness.dev/v1`) and backup (`n-cf.tokeness.dev/v1`).
Only status codes, error metadata, usage fields, finish reasons, and redacted
shape flags were retained.

- Authentication, model listing, basic non-streaming, streaming termination,
  thinking enabled/disabled, `reasoning_effort=none`, JSON mode, stop, logprobs,
  and `tool_choice=auto` were structurally compatible on all three routes.
- `thinking.type=disabled` produced non-empty `content` and no non-empty
  `reasoning_content` on all three routes. The local adapter therefore maps it
  to upstream `reasoning_effort=none` for V4, while preserving the raw field.
- The official endpoint rejected `reasoning_effort=extreme`, `top_p=1.5`, and
  `top_p=0` with 400 `invalid_request_error`. The local candidate now rejects
  the same V4 inputs at the request boundary instead of clamping or renaming
  them, so the gateway does not claim an extension while measuring official
  parity.
- A post-rollout recheck found the official endpoint now accepts
  `tool_choice=required` and returns a valid tool call. The earlier 400 result
  was a provider snapshot, not a durable constraint. The follow-up candidate
  therefore preserves `required` instead of rejecting it locally.
- `stream=true` without `include_usage` returned a usage event on all three
  routes. This is observable parity, not transparent passthrough parity:
  Tokeness defaults `ShouldIncludeUsage=true` when `stream_options` is omitted
  and may synthesize a final usage event if the upstream does not provide one.
- Unknown model behavior differed: official returned 400, while both Tokeness
  routes returned 503 `model_not_found`. A broad local `503 -> 400` change was
  rejected and reverted because the same selection result also represents
  disabled channels, temporary unavailability, and internal selection errors.
  The local compatibility candidate now carries typed selection outcomes:
  unknown model (`400 model_not_found`), temporary unavailability (`503`,
  public `server_error` envelope), policy denial (`403 access_denied`), and
  selection inconsistency (`500`, public `server_error` envelope). The
  detailed `get_channel_failed` code remains an internal diagnostic for the
  server-side cases. Advanced Custom path mismatches are diagnosed as no
  configured model for the requested endpoint rather than temporary capacity
  loss. This candidate is not deployed.
- The official endpoint rejected `top_logprobs` without `logprobs=true` and
  rejected `top_logprobs=21` with 400 `invalid_request_error`. The gateway now
  applies the same V4 trust-boundary validation and emits the official-shaped
  error envelope with `param: null`; valid `top_logprobs` values through 20 are
  accepted. These checks are present in the release candidate; production
  revalidation is required after the next image rollout.
- Non-stream forced response formatting preserves both
  `choices[0].logprobs.content` and `reasoning_content` arrays as received. The
  gateway never synthesizes reasoning logprobs; an upstream DFLASH capability
  error remains a typed retryable unsupported-feature result.
- The public middleware error boundary is normalized to the official
  OpenAI-compatible shape: `param` is JSON null, authentication uses
  `authentication_error` plus `invalid_request_error`, validation uses
  `invalid_request_error`, and internal `new_api_error` is not exposed. This is
  committed locally and awaits the next production image rollout.
- The supplied fit request with `messages[].name`, `reasoning_effort=low`, and
  `max_tokens=256` was isolated from the name field: both named and unnamed
  variants consumed all 256 completion tokens in reasoning and ended with
  `finish_reason=length`; raising the limit to 1024 returned final content and
  stopped after 401 completion tokens, while disabling thinking returned final
  content in 13 completion tokens. The probe therefore records
  `protocol_accepted=true` separately from `effective_success=false` when final
  content is empty.
- A focused post-rollout repeat found the complex SSE, disabled-thinking tools,
  and repeated stop cases structurally valid. The primary route had one K08
  stream with reasoning-only chunks and no terminal usage/content; the runner
  records this as `empty_final_content` rather than a successful 200. K02/K03
  and the normalized authentication envelope passed on both gateway routes.

This baseline is a compatibility snapshot, not a production-release claim. The
remaining 36 matrix cases are intentionally `inconclusive` until their live
fixtures and mock-upstream tests are implemented.

### 2b. Compliance Verdict

| Area | Verdict | Evidence / remaining risk |
| --- | --- | --- |
| Basic non-stream response | Partially aligned | Probe requires non-empty `content`; the 256-token named-message fit case is protocol-accepted but frequently has no final content after reasoning exhausts the limit |
| Streaming response | Aligned in the observed sample | Probe requires non-empty content, `[DONE]`, and a separate usage event when requested |
| Thinking disabled | Compatible in candidate and observed production sample | V4 maps `thinking.type=disabled` to `reasoning_effort=none` and preserves the raw thinking field |
| Thinking enabled / reasoning fields | Aligned in the observed sample | Multi-round thinking/tool replay is now covered by live fixtures; broader provider variance remains |
| Sampling validation | Aligned in candidate; rollout recheck pending | Official rejects `extreme`, `top_p=1.5`, and `top_p=0`; the candidate returns the same validation class |
| JSON, stop, logprobs | Aligned for observed valid and invalid cases | Probe validates JSON parsing, stop termination, usage arithmetic/cache bounds, both reasoning/content logprob arrays when supported, the `top_logprobs <= 20` bound, and official validation fingerprints |
| Tools | Follow-up candidate ready | Official currently accepts `tool_choice=required`; release `.5` still rejects it from an older probe snapshot, while the follow-up candidate preserves the value |
| Stream usage when omitted | Observable parity with different semantics | Official emitted usage; Tokeness may preserve or synthesize it because omission defaults to include |
| Unknown model | Production not aligned; local candidate ready | Official 400 versus current production 503; local typed classification maps only truly unconfigured models to `400 model_not_found` and keeps unavailable/inconsistent selection failures at 503/500 with the public server-error envelope |
| Capability failover | Locally verified | DFLASH unsupported-logprob errors are retryable, queryable as `channel:unsupported_feature`, and do not auto-disable the channel |
| Logging safety | Locally hardened | Debug and direct response-body diagnostics are credential-masked and bounded to 2 KiB; no live response bodies are persisted by the probe |
| Full matrix / release gate | Not complete | 36 case IDs, including live failover and single-charge billing assertions, remain inconclusive; full official/gateway paired runs still require controlled runtime credentials |

## 3. Environment and Safety

The runner uses environment variables only. No key, cookie, full request body,
or response body may be committed, printed, or stored in evidence.

```text
DEEPSEEK_API_KEY       # official provider probe; required only for live tier
DEEPSEEK_BASE_URL      # default: https://api.deepseek.com
NEW_API_BASE_URL       # e.g. https://n.tokeness.dev/v1
NEW_API_BACKUP_URL     # e.g. https://n-cf.tokeness.dev/v1
NEW_API_KEY            # gateway test token, supplied by the test environment
FEATURE_PROBE_RUN_ID   # non-secret correlation ID
```

Live execution rules:

- Use one short, deterministic prompt per case and at most one request in
  flight per provider key.
- Do not run live probes in image-publish or deployment workflows.
- Use a separate disposable gateway token with a bounded quota.
- Never retry an official provider request automatically; gateway retry cases
  use a mock upstream unless the case explicitly says otherwise.
- Store only case ID, route label, status code, error code, model, latency,
  usage numbers, finish reason, and redacted structural fields.

## 4. Result Model

Each case emits one record:

```json
{
  "case_id": "DS-B02",
  "tier": "official-live",
  "surface": "chat-completions",
  "route": "official",
  "status": "pass",
  "http_status": 200,
  "error_code": "",
  "evidence": {
    "finish_reason": "stop",
    "usage": true,
    "prompt_tokens": 0,
    "completion_tokens": 0,
    "total_tokens": 0,
    "has_content": true,
    "has_reasoning_content": false
  }
}
```

Allowed statuses:

- `pass`: all assertions passed.
- `expected_unsupported`: the provider explicitly rejects an optional,
  capability-gated feature and the gateway records it as such.
- `doc_drift`: live behavior differs from the documented provider contract.
- `fail`: gateway contract or a required provider contract is broken.
- `inconclusive`: timeout, quota exhaustion, or provider outage prevented a
  meaningful assertion.

Natural-language output is compared only for exact protocol fixtures such as
the `stop` prefix and JSON validity. Otherwise assertions are structural.

## 5. Test Tiers

| Tier | Target | Purpose | Network |
| --- | --- | --- | --- |
| T0 | Go/RelayKit unit tests | DTO normalization, error classes, logprobs and usage preservation | none |
| T1 | Mock upstream + gateway | Retry, multi-key rotation, channel exclusion, billing and redaction | local |
| T2 | Official API | Provider behavior and documentation drift | official API |
| T3 | Tokeness main/backup routes | End-to-end OpenAI compatibility and CDN route parity | staging or production canary |
| T4 | Production acceptance | Repeat only the P0/P1 subset on both public routes | controlled live |

T0/T1 are blocking for code changes. T2 is required before claiming provider
compatibility. T3/T4 are required before a production release claim.

## 6. Deterministic Fixtures

The runner should reuse these fixtures rather than inventing prompts per case.

The redacted runner is `scripts/feature-probe`. It always emits all 85 case
IDs, currently implements 45 live fixture IDs, performs only the live cases for
which runtime environment variables are present, and marks missing cases
`inconclusive` rather than fabricating a pass. It never writes request bodies,
response bodies, or credentials.

| Fixture | Purpose |
| --- | --- |
| `basic_math` | `1+1=?`; short non-stream response |
| `stop_sequence` | `Say: apple, banana, orange, watermelon.` with `stop: banana` |
| `weather_tool` | One `get_weather(city)` function and a Beijing question |
| `json_object` | Explicit instruction to return `{"answer": 2}` |
| `reasoning_math` | A short arithmetic comparison that produces reasoning and content |
| `multi_turn_plain` | Two turns without `tools` |
| `multi_turn_tool` | User -> assistant tool call -> tool result -> assistant |
| `logprob_text` | Short fixed text with `logprobs=true` |
| `context_prefix` | Repeated long-enough prefix for cache capability probes |

## 7. Provider Feature Matrix (T2)

### 7.1 Discovery and Authentication

| ID | Request | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-A01 | `GET /models` with a valid key | 200; response has `data`; advertised V4 model is present when enabled | P0 |
| DS-A02 | `GET /models` without `Authorization` | 401; OpenAI-shaped error; no internal detail | P0 |
| DS-A03 | Chat request with a syntactically invalid key | 401; no retry in the official runner | P0 |
| DS-A04 | Unknown model | Provider 400/422 according to the live error contract; error is not treated as a successful empty response | P1 |
| DS-A05 | Valid request over HTTPS | TLS succeeds; no redirect to an unauthenticated HTTP endpoint | P0 |

### 7.2 Chat Completions Baseline

| ID | Request | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-B01 | Non-stream `basic_math` | 200; `object=chat.completion`; one choice; assistant `content` exists; `finish_reason` is present | P0 |
| DS-B02 | `stream=true`, `stream_options.include_usage=true` | Valid SSE chunks; deltas preserve order; one final usage chunk with empty `choices`; terminator is `data: [DONE]` | P0 |
| DS-B03 | `stream=true` without `include_usage` | Valid SSE and `[DONE]`; record whether the provider emits a usage event despite the option being omitted. The gateway must not fabricate or overwrite provider usage | P1 |
| DS-B04 | Non-stream usage | `total_tokens = prompt_tokens + completion_tokens`; nested cache usage, when present, is non-negative and not greater than prompt tokens | P0 |
| DS-B05 | `messages` with system, user, assistant, and tool roles where applicable | Roles and text survive round-trip; no role is silently dropped or reordered | P0 |
| DS-B06 | Same request twice with the same `seed` when supported | Shape and finish reason remain valid; exact text equality is advisory only | P2 |
| DS-B07 | `user_id` containing only permitted characters and a boundary-length value | Accepted and not echoed into sensitive logs; invalid characters are rejected or documented | P1 |
| DS-B08 | Unknown request field | Provider behavior is recorded; gateway must not leak the field to unrelated adapters | P2 |

### 7.3 Thinking and Reasoning

| ID | Request | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-C01 | Omit thinking controls | Provider default is recorded; for V4 the response is classified as thinking/non-thinking from fields, not guessed from text | P0 |
| DS-C02 | `thinking: {"type":"enabled"}` | 200; `reasoning_content` is a sibling of `content`; both fields are preserved by the gateway | P0 |
| DS-C03 | `thinking: {"type":"disabled"}` | 200; no non-empty reasoning channel is synthesized by the gateway | P0 |
| DS-C04 | `reasoning_effort` values `low`, `medium`, `high`, `xhigh`, `max` | Record provider mapping; current docs require `medium/xhigh -> high`; no value is silently renamed in the official evidence | P0 |
| DS-C05 | `reasoning_effort=none` and `auto` | Record whether the current endpoint accepts these observed compatibility values; classify undocumented acceptance as `doc_drift`, not as a crash | P1 |
| DS-C06 | `reasoning_effort=extreme` | Direct provider and gateway both return the official-shaped 400 validation envelope; no V4-only normalization is applied | P0 |
| DS-C07 | Thinking plus `temperature`, `top_p`, `presence_penalty`, `frequency_penalty` | Request succeeds and output remains structurally valid; paired output comparison is non-blocking because model sampling is nondeterministic | P0 |
| DS-C08 | Two-turn thinking conversation without `tools`, replaying `reasoning_content` | Provider accepts the request; prior reasoning may be ignored as documented; gateway keeps message shape valid | P0 |
| DS-C09 | Two-turn thinking conversation without `tools`, omitting prior `reasoning_content` | Provider accepts the request; gateway must not require a field the provider says is optional | P0 |
| DS-C10 | Thinking conversation with `tools`, replaying every assistant `reasoning_content` | Provider accepts the tool round-trip; reasoning and tool call fields remain separate | P0 |
| DS-C11 | Thinking conversation with `tools`, omitting prior `reasoning_content` | Provider returns the documented 400-style validation; gateway must expose a client error and must not retry another channel | P0 |
| DS-C12 | V4 model suffix aliases `-none` and `-max` through gateway | Gateway converts only the V4 alias, strips the suffix upstream, and sets the corresponding thinking control | P1 |

### 7.4 Sampling, Limits, Stop, and JSON

| ID | Request | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-D01 | `temperature` values 0, 1, and 2 | Boundary values follow the provider schema; no value is changed for non-thinking requests | P1 |
| DS-D02 | `top_p` values 0.000001, 0.5, and 1 | Valid values are preserved | P0 |
| DS-D03 | `top_p=1.5` against the official API | Record the provider 400/422 validation response; it is a provider-boundary case | P0 |
| DS-D04 | `top_p=1.5` through gateway V4 | Gateway returns the official-shaped 400 validation envelope; the invalid value is not dispatched | P0 |
| DS-D05 | `top_p=0` and a negative value through gateway V4 | Gateway returns the official-shaped 400 validation envelope; invalid values are not dispatched | P1 |
| DS-D06 | `max_tokens=1` with a longer answer | `finish_reason=length`; partial content is valid and usage remains arithmetic-consistent | P0 |
| DS-D07 | `max_tokens` omitted, zero, negative, and an excessive value | Invalid values are rejected at the trust boundary; omitted value is not rewritten to an arbitrary limit | P0 |
| DS-D08 | `stop` as a string | Output stops before the sequence; `finish_reason=stop` | P0 |
| DS-D09 | `stop` as a string array, including two sequences | At most 16 sequences are accepted; ordering and stop semantics are preserved | P1 |
| DS-D10 | `response_format={"type":"json_object"}` with an explicit JSON instruction | Content parses as JSON; no markdown fence is required or synthesized | P0 |
| DS-D11 | Invalid `response_format` type or missing JSON instruction | Provider/gateway returns a clear 400-style error or documented behavior; no infinite whitespace stream | P1 |
| DS-D12 | Non-thinking `presence_penalty` and `frequency_penalty` | Values survive conversion when the provider supports them; no cross-model leakage | P2 |

### 7.5 Logprobs

| ID | Request | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-E01 | Non-thinking `logprobs=true`, `top_logprobs=5` | 200; `choices[0].logprobs.content` exists; each position has at most five top entries | P0 |
| DS-E02 | Thinking with `logprobs=true`, `top_logprobs=5` | If supported, both `content` and `reasoning_content` logprob arrays are non-empty and bounded by five; otherwise classify the provider capability error | P0 |
| DS-E03 | `top_logprobs` values 0, 1, 5, and 20 | Accepted values do not exceed the requested bound; response shape remains valid | P1 |
| DS-E04 | `top_logprobs=5` without `logprobs=true` | Provider and gateway return the official 400 validation envelope; gateway does not fabricate logprobs | P1 |
| DS-E05 | Streaming logprobs | Logprob deltas are parseable and preserved; final usage does not overwrite real usage | P0 |
| DS-E06 | DFLASH capability response: HTTP 400 with `return_logprob` message | Classified as `channel:unsupported_feature`; retryable across channels; channel is not auto-disabled; original message remains diagnosable | P0 |
| DS-E07 | `logprobs=true`, `top_logprobs=21` | Provider and gateway return the official 400 validation envelope with the `[0, 20]` range fingerprint | P1 |

### 7.6 Tools and Multi-turn Tool Calls

| ID | Request | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-F01 | `tools` with `tool_choice=auto` | Model may return text or a function call; both are valid and structurally distinct | P0 |
| DS-F02 | `tool_choice=required` | Official provider and gateway both accept the request and return a valid function call with parseable JSON arguments | P0 |
| DS-F03 | Named function tool choice | Requested function name is selected | P1 |
| DS-F04 | `tool_choice=none` | No tool call is returned | P1 |
| DS-F05 | Malformed function schema | Clear client error; no partial billing or retry to a different channel | P1 |
| DS-F06 | Non-thinking tool result round-trip | `tool_call_id`, arguments, and tool result survive; final answer has a valid stop reason | P0 |
| DS-F07 | Thinking tool result round-trip with reasoning replay | Provider accepts the required reasoning context and returns a valid final answer | P0 |
| DS-F08 | Thinking tool round-trip without reasoning replay | Provider validation error is returned; gateway classifies it as a request error, not a credential failure | P0 |
| DS-F09 | Streaming tool call | Fragmented name/arguments reassemble into valid JSON; terminal finish reason is `tool_calls` or provider equivalent | P0 |
| DS-F10 | Multiple tools or parallel calls when advertised | Calls are uniquely identified and not duplicated during conversion or billing | P1 |

### 7.7 Optional Provider Capabilities

These cases run only when the selected model appears in the provider's
capability list. An unadvertised capability is `expected_unsupported`, not a
release failure.

| ID | Capability | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-G01 | Responses non-stream | `response` object, output item types, usage, and status are valid | P1 |
| DS-G02 | Responses stream | Semantic events are ordered and end with `response.completed`, `response.incomplete`, or `response.failed`; do not require `[DONE]` | P1 |
| DS-G03 | Responses reasoning output | Reasoning events and output text events remain distinguishable | P1 |
| DS-G04 | Responses function call | Function-call arguments reassemble and `function_call_output` can continue the conversation | P1 |
| DS-G05 | Vision model | A small fixed image fixture returns a valid multimodal response; no image data is logged | P2 |
| DS-G06 | Context caching | Repeated prefix probe reports only documented cache fields; cache hit is advisory, not a correctness requirement | P2 |
| DS-G07 | Anthropic-compatible endpoint | Thinking and tool fields map to the documented Claude envelope; no OpenAI SSE terminator is assumed | P2 |

## 8. Gateway Contract Matrix (T0/T1/T3/T4)

### 8.1 Request and Response Fidelity

| ID | Assertion | Severity |
| --- | --- | --- |
| DS-H01 | V4-only `extreme` | V4 returns the official-shaped 400 validation envelope; legacy DeepSeek models and unrelated vendors remain unchanged | P0 |
| DS-H02 | V4 invalid `top_p` | V4 returns the official-shaped 400 validation envelope for `top_p > 1` and `top_p <= 0`; omitted `top_p` stays omitted | P0 |
| DS-H03 | `reasoning_content`, `content`, `tool_calls`, `stop`, `logprobs`, and `usage` are preserved by forced response formatting | P0 |
| DS-H04 | Stream usage received before the final event is authoritative; no local estimate overwrites non-zero provider usage | P0 |
| DS-H05 | Explicit zero/false request values are retained when the upstream DTO uses optional fields | P1 |
| DS-H06 | `/v1/chat/completions` conversion is used for OpenAI-compatible channels; Gemini/Vertex conversion is not bypassed by global passthrough | P0 |
| DS-H07 | `/v1/responses` uses Responses event conversion and does not reuse Chat Completions `[DONE]` assumptions | P1 |

### 8.2 Retry, Key Rotation, and Channel Failover

The distinction is based on the error source, not HTTP status alone:

- **Credential-like upstream errors**: 401, 403, and 429 rotate an untried
  key on the same multi-key channel. They do not consume the global channel
  retry slot.
- **Channel/upstream errors**: upstream 400, 5xx, unsupported endpoint, and
  DFLASH unsupported feature exclude the current channel and consume one
  channel retry before selecting the next channel.
- **Local request errors**: gateway validation 400, policy denial, and malformed
  client input do not retry another channel.

| ID | Injected outcome | Required assertion | Severity |
| --- | --- | --- | --- |
| DS-I01 | First key returns 401, second key succeeds | Same channel; key rotates; global retry counter stays unchanged | P0 |
| DS-I02 | First key returns 403, second key succeeds | Same as DS-I01; no automatic channel disable | P0 |
| DS-I03 | First key returns 429, second key succeeds | Same channel; `Retry-After` is retained when exposed; no global slot consumed | P0 |
| DS-I04 | All keys return credential-like errors | Channel is exhausted once, then next eligible channel is selected | P0 |
| DS-I05 | Upstream 400 from channel A, channel B succeeds | Channel A is excluded; next channel is selected; retry count increments once | P0 |
| DS-I06 | Upstream 500/503 from channel A, channel B succeeds | Same as DS-I05; no reuse of excluded channel in the same request | P0 |
| DS-I07 | DFLASH logprob 400 from channel A, channel B succeeds | Channel capability error fails over; channel A is not auto-disabled | P0 |
| DS-I08 | Local malformed request 400 | No upstream call and no channel retry | P0 |
| DS-I09 | Affinity `skip_retry_on_failure=true` plus 401/403/429 on a multi-key channel | Key rotation remains allowed; affinity does not suppress credential retry | P0 |
| DS-I10 | Locked task plus upstream 400/500 or exhausted keys | Lock is released/excluded before next channel; no endless reuse of the locked channel | P0 |
| DS-I11 | Channel already excluded by the current request | Affinity selection skips it; no duplicate attempt | P1 |

### 8.3 Billing, Logs, and Security

| ID | Assertion | Severity |
| --- | --- | --- |
| DS-J01 | A request succeeds after one or more failed attempts | Exactly one user charge and one successful usage settlement | P0 |
| DS-J02 | Failed upstream attempts return malformed or absent usage | No negative, duplicate, or estimated charge from the failed attempt | P0 |
| DS-J03 | Retry metadata | Attempt count, selected channel IDs, key credential IDs, and error classes are auditable without raw keys | P0 |
| DS-J04 | Error logging | Prompts, Authorization headers, tool arguments containing user data, and full upstream bodies are redacted/truncated | P0 |
| DS-J05 | DFLASH and unsupported endpoint errors | Error code is queryable as a capability failure and does not disable a healthy channel globally | P1 |
| DS-J06 | Client disconnect or upstream reset after partial output | Stream ends as an error, partial billing follows the existing policy, and no assistant cache candidate is committed as a success | P1 |

## 9. Route Parity and Production Acceptance

Run the P0 subset `DS-B01`, `DS-B02`, `DS-C02`, `DS-C03`, `DS-C06`, `DS-D04`,
`DS-D08`, `DS-E01`, `DS-E02`, `DS-E06`, `DS-F02`, `DS-F06`, `DS-H01`-`DS-H04`,
and `DS-J01`-`DS-J04` against each route below:

| Route | Expected |
| --- | --- |
| Official provider | Provider contract baseline |
| Tokeness main API | Gateway contract and OpenAI shape |
| Tokeness backup API | Same observable contract as main API |

For each route, run the P0 set three consecutive times. A single transient
provider `length` or timeout result is `inconclusive` until the case is repeated;
three consistent failures are a release blocker.

## 10. Pass/Fail Rules

The release is accepted only when:

1. All T0/T1 P0 cases pass.
2. Official P0 cases either pass or have an explicit, documented
   `doc_drift`/`expected_unsupported` result with an owner and mitigation.
3. Main and backup routes have identical response-shape assertions for the P0
   set.
4. No P0 case leaks a credential or charges more than once.
5. Retry evidence proves credential-like errors rotate keys while upstream
   400/5xx/capability errors advance channels.
6. `git diff --check`, focused Go tests, RelayKit standalone tests, and the
   probe parser's own unit tests pass.

## 11. Implementation Order

1. Add/adjust T0 deterministic tests for normalization, response logprobs,
   usage, error classification, and retry state.
2. Add T1 mock-upstream tests for key rotation, channel exclusion, locked tasks,
   billing, and redaction.
3. Build a read-only T2 runner driven by the fixtures and environment variables.
4. Run T2 against the official endpoint and store only redacted result records.
5. Run the P0 T3/T4 route subset, then update this document with the observed
   provider version, route, and result summary.

No production deployment is part of this specification. A code or deployment
change requires a separate approval and the existing digest-pinned release
workflow.
