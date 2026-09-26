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

生成时间 2026-09-25（引入 `scripts/fork-invariants` 门禁与 i18n overlay 时复核）；`fork = origin/tokeness/main @ 3bf0d16b3`、`upstream/main @ c2b7a9a9e`、rc.40 锚点 `v1.0.0-rc.40 = 0aec08fee`、`BASE = git merge-base upstream/main origin/tokeness/main = 0aec08fee`；该范围内自有非 merge 提交 **518 个**（7 月 45 / 8 月 218 / 9 月 255）。

> **清单一经登记即由门禁核对**：`node scripts/fork-invariants/main.mjs --check manifest` 按 `scripts/fork-invariants/manifest.json` 逐条断言本文件的条目仍在树里（关键文件、fork 独有符号、测试名）。本文件的表格是给人读的，`manifest.json` 是给门禁读的，两者同批更新（§10）。

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
| 官方拟合 pin 的请求不被运营重试关键词换渠道（官方原文须原样回客；多 Key 官方渠道仍换 Key） | `service/relay_error.go`（`officialFitPinKeepsVerdict`） | `service/relay_error_test.go`（`TestOfficialFitPinKeepsUpstreamVerdict`） | 中：与上游重试决策同函数，上游重构 `DecideRelayRetry` 时需重新接上；`ContextKeyV4OfficialPin` 是本 fork 的上下文键 |
| 视频计费：能力维度选路 + 从容器定价 + 预取/结算缓存协议（详见 §4） | `service/video_token.go`、`service/video_estimate.go`、`relay/request_billing.go` | `service/video_estimate_test.go`、`relay/request_billing_test.go` | 高：上游同一批文件重写过一次，已验证会整块丢加固 |
| 渠道已用额度重置 | `docs/CHANNEL_USED_QUOTA_RESET.md`、`controller/channel.go` | 该文档内的验证步骤 | 中 |
| 计费会话与资金来源（预扣费 / 退款 / 违规费语义） | `service/billing_session.go`、`service/funding_source.go`、`service/text_quota.go` | `service/billing_session_test.go`、`service/text_quota_test.go` | 高：上游改结算路径时的默认落点 |
| 每用户-模型速率限制（RPM 与长窗口并存） | ~~`middleware/model-rate-limit.go`~~ 上游已吸收（`v1.0.0-rc.40` 起该文件与其测试与上游逐字节相同） | `middleware/model_rate_limit_test.go`（上游同样有） | 无（`manifest.json` 中该条目 `status: absorbed-upstream`，不再作为 fork 契约核对） |
| 邀请首充奖励排除 partner 邀请人；充值/配额审计日志本地化 | `model/user.go`、`web/src/features/usage-logs/lib/format.ts`、`quota-audit-operation.ts` | `web/src/features/usage-logs/**/*.test.ts` | 高：`format.ts` 是已知双向冲突（保留 fork 的 `getEffectiveBillingRatio` + 上游的 quota 委托） |
| 日志可见性：普通用户视图额外剥掉 `response_model`（其 `upstream_model` 是与 `upstream_model_name` 同一个上游模型 id，上游只在顶层剥离，嵌套字段会带着它绕过） | `model/log_other.go` | `model/log_format_test.go`（`TestFormatUserLogsHidesResponseModelObservation`；管理员与 root 视图保留该字段） | **高**：`response_model` 是上游 2026-09-18/19 新增（PR #7418/#7464），与本清单的剥离逻辑相隔两周，上游再动 `log_other.go` 时容易把这条挤掉 |

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
| 渠道抽屉可写视频标记：`supports_video` 开关 + 条件出现的 `video_usage_mode` 下拉（默认值下两键从 settings 删除，关掉能力即真的移除选路限制） | `web/src/features/channels/components/drawers/channel-mutate-drawer.tsx`、`web/src/features/channels/lib/channel-form.ts`、`web/src/features/channels/types.ts` | `web/src/features/channels/lib/__tests__/video-understanding.test.ts`（8 例） |
| 上线顺序约束：渠道标记是惰性的，必须与代码同批发布 | `docs/video-usage-estimation.md` | 该文档的"上线顺序"章节 |

## 5. 路由与可靠性

| 改造 | 关键文件 | 检测 |
| --- | --- | --- |
| affinity 绑定必须先过约束过滤，视频请求不记录绑定（否则会话黏性把视频请求钉在读不了视频的渠道上） | `middleware/distributor.go` | `middleware/distributor_video_affinity_test.go`、`middleware/distributor_affinity_test.go` |
| fixed-affinity 多 Key 选路（token-affinity） | `service/channel_select.go`、`model/channel_cache.go`、`web/src/features/system-settings/general/channel-affinity/*` | `model/channel_cache_test.go`、web 对应测试 |
| 空输出的 chat completions 不再合成 502；客户端中途断开不再合成 502 | `relay/channel/openai/relay-openai.go`、`relay/helper/stream_scanner.go` | `relay/channel/openai/*stream*test.go` |
| 流式 usage 完整保留 + passthrough 原始字节 + websocket 负载归一化 | `relay/channel/openai/relay_responses.go`、`relay/helper/valid_request.go` | `relay/channel/openai/relay_openai_stream_usage_test.go`、`relay/passthrough_body_test.go` |
| official-fit：官方渠道定型（Kimi K3 / Moonshot 契约、family registry、serde 位置后缀） | `officialfit/officialfit.go`、`relay/channel/openai/deepseek_v4_fit.go`、`docs/official-fit-mode.md` | `relay/channel/openai/deepseek_v4_fit_test.go` |
| official-fit：K3 业务 400 只用 Moonshot 的两字段信封 `{message,type}`（共享 `OpenAIError` 恒带 `param`/`code`，必须显式映射） | `controller/relay.go` | `controller/relay_committed_response_test.go`（`TestRelayRendersMoonshotTwoFieldEnvelopeForKimiK3`） |
| official-fit：K3 流式在终端帧之后按**客户端自己的** `stream_options.include_usage` 补发官方的 usage-only 帧（`choices:[]` + 顶层 usage），否则标准 OpenAI 客户端读不到任何 token 数 | `relay/channel/openai/kimi_k3_fit.go`、`relay/channel/openai/relay-openai.go`、`relay/channel/openai/helper.go`、`relay/common/relay_info.go`、`relay/compatible_handler.go` | `relay/channel/openai/kimi_k3_fit_test.go`（`TestFitKimiK3StreamUsageOnlyChunk*`） |
| official-fit：K3 本地参数校验里与思考状态相关的两条规则（temperature 的固定值与 `tool_choice` 的「specified」类）必须按**思考状态**分支。`tool_choice`：「specified」只在思考开启时成立，关闭时命名函数对象合法并真的强制调用，未知字符串改回官方的「unknown tool choice strategy」原文。「关闭思考」有两个控制轴（`thinking.type=disabled` 与 `reasoning_effort:"none"`），显式 type 优先于 effort | `relay/helper/valid_request.go` | `relay/helper/kimi_k3_official_fields_test.go`、`relay/helper/deepseek_v4_logprobs_test.go`（`TestKimiK3NamedToolChoiceAcceptedWithThinkingOff`，实测 2026-09-22 官方端点逐条校准） |
| DeepSeek V4 / Ollama / GLM 等适配加固（内容类型校验、thinking、prompt cache） | `relay/channel/openai/*`、`relay/channel/ollama/*` | 各自 `*_test.go` |
| Ollama 近似缓存：分区身份必须排除**只影响解码**的 Ollama 选项（`num_predict`、采样参数、`stop`）。客户端每轮重算输出预算（ZCode 按剩余上下文发新的 `max_output_tokens` → 转成 `num_predict`），把它们算进 key 会让同一会话每一轮落进新分区、估算永不命中 | `relay/channel/ollama/prompt_cache.go` | `relay/channel/ollama/prompt_cache_generation_options_test.go`（`TestOllamaPromptCacheIdentityIgnoresGenerationOnlyOptions`、`TestOllamaPromptCacheEstimatorHitsWhenBudgetChangesEachTurn`） |
| Claude prompt cache 断点必须穿过多协议转换（**上游缺陷**：`cache_control` 在 DTO 解析层就没被提取，两个 converter 也未带上）。Claude 缓存是显式的，断点丢失 = 每轮按全价重算整段上下文，OpenAI 兼容协议进来的 Claude 流量因此 100% 无缓存 | `relaykit/dto/openai_request.go`（`ParseContent`）、`relaykit/relayconvert/internal/oai_chat/to_claude_messages_req.go`、`relaykit/relayconvert/internal/oai_responses/to_claude_messages_req.go`；防护：`relay/channel/mistral/text.go`、`relay/channel/zhipu_4v/relay-zhipu_v4.go`（这两条链路会把解析结果重新序列化，必须剥掉该字段） | `relaykit/relayconvert/claude_cache_control_test.go`（4 例：part 级 / system 级保留、不臆造断点、Responses 路径） |
| Fit Policy（Phase 3）：`pkg/fitpolicy` 是 **fork 独有包**，把族级 pin 谓词组合外提为可热加载数据（规则 JSON → expr 程序 → 原子替换快照），并加两阶段收窄。**当前 shadow：策略只观测不改路**（shadow 时 requirement 根本不写入 context，消费者拿不到）。上游 hook 逐个符号：`constant/context_key.go`（`ContextKeyFitRequirement`）、`model/option.go`（import、`validateOptionValue` 提交前编译拒绝、`loadOptionsFromDatabase` 安装快照——**不要改用独立 reload hook**，该函数同时覆盖启动/epoch/周期同步）、`model/channel_cache.go`（`preferOfficialFitChannels` 多一个 fit 参数 + additive 的 `GetRandomSatisfiedChannelPinnedWithFit`；旧签名保留，上游测试不受影响）、`model/ability.go`（`GetChannelWithBlockedChannelsPinnedWithFit` + `preferOfficialFitAbilities` 的 fit 分支）、`service/channel_select.go`（两处改调 WithFit 变体）、`middleware/distributor.go`（`applyFitPolicy` 调用 + affinity 分支的 `fitPolicyAllowsAffinity`）。**注意 `BuiltinPolicy()` 与既有谓词的等价、以及「无 mark 数据时 narrow == 官方硬 pin」这两条由测试逐例保证**，是它敢在能力表之前落地的依据。**两个顺序陷阱（都有回归测试锁住，改前先读）**：① `preferOfficialFitChannels`/`preferOfficialFitAbilities` 里 fit 收窄必须排在 `!pinOfficial` 早退**之前**——策略可能对 legacy 谓词不 pin 的形状要求官方行为，排在后面等于在该情形下静默失效；② `GetRandomSatisfiedChannelPinnedWithFit` 的**元数据快路径**（`selectChannelFromMetadata`，按全量候选集选路）必须同时被 `!fit.hasOpinion()` 挡住，只用 `officialFitPreferenceApplied` 会漏掉「仅策略有意见」的情形，把收窄结果整个绕过。这两条都是实测踩到并修复的，不是推测 | `pkg/fitpolicy/*.go`、`model/fitpolicy_option.go`、`model/fitpolicy_filter.go`、`middleware/fitpolicy_shadow.go`、`model/option.go`、`model/channel_cache.go`、`model/ability.go`、`middleware/distributor.go`、`service/channel_select.go`、`constant/context_key.go`；设计见 `docs/fitpolicy-tech-spec.md` | `pkg/fitpolicy/fitpolicy_test.go`（`TestBuiltinPolicyCoversEveryRegisteredFamily`、`TestPrimitivesMatchShippedSemantics`、`TestNarrowTwoPhase`、`TestNarrowMatchesLegacyOfficialPin`）、`middleware/fitpolicy_equivalence_test.go`（`TestFitPolicyBuiltinMatchesShippedPredicates` 等 3 例）、`middleware/fitpolicy_shadow_test.go`（`TestFitPolicyShadowDoesNotChangePin`、`TestFitPolicyAttachesRequirementOnlyOutsideShadow`、`TestFitPolicyScopeAndPinGating`、`TestFitPolicyAllowsAffinityNarrowsTheDirectBranch`）、`model/fitpolicy_filter_test.go`（`TestFitAwareSelectionNarrowsToMarkedChannels`、`TestFitAwareSelectionWithoutMarksMatchesLegacyPin`、`TestFitAwareSelectionFailsClosedWhenNoMarkedChannel`、`TestFitNarrowingAppliesWithoutTheLegacyPin`）、`model/fitpolicy_option_test.go`（`TestValidateFitPolicyOption`、`TestRefreshFitPolicySnapshotFollowsOptionMap`）、`controller/relay_fitpolicy_reuse_test.go`（`TestGetChannelReuseBranchCannotIntroduceUnconstrainedChannel`） |

| Fit Policy（Phase 3）步 B：`channel_fit_capabilities` **独立表**承载行为级渠道标记（不放进 `channels.settings`——通用 `UpdateChannel` 整行覆盖会静默重置它）。CAS 是**条件 UPDATE**（`expected_revision` 必填、`WHERE revision=?`、`RowsAffected==1`，0 即 409），不是 `SELECT ... FOR UPDATE`（`lockForUpdate` 在 SQLite 静默跳过）；首插竞态由唯一键拦下，失败方重读并报冲突。**manual sticky**：套件不能覆盖未过期人工项，除非显式 `force` 且持有 `capability.force`；人工项到期自动回退实测值。**状态机**：`unknown/suite_fresh/suite_stale/suite_failed/manual_active/manual_expired`，只有 fresh 或未过期 manual 才满足保守策略，`supported:false` 是明确否定而非未知，policy/baseline hash 变化使旧报告 stale。**能力索引 fail-open**：随 `InitChannelCache` 重建（骑既有 epoch 通知），表缺失/未构建时一律「未验证」，行为与无此表时完全一致。**endpoint `PUT /api/fit-capability` 刻意不放在 `/api/channel` 组内**（该组对全部路由强制 `AdminAuth`，而 suite applier 不得持有通用管理员身份），只带 `UserAuth` + 专用 `capability.write`；409 必须显式 `c.JSON`，因为 `common.ApiError` 恒返回 200。**审计跨库非原子**：主库先提交、审计 best-effort，审计失败只记日志、绝不回滚业务写入（有测试）。渠道删除/批量删除与能力行同事务清理（表不存在时按「不可能有孤儿行」跳过，不阻断删除）。**suite 报告写回**：`pkg/fitpolicy.ValidateSuiteReport` 做形状约束（结果数/轮次/字段长度上限、族必须已注册、行为名必须是合法标识符形状——防拼错造出永远没人读的标记、同一 channel/model/behavior 不得重复）并强制**绑定 policy_version + policy_hash**（可选 baseline）：绑定不符整份报告 409 拒绝，而不是「带警告应用」，因为规则变了之后旧报告描述的是已不存在的要求。applier 逐项 CAS（读 revision → 条件 UPDATE），**单项冲突不丢弃整批**，且**永不设置 force**——套件 applier 只持 `capability.write`，结构上无法越过人工 sticky 标记。`POST /api/fit-capability/report` 在无策略安装时 409（无规则可绑定则标记无意义）。另：`UpdateOption`/`UpdateOptionsBulk` 现在**在写节点立即安装快照**，不再只依赖 epoch 往返（Redis 不可用时写入方自己的策略变更此前要等周期同步） | `model/channel_fit_capability.go`、`model/channel_fit_capability_index.go`、`controller/channel_fit_capability.go`、`model/channel_cache.go`、`model/channel.go`、`model/main.go`、`service/authz/resources_channel.go`、`router/channel-router.go`；设计见 `docs/fitpolicy-tech-spec.md` §6.2/§9 | `model/channel_fit_capability_test.go`（`TestFitCapabilityCASLifecycle`、`TestFitCapabilityInsertRaceReportsConflict`、`TestFitCapabilityManualIsStickyAgainstSuite`、`TestFitCapabilityStateMachine`、`TestFitCapabilityIndexIsConservativeAndFailsOpen`、`TestChannelDeletesRemoveCapabilities`）、`model/channel_fit_capability_dialect_test.go`（`TestFitCapabilityDatabaseMatrix`，SQLite 实跑，MySQL/PG 需 `TEST_FITCAP_MYSQL_DSN`/`TEST_FITCAP_POSTGRES_DSN`）、`controller/channel_fit_capability_test.go`（`TestPutFitCapabilityCASLifecycle`、`TestPutFitCapabilityForceNeedsTheForcePermission`、`TestPutFitCapabilityAuditFailureDoesNotFailTheWrite` 等 6 例） |

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
| 卡片本地化（`<tnt l="zh">` 标记解析）与 7 语言键集合契约（当前 **上游 6778 键 + fork overlay 650 键 = 7428 键**） | 上游文案在 `web/src/i18n/locales/*.json`（**上游所有，不得编辑**）；fork 文案在 `web/src/i18n/overlay/*.json`；合并点 `web/src/i18n/fork-bundles.ts`、`web/src/i18n/overlay.ts` | `node scripts/fork-invariants/main.mjs --check i18n`（键集合一致、0 重复、与上游键不冲突、失效 override）+ `web/src/i18n/__tests__/fork-overlay.test.ts`；发布前必跑 |
| i18n overlay 机制本身：上游 bundle 与 fork 文案分离，sync 时 `git checkout upstream/main -- web/src/i18n/locales` 整目录取上游，fork 侧只动 `overlay/` | `web/src/i18n/overlay.ts`、`web/src/i18n/fork-bundles.ts`、`scripts/fork-invariants/upstream-locales.json`、`.github/workflows/tokeness-upstream-sync.yml` | 同上；**任何直接 import `i18n/locales/*.json` 的文件（除 `fork-bundles.ts`/`overlay.ts`）都会被门禁判失败** |
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
| 渠道视频标记的读写两端 | 渠道抽屉表单 ↔ `relaykit/dto/channel_settings.go` 的 `supports_video`/`video_usage_mode` | 表单写入后重开抽屉须回读一致；关掉能力后两键必须从 settings 消失 |
| 日志字段的可见性分级 | `model/log_other.go` 的 user/admin/root 投影 ↔ 前端读取 | 新增任何会落到 `other` 的上游字段时，判断它是否重复了顶层被剥的值（`response_model` 就是这么漏过去的） |
| K3 本地校验器与官方契约的逐条对齐 | `relay/helper/valid_request.go` 的校验文本/规则 ↔ `IsStrictFitValidationMessage` 的识别清单 ↔ `controller/relay.go` 的 Moonshot 两字段信封 | 改任一校验文本时必须三处同步：文本常量、识别前缀（否则错误会带上网关 request id、破坏 byte-identical）、信封渲染；新增/修改规则后用官方端点逐条复测 |
| locale 键集合 | 7 个 locale 文件 | 键集合完全一致、0 重复、无 BOM/格式翻动 |
| 主题偏好存储与首屏应用 | `theme-customization-provider.tsx` ↔ `initializeThemeCustomizationDom`（`web/src/main.tsx`） | 换存储底座时两侧一起换；键集合必须一致 |
| 邀请码与账本字段 | new-api ↔ `tools/partner-console`（根仓库） | 跨仓库，必须同批发布 |
| 估算缓存的模型 id 与键 | 预取端 ↔ 结算端 | 两端都必须是 `OriginModelName` |
| 计费数量与配额字段 | `relay/common/relay_info.go` ↔ 前端展示 | 改名/改形状必须同批 |

## 10. 维护规则

1. **谁改行为谁登记**：任何改变 fork 行为或对外契约的提交，同批更新本文档（新增条目，或把被替代的条目标成 `superseded`），并在同批运行 `node scripts/fork-invariants/seed-manifest.mjs` 后把新条目的锚点补进 `scripts/fork-invariants/manifest.json`——**表格给人读，manifest 给门禁读**。
2. **每次 `rcNN` 合并后（同一批必须做）**：跑 `node scripts/fork-invariants/main.mjs`（等价于「三项机械检查 + 本清单逐条核对」，见 `scripts/fork-invariants/README.md`），CI 里由 `.github/workflows/tokeness-fork-invariants.yml` 强制；把这次核对的日期写进 `§2 生成时间/ref`。手工命令仍保留在 §2 里，用于解释门禁为什么失败。
3. **每次生产发布前**：确认 §9 的契约两侧都在，并确认必须同批上线的条目（跨仓库、前后端、渠道标记）确实同批；确认门禁在发布提交上为绿。
4. **删除条目必须写原因**（上游吸收了 / 主动废弃 / 被替代），不允许静默删行——静默删行正是这份清单要防的事。上游吸收的条目在 `manifest.json` 里标 `status: absorbed-upstream` 并写明 `supersededBy`（先例：每用户-模型速率限制，`v1.0.0-rc.40` 起文件与上游逐字节相同）。
5. **locale 由上游所有**：`web/src/i18n/locales/*.json` 一律取上游字节，fork 文案只进 `web/src/i18n/overlay/`（`translation` = 上游没有的键，`overrides` = 措辞不同的键）。上游开始发同名键时，门禁会拦下并要求二选一（让位或搬进 `overrides`），不允许两个来源各写一份。
6. 只写"secret 配置在哪里"，不写 secret 值。
7. 条目要可验证：给出关键文件，能给出测试/脚本/文档就给出，避免"某处支持了 X"这种无法核对的说法。
