# Stream Termination Hardening

## Goal

Prevent real upstream stream failures from being finalized as successful OpenAI-compatible streams while preserving upstream-compatible clean EOF behavior.

## Scope

- OpenAI-compatible chat/completions stream handling in `OaiStreamHandler`.
- Production mitigation for the active `claude-opus-5` channel 76 retry storm.
- No protocol changes for Responses, native Claude, Gemini, image, or task relays unless verification proves they share the same defect.

## Required Behavior

- `[DONE]`, clean EOF, and handler stop remain compatible normal terminations.
- `scanner_error`, timeout, client disconnect, panic, and ping failure are incomplete terminations.
- An incomplete termination must not emit a synthetic `[DONE]` or terminal usage event.
- If no upstream data was received and no downstream bytes were committed, return HTTP 502 `server_error` and allow normal channel retry policy.
- Once upstream data was received, return a skip-retry error so a provider-billed partial generation is not duplicated on another channel. If downstream bytes were already committed, the connection closes without a false success marker.
- Usage already reported by the upstream remains available for settlement. When the provider omits terminal usage, bill the observed input and partial output using the existing local estimation path. The consume log must retain `stream_status=error` and its end reason.
- Partial tool calls retain their configured per-call surcharge, and audio-token streams retain the existing audio settlement path.
- A received stream with zero measurable tokens still records an incomplete consume log and settles at the supported zero-usage amount instead of silently disappearing into the generic refund path.
- Upstream error SSE events and empty-output rejection retain their current behavior.

## Production Mitigation

- Confirm whether the single-token retry storm is still active before changing state.
- Prefer the narrowest reversible mitigation that stops further billed failures.
- Back up affected channel/token rows before any database write.
- Do not expose or copy credentials.
- Verify the mitigation from shared PostgreSQL and all serving nodes.

## Acceptance Criteria

- A stream reader that returns partial content followed by a scanner error returns a skip-retry `server_error`, preserves `scanner_error`, settles observed billable usage, and does not emit `[DONE]`.
- Partial tool-call and audio-token failures preserve their existing billing modifiers.
- A zero-token partial stream remains auditable, while a reader error before any upstream data remains retryable and refundable.
- Clean EOF after partial content remains successful and emits `[DONE]`.
- Explicit `[DONE]` remains successful.
- Existing upstream-error and empty-output tests continue to pass.
- Focused relay tests, full root Go tests, `go vet ./...`, and `git diff --check` pass.
- The release is source-built, digest-pinned, staged across all four production nodes, and verified through both public API routes.

## Risks

- HTTP status cannot be changed after streamed bytes are committed; clients detect failure from the missing terminal marker and connection close.
- Retrying after any upstream data can duplicate provider charges even when the one-chunk output buffer has not committed client bytes, so observed partial failures must remain skip-retry.
- Transport changes on channel 76 may affect unrelated successful traffic and require an observed A/B result before becoming permanent.
