# Task plugins managed outside the embedded set

Plugins in this directory are **uploaded to the database**, not embedded into the
image.

## Why not `plugins/tasks/`

`plugins/tasks/*` are factory plugins compiled into the binary via `//go:embed`.
The registry resolves a key by copying factory plugins first and then database
overrides on top (`pkg/jsplugin/routing.go`, `effectivePlugins`), so **a database
version of the same key shadows the factory plugin**. Keeping a source file in
both places therefore produces a silent divergence: the image ships a new plugin
while production keeps running the older database copy, and nothing in code
review surfaces it.

So each plugin lives in exactly one place. These are database-managed because
they pin aggregator-specific model IDs, which is deployment configuration rather
than product code.

If a plugin here ever needs to ship with the image, move its directory into
`plugins/tasks/<key>/` and delete the database version — do both in one change,
never one at a time.

## openai-video-agg

Generic OpenAI-compatible async video plugin (`POST /v1/videos`,
`GET /v1/videos/{id}`, `GET /v1/videos/{id}/content`) for the `zzone.cc.cd`
aggregator channels (`Video_ZZ1` 136 on zzone, `Video_XT1` 150 on xuetianai).

It exists because task-plugin model ownership is declared statically per plugin
and billing only accepts `u("<fact>")` for a model that a plugin declares. The
aggregator's own model IDs cannot be added to a built-in plugin without either
forking that plugin or changing its namespace.

Declared models and their official billing dimension:

| Model | Upstream | Official billing |
|---|---|---|
| `seedance2.5` | zzone | **per token** (Ark formula) |
| `kling-video-v3` | zzone | per second, by resolution |
| `wan3-720p` | zzone | per second (flat) |
| `hailuo-h3` | zzone | per second, by resolution |
| `grok-1.5-video` | xuetianai | per second (flat) |
| `grok-imagine-video` | xuetianai | per second (flat) |
| `grok-imagine-video-1.5` | xuetianai | per second (flat) |

`hailuo-h3` is not the aggregator's spelling. The aggregator calls the model
`minimax-h3`, but plugin model names are matched case-folded across all plugins
(`pkg/jsplugin/model_fold.go` lowercases only), so `minimax-h3` collides with the
built-in `hailuo` plugin's `MiniMax-H3` and the whole plugin is rejected at load.
`hailuo-h3` is the market alias for the same model; channel 136 maps it back to
`minimax-h3` through its `model_mapping`, which is also what the upstream request
carries (`ModelMappedHelper` sets `UpstreamModelName` to the mapping target).

Billing facts:

- `seconds` — request duration (`seconds` or `duration`, default 5s). Replaced by
  the measured value when the upstream reports one on completion. Read from
  `ctx.requestBody`, falling back to an unwrapped `{kind, value}` decoded body —
  reading the envelope itself silently yields the 5s default.
- `tokens` — Seedance only, from the official Ark formula
  `duration × width × height × 24 / 1024`, estimated at submit and overlaid by
  the upstream's measured `usage.completion_tokens` on completion.
- `video_input` — Seedance only, `none` / `video`, selects the token rate.
- `resolution` — `480p`/`512p`/`720p`/`768p`/`1080p`/`2k`/`4k`, normalized from
  `metadata.resolution`, `size`, `resolution`, or a `WxH` pixel size. Only the
  models whose official price varies by tier read it.

Completion responses nest the measured duration under `video`
(`{"video": {"duration": 4}}` on xuetianai); the completion hook reads that
shape as well as the flat keys and the persisted `task.data`.

### Deploy

```sh
# from apps/new-api
go run . plugin lint deployment/tokeness/task-plugins/openai-video-agg/plugin.js
go run . plugin test deployment/tokeness/task-plugins/openai-video-agg/plugin.js \
  --fixture deployment/tokeness/task-plugins/openai-video-agg/plugin.fixture.json
```

Then upload and activate (root-scoped token required):

```
POST /api/plugin/task            {"source": "<plugin.js contents>", "remark": "..."}
POST /api/plugin/task/<key>/activate   {"version": "<version to activate>"}
GET  /api/plugin/task/runtime/status
```

Upload does not activate. Activate binds one uploaded version and requires an
explicit body naming it; a bodyless POST fails with `EOF`. A successful activate
advances `current_generation` in `/api/plugin/task/runtime/status` — compare it
before and after, and ignore the pre-existing `jimeng`/`kling`/`sunoapi` route
conflicts in `plugin_errors`.

Bump `meta.version` on every source change: reusing a key/version with different
source is rejected.

### Live verification (2026-09-11)

Three real calls through `tokeness.ai` on channel 150 (`grok-imagine-video`,
`tier("base", u("seconds") * 0.05)`, group ratio 1, QuotaPerUnit 500000):

| Call | Request | Pre-consumed | Settled | Notes |
|---|---|---|---|---|
| 1 | `seconds: "1"` | 125000 | 125000 | plugin 1.0.2: read the decoded-body envelope, fell back to the 5s default and never corrected it (the completion hook missed `video.duration`) |
| 2 | `seconds: "1"` | 25000 | 25000 | plugin 1.0.3: 1s × $0.05 × 500000 |
| 3 | omitted | 125000 | 100000 | estimate asserted the 5s default, settlement refunded to the measured `video.duration: 4` |

Also confirmed: the `tasks` row, one `logs` row per call (`other.is_task: true`,
`usage_facts`, plugin version and generation), the admin task list
(`GET /api/task`) behind `/usage-logs/task`, and
`GET /v1/videos/{id}/content` returning `video/mp4` (513 KB for a 1s clip).

Omitting `seconds` reserves the 5s default while xuetianai's own default is 4s,
so the reservation over-reserves and is refunded at settlement (`seconds` must
stay present: a per-second expression errors on a missing usage key rather than
treating it as zero).

### Known caveat

`seedance2.5` is tagged `openai` (chat) rather than `videos` in the upstream
aggregator's own pricing metadata, while the other zzone models are tagged
`videos`. If the aggregator only serves it over chat completions, calls through
`/v1/videos` fail loudly at submit. Confirming this needs one real call through
the platform; the direct-upstream probe only proved the request shape is accepted
for `grok-imagine-video`.
