# 视频用量估算与视频路由（supports_video / video_usage_mode）

## 问题

部分上游能理解视频，但**用量口径不可信**：实测某聚合上游对 3.3 MB 视频请求固定回报
`prompt_tokens: 27`，而它自己的账单按真实视频 token 扣费（同一请求扣 0.30 单位 vs
纯文本 0.0055 单位，**54 倍**）。官方同一请求是 **42,416**。

后果：我们按 27 计费但按 42,416 付费——成本与收入对不上，且缓存/TPM 类看板全部失真。

更严重的是**另一类上游根本读不了视频**：它不报错，而是**静默丢掉媒体**、仅凭文本作答，
产出一段看起来合理的「描述」。实测国内站最高优先级的聚合上游就是如此——视频请求落到
它上面，用户拿到的是没看过视频的答案。**计费再准也救不了选错渠道**，所以能力维度必须
先于计费维度存在。

## 解决（两个正交维度）

| 维度 | 字段 | 含义 | 作用点 |
| --- | --- | --- | --- |
| 能力 | `supports_video` | 该渠道上游**真的会读**视频 part | **选路** |
| 计费 | `video_usage_mode: "estimate"` | 该渠道上游**读得了但报不准** | **结算** |

两者独立：官方直连两者都正常（只标能力）；能读但恒报 27 的上游标两者；读不了的上游
（如最高优先级的聚合上游）**什么都不标**，于是视频请求会被选路自动绕开。

### 选路（`supports_video`）

请求体带 `video_url` part 时，该请求只会在**声明了能力的渠道**之间选择：

- 检测在**分发中间件**里做（body 已在手），置 `ContextKeyVideoRequest`；
- 收窄在选路内部完成（缓存路径与 DB 路径各一处），**不是**「先选再否决」——否则最高
  优先级是不可用渠道时，会返回「无可用渠道」而不是落到下一个优先级；
- `FilterVideoRequest` 过滤器同时守住**渠道钉定**与**亲和相关**这两条绕过选择器的路径；
- 没有任何渠道声明能力时，请求**诚实失败**（无可用渠道），而不是退化成「用看不见视频的
  上游作答」；
- 非视频请求完全不受影响：不声明能力的渠道照样按原优先级服务文本流量（声明不会让渠道
  被降级）。

### 计费（`video_usage_mode`）

开启后，带视频 part 的请求在**结算时**用视频感知的 prompt 数替换上游上报值，并标记为
「估算」：`prompt_tokens` 记的是**实际扣费的那个数**，消费日志的计费路径变为
`billing-usage-openai-estimated`（前端显示为「Upstream Response
(billing-usage-openai-estimated)」），与上游原值可区分。客户端响应的 usage
**不被改动**——它仍是上游自己的报告，两者在日志里可对照。

取值优先级（权威性从高到低）：

1. **provider tokenizer 总值** —— 在控制台配了端点 + 密钥（或环境里配了
   `VIDEO_ESTIMATE_BASE_URL` + `VIDEO_ESTIMATE_API_KEY`）时调用官方
   `POST /v1/tokenizers/estimate-token-count`（它接受 `video_url` part，
   实测与真实 prompt 差 **0.08%**，**免费且不占 RPM/TPM**——已用官方请求日志逐条核实）。
   它同时包含文本与媒体，因此**整体替换**上报值。调用在计费准备阶段**并发预取**（携带
   整个视频，实测 3.3 MB 约 2.8 s），结算只读缓存，**不给首字增加延迟**。
2. **本地容器模型** —— 从视频容器自身读取分辨率与时长，按标定公式定价（见下）。
   上游的文本计数是正确的，所以这里是**加到**上报值上。
3. **上游原值** —— 以上都不适用时不动（含未开启开关、非视频请求、容器无法解析）。

**媒体感知护栏**：只有当中被证明「没算媒体」时才替换。判定式是
`reported < video/2`——实测失败态是 27 或 96（不到视频价的 1%），而任何算过媒体的
来源至少等于视频价本身，两侧相距悬殊；**部分计数**的上游因此保持原值，不会被
重复计费。

**判定按「实际服务该请求的渠道」**：`info.ChannelMeta` 是**每次尝试**重建的，结算读它，
因此跨渠道重试不会把 A 渠道的修正算到 B 渠道的上报值上。这一条同时修掉了此前的一个
崩溃：早期实现在**建立 ChannelMeta 之前**读渠道设置（`request_billing.go` 的
`info.ChannelOtherSettings` 解引用 nil 指针），任何中继请求都会 panic——故计费准备阶段
改从 **gin context** 读渠道设置。


## 本地公式

15 点 ffmpeg 标定（`docs/cdp/mainland-k3-video-capability-20260921.md` §5）：

```
per_frame = min(pixels / 11400, 282)
frames    = 时长秒 × 30      # 与源 fps 无关
video_tok = min(frames × per_frame, 42320)
```

三条反直觉但已钉死的规则（单测固定，勿「优化」掉）：

- **与源 fps 无关**：15 fps 源不折价、60 fps 源不加价，只按时长；
- **15 帧下限**：1 帧的视频也按 15 帧计（4,241 tok）；
- **总量上限 42,320**：原生分辨率约 5.5 s 触顶（低分辨率可更长）。

精度：中/高分辨率 12 点全在 ±0.5% 内；640×346 偏差 −4~−8.5%（帧数取整主导）。
容器解析只读 moov/mvhd/tkhd，覆盖 mp4/mov（浏览器、ffmpeg、手机录像都适用）；
解析不了时退回保守固定值，不会把大文件算成免费。

## 配置

```jsonc
// 渠道 other settings（与 official_fit_models 同处）
{
  "supports_video": true,          // 上游真的会读视频 → 选路据此收窄
  "video_usage_mode": "estimate"   // 额外声明：读得了但报不准 → 结算据此修正
}
```

- `video_usage_mode` **必须与 `supports_video` 同时设置**：不带能力的计费模式是死配置
  （视频请求永远不会路由到该渠道），保存时直接拒绝（`ValidateVideoUsageMode`）。
- 非法值同样在保存时被拒绝，避免拼错后静默沿用失真值。
- 只标 `supports_video`（不标模式）= 「读得准」，如官方直连。

估算端点（可选，不配则只用本地模型）。两个设置都在控制台
**系统设置 → 集成 → Video token estimation** 里，保存即生效（渠道设置是运行时读的，
不需要重启；每个节点 60 s 内收敛）：

| 设置键 | 含义 |
| --- | --- |
| `video_estimate_setting.base_url` | 端点根地址，默认 `https://api.moonshot.cn/v1` |
| `video_estimate_setting.api_key` | tokenizer 所有者侧的 key（存储后不回显） |

环境变量 `VIDEO_ESTIMATE_BASE_URL` / `VIDEO_ESTIMATE_API_KEY` 仍然有效，作为
**bootstrap 与回退**：库里的值优先，且**逐字段**回退（只填了库里的 base URL、
key 留在环境里也能正常工作），清空库里的一项即把该字段交还环境。密钥走框架既有的
脱敏通道（`GetOptions` 跳过 `*Key`/`*Token`/`*Secret` 后缀的键），浏览器拿不到已存值，
提交空值表示「不修改」。`/api/status` 上的 `video_estimate_configured` 只报
「配没配」，不报任何值。

**为什么不默认开启**：调用它会把用户视频发给 tokenizer 的持有方。经第三方上游进来的
请求再转投官方，属跨供应商数据外发，必须由运营显式决定。端点答案按**视频内容哈希 +
端点 URL** 缓存（512 条上限），同一片段只调一次——单次调用实测 ~2.8 s。端点进键是为了
换端点后旧答案立即失效，而不是继续拿上一个 tokenizer 的数计费。

## 落点（改动清单）

| 文件 | 作用 |
| --- | --- |
| `relaykit/dto/channel_settings.go` | `SupportsVideo` / `VideoUsageMode` 字段 + 校验 + 判定 |
| `relaykit/dto/openai_request.go` | **修复**：`video_url` 支持对象形式（`{"url":...}`），此前只认字符串，官方文档形式被静默丢弃 |
| `model/channel.go` | 保存渠道时校验 |
| `model/channel_cache.go` | 能力索引 `channel2supportsVideo` + 缓存路径按能力收窄 |
| `model/ability.go` | DB 路径按能力收窄（**先过滤后分优先级**，否则高优先级不可用渠道会挡住后备） |
| `model/channel_constraint.go` / `dto/channel_constraints.go` | `FilterVideoRequest`（守住钉定/亲和路径） |
| `constant/context_key.go` / `middleware/distributor.go` | 请求级视频标记（body 层检测，置于选路前） |
| `service/channel_select.go` | 把标记透传给选择器（每次重试都保持收窄） |
| `service/video_token.go` | 请求/正则层视频检测、容器解析 + 标定公式 + 媒体感知护栏 |
| `service/video_estimate.go` | 官方估算端点客户端 + 内容哈希缓存 + 并发预取 + 端点解析（库 > env > 默认） |
| `setting/operation_setting/video_estimate_setting.go` | 控制台设置的注册（`video_estimate_setting` 模块） |
| `controller/misc.go` | `/api/status` 的 `video_estimate_configured`（只报配没配） |
| `web/src/features/system-settings/integrations/video-estimate-settings-section.tsx` | 控制台表单（集成页），含跨供应商外发的提示文案 |
| `service/token_counter.go` | `FileTypeVideo` 从固定 8192 改为按容器定价 |
| `relay/common/relay_info.go` | 请求携带的视频补算值 / 权威总值 |
| `relay/request_billing.go` | 计费准备阶段从 **gin context** 读渠道设置（避免 ChannelMeta 未建时 panic）+ 并发预取 |
| `service/text_quota.go` | 结算时按优先级替换（读**实际服务渠道**的设置），并标记 Estimated |
| `relaykit/dto/billing_usage.go` | `NewEstimatedOpenAIChatBillingUsage` |

## 验收

- `go test ./service/`：容器解析（含真彩/版本 1 头/垃圾输入）、15 点标定、
  三条规则、护栏阈值、端点解析与失败回退、`estimated` 标记、开关校验、
  body 层视频检测（含「文本里提到 video_url」不得误判）、两个检测器一致性、
  结算按实际服务渠道归属、无 ChannelMeta 时不 panic；**修正后的消费日志行**
  （`video_log_path_test.go`：走真实结算与落库，断言记录的是修正值且计费路径为
  `billing-usage-openai-estimated`——该用例在标签回退成上游原值时会失败）。
- `go test ./model/`：能力索引解析、缓存路径收窄、DB 路径「高优先级不可用渠道不挡住
  后备」、无渠道声明时诚实失败（返回 nil 而非盲渠道）、模式必须带能力。
- `go test ./relaykit/dto/`：`video_url` 三种拼写（ms:// 对象、http 对象、字符串）
  均能解析出可读媒体，无 url 的 part 不被当作视频。
- 真实样本回归（fixture 在仓库外，未设置则跳过）：
  `CDP_BENCH_VIDEO=../tools/cdp-bench/data/tmp/sample.mp4 go test ./service/ -run RealContainer -v`
  ——实测解析 3612×1952 5.533s → **42320**（与官方 42,416−96 吻合）。

## 上线顺序（重要）

**先部署代码，再写渠道标记**，两步都不可省：

1. **代码**：本改动（国内自 `v1.0.0-rc.37-tokeness-mainland.5`、海外自
   `v1.0.0-rc.37-tokeness-intl.8` 起已在线）。此前整条线（至 `mainland.4`）
   **完全不认识 `supports_video` / `video_usage_mode`**（解析结构体里没有这两个字段），
   且带着一个未部署的 P0：计费准备阶段在 `ChannelMeta` 建立之前解引用
   `info.ChannelOtherSettings`，任何中继请求都会 panic——该 P0 已由本改动一并修掉，
   所以「直接给旧主线打 tag」在修好前是禁止动作。
2. **渠道标记**：按实测逐个渠道写入（定级表见下）。

**为什么不能反过来**：标记是**惰性**的——老代码忽略未知字段，所以「先写标记」不会立刻生效，
但会在**下一次任何人部署该功能时静默变成线上选路**，而不是在一次受控、可验证的发布里生效。
代码上线后标记即写即生效（渠道设置是运行时读的），写入后立刻用真实视频请求验证。

国内站 `kimi-k3` 的实测定级（2026-09-21 晚，共 22 条渠道；当晚新测结果见
`docs/cdp/mainland-k3-new-channels-20260921.md`）：

| 渠道 | 实测行为 | 应写标记 |
| --- | --- | --- |
| ch8 `DEF_Kimi`（官方直连，enabled） | usage 真实（42,416） | `supports_video: true` |
| ch21 `DEF_gwlink`（disabled） | **午后** usage 真实（42,417）；**当晚 10 次尝试 0 成功**（静默丢弃/400 拒绝，异构池漂移） | **暂不写**，启用前重测 |
| ch17 / ch15 / ch14 / ch34 | **能读**但 usage 恒报 27 | `supports_video` + `video_usage_mode: estimate` |
| ch37 `DEF-neurvibe`（enabled，**priority 50 最高**） | **静默丢弃视频** | **两个都不写** |
| ch38/ch39 `DEF_APlink-B`/`DEF_APlink`（OFF-manual，同源） | 能读、usage 真实（≤1.9 MB；2.4 MB 上游 504） | 启用时可写 `supports_video: true`（**不要**加 estimate） |
| ch40 `DEF_Tongba-Tokens`（OFF-manual） | 同上（能读、usage 真实、ms:// 不可用） | 同上 |
| 其余各条 | 明确拒绝 / 到不了上游 / 静默丢弃 | 不写 |

**写标记前必须先拍板的一件事**：优先级顺序会让 `zzzzz` 系（ch17 priority 2、ch15 priority 16）
**高于**官方直连 ch8（priority 0）。即视频流量会优先走「能读但计费不可信」的池子，靠估算兜底，
而不是走 usage 精确的官方直连。若验收要求「原生精确 usage」，需先调整视频相关渠道的
priority，或只给 ch8/ch21 打标记（其余不打），让视频请求只能落到计费可信的渠道。


