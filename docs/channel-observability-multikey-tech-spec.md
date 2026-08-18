# Channel Observability and Multi-Key Technical Specification

## Data model

`channel_credentials` is the stable credential identity. It stores `channel_id`, `position`, secret material using the existing storage boundary, a non-reversible fingerprint, status, disabled metadata, `proxy_mode` (`inherit`, `direct`, `custom`), optional proxy URL, `keys_revision`, and last-test fields. Legacy `channels.key` remains a read fallback during migration.

`channel_model_perf_metrics` is independent from the existing `perf_metrics` table. Its dimensions are time bucket, channel, credential, requested model, upstream model, group, and protocol. It stores request/attempt counters, outcome counters, normalized cache usage, coverage counters, sums for averages, and mergeable latency/TTFT/FRT histograms.

## Metric semantics

- `request_count`: one per user request.
- `attempt_count`: one per selected upstream channel attempt, including retries.
- Request success rate is final request success divided by request count.
- Attempt success rate is normal upstream completion divided by attempt count.
- Cache hit rate is cache-hit requests divided by cache-observable requests.
- Cache token rate is cache-read tokens divided by normalized input tokens.
- TTFT is the first actual downstream content flush; upstream FRT is the first upstream data event.
- P95 is computed by merging fixed histogram buckets: 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000 ms, plus overflow.

## Observation pipeline

Relay attempts emit a lightweight observation lifecycle. In-process buckets are updated synchronously; Redis stores the current bucket; completed buckets are persisted to the main database. Queries merge persisted buckets and Redis active buckets. Metric failures are logged and sampled but never fail an API request.

## Key testing

Key tests are system tasks with explicit credential IDs, bounded concurrency, timeout, model, and endpoint. A test directly injects the selected credential and effective proxy. Results classify authentication, model access, rate limit, proxy/network, timeout, upstream, and parse failures. Only explicit administrator action changes status.

## State safety

Batch status/proxy operations lock the channel row, validate `keys_revision`, update credential rows transactionally, synchronize the channel aggregate status and abilities, then refresh local/distributed caches. Empty arrays do not mean “all”; callers use an explicit `all` flag.

## Proxy resolution

`custom` credential proxy wins, `inherit` uses the channel proxy, and `direct` bypasses both. The resolved proxy is attached after credential selection and reused by common HTTP/SSE paths, the supported WebSocket adapters, and provider task fetches. Async tasks persist the credential reference so polling does not randomly select another credential. Proxy client caches are invalidated after changes.

## Multi-key text input

The create and update requests may include `multi_key_credentials`, an ordered
array of `{secret, proxy_url?}` objects. This field applies only to ordinary
newline-based multi-key channels. Vertex JSON arrays and other structured
credential formats keep their existing input contract. `channels.key` stores
only the normalized secrets joined by newlines; an optional `proxy_url` is
validated by `common.ParseProxyURLStrict` and stored as the matching credential's
`custom` proxy. A missing proxy uses `inherit`.

The UI parses non-empty lines from top to bottom. A line that passes the same
strict proxy URL rules belongs to the immediately preceding key and is consumed;
otherwise it starts a new key. Supported schemes are `http`, `https`, `socks5`,
and `socks5h`. Blank lines are ignored. Duplicate secrets keep their first
occurrence. Append mode preserves existing credentials and their proxies and
adds only new secrets. Replace mode uses the submitted order and resets a key to
`inherit` when no proxy follows it.

The server treats the structured field as the source of truth and revalidates
every entry. Requests without the field retain the legacy behavior. General
channel and multi-key status APIs never return secrets or full proxy URLs. The
existing second-factor-protected channel-key reveal endpoint may return the
authorized newline representation (`secret`, then optional `proxy_url`) for an
ordinary multi-key channel.

## API contract

- `GET /api/observability/channel-model`
  - Filters: `start`, `end` (Unix seconds or RFC3339), `channel_id`, `credential_id`, `model`, `group`, `protocol`.
  - Pagination: `page`, `page_size` (1-200).
  - Sorting: `sort_by`, `sort_order=asc|desc`.
  - Response data: `{items,total,page,page_size,total_pages}`. Each item includes `error_trends`, `sample_count`, `sample_status`, `sample_sufficient`, and `usage_sufficient`.
- `GET /api/channel/observability` remains as a legacy array-shaped compatibility response.
- `POST /api/channel/:id/multi-key/test`
  - Selection: `credential_ids`, `key_indices`, or explicit `all=true`.
  - Options: `include_disabled`, `model`, `endpoint_type`, `concurrency` (1-16), `timeout` (seconds, 1-300).
  - Results contain `credential_id`, fingerprint, status, HTTP status, latency, `error_class`, and `tested_at`.
- `GET /api/channel/:id/multi-key/test/:task_id`
- `POST /api/channel/:id/multi-key/test/:task_id/cancel`
- `POST /api/channel/:id/multi-key/status`
  - Selection: `credential_ids`, `key_indices`, or explicit `all=true`; requires `keys_revision` when supplied.
- `PATCH /api/channel/:id/multi-key/proxy`
  - Selection: `credential_id`, `credential_ids`, or explicit `all=true`.
  - Modes: `inherit`, `direct`, `custom`; custom URL is write-only and is never returned.
- `POST /api/channel`
  - In `multi_to_single` mode, optional `multi_key_credentials` carries ordered
    secrets and per-key proxy URLs. Legacy `channel.key` remains accepted when
    this field is absent.
- `PUT /api/channel/`
  - For an existing ordinary multi-key channel, optional
    `multi_key_credentials` follows `key_mode=append|replace` semantics.
- `POST /api/channel/:id/key`
  - After the existing second-factor verification, an ordinary multi-key
    channel is revealed in key/proxy newline format. This is the only read path
    that may return full independent proxy URLs.

All routes use existing admin authentication and permission boundaries. Credential and proxy secrets are write-only and never returned.

## Compatibility and rollout

Legacy channels continue to use the existing key and channel proxy behavior. Startup `AutoMigrate` creates `channel_credentials`, `channel_credential_revisions`, `channel_model_perf_metrics`, and the system-task cancellation/error-class columns, then imports legacy key order and status maps idempotently for SQLite, MySQL, and PostgreSQL. Historical p95 is not backfilled as exact data. Rollout order is schema/read compatibility, effective proxy resolution, observation recording, admin APIs, frontend, then optional alerts/OpenMetrics export.
