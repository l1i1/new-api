# Content Moderation Production Rollout Runbook

Status: code deployed with moderation disabled, 2026-08-20

This runbook covers the production release and staged enablement of the
affinity-aware multimodal content moderation gate. It does not authorize a
production deployment. A release operator must approve each external action
separately.

## Release decision

The implementation is safe to release with the gate disabled, then enable in
progressive stages. Do not start with broad `pre_block`, email notification, or
automatic account disablement.

The reviewed moderation implementation and release preparation were merged to
`tokeness/main` at commit `23a2fec86facd375d6cc0fa2480a26dbbe5c9e20` and
deployed with the feature disabled. Provider configuration and staged feature
enablement remain subject to the gates in this runbook.

Production deployment record:

- source commit: `23a2fec86facd375d6cc0fa2480a26dbbe5c9e20`;
- version: `v1.0.0-rc.25-tokeness-moderation.1`;
- immutable digest: `sha256:5c18d5e6d5e36f84bde90e43644b24bc1afded838df3cf4f9e25e2ced342a88d`;
- baseline verify workflow: `32379322141`;
- publish workflow: `32379661916`;
- post-publish verify workflow: `32380481122`;
- staged deployment workflow: `32380644587`;
- post-deployment verify workflow: `32381279396`;
- rollback digest: `sha256:5a32198cb10a2a1fddbc09357eda617b3555dc530f6760e270749117dcaf31c5`.

The deployment completed in the documented `JP-N2`, `EV-JP`, `JP-M`,
`EV-JP2` order. All nodes selected and ran the new digest; dashboard probes
returned 200 and both CDN `/v1/models` probes returned the expected 401 with
the new version header. No provider key or moderation policy was configured,
so the feature remains disabled pending the provider, privacy, database,
Redis, SMTP, and observe-canary gates below.

The required production behavior is:

- moderation runs after typed request validation and channel selection, before
  token pricing, quota reservation, billing, and upstream forwarding;
- a stable channel-affinity conversation is audited once per selected channel
  and affinity TTL, excluding rotating multi-key credentials from the cache
  identity;
- the final eligible conversation item can contain text, HTTP(S) images, or
  `data:image/*` images; duplicate images are removed and at most one image is
  sent in the OpenAI-compatible multimodal moderation request;
- assistant/model turns and tool/function responses do not fall back to an
  older user request;
- a cached flagged decision remains blocking in `pre_block`;
- request-time moderation API, Redis, configuration-read, and audit persistence
  failures remain fail-open and produce safe request-scoped logs;
- provider concurrency is limited per moderation API key across gateway nodes
  through Redis slot leases, with a process-local limit retained during Redis
  degradation;
- capacity exhaustion is always recorded as `skipped_capacity` and remains
  fail-open in both `observe` and `pre_block`; the model request continues, and
  the event is surfaced through safe logs/metrics rather than a client error;
- no raw prompt or API key is written to the audit log, admin API, workflow
  output, or deployment log.

## Hard release gates

Stop the rollout if any gate below is unresolved.

1. **Candidate and tests**

   The candidate is merged into `tokeness/main`, the app repository is clean,
   and the full release checks pass:

   ```text
   go test ./...
   go vet ./...
   go test -race ./service -run "ContentModeration|Affinity|Email" -count=1
   go test -race ./common -run "SendEmailContext|Email" -count=1
   (cd relaykit && GOWORK=off go test ./...)
   (cd web && bun run test && bun run typecheck && bun run build)
   ```

   The publish workflow runs the full Go test suite, `go vet`, standalone
   RelayKit tests, and frontend distribution regression checks. Race tests and
   the complete frontend suite above remain additional release gates.

2. **Moderation provider boundary**

   Use a fixed HTTPS moderation endpoint with a network egress allowlist. The
   application appends `/v1/moderations` to `base_url`; do not put credentials
   in the URL query or path. Verify TLS, DNS, provider model availability,
   rate limits, timeout behavior, retry behavior, and the exact OpenAI
   moderation response contract before enabling the feature. The provider must
   accept the OpenAI multimodal input contract, including one aggregate result
   for text-plus-image and image-only requests. Verify the contract with all
   four supported protocols; a text-only smoke test is not sufficient.

   Confirm the provider's image retention, logging, training-use, and data
   residency terms. Image URLs, including query strings, and Base64 image data
   are disclosed to the provider even though they are not persisted by New
   API. If long-lived credential-bearing signed URLs are possible in the
   enabled scope, add a sanitization/proxy boundary or explicitly accept that
   disclosure in the privacy review. The application currently accepts
   client-supplied `http://` image URLs; either accept that policy or add an
   HTTPS-only restriction before enablement.

   The current configuration accepts arbitrary HTTP(S) URLs and stores API
   keys in the existing `Option` JSON value. Enter keys only through the
   HTTPS root-only admin endpoint or the authenticated security UI. Restrict
   database backups, replicas, and operator access accordingly. If a key may
   have leaked, revoke it at the provider first, then use the explicit
   `clear_api_keys` action and add the replacement through the protected UI.

3. **Database and migration**

   Back up the primary database and verify the backup is restorable. Confirm
   which production process has `NODE_TYPE != slave`; only that master process
   runs the main-database migration. `content_moderation_logs` is a main
   database table, not a `LOG_SQL_DSN` table.

   After the first upgraded node starts, verify the table, indexes, and a
   representative insert/query on the actual production database engine
   (MySQL or PostgreSQL). The additive `content_moderation_user_states` table
   stores per-user violation-reset log-ID boundaries and must also be present.
   Do not rely only on the SQLite test suite.

4. **Redis and affinity**

   All gateway nodes must use the same Redis instance with working ACLs,
   connectivity, conditional release scripts, and expiration commands. Redis
   coordinates both first-audit de-duplication and the per-key provider slot
   leases. Without Redis, each process keeps the configured local per-key
   limit, but fleet-wide concurrency can temporarily rise to the number of
   active gateway nodes multiplied by `max_in_flight_per_key`. A Redis outage
   is therefore a capacity-control incident and must be visible before broad
   `pre_block` use.

5. **Conversation semantics**

   The desired de-duplication means later user turns in the same affinity key
   are not re-audited until the affinity TTL expires. This is intentional but
   means the first decision covers the conversation window. Confirm that the
   selected affinity rule has a stable conversation identity and a practical
   TTL. If every message must be independently moderated, the affinity key
   must include a message/revision identity; that is a different product
   behavior and defeats this rollout's de-duplication goal.

6. **Protocol scope**

   OpenAI Chat, OpenAI Responses (including `/v1/responses/compact`),
   Anthropic Messages, and Gemini conversation requests are covered. OpenAI
   Images, Realtime, Audio, and other non-conversation routes remain outside
   this release. Multimodal conversation moderation is not full coverage of
   every image-capable endpoint; exclude those routes from enforcement claims.

7. **Image capacity and latency**

   Record the ingress request-body limit and the provider's supported image
   formats, dimensions, and byte limits. Define an accepted maximum for data
   URLs and the complete moderation payload, plus egress bandwidth and memory
   budgets. The service enforces a 20 MB decoded data-image limit, supports
   strict Base64 PNG/JPEG/WEBP/GIF data URLs, caps image URLs at 8 KiB, rejects
   embedded URL user info, accepts at most 16 distinct candidates, and sends at
   most one selected image. The provider may impose stricter format,
   dimension, fetch, or aggregate-payload limits; validate those separately.

   Measure image-only and mixed-request p50/p95/p99 moderation latency, retry
   rate, provider 4xx/5xx/timeout rate, and fail-open rate. Stop before
   enablement if the configured timeout and retry budget cannot bound relay
   latency under representative image sizes.

## Release and deployment sequence

### 0. Prepare the release record

Record the source commit, reviewer, release version, previous production image
digest, database backup identifier, moderation provider endpoint owner, and the
operator responsible for each approval. Use a new immutable version such as
`v1.0.0-rc.25-tokeness-moderation.1`; never use `latest`.

### 1. Merge and publish the immutable artifact

After review, merge the candidate into `tokeness/main`. The manual publish
workflow is `.github/workflows/tokeness-publish.yml` and builds
`Dockerfile.tokeness`, including the frontend CDN distribution. Capture the
resulting GHCR digest from the workflow summary. The digest, not the tag, is
the deployment input.

Example operator commands (run only by the release operator after approval):

```text
gh workflow run tokeness-publish.yml --repo l1i1/new-api --ref tokeness/main \
  -f version=v1.0.0-rc.25-tokeness-moderation.1

gh workflow run tokeness-deploy.yml --repo l1i1/new-api --ref tokeness/main \
  -f operation=verify

gh workflow run tokeness-deploy.yml --repo l1i1/new-api --ref tokeness/main \
  -f operation=deploy \
  -f image_digest=sha256:<published-digest> \
  -f confirmation=deploy-production
```

Do not place moderation keys, database URLs, or other secrets in workflow
inputs, tags, commit messages, or summaries.

### 2. Deploy code with moderation disabled

The production workflow `.github/workflows/tokeness-deploy.yml` performs a
four-node digest-pinned rollout in this order: `JP-N2`, `EV-JP`, `JP-M`, and
`EV-JP2`. Run the preflight `verify`, then deploy the captured digest with the
required `deploy-production` confirmation. The remote rollout script performs
health checks, public route checks, and automatic digest reconciliation on
failure.

Because the feature defaults to `enabled=false`, publishing the code to all
nodes does not enable moderation. Verify after deployment that:

- all nodes select and run the exact digest;
- the version header and public dashboard/API routes are consistent;
- Redis, primary DB, and SMTP connectivity are healthy;
- `GET /api/content-moderation/config` still reports `enabled=false`;
- no moderation provider calls or moderation logs are created by ordinary
  traffic while the gate is disabled.

### 3. Configure the provider without enabling traffic checks

Use the root-only endpoint `/api/content-moderation/config` over the trusted
HTTPS origin. Read the current configuration first and update the full form;
an empty `api_key` preserves existing keys, while only explicit
`clear_api_keys=true` removes them. The GET response exposes only key count and
masked suffixes.

Set and verify, before enabling traffic:

- fixed HTTPS `base_url` and an available moderation `model`;
- one or more provider keys, entered out-of-band;
- default category thresholds unless the policy owner has approved changes;
- `timeout_ms=1500` and `retry_count=1` as the initial bounded budget;
- `max_in_flight_per_key=1`, `queue_wait_ms=200`,
  `overload_status=503`, and `key_cooldown_ms=5000`;
- `email_on_hit=false` and `auto_ban_enabled=false`;
- `record_non_hits=false` unless a specific capacity/retention budget exists;
- a documented affinity rule and TTL for every canary group/model.

Never copy the redacted GET response into a script as a PUT body without
preserving the current secret through the protected UI/API behavior.

### 4. Observe canary

Start with one low-volume model and one canary group. The recommended initial
policy is:

```json
{
  "enabled": true,
  "mode": "observe",
  "sample_rate": 0.01,
  "all_groups": false,
  "group_ids": ["<canary-group-value>"],
  "all_models": false,
  "models": ["<low-volume-model>"],
  "model_filters": [],
  "record_non_hits": false,
  "email_on_hit": false,
  "auto_ban_enabled": false,
  "timeout_ms": 1500,
  "retry_count": 1,
  "max_in_flight_per_key": 1,
  "queue_wait_ms": 200,
  "overload_status": 503,
  "key_cooldown_ms": 5000,
  "block_status": 403,
  "block_message": "Request blocked by content policy",
  "ban_threshold": 10,
  "violation_window_hours": 24
}
```

Keep the provider key out of this document and out of shell history. The
admin endpoint retains an existing key when `api_key` is omitted.

Run the following canary checks using provider-approved test fixtures and
non-sensitive traffic:

- OpenAI Chat URL/data images, OpenAI Responses `input_image`, Anthropic
  URL/Base64 images, and Gemini `inlineData`/`fileData` reach the moderation
  provider and then the upstream in `observe` mode;
- text-only, text-plus-image, and image-only requests use the expected provider
  contract; multimodal requests return exactly one result and multi-image
  requests forward no more than one deduplicated image;
- an assistant/model final turn or a tool/function response does not re-audit
  an older user request;
- a flagged fixture is logged but still reaches upstream in `observe`;
- two requests with the same user/group/model/protocol/rule/channel affinity
  produce one provider call and one persisted audit decision, even if the
  later request changes text or images;
- rotating the selected multi-key credential does not create another audit;
- changing the selected channel creates an independent audit;
- Redis-backed concurrent first requests produce one lease owner across nodes;
- provider timeout, malformed response, Redis failure, and log-write failure
  are fail-open, safe-log, and retryable rather than cached as allow;
- raw text, image URLs, image Base64, keys, and credential-bearing URLs do not
  appear in application logs, audit logs, or admin responses;
- provider rejection of unsupported, oversized, or unreachable images is
  fail-open, not cached as allow, and remains within the approved latency
  budget.

Observe at least one complete traffic peak and compare provider calls with
affinity conversations, moderation latency with relay latency, flagged rate by
category/score, error rate, and database write volume.

Break image observations down by protocol and `text_only`, `mixed`, and
`image_only`, plus URL versus data URL. Track provider calls, cache hits, lease
waits, retries, 4xx/5xx/timeouts, safe payload-size buckets, fail-open rate,
and relay latency delta. The audit table does not store modality or payload
size, so use provider-side or privacy-safe application metrics without logging
raw URLs or Base64 data.

### 5. Expand observe scope

Increase `sample_rate` gradually (`0.01` → `0.1` → `0.5` → `1.0`) and expand
groups/models one step at a time. Keep `email_on_hit` and `auto_ban_enabled`
disabled. Do not proceed if provider error rate, p95 latency, Redis lease
errors, DB write latency, or unexplained flagged rate increases materially.

### 6. Pre-block canary

After observe acceptance, switch only the canary scope to `mode=pre_block` and
`sample_rate=1`. Verify with a provider-approved flagged fixture that:

- the client receives the configured 403;
- no upstream channel is contacted;
- no token pricing, quota reservation, billing, or upstream retry occurs;
- the audit log records the category, score, request metadata, and redacted
  excerpt/hash only;
- a cached flagged decision blocks a later request even if a later sample would
  have missed;
- moderation failures remain fail-open according to the documented policy.

Hold this stage through a full peak window before broadening.

### 7. Broad pre-block, notifications, and auto-ban

Expand `pre_block` only after the canary has no unresolved false-positive,
latency, or data-retention issue. Enable `email_on_hit` only after SMTP
delivery is tested with a controlled account and the notification claim state
(`email_sent`/`email_sending`) is observable.

Enable `auto_ban_enabled` last, with the approved rolling window and threshold
(default `10` flagged logs in `24` hours). Before enabling it, verify the root
unban endpoint, authentication-version fence, token/session invalidation, and
the manual process for ambiguous email claims. Keep an operator on call for
false-positive review.

Immediately stop scope expansion and set `enabled=false` if image URLs/Base64
appear in logs, provider image retention is unacceptable, multimodal 4xx/5xx
or timeout rates exceed the approved budget, image p95 latency or resource use
exceeds the release budget, image-only/data URL handling is incompatible, or
new images incorrectly reuse an allow decision under an affinity policy that
requires per-image review. There is no separate image switch; `observe` still
sends images to the provider.

## Observability and retention

The root-only log endpoint is `/api/content-moderation/logs`. Monitor at least:

- moderation provider request count, status/error count, timeout count, and
  latency;
- flagged count and category/score distribution by group, model, protocol, and
  channel;
- audit insert failures and table growth;
- Redis lease/cache errors and affinity cache hit behavior;
- `email_sent` and `email_sending` rows;
- users disabled by auto-ban, subsequent unban actions, and violation-count
   reset actions;
- upstream request count for blocked canary fixtures.

There is no built-in automatic retention job for
`content_moderation_logs`. Set a documented retention period, capacity alert,
backup policy, and access policy before enabling `record_non_hits` or broad
traffic. The stored excerpt is redacted, but it is still operational data.

## Rollback

1. **Configuration rollback:** use the root-only config endpoint to set
   `enabled=false`; if required, restore the last-known-good observe policy.
   The local policy update is immediate and other nodes refresh the shared
   database policy within the bounded refresh interval (about one second).
2. **Artifact rollback:** dispatch the deployment workflow with the previous
   known-good immutable digest. The rollout script also reconciles nodes back
   to their pre-deploy digest if a staged deployment fails.
3. **Provider key incident:** revoke the key at the provider, then explicitly
   clear stored keys and add replacements. Do not rely on a Redis cache purge.
4. **False-positive/account incident:** disable auto-ban and email first;
   review flagged logs, use the root unban endpoint, and confirm auth/session/
   relay-token invalidation before restoring traffic.
5. **Schema rollback:** do not drop `content_moderation_logs` during an
   application rollback. The migration is additive and the previous binary can
   continue to run with the table present.

After rollback, retain the audit evidence and record the triggering metric,
affected scope, exact digest, policy version, and operator action.

## Source references

- `docs/content-moderation-prd.md`
- `docs/content-moderation-tech-spec.md`
- `controller/relay.go`
- `service/content_moderation.go`
- `service/channel_affinity.go`
- `model/content_moderation.go`
- `model/main.go`
- `.github/workflows/tokeness-publish.yml`
- `.github/workflows/tokeness-deploy.yml`
- `deployment/tokeness/rollout.sh`
- `deployment/tokeness/nodes.json`
