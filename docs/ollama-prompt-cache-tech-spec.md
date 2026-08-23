# Ollama Prompt Cache Estimation

## Problem

OpenCode-compatible chat requests commonly omit `prompt_cache_key`, `metadata.user_id`, `user`, and session headers. Channel affinity still works for these requests because its established fallback is `token_id`, then user ID. Ollama prompt-cache estimation must use the same request partition when an explicit conversation identifier is absent, otherwise it exits before storing the first prompt.

## Contract

- Explicit body identifiers keep precedence: `prompt_cache_key`, `metadata.user_id`, then `user`.
- Header identifiers remain the next fallback: `session_id`, `conversation_id`, and supported OpenCode aliases.
- When no explicit identifier exists, use `token_id`; if unavailable, use authenticated user ID.
- Prefix the fallback values (`token:` / `user:`) so they cannot collide with client-provided identifiers.
- Keep existing isolation by channel, persisted credential, multi-key position, upstream model, relay mode, authenticated user, and system/tool settings.
- Match a previous message prefix when the current request appends messages or
  replays the same prompt, with a five-minute TTL and upstream `cached_tokens`
  precedence. An upstream `cached_tokens` value greater than zero is authoritative;
  an explicit zero is treated as an upstream miss so the channel-level estimator
  can still provide a simulated cache value.
- Keep a bounded set of recent prompt chains per cache partition instead of one
  mutable chain. Select the longest matching prefix, deduplicate exact chains,
  and update the set atomically when Redis is enabled so interleaved requests do
  not overwrite one another.
- Cache read/write failures are fail-open for the request but must be returned by
  the cache helper and logged with channel/model metadata; prompt content and
  credentials must never be logged.

## Acceptance

- A request without a client session field stores an Ollama prompt-cache entry.
- A follow-up request with the same token fallback and a message prefix (including
  an exact replay) receives estimated `cached_tokens` and estimated billing usage.
- Different token IDs and users do not share entries.
- Explicit body/header identifiers still take precedence over fallback values.
- Interleaved prompt chains in one token fallback partition retain independent
  candidates and match the longest safe prefix without sharing across tokens.
- A response with `cached_tokens: 0` stores state and can receive estimated
  billing on a matching follow-up; a positive upstream value remains unchanged.
- Redis update conflicts retry atomically, and Redis read/write failures do not
  panic, block the request, or silently disappear from operational logs.
- Focused Ollama tests, race tests, `go vet`, RelayKit build, and `git diff --check` pass.
