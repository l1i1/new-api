# Content Moderation Production Rollout Runbook

Status: release planning, 2026-08-20

This runbook covers the production release and staged enablement of the
affinity-aware content moderation gate. It does not authorize a production
deployment. A release operator must approve each external action separately.

## Release decision

The implementation is safe to release with the gate disabled, then enable in
progressive stages. Do not start with broad `pre_block`, email notification, or
automatic account disablement.

The current candidate is commit `22cdb25b5bd172c295205b20e60ae72c19d7aa43`
on `codex/sync-upstream-rc25`. The publish workflow checks out
`tokeness/main`, so this commit must first pass review and be merged into that
branch. The candidate has not been published or deployed.

The required production behavior is:

- moderation runs after typed request validation and channel selection, before
  token pricing, quota reservation, billing, and upstream forwarding;
- a stable channel-affinity conversation is audited once per selected channel
  and affinity TTL, excluding rotating multi-key credentials from the cache
  identity;
- a cached flagged decision remains blocking in `pre_block`;
- request-time moderation API, Redis, configuration-read, and audit persistence
  failures remain fail-open and produce safe request-scoped logs;
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

   The publish workflow currently runs only `model`, `middleware`, and
   `relay/...` regressions. The full service/controller checks above are an
   additional release gate, not a substitute for the workflow.

2. **Moderation provider boundary**

   Use a fixed HTTPS moderation endpoint with a network egress allowlist. The
   application appends `/v1/moderations` to `base_url`; do not put credentials
   in the URL query or path. Verify TLS, DNS, provider model availability,
   rate limits, timeout behavior, retry behavior, and the exact OpenAI
   moderation response contract before enabling the feature.

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
   (MySQL or PostgreSQL). Do not rely only on the SQLite test suite.

4. **Redis and affinity**

   All gateway nodes must use the same Redis instance with working ACLs,
   connectivity, and expiration commands. Without Redis, only process-local
   singleflight deduplicates concurrent first requests; cross-node duplicate
   moderation calls are then expected. A Redis outage is fail-open, but it is
   a rollout incident for `pre_block` and must be visible in operations.

5. **Conversation semantics**

   The desired de-duplication means later user turns in the same affinity key
   are not re-audited until the affinity TTL expires. This is intentional but
   means the first decision covers the conversation window. Confirm that the
   selected affinity rule has a stable conversation identity and a practical
   TTL. If every message must be independently moderated, the affinity key
   must include a message/revision identity; that is a different product
   behavior and defeats this rollout's de-duplication goal.

6. **Known protocol gap**

   `/v1/responses/compact` is not included in the current moderation protocol
   mapping. Before broad `pre_block`, explicitly choose one of:

   - add and test compaction extraction/moderation; or
   - document compaction as an accepted bypass and exclude those models/routes
     from the production moderation scope.

   Do not claim full conversation coverage while this decision is open.

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

- OpenAI Chat, OpenAI Responses, Anthropic Messages, and Gemini requests reach
  the moderation provider and then the upstream in `observe` mode;
- a flagged fixture is logged but still reaches upstream in `observe`;
- two requests with the same user/group/model/protocol/rule/channel affinity
  produce one provider call and one persisted audit decision;
- rotating the selected multi-key credential does not create another audit;
- changing the selected channel creates an independent audit;
- Redis-backed concurrent first requests produce one lease owner across nodes;
- provider timeout, malformed response, Redis failure, and log-write failure
  are fail-open, safe-log, and retryable rather than cached as allow;
- raw text, keys, and credential-bearing URLs do not appear in logs or admin
  responses.

Observe at least one complete traffic peak and compare provider calls with
affinity conversations, moderation latency with relay latency, flagged rate by
category/score, error rate, and database write volume.

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

## Observability and retention

The root-only log endpoint is `/api/content-moderation/logs`. Monitor at least:

- moderation provider request count, status/error count, timeout count, and
  latency;
- flagged count and category/score distribution by group, model, protocol, and
  channel;
- audit insert failures and table growth;
- Redis lease/cache errors and affinity cache hit behavior;
- `email_sent` and `email_sending` rows;
- users disabled by auto-ban and subsequent unban actions;
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
