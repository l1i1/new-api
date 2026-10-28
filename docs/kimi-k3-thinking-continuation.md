# Kimi K3 思考续写（900s 截断后不提前打断）

## 三种模式

| 模式 | 触发 | 可续前缀 | 目标渠道 |
|---|---|---|---|
| length（32k） | `finish_reason=length` + 可见正文 | assistant 正文 | 同渠道，多轮 |
| abnormal（900s） | `finish_reason=error` 等异常值 + content=0 + reasoning | reasoning 作为 assistant 正文 | 同渠道，1 轮 |
| missing（60s 静默截断） | 从未出现 `finish_reason`（EOF/[DONE]）+ content=0 + reasoning | reasoning 作为 assistant 正文 | 同渠道，1 轮 |

missing 模式只在上游侧 EOF、不是客户端断开、且 `streamMissingFinishMarksTruncation` 成立时触发，避免把正常的无 finish 形态或客户端断开误判成可续。

## 行为（abnormal / missing）

流式 OpenAI chat 请求命中上游异常终止（`finish_reason=error` / `engine_overloaded` 等非官方值）时：

1. 第一轮若 **正文 content 为空、已产生 reasoning**，网关不把上游错误终帧转给客户端；
2. 把已捕获的 reasoning 作为 `assistant` 正文回传，并追加一条“从中断处继续思考、给最终答案”的指令；
3. 在同一个客户端 SSE 流里转发第二轮的 reasoning/content，所有 delta 的 `id`/`created`/`model`/`system_fingerprint` 归一化为第一轮的值，只发一个终帧（正常为 `stop`，也可能由上游给出 `length`/`tool_calls`）；
4. 计费：输入 token 只计一次（第一轮 prompt）；两轮实际下发到客户端的 output token 都计费。

不满足条件（有正文、reasoning 过短/被截断、工具调用、非流式、非 OpenAI chat、渠道未白名单）时保持原有行为：异常终帧透传 + 502 + 退款。

## 配置（数据库 OptionMap，后台在线编辑）

所有开关都存放在数据库 `options` 表（`OptionMap`），后台 **系统设置 → 运营 → Chat Continuation** 可直接编辑，保存后当前节点立即生效，其他节点通过 Redis 配置变更/周期同步生效。环境变量只作为首次建库的默认种子；已有数据库以 DB 值为准，不再读取环境变量。

| Option key | 默认 | 说明 |
|---|---|---|
| `relay_chat_continuation_on_abnormal` | `false` | 900s / 异常终止续写开关 |
| `relay_chat_continuation_on_missing` | `false` | 缺失 finish 续写开关 |
| `relay_chat_continuation_channels` | 空 | 渠道白名单，逗号分隔；所有续写模式共用 |
| `relay_chat_continuation_max_steps` | `0` | 32k length 续写轮数；0 关闭 |
| `relay_chat_continuation_abnormal_max_steps` | `1` | 900s 续写轮数；每轮可能再等一个上游超时窗口 |
| `relay_chat_continuation_abnormal_max_reasoning_bytes` | `524288` | 900s reasoning 捕获上限；超限走原失败路径 |
| `relay_chat_continuation_abnormal_min_reasoning_chars` | `512` | 900s 最低 reasoning 长度（按 UTF-8 字节） |
| `relay_chat_continuation_missing_max_steps` | `1` | missing 续写轮数；建议保持 1 |
| `relay_chat_continuation_missing_max_reasoning_bytes` | `262144` | missing reasoning 捕获上限 |
| `relay_chat_continuation_missing_min_reasoning_chars` | `64` | missing 最低 reasoning 长度（按 UTF-8 字节） |

对应的环境变量（`RELAY_CHAT_CONTINUATION_*`）只用于首次建库默认值；修改环境变量不会覆盖已有 DB 值。

## 计费

`mergeContinuationUsage` 的合并规则（`relay/channel/openai/chat_continuation.go`）：

- `PromptTokens` / `InputTokens` / 缓存字段：保留第一轮，不累加第二轮 prompt；
- `CompletionTokens` / `OutputTokens` / reasoning 明细：累加每一轮；
- `TotalTokens = PromptTokens + CompletionTokens`；
- 若 usage 带 `BillingUsage`，用合并后的 top-level 重建 OpenAI 语义副本，避免结算走旧副本漏掉第二轮输出。

本地 e2e 实测（伪造上游）：客户端 usage `prompt_tokens=5, completion_tokens=13`；消费日志一行 `pt=5, ct=13`，无 error 行；客户端只收到一个 `stop` 和一个 `[DONE]`。

## 客户端超时前提

“不提前打断”意味着总时长 = 第一轮 900s + 续写轮（实测真实难题约 290s）≈ **20 分钟**。客户端/中间层超时必须大于该值；若客户端在 900s 就断开，第二轮没有意义。这是部署前必须由客户确认的前提。

## 已验证

- 单元：`TestBuildChatThinkingContinuationBody`、`TestMergeContinuationUsageBillsInputOnce`、`TestOaiStreamHandlerContinuesThinkingAfterAbnormalFinish`、`TestOaiStreamHandlerContinuesMissingFinishStream`、`TestOaiStreamHandlerFallsBackWhenThinkingContinuationFails`、`TestOaiStreamHandlerFallsBackWhenMissingFinishContinuationFails`、`TestOaiStreamHandlerSkipsThinkingContinuationWhenReasoningTooLarge`、`TestOaiStreamHandlerSkipsMissingFinishContinuationWithoutReasoning`、`TestOaiStreamHandlerDedupesRepeatedRoundUsage`、`TestOaiStreamHandlerSuppressesFinishOnDeltaChunk`、`TestChatAbnormalContinuationStepsForRequest`、`TestChatMissingContinuationStepsForRequest`、`TestExtendWriteDeadlineForSetsFutureDeadline`、`TestContinuationKeepsWriteDeadlineAlive`、`TestSendThinkingContinuationSuppressesUpstreamPinger`。
- 回归：`go test ./...` 全绿；`go test -race ./relay/channel/openai/ ./relay/common/` 通过；`go vet ./relay/... ./common/...` 干净。
- 本地假上游 e2e：续写成功、单终帧、输入一次、输出两轮计费；失败轮回落 502/退款。

## 已知边界 / 未做

- 只处理 **content=0 + reasoning** 的形态；已有部分正文的异常截断不在此模式（需另设计）。
- missing 模式默认**同渠道**续写；如果上游 60 秒墙仍是请求级总预算，第二轮可能再次被截断。**跨渠道续写已于 2026-10-29 实现**（`relay/continuation_fallback.go`）：同渠道连续发不出续写轮时，用一次 fallback 换到另一个 OpenAI 兼容渠道，绑定完整（选路、模型映射、凭据/多 key、header/param override、并发槽），计费与日志仍归属原渠道；详见本文末尾“跨渠道续写”。
- missing 模式只做 1 轮，避免 60s×N 空转；续写轮仍失败则回落退款。
- 第二轮等待上游响应头期间每 10s 写一条 SSE ping（`sendThinkingContinuationWithKeepAlive`），并在每次 ping/正文/终帧写出前调用 `helper.ExtendWriteDeadline`，避免 30s 客户端写超时后静默丢帧；即使渠道关闭 ping，也会周期性续写 deadline。
- 续写执行器会临时关闭 `info.DisablePing` 对应的上游 SSE pinger，由网关单一持有客户端 writer，避免两个 goroutine 并发写同一 SSE。
- 续写轮再失败或只产出 reasoning、没有可见正文：回落原有异常失败路径（不按成功计费）。
- 客户端在续写过程中断开：按已观测用量计费并返回成功（与普通流的 client_gone 口径一致）。
- 原生 `/v1/responses`、Claude、Gemini 格式不在本模式范围。
- 前提：请求带 `max_tokens`（否则 `ClientMaxTokens<=0` 不续写）、传输以正常 EOF/[DONE] 结束、`n<=1`、未开启 `thinking_to_content`、非官方 fit V4/K3 路径；JSON error 事件（无 finish_reason）不可恢复。
- 续写轮仍受 `RELAY_STREAM_RESPONSE_HEADER_TIMEOUT`（默认 300s）约束；实测 290s 的第二轮已接近该上限，必要时单独调大。
- 不提前截断：不在 300–600s 主动打断，只在 900s 异常终止后触发。

## 上线建议

1. 先选 1–2 个渠道（如 ch37/ch91）灰度，`MAX_STEPS=1`；
2. 观察 24h：续写成功率、第二轮耗时、客户端超时率、单条计费与退款行为；
3. 客户端确认超时 >20 分钟后，再扩大白名单；
4. 回滚：清空 `RELAY_CHAT_CONTINUATION_CHANNELS` 或关闭 `RELAY_CHAT_CONTINUATION_ON_ABNORMAL`。

## 跨渠道续写（2026-10-29）

- 触发：续写轮在**本渠道连请求都发不出去**（`Send` 返回错误）时，消耗一次 fallback 换渠道；上游返回非 200 或空轮仍在同渠道重试，避免丢掉 prompt cache。
- 限制：仅 OpenAI 兼容渠道（续写 body 与响应解析都是 OpenAI 形态）；`tool_calls` 进行中、official-fit 信封、音频、多 choice 不换道。
- 绑定：`SelectChannelForRequest`（retry>0 绕过 session affinity、排除原渠道与冷却渠道）→ `SetupContextForSelectedChannel` → `InitChannelMeta` → `ModelMappedHelper` → 凭据/多 key → header/param override → 渠道并发槽（持有到该轮 body 关闭）。
- 归属：hop 前对 relay info 与渠道上下文做快照，hop 后恢复，因此计费、请求日志、客户可见流仍归属原渠道；一轮 hop 只算一轮，不做第二次计费。
- 遥测：续写结果按**实际执行轮次的渠道**记录（`ContinuationRoundChannel`），供 `GET /api/continuation_capabilities` 与 `deployment/tokeness-cn/continuation-whitelist.py` 生成白名单建议（默认只读，`--apply` 才写配置）。
- 关键回归用例：`TestOaiStreamHandlerFallsBackToAnotherChannelWhenContinuationCannotStart`、`TestOaiStreamHandlerCreditsResumedRoundToItsChannel`、`TestOaiStreamHandlerKeepsOneTerminalWhenFinishAndUsageAreSeparateChunks`、`TestOaiStreamHandlerDeliversHeldTailBeforeRepair`、`TestOaiStreamHandlerRepairsTransportFailureAfterVisibleContent`。
