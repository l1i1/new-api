# Ollama Prompt Cache Estimation

Product requirements: `docs/ollama-prompt-cache-prd.md`.

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
- Persist each new chain as a message count plus one cumulative SHA-256 prefix
  identity. Legacy per-message hash arrays remain readable during rollout, but
  new writes stay bounded even for long conversations.
- Cache read/write failures are fail-open for the request but must be returned by
  the cache helper and logged with channel/model metadata; prompt content and
  credentials must never be logged.
- Uncacheable outcomes carry a cause (`no_user`, `keep_alive_zero`,
  `unreadable_body`, `empty_messages`) so production logs explain why a request
  was excluded from estimation.
- Normalize Claude `/v1/messages` through the existing Claude-to-OpenAI
  converter before matching. OpenAI chat and Claude Messages belong to the
  Ollama chat prompt family; `/v1/completions` uses a separate generate family.
- Resolve session header aliases in a fixed order. Go map iteration order must
  never choose the cache partition.
- When token/user fallback is required, include the first user-message hash in
  the partition so unrelated conversations using one API token do not evict or
  overwrite each other.
- Completion prompt normalization must match `openAIToGenerate` and include the
  suffix. Semantically different generate requests must not share cache state.
- Final Ollama chat messages are hashed per message with images reduced to
  content digests: identical screenshots keep the chain stable for prefix
  matching while raw image data is never serialized into keys, candidates, or
  logs. An explicit `keep_alive: 0` or an unreadable final body is uncacheable.
  Explicit shorter keep-alive values cap the Redis TTL; negative keep-alive
  values retain the normal five-minute bound.
- The partition includes the normalized channel base URL and final Ollama model
  so a channel endpoint or model override cannot reuse stale simulated state.
- A completed chat response may add a separate assistant-prefix candidate using
  the normalized final Ollama message. Its token count is prompt plus output
  tokens, but it is only reusable when the assistant content, tool calls, and
  thinking fields match exactly; generation responses never add this candidate.
- Candidate reads are snapshotted before the upstream request. The response
  path may commit new candidates, but cannot make a candidate created during
  the request count as a hit for that same request.
- Legacy Redis payloads remain decodable, but the expanded partition key starts
  a bounded cold-cache window after rollout; no cross-partition migration is
  attempted.

## Metrics

- `gross_request_hit_rate = hit_rows / all_usage_rows` includes cold starts.
- `gross_cached_token_rate = sum(cache_tokens) / sum(prompt_tokens)` is the
  production release metric. It must reach 90% as the baseline and stabilize
  above 95% as the final target.
- `reusable_session_hit_rate` measures follow-up requests with a stable cache
  partition and reusable prefix.
- Gross request hit rate and reusable-session hit rate remain separate
  diagnostic metrics. They must not replace or inflate the weighted token
  coverage target.
- A validated prefix contributes its full previous prompt-token count, capped
  at the current prompt-token count. The resulting token rate is therefore
  determined by actual prefix growth rather than a fixed percentage chosen to
  make a chart reach a target.

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
- Claude Messages exact replay and prefix extension receive the same estimator
  behavior as OpenAI chat.
- Conflicting header aliases resolve deterministically.
- A conversation whose sent messages contain identical screenshots receives
  estimated cache hits through the digest chain; a changed or removed image no
  longer matches.
- Generate requests with equal normalized prompts and suffixes can match;
  different suffixes remain isolated.
- Focused Ollama tests, race tests, `go vet`, RelayKit build, and `git diff --check` pass.
