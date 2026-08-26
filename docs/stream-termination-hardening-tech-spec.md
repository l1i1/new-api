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
- If no downstream bytes were committed, return HTTP 502 `server_error` and allow normal channel retry policy.
- If downstream bytes were already committed, return a skip-retry error so the connection closes without a false success marker.
- Usage already reported by the upstream remains available for settlement; the consume log must retain `stream_status=error` and its end reason.
- Upstream error SSE events and empty-output rejection retain their current behavior.

## Production Mitigation

- Confirm whether the single-token retry storm is still active before changing state.
- Prefer the narrowest reversible mitigation that stops further billed failures.
- Back up affected channel/token rows before any database write.
- Do not expose or copy credentials.
- Verify the mitigation from shared PostgreSQL and all serving nodes.

## Acceptance Criteria

- A stream reader that returns partial content followed by a scanner error returns `server_error`, preserves `scanner_error`, and does not emit `[DONE]`.
- Clean EOF after partial content remains successful and emits `[DONE]`.
- Explicit `[DONE]` remains successful.
- Existing upstream-error and empty-output tests continue to pass.
- Focused relay tests, full root Go tests, `go vet ./...`, and `git diff --check` pass.
- The release is source-built, digest-pinned, staged across all four production nodes, and verified through both public API routes.

## Risks

- HTTP status cannot be changed after streamed bytes are committed; clients detect failure from the missing terminal marker and connection close.
- Retrying a committed partial stream can duplicate upstream charges, so committed failures must remain skip-retry.
- Transport changes on channel 76 may affect unrelated successful traffic and require an observed A/B result before becoming permanent.
