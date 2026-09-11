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
      "deepseek-v4":  { "validate": true, "errors": true, "shape": true, "route": true },
      "kimi-k3":      { "validate": true, "errors": true }
    }
  }
}
```

- `profile` 的 key 是模型族匹配前缀（小写、不区分大小写）；匹配 = 精确相等 或 前缀匹配，
  更长前缀优先，`*` 为兜底。未命中（或未配置）→ 全部维度关闭（平台兼容行为）。
  DeepSeek 族的规范 key 是 **`deepseek-v4`（无尾横线）**，同时覆盖 `deepseek-v4-*` 与
  `deepseek-v4.1-*` 两条线；旧的 `deepseek-v4-` key 匹配不到带点的 v4.1 名称，管理端读取时
  会自动归并到 `deepseek-v4`、下次保存时落库。
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
    - **`shape` 只做形态变换，不再拒绝响应（2026-09-11 修复）**。此前存在一个
      "reasoning 必须先于 content" 的响应级 gate：非官方渠道 + `shape` 开启 + 期望思考时，
      缺 `reasoning_content`（流式）或先出 content（非流式）即整条拒绝。但**只有官方端点保证
      该顺序**，聚合器不保证——于是 `route` 关闭（请求落到聚合器）的用户被系统性判失败
      （流式 502 `upstream returned empty final content`、非流式 502
      `upstream did not return reasoning_content in thinking mode`），并触发跨全部候选渠道的
      重试风暴。该 gate 已删除：想要官方顺序保证的用户应开启 `route` pin 官方渠道；未开启者
      接受聚合器的 content-only 应答。空输出仍判失败。
  - `route`：**选择性官方路由（2026-09-06 起，DS 为 hybrid 分类器）**。Route 开启时按请求特征
    决定是否 pin 官方渠道（DS 按渠道类型 43、K3 按类型 25；候选集中同时包含渠道级
    `official_fit_models` 白名单命中的渠道，见下文），复用 `ContextKeyV4OfficialPin`
    机制（distributor 选路前标记，选路时按模型族窄化到对应类型/白名单）。
    - DeepSeek V4：仅三类请求 pin 官方——① `logprobs=true`（聚合器无法复刻官方双路
      logprobs）；② messages 含 `image_url` part（聚合器兼容性未验证；且 2026-09-06 实测
      官方已接受文本模型传图，旧 400 契约不复存在）；③ 思考输出类请求（缺省 / enabled /
      adaptive / effort≠none——实测聚合器池会**非确定性**丢失 `reasoning_content`，
      CN 实测 DS-001/090/091 因此失败）。唯一不 pin 的思考形态是**显式关闭思考**
      （`thinking.type=disabled` 或无 thinking 对象时 `reasoning_effort=none`）：官方返回
      `reasoning: null`，聚合器池实测可稳定复现，走廉价渠道。畸形 thinking 值按
      "思考输出"分类（relay 校验会在进渠道前按官方文案本地 400，多 pin 零成本）。
    - K3 / GLM：保持整族 pin（K3 官方为唯一已验证通道；GLM 思考不可关同理）。
    **route 仍是官方 pin 的唯一触发源**（2026-09-05 起）：早先的"极端采样自动 pin"
    （temperature>1.5 / top_p<0.3 / penalty>1.0 / thinking 字段 / logprobs=true 自动
    钉到官方渠道）已删除——它会在官方渠道不可用时反复清掉渠道粘性缓存，导致
    deepseek-v4 流量永远无法粘在聚合渠道上（prompt cache 全碎）。未开启 route 的用户
    无论带什么采样参数，都保持正常聚合器路由与粘性。
    注意：CN_Kimi 当前为 Moonshot 官方账号最低档限速（org RPM 3）且 priority=0——
    开启 K3 route 前必须先与 Moonshot 谈大额限速，否则买家流量会持续 429。
    成本事实（2026-09-06 CN 实测）：thinking 输出类是买家（ZCode agent 流量）的主体，
    在聚合器池被证明无法保证 100% 拟合前，这部分必须留官方；进一步压缩官方用量的
    前提是**逐渠道验证**（每渠道 × audit 200 例多轮重采样稳定通过后白名单化），
    该机制由下文的渠道级 `official_fit_models` 提供。

    **渠道级官方行为白名单（2026-09-11 新增）**：`channel.settings.official_fit_models`
    是渠道级的"官方行为"模型白名单（逗号分隔的模型 id 列表，大小写不敏感，落库前
    归一化去重，上限 64 条）。声明后，该渠道对**所列模型**即被视为官方行为上游，
    与"模型族的官方渠道类型"（DS=43 / K3=25 / GLM=26）**取并集**参与 pin 候选筛选：
    - 用途：转售商把 `deepseek-v4.1-flash` 映射到官方 `deepseek-flash`（如 ch12/ch17
      `type=1`），实测结构/工具状态机与官方一致，但渠道类型不是 43，旧逻辑永远不会
      把它当作 pin 目标。白名单让实测通过的转售渠道无需改渠道类型即可承接 pin 流量。
    - **按模型生效，不整族提升**：未声明的同族模型（如混合渠道只声明了
      `deepseek-v4.1-flash`，却同时提供 `deepseek-v4-flash`）在该渠道上**不算**官方，
      避免把一个第三方的 v4-flash 连带当成官方。
    - **不越过族边界**：`OfficialFitChannelType(m)==0` 的模型（如 `gpt-4o`）即使被写进
      白名单也不会被视为官方；渠道保存时即拒绝（`ValidateSettings` → "is not an
      official-fit model family"）。
    - **空列表 = 旧行为**：未配置的渠道完全不变。
    - 保存时校验：条目数 ≤ 64、不允许空条目（`dto.ChannelOtherSettings.
      ValidateOfficialFitModels`），并在 `model.ValidateSettings` 校验家族归属。

    **pin 与亲和的不对称（2026-09-11，随白名单一起修正）**：`officialPinAllowsAffinity`
    的两个方向回答不同问题——
    - pin 请求：缓存渠道必须是"本模型的官方行为渠道"（官方类型 **或** 白名单声明），
      否则清掉粘性、改选官方渠道；
    - 未 pin 请求：只排除"模型族的官方**渠道类型**"，防止先前 pin 流量残留的官方粘性
      劫持整个亲和 key。**声明了白名单的聚合器渠道（type≠官方类型）对未 pin 请求是
      普通候选，保留其亲和**——否则会对已验证转售渠道的 prompt cache 造成无谓打断。
    判定所需的渠道级白名单查询走缓存索引（`model.ChannelIsOfficialFitForModel`），
    内存缓存关闭时回退 DB 读取。
    - **亲和与 pin 的互斥按「本次请求是否 pin」判定（2026-09-11 修复）**：官方渠道的粘性
      绑定只可能由 pin 请求写入，因此规则是纯粹的请求级判定——pin 请求必须落在官方渠道
      （排除聚合器粘性），未 pin 请求**必须排除官方渠道粘性**。此前该排除额外要求用户当前
      仍开启 Route，导致关掉 Route 后遗留的官方粘性绑定会劫持该亲和 key 的全部流量
      （含关思考请求；key 回退到 `token_id` 时即整个凭据）。判定抽为
      `middleware.distributor` 的 `officialPinAllowsAffinity`，家族类型用
      `model.OfficialFitChannelType`。

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
| `model/channel_cache.go` `preferOfficialFitChannels` / `OfficialFitChannelType` | 候选窄化从"仅官方渠道类型"扩展为"官方类型 ∪ `channel.settings.official_fit_models` 白名单"（按模型，不整族）；新增 `ChannelIsOfficialFitForModel` 缓存索引查询 |
| `model/ability.go` `preferOfficialFitAbilities` | 无内存缓存（DB）路径同步支持白名单（`SELECT id, type, settings`） |
| `middleware/distributor.go` `officialPinAllowsAffinity` | 改为 (pinActive, officialType, preferredType, preferredIsOfficialBehavior)，pin 用白名单并集、未 pin 只排除官方渠道类型 |

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
- **渠道白名单是人工信任标记，不做运行时校验**：`official_fit_models` 只表示"该渠道
  在这些模型上已通过离线验证"，网关不会为每次请求重新确认上游真的拟合。白名单必须
  以审计/一致性套件的实测结果为依据（见 `docs/mainland-v4x-channel-report-*.md`），
  并在上游行为变化后重新验证。写错白名单会让 pin 流量落到非官方行为的渠道。

## 验收

1. `go build ./...` + `go vet` + `go test ./relay/... ./controller/... ./middleware/... ./model/...`；relaykit `go test ./...`。
2. web `bun run typecheck`。
3. 单测覆盖：匹配器（精确/前缀/`*`/最严匹配/空配置）、DS 校验门控、K3 全规则、fit 开关两态、
   logprobs 硬校验门控、错误原文门控、管理员接口审计、**渠道白名单**
   （`relaykit/dto`: 归一化/去重/上限/空条目/JSON round-trip；`model`:
   `IsOfficialFitChannelForModel` 按模型生效、缓存索引查询、pin 候选窄化压过高优先级
   非官方渠道、无候选时硬失败；`middleware`: 亲和判定 pin/未 pin 不对称——未 pin 的
   白名单聚合器保留亲和；web: `official-fit-models.test.ts` 表单 round-trip）。
