# 流式超时：首字预算与空闲看门狗（可热改）

面向运维。两条独立的超时都会让一次流式请求在「等太久」时失败，默认都是 300 秒，但语义与代价不同；
两者都可以在运行期通过 DB 选项覆盖，**不需要发版、不需要重启容器**。

## 1. 两个旋钮

| 名称 | 环境变量（启动读取） | 默认 | DB 热选项（0 = 沿用 env） | 作用范围 | 到点之后 |
| --- | --- | --- | --- | --- | --- |
| 流式首字预算 | `RELAY_STREAM_RESPONSE_HEADER_TIMEOUT` | 300s | `general_setting.stream_first_byte_timeout_seconds` | 只作用于**流式**尝试（`is_stream=true`）的响应头等待；非流式仍用 `RELAY_RESPONSE_HEADER_TIMEOUT`（默认 3600s） | 传输失败（`do_request_failed`）→ 按重试策略换道 |
| 流式空闲看门狗 | `STREAMING_TIMEOUT` | 300s | `general_setting.stream_idle_timeout_seconds` | 响应头已到之后的 SSE 读循环：**任何**一行数据都会重置时钟，所以它覆盖「首帧迟迟不来」与「流中途静默」 | `stream_status.end_reason=timeout` → 502（未提交正文时才可换道） |

两端点：

- 首字预算是 `http.Transport.ResponseHeaderTimeout`，按**每次尝试**计；一次请求最多在
  `RELAY_TRANSPORT_RETRY_BUDGET_SECONDS`（默认 600s）里消耗这类失败的总时长。把首字预算调到
  420s 后，600s 预算内仍容得下一次重试（第二跳最多再等 420s，最坏总计约 14 分钟）。
- 空闲看门狗在流内，`STREAM_TERMINAL_TAIL_TIMEOUT`（默认 20s，0 关闭）会在收到正常
  `finish_reason` 后提前收尾，所以「上游发完终帧却挂住连接」不会等满看门狗。尾窗若被配置到
  ≥ 空闲预算，会按预算的一半生效（否则空闲分支先命中，尾窗永远走不到）。

## 2. 什么时候需要改

上游首字延迟会随它自己的负载变化：资源紧张时同一条线路可能要几分钟才开流，恢复后又回到几秒。
此时把首字预算调大，可以让「其实还活着」的请求不被判死；上游恢复后再调回 300 秒收紧。

## 3. 怎么改（热更新）

写入一次即可，`model.SyncOptions` 每 `SYNC_FREQUENCY`（默认 60s）在各实例应用；写节点还会经
config epoch 立刻通知全舰队（通常秒级到 1 分钟内全部生效，无需重启、无需发版）：

```bash
curl -sS -X PUT "https://tokeness.cn/api/option/" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"key":"general_setting.stream_first_byte_timeout_seconds","value":420}'

curl -sS -X PUT "https://tokeness.cn/api/option/" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"key":"general_setting.stream_idle_timeout_seconds","value":420}'
```

收紧（恢复默认）把 `value` 写成 `0` —— 0 表示「沿用容器环境变量」，不要把某个历史值硬编码回去。

`GET /api/option/` 返回的扁平列表里可以直接核对当前生效值（键名就是上面两个）。

## 4. 修改后的核对

1. 读回选项值（`GET /api/option/`），确认是预期数字。
2. 管理台「渠道健康 / 日志」里看失败形态：首字预算触发的是 `status_code=502, upstream error: do
   request failed`（`error_type=transport_error` 的重试决定在 `other.admin_info.request_policy` 里）；
   空闲看门狗触发的是 `upstream stream terminated: timeout`，且 `stream_status` 里
   `end_reason=timeout`。
3. 调整之后这两类失败应在新的预算之后才出现（即 `use_time ≈ 新预算`），而不是继续卡在旧值。

## 5. 相关代码

- `relay/channel/api_request.go`（`relayStreamResponseHeaderTimeout`，选流式首字预算）
- `service/http_client.go`（`ResponseHeaderTimeout` 装配，含 `time.Duration` 溢出钳制）
- `relay/helper/stream_scanner.go`（`streamDeadlines` / `streamTimeoutSeconds`：看门狗与尾窗解析、上限钳制）
- `setting/operation_setting/general_setting.go`（两个选项字段；模块名 `general_setting`）
- `service/relay_error.go`（传输失败的预算与换道决策）
