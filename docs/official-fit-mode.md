# Official Fit Mode（官方一致性模式）— Tech Spec

> 用户级、细粒度的"严格拟合官方 API"开关。对特定用户开启后，DeepSeek V4 / Kimi K3
> 的请求校验、错误消息、响应形态与渠道路由按官方行为执行；默认关闭，平台保持兼容行为。
>
> 背景：大客户（对公、压测、协议校验）要求与官方 API 完全一致（含 400 拒绝语义与错误文案），
> 而平台其余用户依赖兼容性适配。两者通过 per-user 配置共存。

## 配置模型

存储在 `users.setting` JSON（`dto.UserSetting`），键 `official_fit`：

```json
{
  "official_fit": {
    "profile": {
      "deepseek-v4-": { "validate": true, "errors": true, "shape": true, "route": true },
      "kimi-k3":      { "validate": true, "errors": true }
    }
  }
}
```

- `profile` 的 key 是模型族匹配前缀（小写、不区分大小写）；匹配 = 精确相等 或 前缀匹配，
  更长前缀优先，`*` 为兜底。未命中（或未配置）→ 全部维度关闭（平台兼容行为）。
- 四个维度互相独立：
  - `validate`：官方参数校验，本地按官方 400 拦截（DS：temperature∈[0,2]、top_p∈(0,1]、
    reasoning_effort 枚举、json_object 需含 "json" 字样、top_logprobs 规则、双路 logprobs 硬校验；
    K3：`thinking` 参数非法、reasoning_effort∈{low,high,max}、temperature=1.0 / top_p=0.95 /
    n=1 / presence_penalty=0 / frequency_penalty=0 / top_logprobs∈[0,20]、
    tool_choice 指定函数对象 → 400）。
  - `errors`：校验错误消息保持官方原文（不附加网关 request id、以官方 Content-Type 返回）。
  - `shape`：响应形态拟合（DS 官方 7 键 usage、流式 usage 单次拼接、剥离聚合器扩展字段、
    SSE Content-Type 镜像；K3 暂无非官方形状可处理，保持透传）。
  - `route`：整族请求固定路由到官方渠道（ChannnelTypeDeepSeek 43，复用
    `ContextKeyV4OfficialPin` 机制）；K3 暂时 no-op——当前平台没有 Moonshot 官方直连渠道，
    接入后可复用同一 pin 机制。

## 行为映射

| 现有行为 | 变更 |
|---|---|
| `relay/helper/valid_request.go` DS 校验（`validateDeepSeekV4OfficialFields` / `validateDeepSeekV4Logprobs`） | 改为 `profile.Validate` 门控；新增 `validateKimiK3OfficialFields`（同门控） |
| `relay/channel/openai/relay-openai.go` `isV4OpenAIStream` / fit 分支（145/214/660） | 改用 `deepSeekV4FitEnabled`（= isDeepSeekV4ChatModel && profile.Shape） |
| `relay/channel/openai/helper.go:308` 合成 usage 分支 | `!isDeepSeekV4ChatModel` → `!deepSeekV4FitEnabled`（fit 关闭时 DS 走通用合成路径） |
| `relay/helper/common.go` `isDeepSeekV4StreamModel`（stream_scanner Content-Type） | 加入 profile.Shape 判定 |
| `relay/channel/openai/relay-openai.go` `requiresDeepSeekV4ReasoningLogprobs` | 加入 profile.Validate 判定 |
| `controller/relay.go` 错误原文 + octet-stream | `IsDeepSeekV4ValidationMessage`（仅 DS）→ `IsStrictFitValidationMessage`（DS+K3），并加 `profile.Errors` 门控 |
| `middleware/distributor.go` `markV4OfficialPinFromDistributor` | 增加 profile.Route 时整族 pin（选路前生效） |

## 管理入口

- 管理员接口 `PUT /api/user/official-fit`（AdminAuth）：body `{ "user_id": 123, "official_fit": {...} | null }`。
  读-改-写 `users.setting`，不覆盖用户自助设置；审计动作 `update_official_fit`。
- 用户自助 `PUT /api/user/setting` 保留 `official_fit` 键，不再整体替换。
- Web 管理端：用户编辑抽屉（update 模式）"Official Fit" 区块，按模型族（DeepSeek V4 /
  Kimi K3）展示 4 个 Switch；提交时通过独立接口写入。

## 已知限制

- K3 校验错误文案按官方风格落地，**精确文案以 Moonshot 官方基准为准**（客户基准校准后
  只改 `relay/helper/valid_request.go` 顶部消息常量）。
- K3 `route` 维度当前无效：无 Moonshot 官方渠道（channels 表 2026-08-27 核查：
  kimi-k3 全部为 OpenAI 类型聚合渠道）；仅需通过 `validate` 保证拒绝语义确定性。
- 无配置用户（绝大多数）行为与启用前完全一致，仅当 profile 命中才改变。

## 验收

1. `go build ./...` + `go vet` + `go test ./relay/... ./controller/... ./middleware/... ./model/...`；relaykit `go test ./...`。
2. web `bun run typecheck`。
3. 单测覆盖：匹配器（精确/前缀/`*`/最严匹配/空配置）、DS 校验门控、K3 全规则、fit 开关两态、
   logprobs 硬校验门控、错误原文门控、管理员接口审计。
