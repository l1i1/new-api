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
| `minimax-h3` | zzone | per second, by resolution |
| `wan3.0-video` | rolldek | per second, by resolution |
| `wan3.0-video-prime` | rolldek | per second, by resolution |
| `grok-1.5-video` | xuetianai | per second (flat) |
| `grok-imagine-video` | xuetianai | per second (flat) |
| `grok-imagine-video-1.5` | xuetianai | per second (flat) |

`minimax-h3` is zzone's own spelling, used directly. Declaring it collides with
the built-in `hailuo` plugin's `MiniMax-H3` because plugin model names are matched
ASCII-case-folded across all plugins (`pkg/jsplugin/model_fold.go` lowercases only),
so the built-in plugin is disabled on this instance through the
`TaskPluginDisabledFactoryKeys` option. The built-in `hailuo` plugin drives no
channel here, so nothing regresses; re-enabling it would re-collide and the upload
would be rejected while `minimax-h3` is declared.

`wan3.0-video` / `wan3.0-video-prime` are platform-side model names; the tier is
a request parameter, not the model name. rolldek bakes the output resolution into
its model name (`wan3.0-video-480p/720p/1080p`) and its gateway forces any
`size`/`resolution` request field to match the suffix, so the plugin rewrites the
submit body's model to the resolution-suffixed upstream name at submit time
(`buildSubmitRequest`): 480p/720p/1080p map to the matching suffix, anything
unrecognized or omitted falls back to 720p. The rewrite is invisible to
`decodeRequest` — the host rejects a decoder whose returned model differs from
the pinned model (`relay/channel/task/jsplugin/adaptor.go`), so the pinned model
must stay the platform name and only the outgoing HTTP body may change. Task
`properties.upstream_model_name` records the platform name, not the rewritten
one; the upstream's raw task response (`tasks.data`) is the place to confirm
what was actually sent. Declaring these names collides with the built-in
`alibaba` plugin (Bailian direct adaptor), which declares the identical names —
that plugin is disabled on this instance the same way (it drove 0 channels;
`POST /api/plugin/task/alibaba/status {"enabled":false}` returned
`disabled_channels:0`).

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

Declaring `minimax-h3` requires the built-in `hailuo` plugin to be disabled
first, because the two model names collide case-insensitively and the upload is
rejected while both are registered:

```
POST /api/plugin/task/hailuo/status   {"enabled": false}
```

The built-in plugin drives no channel on this instance, so disabling it is
routable-safe. Its `TaskPluginDisabledFactoryKeys` entry also silences the
factory layer, so the name is genuinely freed (an override row alone would not
be enough). Re-enabling `hailuo` while `minimax-h3` is declared fails the same
way — disable `openai-video-agg` first.

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

### Rename to minimax-h3 (2026-09-11, plugin 1.0.4)

The alias `hailuo-h3` was dropped and the upstream's own `minimax-h3` declared.
Sequence, all on the intl instance:

1. lint + fixture (`42/42`) against 1.0.4;
2. `POST /api/plugin/task/hailuo/status {"enabled":false}` — frees the folded name;
3. upload 1.0.4 and activate `{"version":"1.0.4"}` (`current_generation` 12 → 14);
4. `PUT /api/channel/` for channel 136: `models` `…,hailuo-h3` → `…,minimax-h3`
   and `model_mapping` `{"hailuo-h3":"minimax-h3"}` → `""`, so no mapping is
   needed and the upstream receives `minimax-h3` verbatim;
5. `PATCH /api/option/model_pricing` — copy the tiered expression onto
   `minimax-h3` (its schema gate passes only because the new plugin declares the
   name) and `reset` the stale `hailuo-h3` entry.

**Channel update trap:** `PUT /api/channel/` rejects a `status` field, so the
usual read-modify-write flow omits it. `Channel.UpdateAbilities` then rebuilds
every ability row from the submitted struct, whose `Status` is the zero value —
which silently disables the whole channel's abilities even though the channel
row still shows status 1. `POST /api/channel/:id/status` with the real status
repairs it (or `POST /api/channel/fix`). Check abilities after any channel PUT.

**Verification:** `GET /api/channel/search?model=minimax-h3` → channel 136 only;
`GET /api/pricing` shows `minimax-h3` with the moved expression and no
`hailuo-h3` row. A live `/v1/videos` call with `minimax-h3` reached channel 136
and the upstream answered `余额不足，当前余额 ¥0.40，需要 ¥1.80` — proof the
rename routed end-to-end; the request fails only because zzone is unpaid. The
generic `temporarily_unavailable` text is the exhausted-retry summary for a
single available channel, not a routing failure — read the `channelId=136`
`logs`/container line for the real upstream error.

### Add rolldek.com upstream (2026-09-11, plugin 1.0.5)

Channel 151 `Video_RD1` (type 61, `task_plugin_key=openai-video-agg`, group
`Video-Test`, base URL `https://rolldek.com`), key stored in
`private/access/rolldek.local.env`. rolldek is itself a new-api instance
(USD display with a 1:1 CNY top-up rate); its `wan` group returns Bailian
official links. Platform models `wan3.0-video` / `wan3.0-video-prime` take the
tier through `resolution` and bill per second at the official Bailian CNY price
divided by 7 (`480p 0.042857 / 0.064286`, `720p 0.085714 / 0.128571`,
`1080p 0.171429 / 0.257143` for standard / prime), defaulting to the 720p tier
when the parameter is missing.

Sequence: disable the unused built-in `alibaba` plugin (name collision, see
above) → upload 1.0.5 and activate (`current_generation` 14 → 16) →
`POST /api/channel/` with `{"mode":"single","channel":{…}}` (the handler
requires the `mode` field and the channel wrapped; type 61 channels also need
the `task_plugin.bind` permission) → `PATCH /api/option/model_pricing` with two
changes at `expected_version = empty_version`.

Live verification, one real call through `tokeness.ai`
(`wan3.0-video`, `seconds:"2"`, `resolution:"480p"`):

- submit 200, pre-consumed **42857** quota = 2s × 0.042857 × 500000 (the 480p
  branch of the expression);
- upstream received **`wan3.0-video-480p`** — confirmed from the persisted
  `tasks.data`, whose raw response carries `"model":"wan3.0-video-480p"` and a
  Bailian OSS `metadata.url`;
- task completed in ~100 s; `GET /v1/videos/{id}/content` returned a 2.7 MB
  `video/mp4`; the settled `logs` row kept the 42857 estimate (rolldek reports
  no measured duration), matching the upstream's own 2s billing.

Known caveat: rolldek bills `wan3.0-video-*` by output seconds **plus
reference-video seconds**, while the plugin's `seconds` fact counts output
only — requests with reference videos undercharge the platform. Also note
480p is nearly break-even (rolldek sells it at 9折 of the official price this
platform charges); margin concentrates in 720p/1080p.

### Known caveat

`seedance2.5` is tagged `openai` (chat) rather than `videos` in the upstream
aggregator's own pricing metadata, while the other zzone models are tagged
`videos`. If the aggregator only serves it over chat completions, calls through
`/v1/videos` fail loudly at submit. Confirming this needs one real call through
the platform; the direct-upstream probe only proved the request shape is accepted
for `grok-imagine-video`.
