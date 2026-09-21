# 视频用量估算（video_usage_mode）

## 问题

部分上游能理解视频，但**用量口径不可信**：实测某聚合上游对 3.3 MB 视频请求固定回报
`prompt_tokens: 27`，而它自己的账单按真实视频 token 扣费（同一请求扣 0.30 单位 vs
纯文本 0.0055 单位，**54 倍**）。官方同一请求是 **42,416**。

后果：我们按 27 计费但按 42,416 付费——成本与收入对不上，且缓存/TPM 类看板全部失真。

## 解决

渠道级开关 `video_usage_mode: "estimate"`。开启后，带视频 part 的请求在**结算时**用
视频感知的 prompt 数替换上游上报值，并标记为「估算」（消费日志的计费路径变为
`openai_estimated`，与上游原值可区分）。

取值优先级（权威性从高到低）：

1. **provider tokenizer 总值** —— 配了 `VIDEO_ESTIMATE_BASE_URL` + `VIDEO_ESTIMATE_API_KEY`
   时调用官方 `POST /v1/tokenizers/estimate-token-count`（它接受 `video_url` part，
   实测与真实 prompt 差 **0.08%**，**免费且不占 RPM/TPM**——已用官方请求日志逐条核实）。
   它同时包含文本与媒体，因此**整体替换**上报值。
2. **本地容器模型** —— 从视频容器自身读取分辨率与时长，按标定公式定价（见下）。
   上游的文本计数是正确的，所以这里是**加到**上报值上。
3. **上游原值** —— 以上都不适用时不动（含未开启开关、非视频请求、容器无法解析）。

**媒体感知护栏**：只有当中被证明「没算媒体」时才替换。判定式是
`reported < video/2`——实测失败态是 27 或 96（不到视频价的 1%），而任何算过媒体的
来源至少等于视频价本身，两侧相距悬殊；**部分计数**的上游因此保持原值，不会被
重复计费。

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
  "video_usage_mode": "estimate"
}
```

非法值在渠道保存时被拒绝（`ValidateVideoUsageMode`），避免拼错后静默沿用失真值。

估算端点（可选，不配则只用本地模型）：

```
VIDEO_ESTIMATE_BASE_URL=https://api.moonshot.cn/v1   # 默认值
VIDEO_ESTIMATE_API_KEY=<tokenizer 所有者侧的 key>
```

**为什么不默认开启**：调用它会把用户视频发给 tokenizer 的持有方。经第三方上游进来的
请求再转投官方，属跨供应商数据外发，必须由运营显式决定。端点答案按**视频内容哈希**
缓存（512 条上限），同一片段只调一次——单次调用实测 ~2.8 s。

## 落点（改动清单）

| 文件 | 作用 |
| --- | --- |
| `relaykit/dto/channel_settings.go` | `VideoUsageMode` 字段 + 校验 + 判定 |
| `model/channel.go` | 保存渠道时校验 |
| `service/video_token.go` | 容器解析 + 标定公式 + 媒体感知护栏 |
| `service/video_estimate.go` | 官方估算端点客户端 + 内容哈希缓存 |
| `service/token_counter.go` | `FileTypeVideo` 从固定 8192 改为按容器定价 |
| `relay/common/relay_info.go` | 请求携带的视频补算值 / 权威总值 |
| `relay/request_billing.go` | 仅在开关开启 + 有视频 part 时计算（默认路径零开销） |
| `service/text_quota.go` | 结算时按优先级替换，并标记 Estimated |
| `relaykit/dto/billing_usage.go` | `NewEstimatedOpenAIChatBillingUsage` |

## 验收

- `go test ./service/`：容器解析（含真彩/版本 1 头/垃圾输入）、15 点标定、
  三条规则、护栏阈值、端点解析与失败回退、`estimated` 标记、开关校验。
- 真实样本回归（fixture 在仓库外，未设置则跳过）：
  `CDP_BENCH_VIDEO=../tools/cdp-bench/data/tmp/sample.mp4 go test ./service/ -run RealContainer -v`
  ——实测解析 3612×1952 5.533s → **42320**（与官方 42,416−96 吻合）。
