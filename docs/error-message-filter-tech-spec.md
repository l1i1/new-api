# Relay Error Message Privacy Filter Technical Specification

## Configuration Contract

| Option key | Type | Default |
| --- | --- | --- |
| `ErrorMessageFilterEnabled` | boolean | `true` |
| `ErrorMessageFilterPattern` | Go RE2 expression | `(?i)\s*\(providers=[^)]*\)|\s*providers=[^()\r\n]*[^\s()]|\s*\(console go\)` |

The pattern is a remove-only expression. Every match is replaced with the
empty string. The default expression removes both a parenthesized
`(providers=..., model=...)` segment and an unparenthesized `providers=...`
segment, stopping before adjacent parentheses such as the local request ID;
it also removes the `(Console Go)` marker including its leading space.

The option API stores the exact administrator-provided pattern. The backend
validates it with `regexp.Compile` before writing it. Runtime compilation is
also guarded so an invalid value loaded from an older or manually edited
database cannot panic the relay path.

## Request/Response Flow

1. An upstream relay failure is parsed into `NewAPIError` as today.
2. The existing controller error defer keeps the original error for logging.
3. Immediately before creating the client response, the configured filter is
   applied to the error message.
4. The local request ID is appended after filtering, ensuring the configured
   pattern cannot remove or alter the local diagnostic ID.
5. The filtered message is returned through the existing OpenAI, Claude, or
   realtime error shape.

`MessageWithRequestId` remains the final request-id formatter. The
`NewAPIError.SetMessage` path synchronizes the serialized relay error message
with the filtered message so OpenAI/Claude error variants cannot retain a stale
upstream message.

## Backend Ownership

- `setting/operation_setting/error_message_filter.go` owns option keys,
  defaults, validation, and runtime filtering.
- `model/option.go` publishes defaults, validates updates, and applies changes
  to the process-local runtime state.
- `controller/relay.go` applies filtering only at the outbound relay error
  boundary.

## Admin UI

The Operations settings page adds an `Error message privacy` section with:

- A switch bound to `ErrorMessageFilterEnabled`.
- A multiline expression field bound to `ErrorMessageFilterPattern`.
- A short note that matches are removed from client-facing relay errors while
  server diagnostics remain unchanged.

The section uses the existing `/api/option/` query/mutation and follows the
existing settings form/reset/error handling patterns.

## Verification

- Go unit tests for default matching, disabled behavior, custom expressions,
  malformed expressions, and option validation.
- Relaykit error-payload tests prove the serialized OpenAI/Claude error uses
  the filtered message; the controller logs before applying the filter.
- Frontend typecheck, focused lint, i18n synchronization, and production
  build.
