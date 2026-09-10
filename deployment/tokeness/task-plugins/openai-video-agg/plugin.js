/*
 * Generic OpenAI-compatible async video generation plugin.
 *
 * For aggregator upstreams that speak the OpenAI video wire format
 * (POST /v1/videos, GET /v1/videos/{id}, GET /v1/videos/{id}/content).
 *
 * This plugin exists because task-plugin model ownership is declared statically
 * per plugin: billing only accepts u("seconds") when the requested model name
 * belongs to a plugin. Aggregator model IDs cannot reuse the built-in sora
 * plugin's namespace, so they get their own plugin key here.
 *
 * Shipping: uploaded through POST /api/plugin/task and activated in the
 * database (see README.md in this directory). It is deliberately NOT placed in
 * plugins/tasks/, because a database override for the same key shadows the
 * embedded factory plugin and would silently freeze it.
 */

const DEFAULT_SECONDS = 5;
const MAX_SECONDS = 3600;

function trimmed(value) {
  return String(value === undefined || value === null ? "" : value).trim();
}

// Returns undefined when the request omits a duration; rejects out-of-range or
// non-numeric values instead of silently clamping, so a bad request fails at
// submit time rather than after the upstream already started billing.
function requestedSeconds(body) {
  const raw = body.seconds === undefined ? body.duration : body.seconds;
  if (raw === undefined || raw === null || raw === "") return undefined;
  const value = Number(raw);
  if (!Number.isFinite(value)) throw new Error("seconds must be a number");
  if (value <= 0 || value > MAX_SECONDS) throw new Error("seconds must be between 1 and " + MAX_SECONDS);
  return value;
}

const TOKEN_FPS = 24;
const TOKEN_DIVISOR = 1024;

// 16:9 max-pixel dimensions per tier, matching the official Ark price examples
// (854x480 → 48038 tokens for 5s, 1280x720 → 108000 for 5s, 1920x1080 → 243000).
function tierPixels(tier) {
  if (tier === "480p") return [854, 480];
  if (tier === "512p") return [912, 512];
  if (tier === "768p") return [1366, 768];
  if (tier === "1080p") return [1920, 1080];
  if (tier === "2k") return [2560, 1440];
  if (tier === "4k") return [3840, 2160];
  return [1280, 720];
}

function estimateTokens(seconds, tier) {
  const dims = tierPixels(tier);
  return (seconds * dims[0] * dims[1] * TOKEN_FPS) / TOKEN_DIVISOR;
}

// Normalizes the many spellings upstreams accept ("480P", "1280x720", "2K",
// "768") to the declared resolution enum. Returns "" when unrecognized so the
// caller can fall back rather than assert a tier the client never asked for.
function normalizeResolution(raw) {
  const value = trimmed(raw).toLowerCase().replace(/\*/g, "x");
  if (!value) return "";
  if (/^\d+x\d+$/.test(value)) {
    return tierForHeight(Number(value.split("x")[1]));
  }
  if (value === "4k" || value === "2k") return value;
  const match = value.match(/^(\d+)p?$/);
  return match ? tierForHeight(Number(match[1])) : "";
}

function tierForHeight(height) {
  if (!Number.isFinite(height)) return "";
  if (height >= 2160) return "4k";
  if (height >= 1440) return "2k";
  if (height >= 1080) return "1080p";
  if (height >= 768) return "768p";
  if (height >= 720) return "720p";
  if (height >= 512) return "512p";
  if (height >= 480) return "480p";
  return "";
}

// Default tier used only to size a Seedance token estimate when the request
// names no resolution; 720p is Seedance 2.5's highest listed tier, so the
// submit-time reservation overestimates rather than underestimates.
const DEFAULT_RESOLUTION = "720p";

// A request carries reference video input when any of the shapes the OpenAI
// video format uses for it is present, or when the caller tags it explicitly.
function hasVideoInput(req, headers) {
  const header = trimmed((headers || {})["x-input-video"]);
  if (header) return true;
  for (const key of ["video", "video_url", "input_video", "reference_video"]) {
    if (trimmed(req[key])) return true;
  }
  for (const key of ["videos", "video_urls", "reference_videos", "video_list"]) {
    if (Array.isArray(req[key]) && req[key].length > 0) return true;
  }
  const metadata = req.metadata;
  if (metadata && typeof metadata === "object" && !Array.isArray(metadata)) {
    if (trimmed(metadata.video) || trimmed(metadata.video_url)) return true;
    if (Array.isArray(metadata.content)) {
      return metadata.content.some((item) => item && (item.type === "video_url" || item.video_url));
    }
  }
  return false;
}

function requestedResolution(req) {
  const metadata = req.metadata;
  if (metadata && typeof metadata === "object" && !Array.isArray(metadata) && trimmed(metadata.resolution)) {
    return metadata.resolution;
  }
  return req.resolution || req.size;
}

export const meta = {
  apiVersion: 1,
  key: "openai-video-agg",
  name: "OpenAI Video (Aggregator)",
  description: {
    en: "Generic OpenAI-compatible async video generation for aggregator upstreams (POST /v1/videos).",
    zh: "通用 OpenAI 兼容异步视频生成，用于提供 OpenAI 视频线格式的聚合上游（POST /v1/videos）。",
  },
  version: "1.0.2",
  author: { name: "Tokeness" },
  // Model IDs are the upstream aggregator's own names, except hailuo-h3: the
  // aggregator calls it minimax-h3, but model names are matched case-folded
  // across plugins, so that spelling is already owned by the built-in hailuo
  // plugin's MiniMax-H3. hailuo-h3 is the market alias for the same model; the
  // channel maps it back to minimax-h3 for the upstream request.
  //
  // Two different upstreams are served under this one plugin key:
  //   zzone.cc.cd    seedance2.5, kling-video-v3, wan3-720p, hailuo-h3
  //   xuetianai.com  grok-1.5-video, grok-imagine-video, grok-imagine-video-1.5
  // One plugin key may serve several channels; each channel pins the key through
  // its task_plugin_key setting, which is what the identity filter matches on.
  //
  // Note: seedance2.5 is tagged "openai" (chat) rather than "videos" upstream;
  // if the aggregator rejects it on /v1/videos this surfaces as a submit error.
  models: [
    "seedance2.5",
    "kling-video-v3",
    "wan3-720p",
    "hailuo-h3",
    "grok-1.5-video",
    "grok-imagine-video",
    "grok-imagine-video-1.5",
  ],
  fetchMode: "per_task",
  usageSchema: {
    // Requested video duration in seconds. Billing dimension for every model
    // whose official price is per output second.
    seconds: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
    // Seedance 2.5 is billed by tokens, not seconds. Official Ark formula:
    // tokens = duration × width × height × 24 / 1024.
    tokens: {
      type: "number",
      unit: "token",
      description: { en: "Billing token unit price", zh: "计费 Token 单价" },
    },
    // Reference-video input selects Seedance's lower token rate.
    video_input: {
      enum: ["none", "video"],
      enumLabels: {
        none: { en: "No reference video", zh: "无参考视频" },
        video: { en: "With reference video", zh: "有参考视频" },
      },
      description: { en: "Reference video input", zh: "参考视频输入" },
    },
    // Output resolution. Kling and Hailuo publish a distinct per-second price
    // per resolution tier; models with a flat rate never read this fact.
    resolution: {
      enum: ["480p", "512p", "720p", "768p", "1080p", "2k", "4k"],
      enumLabels: {
        "480p": { en: "480p", zh: "480p" },
        "512p": { en: "512p", zh: "512p" },
        "720p": { en: "720p", zh: "720p" },
        "768p": { en: "768p", zh: "768p" },
        "1080p": { en: "1080p", zh: "1080p" },
        "2k": { en: "2k", zh: "2k" },
        "4k": { en: "4k", zh: "4k" },
      },
      description: { en: "Output video resolution", zh: "输出视频分辨率" },
    },
  },
  usageExamples: [
    { label: "seedance2.5 720p 5s", facts: { seconds: 5, tokens: 108000, video_input: "none", resolution: "720p" } },
    { label: "seedance2.5 480p 5s", facts: { seconds: 5, tokens: 48037.5, video_input: "none", resolution: "480p" } },
    { label: "seedance2.5 720p 5s +参考视频", facts: { seconds: 5, tokens: 108000, video_input: "video", resolution: "720p" } },
    { label: "kling-video-v3 720p 5s", facts: { seconds: 5, tokens: 0, video_input: "none", resolution: "720p" } },
    { label: "kling-video-v3 1080p 5s", facts: { seconds: 5, tokens: 0, video_input: "none", resolution: "1080p" } },
    { label: "wan3-720p 5s", facts: { seconds: 5, tokens: 0, video_input: "none", resolution: "720p" } },
    { label: "hailuo-h3 768p 5s", facts: { seconds: 5, tokens: 0, video_input: "none", resolution: "768p" } },
    { label: "hailuo-h3 2k 5s", facts: { seconds: 5, tokens: 0, video_input: "none", resolution: "2k" } },
    { label: "grok-imagine-video 5s", facts: { seconds: 5, tokens: 0, video_input: "none", resolution: "720p" } },
  ],
  protocols: ["openai_video"],
};

export function buildSubmitRequest(ctx) {
  const req = ctx.requestBody || {};
  if (!trimmed(req.prompt)) throw new Error("field prompt is required");
  if (ctx.action === "remix") throw new Error("remix is not supported by this upstream");

  const headers = { Authorization: "Bearer " + ctx.apiKey };
  const model = ctx.upstreamModel || ctx.model;

  if ((ctx.files || []).length) {
    const values = Object.assign({}, req, { model: model });
    const parts = [];
    // Sorted field names keep the multipart part order deterministic.
    for (const key of Object.keys(values).sort()) {
      const value = values[key];
      if (value === undefined || value === null || typeof value === "object") continue;
      parts.push({ name: key, value: value });
    }
    if (values.metadata && typeof values.metadata === "object" && !Array.isArray(values.metadata)) {
      parts.push({ name: "metadata", value: JSON.stringify(values.metadata) });
    }
    for (const file of ctx.files) parts.push({ name: file.field, fileRef: file.ref, filename: file.filename });
    return { url: ctx.baseUrl + "/v1/videos", method: "POST", headers, bodyType: "multipart", parts };
  }

  headers["Content-Type"] = "application/json";
  return {
    url: ctx.baseUrl + "/v1/videos",
    method: "POST",
    headers,
    body: Object.assign({}, req, { model: model }),
  };
}

export function parseSubmitResponse(ctx, resp) {
  const body = resp.body || {};
  const taskId = trimmed(body.id) || trimmed(body.task_id);
  if (!taskId) throw new Error("task_id is empty");
  return { taskId, taskData: body };
}

export function buildQueryRequest(ctx) {
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.taskId),
    method: "GET",
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

export function parseTaskResult(ctx, body) {
  const statuses = {
    queued: "QUEUED",
    pending: "QUEUED",
    not_start: "QUEUED",
    processing: "IN_PROGRESS",
    in_progress: "IN_PROGRESS",
    running: "IN_PROGRESS",
    completed: "SUCCESS",
    succeeded: "SUCCESS",
    success: "SUCCESS",
    failed: "FAILURE",
    failure: "FAILURE",
    cancelled: "FAILURE",
    canceled: "FAILURE",
  };
  const raw = trimmed(body.status).toLowerCase();
  const result = { status: statuses[raw] || "UNKNOWN" };
  if (!statuses[raw]) result.reason = "unrecognized status: " + trimmed(body.status);

  const progress = Number(body.progress);
  if (Number.isFinite(progress) && progress > 0 && progress < 100) result.progress = progress + "%";

  if (result.status === "FAILURE") {
    const detail = body.error && (trimmed(body.error.message) || trimmed(body.error.code));
    result.reason = detail || "task failed";
  }
  return result;
}

// Billing facts per model. Seedance 2.5 is billed by tokens (official Ark
// formula); every other declared model is billed per output second, several of
// them by resolution tier. Facts the model's expression does not reference are
// harmless — the engine only prices the variables the expression actually uses.
export function extractUsage(ctx) {
  const req = ctx.requestBody || {};
  const body = ctx.body || ctx.requestBody || {};
  const seconds = requestedSeconds(body);
  const facts = {};
  // An omitted duration keeps the upstream default so a per-second expression
  // always has a multiplier instead of evaluating an absent usage key.
  facts.seconds = seconds === undefined ? DEFAULT_SECONDS : seconds;

  const resolution = normalizeResolution(requestedResolution(body));
  if (resolution) facts.resolution = resolution;

  // Seedance 2.5 estimate. The estimated tier is only a reservation sizing; the
  // completion hook overlays the measured token count.
  const model = trimmed(ctx.upstreamModel || ctx.model).toLowerCase();
  if (model === "seedance2.5") {
    facts.tokens = estimateTokens(facts.seconds, resolution || DEFAULT_RESOLUTION);
    facts.video_input = hasVideoInput(body, ctx.requestHeaders) ? "video" : "none";
  }
  return facts;
}

// Measured values replace the submitted estimates when the upstream reports
// them; an omitted value keeps the estimate recorded at submit time.
export function extractUsageOnComplete(task, result, body) {
  const source = body || {};
  const facts = {};

  const measured = Number(
    source.seconds === undefined ? (source.duration === undefined ? source.video_duration : source.duration) : source.seconds
  );
  if (Number.isFinite(measured) && measured > 0) facts.seconds = Math.min(measured, MAX_SECONDS);

  const resolution = normalizeResolution(source.resolution || source.size);
  if (resolution) facts.resolution = resolution;

  // Seedance reports a real billable token count once the task succeeds.
  const usage = source.usage || {};
  let tokens = Number(usage.completion_tokens);
  if (!Number.isFinite(tokens) || tokens <= 0) tokens = Number(usage.total_tokens);
  if (Number.isFinite(tokens) && tokens > 0) facts.tokens = tokens;

  return facts;
}

export function listArtifacts(task) {
  return task.status === "SUCCESS" ? [{ key: "video", type: "video" }] : [];
}

export function buildContentRequest(ctx) {
  if (ctx.artifactKey !== "video") throw new Error("artifact_not_found");
  return {
    url: ctx.baseUrl + "/v1/videos/" + encodeURIComponent(ctx.upstreamTaskId) + "/content",
    method: ctx.clientRequest.method,
    headers: { Authorization: "Bearer " + ctx.apiKey },
  };
}

const videoStatuses = {
  NOT_START: "queued",
  SUBMITTED: "queued",
  QUEUED: "queued",
  IN_PROGRESS: "in_progress",
  SUCCESS: "completed",
  FAILURE: "failed",
};

export const protocols = {
  openai_video: {
    decodeRequest: function (ctx) {
      if (!ctx.body || (ctx.body.kind !== "json" && ctx.body.kind !== "multipart")) {
        throw new Error("JSON or multipart body required");
      }
      if (ctx.body.kind === "json") {
        const req = ctx.body.value;
        if (!req || typeof req !== "object" || Array.isArray(req)) throw new Error("JSON object required");
        requestedSeconds(req);
        return {
          kind: "submit",
          model: ctx.model,
          action: req.input_reference || req.image ? "image_to_video" : "text_to_video",
          requestBody: Object.assign({}, req, { model: ctx.model }),
        };
      }

      const fields = ctx.body.fields || {};
      const req = {};
      for (const name of Object.keys(fields)) {
        const values = fields[name];
        if (values.length > 1) throw new Error(name + " must be provided once");
        req[name] = values[0];
      }
      if (req.metadata !== undefined) {
        let parsed;
        try {
          parsed = JSON.parse(req.metadata);
        } catch (e) {
          throw new Error("metadata must be a JSON object string");
        }
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
          throw new Error("metadata must be a JSON object string");
        }
        req.metadata = parsed;
      }
      requestedSeconds(req);
      const hasFile = (ctx.body.files || []).length > 0;
      return {
        kind: "submit",
        model: ctx.model,
        action: hasFile || req.input_reference || req.image ? "image_to_video" : "text_to_video",
        requestBody: Object.assign({}, req, { model: ctx.model }),
      };
    },
    render: function (ctx, task) {
      const output = {
        id: task.task_id,
        object: "video",
        model: (task.properties || {}).origin_model_name || "",
        status: videoStatuses[task.status] || "unknown",
        progress: Number(String(task.progress || "0").replace("%", "")),
        created_at: Number(task.created_at || 0),
      };
      const completedAt = Number(task.finished_at || task.updated_at || 0);
      if (completedAt > 0) output.completed_at = completedAt;
      if (task.status === "FAILURE") {
        output.error = { code: "video_generation_failed", message: task.fail_reason || "The video generation task failed." };
      }
      return output;
    },
  },
};
