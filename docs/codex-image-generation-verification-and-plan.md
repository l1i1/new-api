# Codex Image Generation on Tokeness — Verification & Plan

Status: verification complete, plan revised
Supersedes: the earlier "hosted image generation bridge" draft, whose premise
(Codex declares a hosted `image_generation` tool on every `/v1/responses` turn)
holds only for Codex builds released before the local-extension migration.

Source of truth for Codex behaviour: `E:\Ai\Agents\codex` at
`rusty-v8-v150.4.0-1914-g818f1cca8c`.

## 1. Verified Codex behaviour

### 1.1 Current Codex never sends a hosted `image_generation` tool

`ToolSpec` has exactly five variants — `function`, `namespace`, `tool_search`,
`web_search`, `custom` (`codex-rs/tools/src/tool_spec.rs:22`). There is no
hosted image-generation variant.

`create_image_generation_tool` (the old hosted spec constructor) no longer exists
in the tree; it was removed by `a7c72aee8b` ("Use the image generation extension
by default"), which is contained in `rust-v0.146.1`, `rust-v0.152.0`,
`rusty-v8-v150.4.0`. `hosted_model_tool_specs`
(`codex-rs/core/src/tools/spec_plan.rs:596`) only ever emits a hosted
**web_search** tool.

Consequence: the `403 Image generation is not enabled for this group` seen by
users comes from Codex builds **before** that migration (the hosted-tool era,
roughly `rust < 0.145/0.146`), not from current Codex.

### 1.2 Current Codex generates images locally, against our own base URL

Image generation is an extension tool `image_gen.imagegen`
(`codex-rs/ext/image-generation/src/tool.rs`):

- declaration is a namespace tool (`{"type":"namespace","name":"image_gen",...}`),
  not a hosted image tool;
- model-facing args: `{prompt, referenced_image_paths?, num_last_images_to_include?}`
  (`ImagegenArgs`) — i.e. **the model supplies the prompt**;
- execution calls `CodexImagesBackend` → `ImagesClient` → `POST
  {provider.base_url}/images/generations` with body
  `{prompt, model:"gpt-image-2", background:"auto", quality:"auto", size:"auto"}`
  (`IMAGE_MODEL = "gpt-image-2"` is hardcoded, `tool.rs`; edits go to
  `images/edits` with an `images: [data-url]` array);
- URL join is `base_url + "/" + path` (`codex-api/src/provider.rs:53`), so a
  provider configured with `base_url = "https://tokeness.ai/v1"` calls
  `https://tokeness.ai/v1/images/generations`;
- provider headers are attached to that request (`provider.rs:77` clones
  `self.headers`), so anything in `http_headers` reaches us;
- the tool description already instructs the model to use it only when the user
  requests an image ("Use it when: The user requests an image…").

### 1.3 Registration gate

`image_generation_available` (`spec_plan.rs:699`) requires all of:

1. `Feature::ImageGeneration` enabled — stage Stable, `default_enabled: true`
   (`codex-rs/features/src/lib.rs:1474`);
2. account plan type is not `Free` (a third-party API key has no plan type, so
   this passes);
3. provider capabilities `image_generation && namespace_tools` — both default
   `true` (`model-provider/src/provider.rs`);
4. model `input_modalities` contains `Image` — default is `[Text, Image]`
   (`protocol/src/openai_models.rs:189`);
5. `provider.uses_openai_actor_authorization()` OR official Codex-backend auth.

Condition 5 is the decisive one for us: it is true when the provider is
**not** `requires_openai_auth` and `http_headers` contains a non-empty
`x-openai-actor-authorization`
(`model-provider-info/src/lib.rs:539`). Without it a third-party provider gets
**no image tool at all** — the model reports there is no built-in image tool,
which matches the widely reported "no built-in image generation tool" symptom.

### 1.4 What Tokeness already supports

- `POST /v1/images/generations` → `RelayModeImagesGenerations`
  (`relay/constant/relay_mode.go:71`, `router/relay-router.go:118`).
- `dto.ImageRequest` already carries `background`, `output_format`,
  `output_compression`, `partial_images`, `images`, `mask` as raw passthrough
  (`relaykit/dto/openai_image.go`), so Codex's body maps without loss.
- Validation accepts `size:"auto"`, `quality:"auto"`, omitted `n` (defaulted to
  1), and does not require `prompt` for non-dall-e models
  (`relay/helper/valid_request.go`).
- JSON image-edit passthrough exists (`ConvertImageRequest` returns the request
  as-is for JSON bodies).

### 1.5 Gap

`gpt-image-2` has **no pricing/ratio entry** in Tokeness; only `gpt-image-1`
exists (`setting/ratio_setting/model_ratio.go`). A request for `gpt-image-2`
would not resolve a price.

## 2. Conclusion

The `/v1/responses` bridge is **not needed** and should not be built: current
Codex already performs client-side local image generation and calls our image
endpoint directly, and the hosted tool it was meant to replace no longer exists
in current Codex. The remaining work is endpoint/model configuration plus user
documentation.

## 3. Plan

### P0 — make the client-side path work (config only)

1. Route the model name Codex hardcodes: add `gpt-image-2` to the serving
   channel's model list and map it to the actual upstream image model
   (`gpt-image-2.5-flare`), or provision a channel that serves `gpt-image-2`.
2. Add pricing/ratio for `gpt-image-2` so quota resolves (mirror the
   `gpt-image-1` entries: model ratio, image ratio, and any per-image override).
3. Verify end-to-end with a real Codex client: provider config

   ```toml
   [model_providers.tokeness]
   base_url = "https://tokeness.ai/v1"
   wire_api = "responses"
   requires_openai_auth = false
   http_headers = { "x-openai-actor-authorization" = "local-image-extension" }
   ```

   then confirm the model calls `image_gen.imagegen` and that Tokeness serves
   `POST /v1/images/generations` (`model: gpt-image-2`) returning
   `data[].b64_json`; the image is written under `generated_images/`.
4. Verify the edit path (`POST /v1/images/edits`, JSON `images: [data-url]`)
   against the chosen upstream.
5. Publish user-facing setup instructions (provider block above, or the
   equivalent "API Key Mode" entry in CC Switch); note that without the actor
   header Codex exposes no image tool at all.

### P1 — legacy Codex builds that still declare the hosted tool

For clients before the migration, the only platform-side need is to stop a
speculative `image_generation` declaration from breaking ordinary text turns:
strip unsupported hosted tools per channel (or fail fast when `tool_choice`
forces that tool). This is a small, independent change; it is **not** a local
image-generation feature, because those clients cannot consume the local tool.

## 4. Open items

- Confirm the exact minimum Codex release users run, to size the P1 population.
- Confirm the upstream image channel accepts `background:"auto"`,
  `size:"auto"`, `quality:"auto"` and the JSON edit body.
- Decide whether to return `x-codex-imagegen-request-id` (optional; Codex reads
  it if present, for analytics correlation).
- `gpt-image-2` contains no entry in `common.ImageGenerationModels`
  (`common/model.go`), so the marketplace endpoint badge will be wrong until a
  `gpt-image-` pattern is added; display-only.
