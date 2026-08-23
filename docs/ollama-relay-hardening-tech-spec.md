# Ollama Relay Hardening Tech Spec

## Scope

Harden the existing Ollama channel implementation without changing channel routing,
retry policy, automatic channel disablement, model priority, or configuration UI.

Covered relay formats and operations:

- OpenAI Chat Completions (`/v1/chat/completions`)
- OpenAI Completions (`/v1/completions`)
- Anthropic Messages compatibility (`/v1/messages`)
- OpenAI Embeddings (`/v1/embeddings`)
- Ollama model list and pull operations

Unsupported relay formats must fail explicitly with a non-retryable client error.

## Required Behavior

### Response contracts

- Chat Completions returns `chat.completion` and `chat.completion.chunk` payloads.
- Completions returns `text_completion` payloads with `choices[].text`.
- Anthropic Messages returns native Messages JSON and SSE events.
- Client-visible usage frames respect `stream_options.include_usage`; internal usage is
  always retained for billing.

### Stream integrity

- An Ollama top-level `error` is an upstream failure even when HTTP status is 200.
- A successful stream must contain a terminal `done` frame.
- JSON decode, scanner, or downstream write failures terminate the relay as errors.
- Client cancellation propagates through the upstream request context.
- Once an SSE response is committed, controller error handling must not append a
  second non-stream JSON response.
- Incomplete streams do not write response cache candidates or settle as success.

### Billing and cache safety

- Tool calls increment the existing billable function-call counter.
- Upstream token counts are normalized and combined with saturating arithmetic.
- Prompt cache identity includes all request fields that affect Ollama prompt state.
- Response candidates use the same Ollama message representation as later requests.
- `keep_alive: 0` invalidates cached state; shorter keep-alive values constrain Redis TTL.

### Input and operational safety

- Unsupported structured content and malformed tool-call arguments return explicit
  validation errors instead of being silently discarded or replaced.
- Ollama conversion validation failures return `400 invalid_request` without retry.
- `/v1/completions` accepts one prompt and returns `400 invalid_request` for multiple
  prompts instead of concatenating them.
- Model pull helpers do not mutate shared HTTP clients and validate body-level status.
- Model list, pull, delete, and version requests inherit caller cancellation; pull and
  version keep their operation-specific upper timeouts.
- Upstream response logging uses masking and bounded previews.

## Verification

- Focused Ollama relay and cache tests, including protocol and failure cases.
- Focused public request cancellation tests.
- Race tests for Ollama and cache paths.
- `go test ./...`, `go vet ./...`, RelayKit standalone build, and `git diff --check`.

## Risks

- Strict stream completion checks can surface previously hidden upstream truncation as
  an error. This is intentional because billing an incomplete response is unsafe.
- Correcting completion and Messages response formats may expose clients that depended
  on the previous incorrect Chat Completions payloads.
- Cache-key changes cause at most one cache TTL of cold starts and prevent unsafe reuse.
