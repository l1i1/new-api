# 2026-10-28 单台回源实例到 neurvibe 出口被 RST（客户 cdp 约 12% 请求 500）

**一句话**：7 台回源 ECI 里 1 台（`eci-uf65nl94wt25j2yugfn6` / 10.0.0.34 / EIP `8.133.190.111`）到
`neurvibe.com` 的连接被对端 RST，只有它坏；重试又落在同一台，所以客户看到最终
`status_code=500, upstream error: do request failed`。16:17 移除该实例、ESS 自动补一台后归零。

## 影响

| 窗口 | 现象 | 量 |
|---|---|---|
| 12:41–12:58 | 同签名错误 | 645 行 / 263 个客户请求 |
| 15:02–16:06（`.81` 换代后） | 同签名错误，全部客户 `cdp`（`cdp-kimi`、`cdp-kimi-k31`） | 2336 行 / 992 个请求 ≈ 该客户请求量的 **12%** |
| 16:18 起（处置后） | 该错误 **0 行** | — |

日志页看到的是「每次上游尝试」的行；按 `request_id` 去重后失败请求约为行数的 1/2.35。
渠道分布只有 neurvibe 四把：37（975）/48（586）/102（386）/91（366），另有 19/10/103 共 23 行属其它上游。

## 根因

单实例级的**出口被上游拒**，不是平台故障，也不是 `.81` 的代码问题（同签名在 `.81` 之前的 12:41 就出现过）。

证据（15:49–16:00 各实例容器日志尾 4000 行）：

- `fn6` 该窗口 **139 条** `do request failed`，**全部**是
  `read tcp 10.0.0.34:<port>->222.128.24.123:8666: read: connection reset by peer`
  （另有 `www.neurvibe.com:8000` → `222.128.24.115`）；同期另外 **6 台 0 条**同类错误；
- `fn6` 对**其它上游正常**：同期完成 111 条流、ch8（Moonshot）拿到 429、ch6/ch94 有正常响应；
  同伴同窗口完成 227–240 条流；
- 从入口 ECS 打 `neurvibe.com:8111/8666`、`www.neurvibe.com:8000` 均 404（服务活着、可达）
  ⇒ 不是服务宕机，是**按源 IP** 拒绝了 `8.133.190.111`；
- 每次请求的全部 attempt 都落在同一实例（`request_policy` 里 attempt1 ch48、attempt2 ch37 都是
  `do_request_failed` / `error_source=transport` / elapsed 50–95ms），故重试无法兜住。

`8.133.190.111` 是 `.81` 换代（15:02，全 7 台新 EIP）时新建的 EIP；疑为上游 WAF 对
新/陌生源 IP 的限流或黑名单（未获上游确认，本条为推断）。

## 处置（2026-10-28 16:17–16:19，用户授权）

```bash
# 只读定位：ESS 实例表 + ECI DescribeContainerLog（逐实例 grep "do request failed"）
aliyun ess DescribeScalingInstances --ScalingGroupId asg-uf641n1j5akwa1ozcz6t --region cn-shanghai --PageSize 100
aliyun eci DescribeContainerLog --region cn-shanghai --ContainerGroupId <id> --ContainerName newapi --Tail 4000

# 移除坏实例，并保持期望容量（false = 删掉后仍要 7 台，ESS 自动补一台新 EIP）
aliyun ess RemoveInstances --RegionId cn-shanghai \
  --ScalingGroupId asg-uf641n1j5akwa1ozcz6t \
  --InstanceId.1 eci-uf65nl94wt25j2yugfn6 --DecreaseDesiredCapacity false
```

- ESS：`Remove "1" ECI instance` Successful（`asa-uf6cvus3lywagda64684`），随后 `Add "1"` 补齐 desired=7；
- 新实例 `eci-uf6102vlm87uh5tfdi5l` / 10.0.0.56 / EIP `101.133.131.64`，同 `.81` digest；
- 入口 ECS `/etc/nginx/fleet/newapi_ml_servers.conf` 已由 `ecs-fleet-sync` 收敛为
  `10.0.0.33/35/36/37/53/54/56`（10.0.0.34 消失）；
- 新实例日志：正常 `stream ended: reason=done`，ch37/ch102 返回 400（对端有响应），**0 条 reset**；
- 代价：被删实例上的在途流受 240s 优雅终止上限保护，超过该时长的长流会被切断（估计个位数）。

## 复发风险与建议

- **为什么复发**：今天 09:16/10:13/11:36/12:15/15:02 各有一次 7↔14 换代，每次换代都换一批新 EIP；
  12:41–12:58 那次在 13:04 有一条「移除实例 `eci-uf6e43elt88playyosvi`」记录，之后归零——先例相同。
- **现有 postcheck 看不见它**：nginx 视后端返回 500 为「活着」（连接成功），
  所以「某台实例对某家上游 100% 失败、其它上游正常」这种半坏状态不会被部署门禁或哨兵发现。
- **建议**：发版/扩容后跑一次「逐实例 × 逐上游 base_url」连通性预检（在每台 ECI 内对该渠道域名发一次
  真实请求，比较 reset/timeout），失败的实例自动 park + 移除并告警；另向上游确认
  `8.133.190.111` 为何被 RST、是否可按 EIP 白名单放行，避免每次换代都撞一次。

## 本次未改动的

- 未回滚 `.81`（根因与版本无关）；未改任何渠道配置、分组、计费；只读凭据用于日志/监控 API 与
  ECI `Describe*`，唯一的写操作是上面那一条 ESS `RemoveInstances`。

## 后续加固：入口 nginx 请求级节点故障切换（2026-10-28 16:51 CST，用户授权）

**动机**：坏实例返回的 500 是"应用层 500"，nginx 默认只对 `error/timeout` 重放，所以一条请求落到坏实例就只能失败；
网关内部重试又只在**渠道**维度换（实测 992 个失败请求里 963 个只试过 neurvibe 四把渠道，3 次尝试预算轮不到 ch8/ch103），
而故障在**实例出口**，换渠道无效。缺的是**节点维度**的重放。

**改动**（入口 ECS `8.133.244.241`，`/etc/nginx/sites-available/tokeness-ml.conf`，5 个 server 块各加一段，
备份 `tokeness-ml.conf.bak-1793177471`）：

```nginx
location ~ ^/v1/(chat/completions|messages|responses)$ {
    access_log /var/log/nginx/canary.log canary;
    if ($canary_flag = 1) { proxy_pass http://gateway_canary; }
    proxy_pass http://newapi_ml;
    proxy_request_buffering on;                                  # 请求体可重放（仅聊天类接口）
    proxy_next_upstream error timeout http_500 non_idempotent;    # POST 需显式 non_idempotent
    proxy_next_upstream_tries 2;                                  # 1 次重试
    proxy_next_upstream_timeout 0;
}
```

图片/文件等其它 location 保持 `proxy_request_buffering off`（流式转发不变）；响应侧 `proxy_buffering off` 未动，
SSE 流不受影响。`nginx -t` 通过后 `nginx -s reload`（不重启、不断连）。

**机制验证**（本机 docker nginx 隔离实验，非生产）：上游 A `return 500`、上游 B `return 200 OK2`，
上游 A `weight=1000` 保证第一跳命中：

| 场景 | 结果 |
|---|---|
| `http_500` + buffering **on**，小请求体 | **200 OK2**（重放到 B 成功） |
| `http_500` + buffering **on**，2MB 请求体 | **200 OK2** |
| 不列 `http_500`（原配置） | 500（不重放） |
| `http_500` + buffering **off**，小请求体 | 200（小体能重放） |
| `http_500` + buffering **off**，2MB 请求体 | **500**（请求体不可重放） |

所以 `proxy_request_buffering on` 是**大请求体**（长上下文 / 视频）能重放的前提，只对小请求体可省。
另：fleet include 每行是 `max_fails=2 fail_timeout=5s`，按 nginx 语义 `proxy_next_upstream` 列出的条件即"失败尝试"
→ 连续 2 个 500 后该节点被摘 5 秒，等于自动摘坏节点。

**生产验证**：`nginx -T` 中该指令出现 5 处；无鉴权 `POST /v1/chat/completions` 返回 401（0.14s）、
2MB 请求体返回 401（0.43s，缓冲路径正常）；reload 后 error.log 无新类型错误
（"upstream prematurely closed" 全天都有，来自客户取消，基线约 1 次/3–15 分钟，未因本次变更升高）。

**回滚**：
```bash
cp -a /etc/nginx/sites-available/tokeness-ml.conf.bak-1793177471 \
      /etc/nginx/sites-available/tokeness-ml.conf && nginx -t && nginx -s reload
```

**已知影响**：真正的 500 会被重放一次（上游多做一遍活，可能双倍上游消耗；失败请求不计客户费用）；
已开始输出字节的流无法重试（本来也救不了）；`proxy_request_buffering on` 使这三个接口先收完请求体再回源
（JSON 请求无感知）。
