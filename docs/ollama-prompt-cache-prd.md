# Ollama Prompt Cache Reliability

## Problem

`CN_Ollama` uses simulated prompt-cache usage because its upstream does not
reliably report cached input tokens. Production traffic proves the estimator
works for stable OpenAI chat sessions, but aggregate cache hit rate remains far
below comparable channels. Claude `/v1/messages` requests currently never enter
the estimator, conflicting session headers can select different partitions, and
token-level fallback can mix many independent conversations.

## Goal

- Reach a production weighted cached-token coverage above 95% as the final
  business target: `sum(cache_tokens) / sum(prompt_tokens)`.
- Treat 90% weighted cached-token coverage as the minimum production baseline.
  A release must not be declared healthy below that threshold.
- Report gross request hit rate and reusable-request hit rate separately. Cold
  starts remain misses and must never be hidden to improve either metric.
- Preserve correct billing isolation across users, tokens, channels, models,
  credentials, prompt families, and explicit sessions.

## Non-Goals

- Do not hardcode a target percentage as the simulated cached-token value.
- Do not fabricate cache hits for unrelated prompts or expired state.
- Do not change non-Ollama channel behavior, routing, retry, or channel priority.
- Do not send paid production probes or pre-warm production Redis for KPI gains.

## Requirements

- OpenAI chat and Claude Messages requests that produce the same Ollama chat
  prompt must use the same cache matching semantics.
- Ollama generate requests must remain isolated from chat requests and include
  both prompt and suffix in cache identity.
- Explicit body identifiers take precedence over deterministic header aliases;
  token/user fallback is used only when no explicit identifier exists.
- Token/user fallback must partition independent conversations by the first
  user message while retaining exact replay and prefix-extension hits.
- Interleaved conversations must retain a bounded set of recent prompt chains.
- A validated prefix is estimated as the complete previous prompt-token count,
  capped at the current prompt-token count. Do not apply an arbitrary discount
  to an already matched prefix.
- The final Ollama request body is authoritative. Images, explicit
  `keep_alive: 0`, and unreadable final bodies are uncacheable rather than
  falling back to a looser client request identity.
- Cache state is isolated by upstream base URL and final upstream model, and its
  TTL never exceeds either five minutes or an explicit shorter `keep_alive`.
- Redis entries store compact cumulative prefix identities so long and
  interleaved conversations do not create oversized read-modify-write payloads.
- Cache failures remain fail-open and must not block the model request.
- Production logs must distinguish estimated cache hits from upstream cache
  hits using the existing billing path.

## Acceptance

- Claude `/v1/messages` first request is a miss and a matching follow-up is an
  estimated hit.
- Equivalent OpenAI chat and Claude Messages prompts share the Ollama chat
  prompt family when their session partition is the same.
- Conflicting header aliases always resolve with a fixed priority.
- Different completion suffixes never share estimated cache state.
- Different token/user/session/credential/model partitions never share state.
- Focused tests, race tests, vet, RelayKit independent build, full Go tests, and
  `git diff --check` pass before release.
- Production validation uses complete natural-traffic windows and reports:
  gross request hit rate, weighted cached-token coverage, and reusable-session
  hit rate. Weighted coverage must reach 90% before rollout is considered
  healthy and must stabilize above 95% for the final target.
