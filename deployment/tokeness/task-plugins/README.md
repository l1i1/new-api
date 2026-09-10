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
aggregator channel (`Video_ZZ1`, channel 136).

It exists because task-plugin model ownership is declared statically per plugin
and billing only accepts `u("seconds")` for a model that a plugin declares. The
aggregator's own model IDs cannot be added to the built-in `sora` plugin without
either forking that plugin or changing its namespace.

Declared models: `seedance2.5`, `kling-video-v3`, `wan3-720p`, `hailuo-h3`.

`hailuo-h3` is not the aggregator's spelling. The aggregator calls the model
`minimax-h3`, but plugin model names are matched case-folded across all plugins
(`pkg/jsplugin/model_fold.go` lowercases only), so `minimax-h3` collides with the
built-in `hailuo` plugin's `MiniMax-H3` and the whole plugin is rejected at load.
`hailuo-h3` is the market alias for the same model; channel 136 maps it back to
`minimax-h3` through its `model_mapping`, which is also what the upstream request
carries (`ModelMappedHelper` sets `UpstreamModelName` to the mapping target).

Billing facts: `seconds` only. Duration comes from the request body
(`seconds` or `duration`, default 5s) and is replaced by the measured value when
the upstream reports one on completion.

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
POST /api/plugin/task/<key>/activate
GET  /api/plugin/task/runtime/status
```

Bump `meta.version` on every source change: reusing a key/version with different
source is rejected.

### Known caveat

`seedance2.5` is tagged `openai` (chat) rather than `videos` in the upstream
aggregator's own pricing metadata, while the other three are tagged `videos`.
If the aggregator only serves it over chat completions, calls through
`/v1/videos` fail loudly at submit. Confirming this needs one real call; if it
does fail, either drop it from `meta.models` (and stop routing it) or bill it by
tokens, which is what ByteDance publishes for Seedance 2.5 officially.
