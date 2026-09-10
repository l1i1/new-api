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

export const meta = {
  apiVersion: 1,
  key: "openai-video-agg",
  name: "OpenAI Video (Aggregator)",
  description: {
    en: "Generic OpenAI-compatible async video generation for aggregator upstreams (POST /v1/videos).",
    zh: "通用 OpenAI 兼容异步视频生成，用于提供 OpenAI 视频线格式的聚合上游（POST /v1/videos）。",
  },
  version: "1.0.1",
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
    // Requested video duration in seconds.
    seconds: {
      type: "number",
      unit: "second",
      description: { en: "Video generation unit price", zh: "视频生成单价" },
    },
  },
  usageExamples: [
    { label: "seedance2.5 5s", facts: { seconds: 5 } },
    { label: "kling-video-v3 5s", facts: { seconds: 5 } },
    { label: "wan3-720p 5s", facts: { seconds: 5 } },
    { label: "hailuo-h3 10s", facts: { seconds: 10 } },  ],
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

export function extractUsage(ctx) {
  const seconds = requestedSeconds(ctx.requestBody || {});
  return { seconds: seconds === undefined ? DEFAULT_SECONDS : seconds };
}

// Measured duration replaces the submitted estimate when the upstream reports
// it; an omitted value keeps the estimate recorded at submit time.
export function extractUsageOnComplete(task, result, body) {
  const source = body || {};
  const measured = Number(
    source.seconds === undefined ? (source.duration === undefined ? source.video_duration : source.duration) : source.seconds
  );
  if (!Number.isFinite(measured) || measured <= 0) return {};
  return { seconds: Math.min(measured, MAX_SECONDS) };
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
