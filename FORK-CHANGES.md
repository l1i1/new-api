# FORK-CHANGES.md — Tokeness 自有改造清单

> English abstract: an inventory of the changes this fork makes on top of upstream
> new-api, with the file, test and cross-boundary evidence for each entry.
> Regenerate the raw list with the commands in §2 after every `rcNN` sync and
> verify each entry against the current tree before releasing. A green test suite
> does not prove a fork change survived a merge: rc35 lost five changes and rc39
> lost the whole video-token hardening block while every gate stayed green.

## 1. 用途与分工

本文档回答"**我们现在对上游做了哪些功能改造**"，是每次 `rcNN` 上游同步与生产发布前的核对清单。

- `MEMORY.md` = 时间线：什么时候发生了什么、踩了什么坑、为什么这样决定。
- `AGENTS.md` 的 *Upstream Sync (rcNN merges)* = 合并纪律（三项机械检查、并集解决、locale 契约、信任当前树而不是历史）。
- `Tech-Spec.md` / `docs/*-prd.md` / `docs/*-tech-spec.md` = 单个改造的设计与验收。
- **本文档 = 被核对的现状清单**。同步时用它逐条确认改造还在；发布前用它确认哪些条目必须同批上线。

不在清单内：上游自带功能、纯文档、纯发布流程（§9 只给一句话）。条目粒度是"用户或运维可感知的行为 / 对外契约"，不是提交。

## 2. 清单的机械来源（每次同步后重跑）

生成时间 2026-09-22（rc.40 并主线前复核）；`fork = origin/tokeness/main @ eac71cfd8`、`upstream/main @ 9310231b3`、rc.40 锚点 `v1.0.0-rc.40 = 0aec08fee`、`BASE = git merge-base upstream/main origin/tokeness/main = 69a500298`；该范围内自有非 merge 提交 **481 个**。

```bash
BASE=$(git merge-base upstream/main origin/tokeness/main)

# 1) 只属于 fork 的提交（清单的来源；注意 --not upstream/main）
git log --no-merges --format='%h %ad %s' --date=short "$BASE..origin/tokeness/main" --not upstream/main

# 2) 哪些区域在活跃改动（scope 直方图）
git log --no-merges --format='%s' "$BASE..origin/tokeness/main" --not upstream/main \
  | sed -n 's/^[a-z]*(\([^)]*\)).*/\1/p' | sort | uniq -c | sort -rn

# 3) 某个区域的关键文件（把 <scope> 换成 2) 里的名字，再人工筛掉测试/文档噪声）
git log --no-merges --name-only --format= "$BASE..origin/tokeness/main" --not upstream/main \
  | sort | uniq -c | sort -rn | head -40

# 4) 同步时的四棵树比对（base / ours=候选 / theirs=上游或主线 / result=合并结果）
git ls-tree -r -z <ref>   # 逐路径比 blob hash；不要用"看 diff"代替
```

**不要**用 `git diff upstream/main..origin/tokeness/main` 当清单来源：它把"上游新提交"也算成我们的改动。清单来源只能是"只属于 fork 的提交"。

## 3. 计费与配额

| 改造 | 关键文件 | 检测 | 上游覆盖风险 |
| --- | --- | --- | --- |
| 三决策重试：永不重试（最先判定）→ 换同渠道另一个 Key → 换渠道，顺序在 UI 里写明；关键词列表可由运营编辑 | `model/request_policy.go`、`controller/request_policy.go`、`service/upstream_request_rejection.go`、`web/src/features/system-settings/request-policies/*` | `controller/relay_retry_test.go`、`model/request_policy_test.go` | 高：上游持续改 retry/request-policy；新增选项必须同时进 `requestPolicyDefaultOptions` 与 `IsRequestPolicyOption`，否则设置页保存被拒 |
| 请求形状错误的 4xx 不重试（换渠道重试救不了无效请求） | `relay/helper/valid_request.go`、`service/upstream_request_rejection.go` | `controller/relay_retry_test.go` | 中：与上游"自动关键词/状态码区间"判定共用入口 |
| 视频计费：能力维度选路 + 从容器定价 + 预取/结算缓存协议（详见 §4） | `service/video_token.go`、`service/video_estimate.go`、`relay/request_billing.go` | `service/video_estimate_test.go`、`relay/request_billing_test.go` | 高：上游同一批文件重写过一次，已验证会整块丢加固 |
| 渠道已用额度重置 | `docs/CHANNEL_USED_QUOTA_RESET.md`、`controller/channel.go` | 该文档内的验证步骤 | 中 |
| 计费会话与资金来源（预扣费 / 退款 / 违规费语义） | `service/billing_session.go`、`service/funding_source.go`、`service/text_quota.go` | `service/billing_session_test.go`、`service/text_quota_test.go` | 高：上游改结算路径时的默认落点 |
| 每用户-模型速率限制（RPM 与长窗口并存） | `Tech-Spec.md`、`docs/global-model-rate-limit-tech-spec.md`、`middleware/model-rate-limit.go` | `middleware/model_rate_limit_test.go` | 中 |
| 邀请首充奖励排除 partner 邀请人；充值/配额审计日志本地化 | `model/user.go`、`web/src/features/usage-logs/lib/format.ts`、`quota-audit-operation.ts` | `web/src/features/usage-logs/**/*.test.ts` | 高：`format.ts` 是已知双向冲突（保留 fork 的 `getEffectiveBillingRatio` + 上游的 quota 委托） |

## 4. 视频能力（最新的自有维度）

| 改造 | 关键文件 | 检测 |
| --- | --- | --- |
| `supports_video` 能力维度：带视频的请求只路由到声明了能力的渠道；无声明就诚实失败，文本流量不受影响 | `middleware/distributor.go`、`model/channel_constraint.go`、`model/channel_cache.go`、`service/channel_select.go`、`constant/context_key.go` | `model/channel_video_selection_test.go`、`middleware/video_request_flag_test.go` |
| body 层检测器（`RequestBytesCarryVideo`，按 part 形状匹配）与 DTO 层检测器必须一致 | `service/video_token.go`、`relaykit/dto/openai_request.go` | `service/video_routing_test.go`（含"正文提到 video_url 不得误判"） |
| 本地容器定价（Kimi vision 系标定模型）含非有限值/溢出/饱和加固 | `service/video_token.go` | `service/video_token_test.go` |
| 估算端点可由控制台配置：管理员设置优先、env 作为引导与回退 | `setting/operation_setting/video_estimate_setting.go`、`service/video_estimate.go`、`web/src/features/system-settings/integrations/video-estimate-settings-section.tsx` | `service/video_estimate_test.go`（含 `TestLoadVideoEstimateConfigPrefersTheAdminSetting`）、`controller/video_estimate_option_test.go` |
| 估算端点预取 + 结算读缓存：两端必须用同一个模型 id（`OriginModelName`），缓存键覆盖整个计价载荷 | `service/video_estimate.go`、`relay/request_billing.go` | `service/video_estimate_test.go`、`relay/request_billing_test.go` |
| 估算端点在控制台可配（库 > env > 默认，逐字段回退）；密钥走既有脱敏通道不回显；缓存键含端点 URL，换端点即失效 | `setting/operation_setting/video_estimate_setting.go`、`service/video_estimate.go`、`controller/misc.go`、`web/src/features/system-settings/integrations/video-estimate-settings-section.tsx` | `controller/video_estimate_option_test.go`、`service/video_estimate_test.go` |
| `video_usage_mode` 只在**服务该请求的**渠道声明时才修正上报值 | `service/video_token.go`、`relaykit/dto/channel_settings.go` | `service/video_routing_test.go` |
| 消费日志的 `prompt_tokens` 与 `usage_billing_path` 必须同时反映**实际计费值**（修正后为 `billing-usage-openai-estimated`）；客户端响应的 usage 保持上游原值 | `service/text_quota.go` | `service/video_log_path_test.go`（真实结算+落库） |
| 上线顺序约束：渠道标记是惰性的，必须与代码同批发布 | `docs/video-usage-estimation.md` | 该文档的"上线顺序"章节 |

## 5. 路由与可靠性

| 改造 | 关键文件 | 检测 |
| --- | --- | --- |
| affinity 绑定必须先过约束过滤，视频请求不记录绑定（否则会话黏性把视频请求钉在读不了视频的渠道上） | `middleware/distributor.go` | `middleware/distributor_video_affinity_test.go`、`middleware/distributor_affinity_test.go` |
| fixed-affinity 多 Key 选路（token-affinity） | `service/channel_select.go`、`model/channel_cache.go`、`web/src/features/system-settings/general/channel-affinity/*` | `model/channel_cache_test.go`、web 对应测试 |
| 空输出的 chat completions 不再合成 502；客户端中途断开不再合成 502 | `relay/channel/openai/relay-openai.go`、`relay/helper/stream_scanner.go` | `relay/channel/openai/*stream*test.go` |
| 流式 usage 完整保留 + passthrough 原始字节 + websocket 负载归一化 | `relay/channel/openai/relay_responses.go`、`relay/helper/valid_request.go` | `relay/channel/openai/relay_openai_stream_usage_test.go`、`relay/passthrough_body_test.go` |
| official-fit：官方渠道定型（Kimi K3 / Moonshot 契约、family registry、serde 位置后缀） | `officialfit/officialfit.go`、`relay/channel/openai/deepseek_v4_fit.go`、`docs/official-fit-mode.md` | `relay/channel/openai/deepseek_v4_fit_test.go` |
| DeepSeek V4 / Ollama / GLM 等适配加固（内容类型校验、thinking、prompt cache） | `relay/channel/openai/*`、`relay/channel/ollama/*` | 各自 `*_test.go` |

## 6. 内容安全

| 改造 | 关键文件 | 检测 |
| --- | --- | --- |
| 内容审核（可运营配置、日志与状态） | `service/content_moderation.go`、`model/content_moderation.go`、`docs/content-moderation-tech-spec.md` | `service/content_moderation_test.go`、`controller/content_moderation_relay_test.go` |
| cyber policy / 违规费用归一化 | `controller/relay.go`、`service/relay_error.go`、`docs/cyber-policy-tech-spec.md` | 对应 `*_test.go` |
| error-message 过滤 | `docs/error-message-filter-tech-spec.md` | 对应 `*_test.go` |

## 7. 前端与国际化

| 改造 | 关键文件 | 检测 |
| --- | --- | --- |
| 原生 Tokeness 前端（导航定制、首页、站点公告） | `web/src/features/*`、`web/src/hooks/use-notifications.ts`、`web/src/main.tsx` | web 测试 + 实机截图 |
| 定价展示：动态价、原价划线、tier 表达式、vendor 本地化、CNY 文案 | `web/src/features/pricing/lib/dynamic-price.ts`、`tier-expr.ts`、`web/src/features/pricing/components/model-details.tsx`、`web/src/features/pricing/components/dynamic-pricing-breakdown.tsx` | `web/src/features/pricing/**/__tests__/*` |
| 卡片本地化（`<tnt l="zh">` 标记解析）与 7 语言键集合契约（当前 7401 键） | `web/src/i18n/locales/*.json`、`web/src/lib/tnt-content.ts` | `.review` 里的 locale 校验脚本（键集合一致 + 0 重复）；发布前必跑 |
| 主题定制持久化与首屏应用 | `web/src/lib/theme-storage.ts`、`web/src/lib/theme-customization-storage.ts`、`web/src/context/theme-customization-provider.tsx` | `web/src/context/__tests__/theme-preferences.test.tsx`、`web/src/context/__tests__/theme-customization-provider.test.tsx`、`web/src/lib/__tests__/theme-customization.test.ts` |
| 主题定制：**存储底座取上游**（rc.40 起 localStorage `newapi:theme:v1:*`，cookie 通道废弃），**默认值与首屏应用取 fork**（`preset: sunset-glow`、`radius: none`；`main.tsx` 挂载前应用） | 同上 | 同上；三处（provider / 首屏初始化 / 测试）必须读同一套键 |
| 测试运行器必须两阶段都跑（vitest + node:test） | `web/scripts/run-tests.mjs`、`web/scripts/node-test-setup.ts` | 运行器自身 + 两段汇总 |

## 8. 运营、合作方与部署

| 改造 | 关键文件 | 备注 |
| --- | --- | --- |
| partner 白标、统计、账本（FIFO 回放）、签名旁路、按用户名解析 | `controller/partner.go`、`model/partner_stats.go`、`setting/operation_setting/partner_setting.go` | 与根仓库的 `tools/partner-console`（不在本仓库内）**必须同批上线** |
| console 签发的邀请码可归因注册（长度即命名空间：账号码恰好 4 字符，console 码 5–32） | `model/partner_invite_code.go`、`setting/operation_setting/partner_setting.go`、`model/user.go` | 合成邀请人 id 落在 `[1_000_000_000, 2_000_000_000)` |
| 渠道可观测性（多 Key 测试、指标、凭据管理） | `pkg/channel_observability/observability.go`、`controller/channel_credentials.go`、`model/channel_observation_metric.go` | |
| invoice / wallet / 支付结算加固 | `docs/invoice-tech-spec.md`、`docs/payment-settlement-hardening.md`、`web/src/features/system-settings/integrations/payment-settings-section.tsx` | |
| 双站发布：`mainland`（CNY + 简体固定）与 `intl`；staged 发布、金丝雀、回滚 | `deployment/tokeness/rollout.sh`、`deployment/tokeness-cn/deploy.sh`、`.cnb.yml`、`.github/workflows/tokeness-publish.yml`、`tokeness-deploy.yml`、`tokeness-upstream-sync.yml` | 版本命名 `v1.0.0-rc.NN-tokeness-<edition>.M` |
| 运营插件（task plugins） | `deployment/tokeness/task-plugins/*`（如 `openai-video-agg`） | 插件契约见 `docs/plugin-api/v1.md` |

## 9. 最容易被静默覆盖的跨边界契约（同步时逐条 grep，两侧都要在）

| 契约 | 两侧 | 检查方式 |
| --- | --- | --- |
| request policy 选项 | 前端表单 ↔ 后端 defaults + `IsRequestPolicyOption` | 新增选项后跑设置页保存往返 |
| `ChannelOtherSettings` 字段（`supports_video`、`video_usage_mode`） | `relaykit/dto/channel_settings.go` ↔ 选路/结算 | `ValidateVideoUsageMode` 要求两者同渠道 |
| 渠道健康条数据 | 后端序列字段 ↔ 前端读取字段 | 前端 `recent_success_series` 与后端必须同名（rc35 这里错配过） |
| 流式 usage / `stream_status` | 后端写入 ↔ 日志与前端读取 | 上游 SSE 提前 EOF 时的语义 |
| locale 键集合 | 7 个 locale 文件 | 键集合完全一致、0 重复、无 BOM/格式翻动 |
| 主题偏好存储与首屏应用 | `theme-customization-provider.tsx` ↔ `initializeThemeCustomizationDom`（`web/src/main.tsx`） | 换存储底座时两侧一起换；键集合必须一致 |
| 邀请码与账本字段 | new-api ↔ `tools/partner-console`（根仓库） | 跨仓库，必须同批发布 |
| 估算缓存的模型 id 与键 | 预取端 ↔ 结算端 | 两端都必须是 `OriginModelName` |
| 计费数量与配额字段 | `relay/common/relay_info.go` ↔ 前端展示 | 改名/改形状必须同批 |

## 10. 维护规则

1. **谁改行为谁登记**：任何改变 fork 行为或对外契约的提交，同批更新本文档（新增条目，或把被替代的条目标成 `superseded`）。
2. **每次 `rcNN` 合并后**：跑 `AGENTS.md` 的三项机械检查 + §2 的脚本，逐条确认清单里的改造仍在当前树里；把这次核对的日期写进 `§2 生成时间/ref`。
3. **每次生产发布前**：确认 §9 的契约两侧都在，并确认必须同批上线的条目（跨仓库、前后端、渠道标记）确实同批。
4. **删除条目必须写原因**（上游吸收了 / 主动废弃 / 被替代），不允许静默删行——静默删行正是这份清单要防的事。
5. 只写"secret 配置在哪里"，不写 secret 值。
6. 条目要可验证：给出关键文件，能给出测试/脚本/文档就给出，避免"某处支持了 X"这种无法核对的说法。
