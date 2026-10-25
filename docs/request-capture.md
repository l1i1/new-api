# 请求捕获（request_capture）操作手册

面向运维/值班：按 token 定向抓取客户请求体，用于回答"客户实际发了什么形状的请求"这类只能看报文才能回答的问题。本功能只存在于 relay 请求路径（鉴权之后、handler 之前），不参与计费，不改变响应。

## 0. 30 秒版本

```bash
# 1) 读权威配置（注意：读的是 request_capture.* 这些分散的 key）
GET /api/option/                       # 找 request_capture.enabled / request_capture.rules

# 2) 只写 request_capture.rules（整份 rules 数组，读改写）
PUT /api/option/
{"key":"request_capture.rules","value":"[{\"name\":\"cdp-k31-test-20261025\",\"enabled\":true,\"token_ids\":[174],\"window_seconds\":7200,\"limit\":400,\"include\":{\"request_headers\":true,\"request_body\":true,\"response_headers\":false,\"response_body\":false},\"redact_headers\":[\"Authorization\",\"X-Api-Key\",\"Cookie\",\"X-Auth-Token\"]}]"}

# 3) 验证：发一条该 token 的请求，再看落盘文件（文件名含规则名 + request id）
GET /api/option/request_capture
GET /api/option/request_capture/content?name=<返回的 name>
```

**最容易踩的坑**：`GET /api/option/` 里还会出现一个**不带点**的 `request_capture` 复合 key（一份完整 JSON）。它只是历史/展示副本，**运行时只读 `request_capture.<字段>` 这些 key**；写复合 key 会返回 `success:true`、读回也变了，但 relay 的解析后配置不会变，表现为"配置成功但捕获仍按旧规则"。写规则一律用 `request_capture.rules`。

## 1. 权威配置项

运行时读取的是 `config.GlobalConfig` 注册的 `request_capture` 模块，经 `ExportAllConfigs()` 展开为以下 key（`PUT /api/option/` 逐 key 写入，走 `handleConfigUpdate` 的 `name.field` 分支）：

| key | 说明 |
|---|---|
| `request_capture.enabled` | 总开关；false 时请求路径上直接跳过 |
| `request_capture.rules` | JSON 数组，见下节 |
| `request_capture.directory` | 容器内落盘目录（生产 `/data/request-captures`，主机 bind mount） |
| `request_capture.max_bytes` | 单条捕获的 body 上限（生产 10MB；代码默认 256KB） |
| `request_capture.retention_hours` | 文件保留时长（生产 48h；代码默认 24h） |
| `request_capture.queue_key` | relay→master 的 Redis list，默认 `request_capture:queue` |

`PUT /api/option/` 的 body：`{"key":"request_capture.rules","value":"<JSON字符串>"}`，需要 root 管理 token + `New-Api-User: 1`。

## 2. 规则字段

```json
{
  "name": "cdp-k31-test-20261025",
  "enabled": true,
  "token_ids": [174],
  "user_ids": [],
  "model_regex": [],
  "path_regex": [],
  "window_seconds": 7200,
  "limit": 400,
  "response_status_in": "",
  "include": {
    "request_headers": true,
    "request_body": true,
    "response_headers": false,
    "response_body": false
  },
  "redact_headers": ["Authorization", "X-Api-Key", "Cookie", "X-Auth-Token"]
}
```

- `token_ids` / `user_ids`：**数字 ID**，不是 `sk-...` key 字符串（token id 从 `/api/token/?p=0` 查）。省略或空数组 = 不限制。
- `model_regex` / `path_regex`：Go 正则，任一匹配即可；省略 = 不限制。路径例：`^/v1/chat/completions$`。
- `window_seconds` + `limit`：**限流窗口**，不是规则过期时间。窗口按 `unix/window_seconds` 分桶，每个桶最多 `limit` 条；桶过期自动重来，规则本身一直生效到你禁用/删除。默认 `window_seconds=600`、`limit=20`。
- `response_status_in`：响应后过滤，语法同平台状态码区间（`"400-599"`、`"429,500-503"`）；空 = 全放行。它在响应之后才决定是否占用 budget，适合"只抓失败"。
- `include`：四项独立；响应体默认关（体积/隐私）。
- `redact_headers`：这些请求头的值会被替换；`Authorization` 无论是否列出都强制脱敏。
- 规则是**首个匹配生效**（数组顺序），budget 计数在 Redis 共享，按规则名 + 窗口桶计。

## 3. 生效链路与验证

1. 写入后 `model.UpdateOption` 先落 DB，再更新内存并 `NotifyConfigChanged()`；其他节点通过 Redis config epoch（约 2s 轮询）重载，Redis 不可用时退回 `SYNC_FREQUENCY`（默认 60s）。
2. 验证解析后的配置（不靠猜）：在入口 ECS 上直接查 relay 实例的 `/api/option/`，看 `request_capture.rules`：
   ```bash
   curl -s -H "Authorization: Bearer $ADMIN_TOKEN" -H "New-Api-User: 1" \
     http://10.0.0.4:3000/api/option/ | jq -r '.data[] | select(.key=="request_capture.rules") | .value'
   ```
   与 `10.0.0.5` 各查一次，确认两台都拿到新规则。
3. **端到端验证**：用目标 token 发一条真实请求，等 5–10 秒，再 `GET /api/option/request_capture`，最新文件名应为 `<HHMMSS>-<rule-name>-<request-id>.json`；文件名里的规则名是判断"哪条规则命中的"最直接证据。
4. 读取单条：`GET /api/option/request_capture/content?name=<name>`（`data.content` 是 JSON 字符串）。删除：`DELETE /api/option/request_capture?name=<name>`；不带 name 删全部。
5. 排空/写入由 master 的 writer 完成（Redis list → `/data/request-captures`）；relay 与 master 不是同一台机器，看到文件有 5–10s 延迟是正常的。

## 4. 语义边界

- **不是"抓 2 小时就停"**：`window_seconds=7200` 只表示"每 2 小时最多 `limit` 条"。要停就 `enabled:false` 或把规则删掉；要缩短文件保存时间改 `request_capture.retention_hours`（全局，影响所有规则）。
- 捕获的是 relay 收到的请求（headers/body）与可选响应；超过 `max_bytes` 会截断并置 `request_body_truncated=true`，不会静默丢。
- 文件写在 master 容器的 `/data/request-captures`，生产 bind mount 到宿主 **`/var/lib/new-api-captures`**；查容量看宿主目录（`du -sh` / `df -h /`）。文件随容器迁移不会跟过去；retention 按日期目录整目录 prune。
- 存储没有 per-rule 硬上界：上限 ≈ `limit × max_bytes × (retention_hours / window_hours)`。生产把 `max_bytes` 收紧到 1MB 就是为这个式子留余量；大流量规则仍应定期看宿主磁盘。
- 客户请求体是敏感数据：只开必要规则、用完删规则并 `DELETE /api/option/request_capture` 清文件，不要外发。

## 5. 常见故障对照

| 现象 | 检查 |
|---|---|
| 配置成功但没有捕获 | 是否写成了复合 key `request_capture`；改 `request_capture.rules` |
| relay 配置已变但文件还是旧规则名 | 改动前已在队列/旧配置窗口内；等待一轮后重发请求确认 |
| 完全无文件 | `enabled` 是否 true；`token_ids` 是否是数字 id；规则是否被前一条更宽的规则先匹配；Redis 队列/master writer 是否正常 |
| 文件有但 body 为空/截断 | `include.request_body`、`max_bytes`、`request_body_truncated` |
| 删文件接口返回 removed:0 | name 必须是列表接口返回的完整相对路径 |

## 6. 生产现状（2026-10-25）

- `enabled=true`，目录容器内 `/data/request-captures`、宿主 `/var/lib/new-api-captures`，`max_bytes=1048576`（1MB），`retention_hours=48`。
- 规则：`cdp-glm-debug-20261016`（user 88/86，1000 条/24h）。临时规则 `cdp-k31-test-20261026`（token 174，window 7200s、limit 400，请求头+体）于 2026-10-26 00:37 开启、02:37 按计划移除；期间捕获 **57 份**文件（`2026-10-26/0037*-…` 至 `2026-10-26/004406-…`，Authorization 已脱敏），按 48h retention 保留供取证。
- “捕获 2 小时”是**操作约定**：窗口是 7200s，规则不会自己过期；到点要停止必须删规则（自动任务或手动，见上例），`DELETE` 文件与否则另算。
- 历史规则 `cdp-k31-test-20261025`、`cdp-k31-cache-20261024` 已被替换；历史文件按 48h retention 自然清理。
