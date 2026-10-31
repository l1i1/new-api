# 可观测性：请求日志字段与每节点负载

本文件描述 2026-10-31 起新增的两块运维可观测性能力，以及它们的字段契约。两块都
**只影响展示与统计，不改变路由、计费或客户端收到的字节**。

## 1. 请求日志新增字段（`/usage-logs/common`）

记录位置：`service/log_info_generate.go` 的 `AppendRelayLogAdminInfo`。成功（type=2
消费）与失败（type=5 错误）请求都走这个入口，因此失败请求同样能回答"哪个节点处理
的、报文多大、有没有续写"。

字段全部写进 `other.admin_info`（管理员可见；普通用户视图会被 `model/log_other.go`
剥掉整个 `admin_info`）。

```jsonc
"admin_info": {
  // 处理该请求的网关实例名。与 /system-info 的实例 node_name 同源
  // （common.NodeName：NODE_NAME 环境变量，未设时回退主机名）。
  "node_name": "i-2ze0abcdef",

  // 三段报文大小（字节）。任一为 0/未知则省略该键；全未知时整个对象省略。
  "body_bytes": {
    "request": 1234,           // 客户端请求体（解压后，缓存 BodyStorage.Size()）
    "upstream_request": 1250,  // 发往上游的最终请求体（最后一次尝试的 ContentLength）
    "response": 56789          // 写给客户端的响应体字节（gin Writer.Size()）
  },

  // 仅当本次请求真的发生过透明续写（至少 1 轮）时存在。
  "continuation": {
    "modes": ["length"],        // 见下表，原始 id，前端原样展示
    "rounds": 2,                // 实际用掉的续写轮数
    "max_steps": 2,             // 该模式配置的预算（每个 runner 自己的 MaxSteps）
    "finish": "stop",           // 拼装后最终送给客户端的 finish_reason
    "extra_completion_tokens": 4096  // 续写轮额外产生的 completion（合并 usage 与本轮首轮之差）
  }
}
```

续写模式 id 按 **runner** 归类（不是按截断形状）：

| id | 含义 | runner |
| --- | --- | --- |
| `length` | 任何"续写可见正文"的修复：输出上限（32k）、流内错误、传输中断、异常/缺失 finish 后正文已开始等形状都走这里 | `runChatContinuation` |
| `length-thinking` | 思考阶段撞上输出上限、还没有可见正文（32k 在思考阶段撞顶） | `runLengthThinkingChatContinuation` |
| `abnormal` | 上游异常 finish（如 900s 的 `finish_reason=error`）、只有思考 | `runAbnormalChatContinuation` |
| `missing` | 上游 EOF/`[DONE]` 但没有终帧 finish_reason、只有思考 | `runMissingChatContinuation` |

更细的截断形状（`stream-error`、`transport-error`、`abnormal-content`、`missing-content`）
只出现在容器 SysLog 的 `mode=` 里，用于和日志行对账；`admin_info.continuation.modes` 只记
上表的 runner id。

审计在 `relay/channel/openai/relay-openai.go` 里每个"把合并后的 usage 返回给结算"
的分支写入，包括客户端中途离开仍保留计费的早返回，因此 `rounds>0` 与账单口径一致。

`other.fit`（official-fit pin：`family` / `marks` / `rules` / `policy` / `baseline`）
是既有字段，由 `appendFitRequirementInfo` 写入；页面只负责展示。`fit` 存在即表示这次
请求被能力匹配收窄（pin）到官方行为的渠道。

前端：`web/src/features/usage-logs/lib/observability.ts`（纯展示模型 + 单测）、
`components/columns/common-logs-columns.tsx`（节点名 / 报文大小收在「详情」
单元格的副行里；fit pin 排在「渠道」单元格的**重试链 / 亲和图标之后**；Stream 列里
`Continued ×N` 紧跟「流」字）、`components/dialogs/details-dialog.tsx`
（Observability 详情段，含三段报文与 `max_steps` 的完整数字）、
`components/common-log-mobile-card.tsx`（移动端）。不单独占列是刻意的：表格宽度不被
这两类信息撑开，点开详情仍能看到全量。

## 2. 每节点负载（`/system-info`）

每个 relay 节点在既有的 30 秒实例心跳里多报一段 `info.extra.traffic`，前端在实例表
里显示 RPM / TPM / 入出流量 / **带宽** 四列。

```jsonc
"extra": {
  "traffic": {
    "window_seconds": 60,     // 下列速率字段的滚动窗口
    "rpm": 12,                // 窗口内完成的 relay 请求数
    "tpm": 4600,              // 窗口内结算的 prompt+completion token
    "prompt_tpm": 1200,
    "completion_tpm": 3400,
    "request_bytes": 123456,  // 窗口内客户端请求体字节
    "response_bytes": 4567890,// 窗口内写给客户端的响应体字节
    "requests_total": 987654, // 进程启动以来累计
    "prompt_tokens_total": 120000,
    "completion_tokens_total": 340000,
    "tokens_total": 460000,
    "request_bytes_total": 12345678,
    "response_bytes_total": 456789012
  }
}
```

口径：

- 计数器在节点进程内存里（`pkg/nodemetrics`：60 个 1 秒桶 + 累计值），不落库、不跨节点
  汇总；心跳每 30 秒快照一次，所以页面最迟 1 分钟内反映新数据（用户接受 1 分钟级）。
- `rpm` 由 `middleware.NodeMetrics` 在 relay 请求结束时计数（请求完成口径）。它只挂在
  **模型服务路由**上；task/video 查询、artifact 下载、MJ/suno fetch 这类客户端轮询 GET
  刻意不计入，否则一个每 2 秒轮询的客户端就能把节点 RPM 打满。GET/HEAD 里仍会计数的只有
  `/v1/responses`、`/v1/realtime`（WebSocket 模型转发）与 gemini 模型列表。
- `tpm` 来自 `model.RecordConsumeLog` 的结算 token（计费口径，`LogConsumeEnabled=false`
  也生效），与 `rpm` 的样本集合略有差异：未交付/零计费的请求 token 记 0，但请求仍计入 rpm。
- `request_bytes` / `response_bytes` 是应用层负载字节（解压后的请求体、写出的响应体），
  不是 TCP/压缩后字节，也不含 HTTP 头。
- **带宽不单独上报**：前端按 `request_bytes / window_seconds`、`response_bytes / window_seconds`
  推导出该统计周期内的平均入/出带宽（cell 显示 `↓ 2 KB/s ↑ 74.3 KB/s`，tooltip 给精确值
  并附 bits/s，因为"带宽"通常以 bit 计）。RPM / TPM / 流量三个 tooltip 同时给出节点
  **进程累计值**（`requests_total` / `tokens_total` / `*_bytes_total`），窗口长度文案取心跳
  上报的 `window_seconds`（默认 60 秒时仍用旧文案）。窗口缺失/为 0 时显示 `-`，不猜测 60 秒，避免把
  别的窗口标错。这样带宽与流量永远同源，不会出现两个字段各自漂移的问题。
- 进程重启后累计值归零；滚动窗口在重启后自然从 0 开始。节点名来自 `common.NodeName`，
  建议每个 ECI 实例配唯一的 `NODE_NAME`，否则用主机名（可读性差，且实例替换后会留下
  stale 实例行，用页面上的 "Delete stale" 清理）。

前端：`web/src/features/system-info/lib/traffic.ts`（格式化 + 单测）、
`components/system-instances-panel.tsx`。

## 3. 诊断字段的展示

以下字段在更早的版本就已写进 `other`（`admin_info` 或公开区），但控制台此前不展示；
2026-10-31 起在**日志详情弹窗**（管理员可见）里成段显示，纯前端改动、不改写入契约。

| 字段 | 写入方 | 展示位置 |
| --- | --- | --- |
| `admin_info.stream_cut` | `relay/common.StreamCutDiagnostics` | 错误行详情「切流诊断」（红色分区） |
| `admin_info.closed_as_tool_calls` | relay 工具调用收尾 | Observability 段「按完整工具调用闭合」 |
| `admin_info.billing_model` | 计费模型映射 | Observability 段「实际计费模型」 |
| `admin_info.last_upstream_error` | `controller/relay.go`（masked） | Observability 段「上次上游错误」 |
| `stream_status.closed_after_terminal` | `appendStreamStatus` | Observability 段「终帧后静默关闭」 |
| `continuation.max_steps` | 续写审计 | Observability 段「最大步数」 |
| `admin_info.billing_anomaly` / `undelivered` / `usage_deviation` | 计费审计（`AuditMap`） | 独立计费审计分区：kind/field/value/threshold/action/raw、未交付原因与上游 token |
| `admin_info.conversion_diagnostics`（+`_truncated`） | 协议转换诊断 | 「转换诊断」段：severity/from→to/path/code/message |
| `other.po` / `other.ro` | 参数覆盖 / 响应改写审计 | 「覆盖审计」段 |
| `other.thinking_type` / `reasoning_content_seen` / `upstream_reasoning_tokens` | 思考观测 | 「推理」段 |

`stream_cut` 是断流复盘的主要证据：`finish_reason`/`missing_finish`、可见与思考字符数、
`tool_calls`、`client_max_tokens`/`completion_tokens`/`budget_left`（非正的
`budget_left` 直接说明续写在设计上就不可能）、三种续写是否配置，以及
`repair_attempted`/`repair_mode`/`repair_rounds`/`repair_failed_reason`/`repair_channels`
（修复真正跑过但没有交付可用结果时才存在）。

### 请求捕获取样标记（request_capture）

目标请求被 `request_capture` 采样并落盘后，日志行会多一个
`admin_info.capture = { rule, file, captured_at, status? }`：

- 写入时机在**文件真正写完之后**，由 master 的 capture writer
  （`service.drainRequestCaptureQueue` → `attachCaptureMarker` →
  `model.AttachLogCaptureMarker`）对 `request_id` 做一次窄范围读改写，因此日志行先落库、
  标记后补上，不需要把 capture 决策挪到请求路径前面（它的状态过滤本来就要等响应）。
- 只有**规则命中、状态过滤与预算都通过、且文件写成功**才会有该字段；字段缺失表示
  "没被取样"，而不是"未记录"。
- `LOG_SQL_DSN` 指向 ClickHouse 时会跳过该标记（日志表没有同步行更新），行为与未标记一致。
- 前端在详情弹窗显示「请求取样」段（取样规则 / 取样文件），便于从日志直接定位磁盘上的
  请求副本；capture 目录只有 master 有，所以标记由 master 写、任何节点都能读。

## 4. 验证方法

```bash
# 单条请求的日志字段（管理员 token）
curl -s 'https://<console>/api/log/?p=1&page_size=1' -H "Authorization: Bearer $ADMIN" \
  | jq '.data.items[0].other | fromjson | .admin_info | {node_name, body_bytes, continuation}'

# 每节点滚动负载
curl -s 'https://<console>/api/system-info/instances' -H "Authorization: Bearer $ADMIN" \
  | jq '.data[] | {node_name, traffic: .info.extra.traffic,
                  inbound_bps: (.info.extra.traffic.request_bytes / .info.extra.traffic.window_seconds),
                  outbound_bps: (.info.extra.traffic.response_bytes / .info.extra.traffic.window_seconds)}'
```

本地端到端回归脚本（假上游 + SQLite + 一条触发续写的流式请求）：`/tmp/obs-e2e`（不入库）。
回归测试见 `service/log_info_generate_test.go`、`pkg/nodemetrics/metrics_test.go`、
`middleware/node_metrics_test.go`、`service/system_instance_test.go` 及两个前端
`__tests__` 目录。
