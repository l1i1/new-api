# Kimi K3 思考续写（900s 截断后不提前打断）

## 行为

流式 OpenAI chat 请求命中上游异常终止（`finish_reason=error` / `engine_overloaded` 等非官方值）时：

1. 第一轮若 **正文 content 为空、已产生 reasoning**，网关不把上游错误终帧转给客户端；
2. 把已捕获的 reasoning 作为 `assistant` 正文回传，并追加一条“从中断处继续思考、给最终答案”的指令；
3. 在同一个客户端 SSE 流里转发第二轮的 reasoning/content，只发一个 `finish_reason=stop` 终帧；
4. 计费：输入 token 只计一次（第一轮 prompt）；两轮实际下发到客户端的 output token 都计费。

不满足条件（有正文、reasoning 过短/被截断、工具调用、非流式、非 OpenAI chat、渠道未白名单）时保持原有行为：异常终帧透传 + 502 + 退款。

## 开关（默认关）

| 环境变量 | 默认 | 说明 |
|---|---|---|
| `RELAY_CHAT_CONTINUATION_ON_ABNORMAL` | `false` | 总开关 |
| `RELAY_CHAT_CONTINUATION_CHANNELS` | 空 | 渠道白名单，逗号分隔；两种续写共用 |
| `RELAY_CHAT_CONTINUATION_ABNORMAL_MAX_STEPS` | `1` | 最多续写轮数；每轮可能再等一个上游超时窗口 |
| `RELAY_CHAT_CONTINUATION_ABNORMAL_MAX_REASONING_BYTES` | `524288` | reasoning 捕获上限；超限跳过续写，走原失败路径 |
| `RELAY_CHAT_CONTINUATION_ABNORMAL_MIN_REASONING_CHARS` | `512` | 低于该长度不续写 |

`RELAY_CHAT_CONTINUATION_MAX_STEPS` 仍只控制 `finish_reason=length`（32k 输出上限）的续写，与本模式独立。

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

- 单元：`TestBuildChatThinkingContinuationBody`、`TestMergeContinuationUsageBillsInputOnce`、`TestOaiStreamHandlerContinuesThinkingAfterAbnormalFinish`、`TestOaiStreamHandlerFallsBackWhenThinkingContinuationFails`、`TestOaiStreamHandlerSkipsThinkingContinuationWhenReasoningTooLarge`、`TestChatAbnormalContinuationStepsForRequest`。
- 回归：`go test ./...` 全绿；`go vet ./relay/... ./common/...` 干净。
- 本地假上游 e2e：续写成功、单终帧、输入一次、输出两轮计费；失败轮回落 502/退款。

## 已知边界 / 未做

- 只处理 **content=0 + reasoning** 的形态；已有部分正文的异常截断不在此模式（需另设计）。
- 两轮之间没有独立心跳；第二轮首包前客户端可能静默（上游首包通常数秒到数十秒）。
- 续写轮再失败：回落原有异常失败路径（不按成功计费）。
- 客户端在续写过程中断开时的计费分支尚未单独实现；当前会落入失败路径。
- 原生 `/v1/responses`、Claude、Gemini 格式不在本模式范围。
- 不提前截断：不在 300–600s 主动打断，只在 900s 异常终止后触发。

## 上线建议

1. 先选 1–2 个渠道（如 ch37/ch91）灰度，`MAX_STEPS=1`；
2. 观察 24h：续写成功率、第二轮耗时、客户端超时率、单条计费与退款行为；
3. 客户端确认超时 >20 分钟后，再扩大白名单；
4. 回滚：清空 `RELAY_CHAT_CONTINUATION_CHANNELS` 或关闭 `RELAY_CHAT_CONTINUATION_ON_ABNORMAL`。
