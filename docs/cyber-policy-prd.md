# Cyber Policy Handling PRD

## Goal

Treat an upstream OpenAI-compatible `error.code == "cyber_policy"` response as a
terminal upstream policy decision. The gateway must return the upstream error,
avoid retry/failover and channel disabling, preserve any upstream usage, and
record a separate risk event after the response path completes.

## Scope

- Non-stream OpenAI-compatible error bodies, including `response.error`.
- OpenAI Responses `response.failed` and `response.error` stream events.
- Chat/Responses relay retry and automatic channel-disable decisions.
- A durable, idempotent cyber-policy audit event using the existing moderation
  log storage without entering ordinary moderation auto-ban counting.
- Optional session blocking is specified for a follow-up change; this slice does
  not invent a session identifier or block requests without one.

## Non-goals

- Applying upstream cyber-policy semantics to local redemption-code redemption.
- Treating cyber-policy as a content-moderation provider result.
- Disabling users, channels, credentials, or triggering failover.
- Persisting complete upstream response bodies or secrets.

## Acceptance criteria

1. A case-insensitive top-level or `response.error` code of `cyber_policy` is
   detected and marked once per request.
2. The original upstream error message and status are returned to the client.
3. A marked error is never retried, failed over, or used to disable a channel.
4. Existing pre-consumption is settled against parsed upstream usage when usage
   is available; otherwise the normal refund behavior remains explicit.
5. A cyber event is written at most once per request and uses action
   `cyber_policy`; it is excluded from ordinary violation counts.
6. Raw bodies are bounded and never logged without the existing redaction path.
7. Tests cover top-level/response-nested detection, retry/disable guards, and
   cyber-event exclusion from violation counting.

## Risks

- A stream may already have committed HTTP 200 before `response.failed`; the
  client receives the standard stream error event and the request remains
  non-retryable.
- If an upstream failed event has no usage, the gateway cannot infer billing
  usage safely and must not fabricate a paid usage record.
- Existing moderation tables are reused only as storage; cyber events must be
  excluded from ordinary content-moderation enforcement.
