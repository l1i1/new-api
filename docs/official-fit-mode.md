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
    K3（2026-08-27 对照 api.moonshot.cn 实测校准）：temperature=1.0 / top_p=0.95 / n=1 /
    presence_penalty=0 / frequency_penalty=0、logprobs=true 拒绝、top_logprobs 成对要求、
    tool_choice 指定函数对象 → 400。注意：实测官方**不**校验 reasoning_effort 字符串枚举
    （"ultra" 也 200）、**接受** K2.x 的 thinking 字段（静默忽略）、max_completion_tokens
    也不做 1M 硬校验——这些本地一律不拦截）。
  - `errors`：校验错误消息保持官方原文（不附加网关 request id、以官方 Content-Type 返回）。
  - `shape`：响应形态拟合（DS 官方 7 键 usage、流式 usage 单次拼接、剥离聚合器扩展字段、
    SSE Content-Type 镜像；K3 暂无非官方形状可处理，保持透传）。
  - `route`：整族请求固定路由到官方渠道——DS 按渠道类型 43（官方 api.deepseek.com）、
    K3 按渠道类型 25（Moonshot，渠道 CN_Kimi id 130），复用 `ContextKeyV4OfficialPin`
    机制（distributor 选路前标记，选路时按模型族窄化到对应类型）。
    **route 是官方 pin 的唯一触发源**（2026-09-05 起）：早先的"极端采样自动 pin"
    （temperature>1.5 / top_p<0.3 / penalty>1.0 / thinking 字段 / logprobs=true 自动
    钉到官方渠道）已删除——它会在官方渠道不可用时反复清掉渠道粘性缓存，导致
    deepseek-v4 流量永远无法粘在聚合渠道上（prompt cache 全碎）。未开启 route 的用户
    无论带什么采样参数，都保持正常聚合器路由与粘性。
    注意：CN_Kimi 当前为 Moonshot 官方账号最低档限速（org RPM 3）且 priority=0——
    开启 K3 route 前必须先与 Moonshot 谈大额限速，否则买家流量会持续 429。

## 行为映射

| 现有行为 | 变更 |
|---|---|
| `relay/helper/valid_request.go` DS 校验（`validateDeepSeekV4OfficialFields` / `validateDeepSeekV4Logprobs`） | 改为 `profile.Validate` 门控；新增 `validateKimiK3OfficialFields`（同门控） |
| `relay/channel/openai/relay-openai.go` `isV4OpenAIStream` / fit 分支（145/214/660） | 改用 `deepSeekV4FitEnabled`（= isDeepSeekV4ChatModel && profile.Shape） |
| `relay/channel/openai/helper.go:308` 合成 usage 分支 | `!isDeepSeekV4ChatModel` → `!deepSeekV4FitEnabled`（fit 关闭时 DS 走通用合成路径） |
| `relay/helper/common.go` `isDeepSeekV4StreamModel`（stream_scanner Content-Type） | 加入 profile.Shape 判定 |
| `relay/channel/openai/relay-openai.go` `requiresDeepSeekV4ReasoningLogprobs` | 加入 profile.Validate 判定 |
| `controller/relay.go` 错误原文 + octet-stream | `IsDeepSeekV4ValidationMessage`（仅 DS）→ `IsStrictFitValidationMessage`（DS+K3），并加 `profile.Errors` 门控 |
| `middleware/distributor.go` `markV4OfficialPinFromDistributor` | 增加 profile.Route 时整族 pin（选路前生效）；DS 与 K3 均可（按模型族类型窄化） |

## 管理入口

- 管理员接口 `PUT /api/user/official-fit`（AdminAuth）：body `{ "user_id": 123, "official_fit": {...} | null }`。
  读-改-写 `users.setting`，不覆盖用户自助设置；审计动作 `update_official_fit`。
- 用户自助 `PUT /api/user/setting` 保留 `official_fit` 键，不再整体替换。
- Web 管理端：用户编辑抽屉（update 模式）"Official Fit" 区块，按模型族（DeepSeek V4 /
  Kimi K3）展示 4 个 Switch；提交时通过独立接口写入。

## 已知限制

- K3 校验规则的官方行为与文案已按 2026-08-27 实测校准（`relay/helper/valid_request.go`
  顶部常量）：官方接受 thinking 字段与任意 reasoning_effort 字符串、不校验
  max_completion_tokens 上限，本地校验仅覆盖固定采样参数 / logprobs / 指定 tool_choice。
- `reasoning_effort` 传非字符串（数字）时官方返回专属类型错误文案；网关在 DTO 反序列化层
  报错，无法复刻该文案（占位偏差，记录中，暂不处理）。
- K3 官方渠道（type 25）已达最低限速档（org RPM 3）；大用量场景启用 K3 `route` 前需
  先与 Moonshot 协商限速，或将 CN_Kimi 仅作为基准/校验渠道。
- 无配置用户（绝大多数）行为与启用前完全一致，仅当 profile 命中才改变。

## 验收

1. `go build ./...` + `go vet` + `go test ./relay/... ./controller/... ./middleware/... ./model/...`；relaykit `go test ./...`。
2. web `bun run typecheck`。
3. 单测覆盖：匹配器（精确/前缀/`*`/最严匹配/空配置）、DS 校验门控、K3 全规则、fit 开关两态、
   logprobs 硬校验门控、错误原文门控、管理员接口审计。
