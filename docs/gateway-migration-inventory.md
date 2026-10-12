# 自有特性迁移清单 —— 自研网关的必须项（v0 草案，待拍板）

**生成时间**：2026-10-13 · **来源基线**：`origin/tokeness/main @ 7ba7e0ab1`（发版 `v1.0.0-rc.40-tokeness-mainland.49`）
**导出命令**：见 §6。**处置一栏是草案，需用户逐条拍板后才算定稿**（agent 不得单方面定稿迁移范围）。

---

## 0. 这份文档是什么 / 不是什么

- **是**：把 fork 在 new-api 上长出来的**自有行为与问题修复**，按「新网关要不要实现」整理成待审清单。条目内容与判据来自 [FORK-CHANGES.md](../FORK-CHANGES.md) §3–§9；机读锚点来自 [`scripts/fork-invariants/manifest.json`](../scripts/fork-invariants/manifest.json)（发版门禁逐条断言"条目还在树里"）。
- **不是**：不是新网关的设计文档（那是 `~/tokeness/docs/rebuild/*`：`01-production-inventory.md` 现网清单、`02-requirements.md` 需求、`07-migration-plan.md` 迁移计划）；也**不是第二份事实来源**——条目原文只在 FORK-CHANGES 里，本文只加"迁移处置 + 验收入口"两列。
- **为什么不复制条目内容**：本仓库的规矩是"表格给人读、manifest 给门禁读"（FORK-CHANGES §10.1）。再抄一份就会出现两个会漂移的真相；所以本文按 **manifest id** 引用条目，用 `jq` 即可展开全文（§6）。
- **许可边界（与 `~/tokeness/docs/rebuild/README.md` 的前提一致）**：新系统**不得复制 new-api 的任何代码，测试代码同样算**（AGPL 衍生）。本文的"验收入口 / 最小验收集"是**行为等价验收**——判据（什么算通过）可以照搬，测试代码必须在新网关自己的许可边界内重写。清单是行为规格，不是移植指南。

## 1. 为什么"问题修复"必须进迁移清单

清单里每一类修复都对应一次真实的生产事故或实测结论，**不是代码风格偏好**。丢掉它们等于把事故在新网关上重演一遍：

| 事故 | 数字 | 对应契约（本文条目 id） |
| --- | --- | --- |
| 换道成功后客户仍收到上一跳的 502（我方记成功并计费） | 2026-10-12 19:00–23:18 该类客户可见 502 共 136 条；修复发版后（23:18 之后）0 条，同形态请求改判 200（入口日志逐行核对） | `relay-status-commit-fidelity-502` |
| 上游欠费 503 被 pin 判词保护原样透传 | 20 分钟 110 条流式请求单跳即失败，客户可见失败率 1.881% | `official-pin-credential-exhaustion-failover` |
| 客户断开后剩余 4–5 家渠道继续陪走 | 烧上游配额，并把官方渠道 ch8 的失败行里 96% 记成假失败（walk-tail 假象） | `request-context-done-stops-the-retry-chain` |
| "重试无候选"500 掩盖真实上游裁决 | 45 分钟 80 条 type=5 错误行里一条都没入库，按 request_id 也查不到 | `retry-error-fidelity-mode-three-states` |
| 写超时静默失效 | 慢下游可永久阻塞 flush 并占住渠道并发槽（`feature not supported` 被丢弃） | `response-writer-unwrap-chain` |

**结论**：迁移时"功能"可以按产品面裁剪，但这五类**正确性与可见性契约**要么在新网关实现，要么显式写下"不实现 + 接受什么后果"。

## 2. 现状盘点（导出即得，不含判断）

| 域（FORK-CHANGES 节） | manifest 条目数 | 带测试锚点 | 说明 |
| --- | --- | --- | --- |
| §3 计费与配额 | 10 | — | 计费/配额/重试决策语义 |
| §4 视频能力 | 9 | — | fork 独有维度（能力选路 + 容器定价） |
| §5 路由与可靠性 | 21 | — | fit/pin、错误保真、停链、写入链 |
| §6 内容安全 | 2 | — | 审核可运营配置 + cyber policy |
| §7 前端与国际化 | 6 | — | 原生前端/主题/定价展示/i18n overlay |
| §8 运营、合作方与部署 | 10 | — | partner/发票/观测/发版机器 |
| §9 跨边界契约 | 6 | — | 同步时逐条 grep 的跨面契约 |
| **合计** | **64** | **23** | risk: high 12 / medium 9 |

## 3. 域级建议处置（草案）

判据三条：① 丢掉它会不会**重演事故/破坏对外契约**；② 它是不是**new-api 特有实现**（新网关换了架构就不需要）；③ 它属于**网关**还是**业务系统/前端**。

| 域 | 建议默认处置 | 必须单独保留的例外 |
| --- | --- | --- |
| §3 计费与配额 | **必须**（计费正确性不接受裁剪） | 三决策重试顺序、官方 pin 判词+逃生口、请求形状 4xx 不重试；视频计费随 §4 决定 |
| §4 视频能力 | **待拍板**：新网关若不出视频能力 → 可弃用；若要出 → 必须（能力选路 + 容器定价 + 预取/结算缓存协议三件套） | 视频能力维度选路、容器定价、cache 键含端点 URL |
| §5 路由与可靠性（保真路由族：fitpolicy / official-fit / pin 穷尽） | **待拍板**：保真路由是产品能力；不做则这批不迁移，**但其错误语义部分仍必须** | `relay-status-commit-fidelity-502`、`retry-error-fidelity-mode-three-states`（可见性/入库部分）、`provable-channel-exhaustion-under-fit-pin`（"猜出来的耗尽不许当证据"这条与是否 pin 无关）、`response-writer-unwrap-chain` |
| §6 内容安全 | **必须**（合规面） | 可运营配置、日志/状态、图片与任务提示词覆盖范围 |
| §7 前端与国际化 | **不迁移**（新网关自有前端与文案体系） | 「日志字段可见性分级」属后端投影，随 §9 迁移 |
| §8 运营、合作方与部署 | **拆分**：partner/发票/邀请码 → 迁到业务系统而非网关；观测与发版 → 只迁"语义"（见下） | `capture-redaction-defaults`（凭据脱敏，必须）、`master-shutdown-budget-is-shared`、`option-periodic-sync-skips-unchanged-values`（若新网关也有多机配置传播） |
| §9 跨边界契约 | **当回归检查表用**，不单独实现 | "改一处必须同批改另一处"的成对关系（前后端字段、识别清单、locale 键集合） |
| §8 发版机器（`deployment/tokeness-cn/*`） | **只迁语义，不照搬脚本**：一次只动一台、先 park 再排空、等全量离场才删、门禁不绿不发、失败走同一条 park 协议回滚 | 平局退役的"宁可不缩容也不删错实例"这条判断值得直接继承 |

## 4. 本轮新登记的 10 条契约（2026-10-10 → 10-12 的修复与行为）

这 10 条此前**没有任何建档**（manifest / FORK-CHANGES / MEMORY 三处命中均为 0），2026-10-13 补登。每条的"新网关要什么"是建议，等拍板。

| id（manifest） | 契约一句话 | 为什么（事故/实测） | 新网关的形态建议 | 验收入口（最小验收集） |
| --- | --- | --- | --- | --- |
| `relay-status-commit-fidelity-502` | 一次失败的尝试**不得**决定客户响应的状态码；只有终局失败才写 5xx | 换道成功后首帧把上一跳 502 发给客户（"502 + 完整正文"，我方记成功计费），19:00–23:18 共 136 条 | 网关必须区分"尝试级失败"与"请求级结局"；响应状态只在结局处提交，且要有一层"未提交即不可改状态"的守卫 | `stream_status_commit_test.go` 2 例 + `relay_stale_status_e2e_test.go` 2 例（E2E：换道成功=200+内容；全失败=5xx） |
| `retry-error-fidelity-mode-three-states` | 只有**可证明**的候选耗尽才允许用路由错误作答；上游真实裁决优先，且这类失败必须入库 | 45 分钟 80 条错误行**一条都没入库**，客户拿 500 而真正原因是上游 400 | 需要显式的保真策略（默认影子）与一条"路由失败"错误行；上游文本只进运维可见域并脱敏 | `relay_error_test.go::TestRetryErrorFidelityGuard`、`relay_routing_failure_test.go` 2 例（含"上游文本不得进用户域"） |
| `provable-channel-exhaustion-under-fit-pin` | "候选耗尽"是**证据命题**：可用性探测、DB 预筛、能力查询失败都**不构成**证据；饱和是例外（真可证明） | 三轮独立复核各给出一个反例（三渠道最小例、Polling 凭据故障、video 能力查询吞成空集） | 分类器只接受结构化证据；每个"没渠道"的结论都要能说出证据来源；饱和与耗尽分开建模 | `zz_pin_exhaustion_test.go` 2 例 + `channel_video_test.go` 1 例 + `relay_fitpolicy_reuse_test.go` 1 例 |
| `official-pin-credential-exhaustion-failover` | 渠道**凭据/额度耗尽**类失败不承载任何官方判词 ⇒ 允许换渠道；本地计费 403 与泛化 5xx 不触发 | 2026-10-10：上游欠费 503 被原样透传，20 分钟 110 条，可见失败率 1.881% | 若新网关保留"官方原样回客"契约，必须同时定义**它的例外**（否则一次欠费就打穿 SLA） | `relay_error_test.go` 2 例（逃生口 + 判词保护不回归） |
| `request-context-done-stops-the-retry-chain` | 请求上下文已取消就地停链：不烧配额、不写假失败样本、不因此禁用渠道 | 客户 600s deadline 到时后 4–5 家渠道继续陪走；官方渠道 96% 失败行是这种假失败 | 取消/超时必须是**一等错误类**（独立指标），不得与上游失败混在一张失败率表里 | `relay_error_test.go::TestDecideRelayRetryStopsWhenRequestContextDone`、`relay_openai_stream_usage_test.go::TestOaiStreamHandlerTreatsClientGoneAsNonError` |
| `glm-group-contract-and-cdp-instruction-following` | 契约漂移要按官方实测对齐：孤儿 tool 结果拒 400 且用上游报文形状；`tool_choice=none` 不投工具；GLM 业务 400 不发 `Content-Type` | fit 全开时官方渠道只过 23/32，其中 4 个 SEVERE 是**我们自己的错误面**造成的 | 上游契约要有"逐条对齐 + 官方端点复测"的机制，而不是一次性修文本 | `glm53_customer_contract_test.go` 3 例 + `glm53_official_fields_test.go` 3 例 |
| `response-writer-unwrap-chain` | ResponseWriter 的每层包装都必须能 `Unwrap`，否则写截止期静默失效 | 四层包装里三层缺 `Unwrap`，非流式 keep-alive 的截止期一直在静默失败 | 新网关若也包装 ResponseWriter，需要一条**机械检查**（测试摘掉任一层必须失败） | `relay_observation_writer_test.go`、`response_writer_unwrap_test.go` 2 例、`keepalive_unwrap_test.go`（各 1 例） |
| `capture-redaction-defaults` | 捕获/审计副本默认脱敏：凭据头、`Cookie`、`Sec-WebSocket-Protocol`（**多值头全量**）、响应 `Set-Cookie` | 多值头只取首值会让 token 从第二个值漏进审计副本 | 脱敏表要有"新增凭据头必须同批登记"的机制；副本脱敏不得影响转发字节 | `request_capture_test.go::TestCaptureRequestHeadersRedactCredentialsByDefault` |
| `master-shutdown-budget-is-shared` | 进程停止：平台 grace 是上限不是延长；后台排空共享一份总预算；预算耗尽仍要走停止接收阶段 | 四个排空各自 30s（最坏 120s）超过平台预留的 allowance；预算被 log drain 吃光会饿死后续阶段 | 优雅停止的总预算必须可被平台配置覆盖，且**预算耗尽不是"跳过停止"的理由** | 部署侧 grace 与 `SHUTDOWN_TIMEOUT_SECONDS` 的一致性检查（当前无单测，属缺口） |
| `option-periodic-sync-skips-unchanged-values` | 多机配置传播的周期路径"值未变即跳过"，管理员写入路径保持全语义 | 每 2s 全量反射重写把竞争面放大成常态 | 新网关若有多机配置，要么事件化（epoch/通知），要么明确"未变不写"的短路 | 无单测（属缺口，已记进 §5） |

## 5. 还需要拍板的判断题（这些决定 §3 的处置）

1. **保真路由（official-fit / fitpolicy / pin）在新网关是产品能力还是历史包袱？** 若不做，`provable-channel-exhaustion-under-fit-pin` 的"证据"部分仍应保留（它防的是"我们替上游编了个理由"），但 `official-pin-*` 两条可整体不迁移。
2. **视频能力**（§4 九条）是否进 V1？不进则整域可弃用，且现网已知"kimi-k3 池不接受 video/audio 输入"会在新网关以另一种形态复现（客户端仍在发这类请求）。
3. **计费与配额语义**（§3）迁到网关内还是迁到新的计费服务？这决定"三决策重试顺序"这类契约落在哪一侧。
4. **partner / 发票 / 邀请码 / 运营插件**（§8 前半）是否随网关迁移，还是留在业务系统（现状是与根仓库 `tools/partner-console` 必须同批上线）。
5. **观测/审计**（`capture-redaction-defaults`、渠道可观测性）在新网关是保留还是由统一可观测栈接手。

## 6. 再生成与核对

```bash
# 1) 展开任一条目全文（示例：502 契约）
jq '.entries[] | select(.id=="relay-status-commit-fidelity-502")' scripts/fork-invariants/manifest.json

# 2) 按域导出条目与测试锚点（本文表格的机器来源）
jq -r '.entries[] | [.source, .id, ([.tests[]?.names[]?] | length)] | @tsv' scripts/fork-invariants/manifest.json | sort

# 3) 门禁（FORK-CHANGES 条目是否还在树里 + i18n + 僵尸 + merge 分歧 + 提交存活）
node scripts/fork-invariants/main.mjs

# 4) 契约原文（人读）
sed -n '/^## 5\. 路由与可靠性/,/^## 6\./p' FORK-CHANGES.md
```

**关联文档**：`FORK-CHANGES.md` §11（迁移视图的定位与规则）· `scripts/fork-invariants/README.md`（门禁每条失败怎么修）· `~/tokeness/docs/rebuild/01-production-inventory.md`（现网功能面）· `~/tokeness/docs/rebuild/02-requirements.md` · `07-migration-plan.md` · 事故记录 `~/tokeness/docs/relay-retry-fidelity-review-20261012.md`、`~/tokeness/docs/stale-status-audit-20261012.md`。

**纪律**：新网关每实现（或决定不实现）一条，都要回到 FORK-CHANGES §10.1 的规矩——**改行为就同批更新表格 + manifest**；本文的处置列在下一次导出时同步，不另立真相。
