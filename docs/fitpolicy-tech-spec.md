# Fit Policy（Phase 3）：行为级渠道标记 × 热可编辑拟合规则

> English abstract: Phase 3 of the official-fit line. Today's channel whitelist
> (`channel.settings.official_fit_models`) is a binary, per-model, human-trust
> mark with no provenance, no expiry and no automatic calibration, and the
> per-family pin predicates live in Go. This spec upgrades the mark to a
> per-behaviour, suite-measured, human-editable capability record, and moves the
> `/v1/chat/completions` route policy into hot-reloadable data after an equivalence
> shadow period. The existing route gate, hard official-candidate fallback and
> P0 behaviour remain the rollback path. Healthy Redis gives a measured fleet
> propagation SLO; Redis failure keeps the last-known-good/legacy behaviour.
> Responses WebSocket, explicit fixed pins, task-plugin selection and contract
> body transforms are separate scopes. All fork integration surfaces, including
> selector consumers and the explicit startup registration, are inventoried and
> guarded by `FORK-CHANGES.md` and `scripts/fork-invariants`.

## 0. 审查结论与最终决策（2026-09-26）

本节是本次最终审查后的冻结结论；后文与本节冲突时，以本节为准。核实依据为当前 `tokeness/main` 的代码，而不是历史提交：`middleware/distributor.go`、`service/channel_select.go`、`controller/relay.go`、`relay/responses_websocket.go`、`model/channel_cache.go`、`model/channel_constraint.go`、`model/config_epoch.go`、`model/option.go`、`dto/channel_constraints.go`、`relaykit/dto/channel_settings.go`。

### 0.1 已核实事实

1. 当前 `/v1/chat/completions` 的第一次 HTTP 选路在 `middleware.Distribute`，普通重试在 `controller/relay.go:getChannel`；实际选择下沉到 `service.CacheGetRandomSatisfiedChannel` → `model.GetRandomSatisfiedChannelPinned`。affinity/preferred 快径在 `Distribute` 和 `getChannel` 各有一份，不能只改一个位置。
2. Responses WebSocket 通过 `relay/responses_websocket.go:selectResponsesWSChannel` 复用 `service.SelectChannelForRequest`，但它是独立协议路径；当前 Phase 3 v1 **不纳入**，避免把 `/v1/chat/completions` 设计错误宣称为全局覆盖。
3. `dto.ChannelConstraints` 当前只有 pin/filter，没有行为能力集合；fitpolicy 的 `RequiredMarks` 不能靠未定义的 `FilterKind` 偷塞，必须新增独立的请求级能力约束，并在首选、随机、重试和直接 pin 入口统一消费。
4. `official_fit_models`、官方渠道类型和 `preferOfficialFitChannels/Abilities` 已经存在；当前 pin 是硬收窄：官方候选为空时返回空，不允许静默退回未验证聚合渠道。最终方案保留这个正确性不变量。
5. `config_epoch` 当前由 `main.go` 显式注册 authz reload hook；`RegisterConfigReloadHook` 本身是 append 注册，没有去重。fitpolicy 不能仅依赖“包 `init()` 自注册”这一未核实假设，最终方案采用显式启动注册并提供只注册一次测试。
6. `model.UpdateOption(s)` 对现有 request policy 已有“构造完整快照、DB transaction、更新内存、epoch 通知”路径；fitpolicy 规则必须接入同一提交前校验和快照路径，不能只在 reload hook 里异步编译。
7. `channel.settings` 当前是 `relaykit/dto.ChannelOtherSettings` 的 JSON，通用 `UpdateChannel` 会整行更新并调用 `InitChannelCacheAndNotify`，但没有 capability 专用 CAS/provenance 语义。因此 suite 写回必须使用专用 endpoint/服务层，不得复用通用 channel patch。
8. 当前仓库没有 `pkg/billingexpr` 目录，billing expression 代码在根模块的 `pkg/billingexpr` 包中；body/usage 规则若触及计费，必须遵守 `.agents/rules/billing.md`，使用现有 quota/usage 规则，不把 fitpolicy expr 与 billing expr 混为一个运行时环境。

### 0.2 最终冻结范围

- **v1 只覆盖**已开启用户 `official_fit.profile[family].route=true` 的 `/v1/chat/completions` HTTP 文本中继：首次选路、HTTP affinity/preferred 快径、普通重试和 channel capability/ability 选择。
- **v1 不覆盖** Responses WebSocket、异步任务轮询/任务插件专用选路、token/channel 的显式固定 pin、非 chat relay 格式和响应级运行时拟合门。
- 任何未覆盖入口都必须保持现有行为；文档与日志不得声称 fitpolicy 已对“全部渠道/全部协议”生效。
- 契约面 F1–F5 是后续独立阶段，不与 v1 route 策略数据化同批上线；其中 F1/F2 的语料前置未满足时不得开工。

### 0.3 最终架构决定

- route 仍是唯一用户侧启用条件；fitpolicy 只能在 `route=true` 且既有族谓词判定本请求需要保真行为时增加候选约束。
- fitpolicy 不替换现有 `official_fit_models` 的官方行为判定，而是在其之上增加行为级 `RequiredMarks`；当没有可满足标记的候选时，v1 默认回到**现有硬 pin 集合**，现有硬 pin 集合为空则沿用现有选择失败语义，绝不把普通未标记渠道当官方行为渠道。
- 决策通过现有 `dto.ChannelConstraints` 旁路字段/请求上下文传递，不增加第二套 selector；所有 selector 必须先消费同一份约束，再执行原有 priority/weight/auto-group/blocked/saturation 逻辑。
- 热策略只引用现有 `officialfit` mechanism registry；family id、官方 channel type、wire shape、exact model names 和跨模块机制仍是代码事实。新增族必须先做代码 registry/消费者适配，不能仅写 JSON。

## 0. 与既有设计的关系（必读，先读这三份）

> 本节路径均相对**工作区根**（仓库 `tkns-workspace`，本机签出为 `~/tokeness/`）：内部文档在根仓库 `docs/`，对外文档站在独立仓库 `apps/docs/`，网关代码在独立仓库 `apps/new-api/`（即本文件所在仓库）。

| 关系 | 文档 / 实现 | 本文件对它的态度 |
|---|---|---|
| **已实现** | `apps/new-api/docs/official-fit-mode.md`（user profile 四维、pin 形状谓词、`official_fit_models` 白名单、亲和不对称、`officialfit` 家族注册表） | **不推翻**：pin/route 模型、四维语义、亲和规则全部保持 |
| **已规划** | `docs/cdp/official-fit-cost-optimization-plan.md`（Phase 2：P0 运行时门 + P1 验证白名单 + 选择顺序 + 应急开关） | **承接**：P1 未开始，本文件是它的细粒度版本 |
| **验收基准** | `docs/cdp/Tech-Spec.md` + `tools/cdp-bench/src/suites/{ds-v4,kimi-k3,glm-v53}.ts`（CDP 双轨：A 拟合三面逐字一致、B 性能） | **作为唯一的标记来源与放行门禁** |

**两处必须写明的更正**（本文件初稿的臆断，已按既有材料改回）：

1. **渠道标记不是新事物**：`official_fit_models` 已在线上，语义是"该渠道在这些模型上已通过离线验证"。本设计是**升级**（二值 → 行为级 + 溯源 + 时效），不是新建机制。
2. **运行时响应门已在 2026-09-11 删除**（`official-fit-mode.md` §配置模型 route 条）：它把 `route` 关闭的用户系统性判失败并引发跨渠道重试风暴。**因此正确性不靠响应门**，而靠 `route` 把请求收窄到"行为已验证渠道"；这条也决定了本设计的定位——标记写错的直接后果是**静默拟合违约**（`official-fit-mode.md` §已知限制已明示："人工信任标记，不做运行时校验…写错白名单会让 pin 流量落到非官方行为的渠道"）。**让标记由套件实测写回，就是为了把这个已知风险变成可校准、可过期、可审计的数据。**

## 1. Goal

把"渠道是否官方行为"从**二值人工信任**升级为**行为级、实测校准、人工可编辑**的标记，并把族级 pin 谓词与触发条件从 Go 代码迁为**热生效数据**：

- 每条渠道能表达"我在**哪些行为**上已验证通过"（而不是整模型的是/否）；
- 标记默认由 `tools/cdp-bench` 一致性套件实测写回，人工可覆盖（sticky + 可过期）；
- 改拟合策略/标记 = 改数据（options / `channel_fit_capabilities`），策略编译在提交前完成，运行时不需要重启；健康 Redis 条件下按 §9 的传播 SLO 生效；
- 规则与标记层**可整层拆卸**，但必须按 §11 顺序关闭、确认旧快照、删除数据并移除显式注册/selector consumers；上游 merge 由门禁保护，不靠数据库数据存活。

## 2. 现状缺陷（逐条有据，全部来自既有实现）

| # | 缺陷 | 证据 |
|---|---|---|
| D1 | 标记只有"是/否"、且按模型不按行为 | `official_fit_models`：逗号分隔模型 id 白名单，声明即视为该模型的官方行为上游 |
| D2 | 标记无溯源、无时效、无自动校准 | `official-fit-mode.md` §已知限制："人工信任标记，不做运行时校验……上游行为变化后重新验证"（谁、何时、测了几例、几轮——都无处记录） |
| D3 | 族谓词是代码 | kimi-k3 五类形状、DS 三类、GLM 整族 pin，全在 Go 谓词里；加一类形状要发版 |
| D4 | 表达不了"部分合格" | ch41 实测 CDP 51 例仅 3 例通过；ch46 仅对 tool 链严格、其余形状可用——同一张二值表两者都表达不了 |
| D5 | 守卫假设"本族只有一条官方渠道" | `officialFitPinKeepsVerdict`（`service/relay_error.go`）→ 2026-09-26 夜间把本可成功的换渠道拦成客户端 429 |
| D6 | 家族注册表是代码 | `officialfit` 包：新增家族要改 Go（清单见 `official-fit-mode.md` §家族注册表） |

## 3. 硬约束（用户逐条提出）

| # | 约束 | 兑现点 |
|---|---|---|
| C1 | pin 条件更细、少一刀切、渠道加标记、物尽其用 | 行为级标记（§6.2）+ 规则化收窄（§8）；默认留在优先级池 |
| C2 | 可拆卸、高灵活、尽量可编辑 | fork 独有包 + 明确 selector consumers + 有界数据规则（§5、§11）；删配置并关闭显式注册即卸载 |
| C3 | 通用（族无关）、改拟合即时生效不编译不重启 | 已注册族的谓词/策略数据化；expr 热编译 + 既有 config epoch（§6、§9）；新族仍需代码 registry |
| C4 | 标记由一致性套件实测写回、可人工编辑 | 套件写回协议（§7）+ sticky/expiry/force + 审计 |
| C5 | 合并上游不能丢，要独立可见 | fork 独有包 + 标记钩子 + `fork-invariants` 锚点（§12） |

## 4. 范围 / 非目标

**范围**：已开启 `official_fit.profile[family].route=true` 用户的 `/v1/chat/completions` HTTP 文本中继：选路前候选约束、HTTP affinity/preferred 快径、普通重试，以及 DS-V4 / kimi-k3 / GLM-5.3 已注册族的规则数据化、标记实测写回与人工编辑。

**非目标（v1）**
- 不改 `route` / `validate` / `errors` / `shape` 的**语义**（四维归一化见 §16，属后续阶段）；
- 不重做已上线的 pin 形状谓词，只把它们**搬家**（代码 → 等价数据，行为必须逐例一致，见 §13）；
- 不恢复任何响应级拒绝门（2026-09-11 已因误伤删除）；
- 不做控制台可视化编辑器（v1 用现有 API 直写）；
- 不引入新依赖（表达式用已在依赖里的 `expr-lang/expr`，套件用既有 `tools/cdp-bench`）。

## 5. 架构

```
请求 ─► explicit ApplyRequirement (middleware/distributor.go, 选路前)
          │  pkg/fitpolicy.Current().Decide(req) → FitRequirement
          │    ├─ snapshot: 规则 vN（expr 程序，原子替换，显式 reload hook）
          │    ├─ marks: channel_fit_capabilities（+ 兼容 official_fit_models）
          │    └─ shadow: 只记录不生效
HTTP retry ─► 同一 DecisionContext（controller/relay.go）
          │  判断是否还有未尝试且标记满足必需行为的渠道
          └─ 有 → 允许换渠道；无 → 按原有错误/重试语义，不制造新 4xx/5xx
```

- **接入面不是“两处就够”**：fitpolicy 的决策入口和 retry 判定只是两个主要接线面，实际约束必须接入现有 selector consumers：`middleware/distributor.go` 的首次 direct/affinity/preferred 路径、`service.CacheGetRandomSatisfiedChannel` 的 auto-group/blocked/saturation/normalized-model 路径、`controller/relay.go:getChannel` 的 retry/affinity/preferred 路径、`model.GetRandomSatisfiedChannelPinned` 与 `model.GetRandomSatisfiedChannelWithBlockedChannels` 的 memory/DB 候选过滤，以及 `model.GetAbilities` 的 ability fallback。Responses WS、显式固定 pin、任务插件选路列为非目标，只做回归保持现状。
- **请求级约束**：在现有 `dto.ChannelConstraints` 增加独立的 `FitRequirement`（family/model、required marks、legacy-hard-pin、policy snapshot/version、empty reason），在 gin context 与 `RetryParam` 之间只传同一不可变快照引用；selector 每次选择都消费它，不能在重试中按当前热配置重新推导。
- **现有 pin 优先级**：token/origin-task 等显式 `ChannelPin` 仍优先于 fitpolicy；fitpolicy 只影响无显式固定 pin 的 route 请求。显式 pin 不被能力标记改写。
- **显式启动注册**：`pkg/fitpolicy` 不依赖 `init()` 自动注册；由 `main.go` 在初始 options/channel cache 完成后显式调用一次 `fitpolicy.RegisterReloadHook()`，注册函数用 `sync.Once` 防重复，测试验证初始化顺序和单次注册。
- **兜底链**：未配置、`enabled=false`、`shadow=true`、`route=false`、规则缺失、编译/求值失败或能力索引不可用 → 不附加 fitpolicy 约束，完全走今天的实现；route 请求的既有硬 pin 逻辑仍由 `official_fit_models`/官方渠道类型负责，永不因 fitpolicy 失败制造 500。

> **请求内一致性约束**：首次 HTTP 选路必须把 `RequiredMarks`、legacy-hard-pin 标志、候选/策略快照版本、已尝试渠道集合和降级等级写入本次 relay context，并把同一 `FitRequirement` 引用带入 `RetryParam`；`DecideRelayRetry` 及后续 selector 只能消费该上下文。重试时沿用同一上下文并追加已尝试渠道，禁止在错误处理路径按当前请求重新计算规则，否则配置热更新会在同一请求内改变语义。

## 6. 数据模型

### 6.1 规则集 `options["official_fit.policy"]`（版本化 JSON）

```json
{
  "version": 3,
  "enabled": true,
  "shadow": true,
  "families": [
    {
      "id": "kimi-k3",
      "match": "lower(model) startsWith \"kimi-k3\"",
      "rules": [
        {"id": "k3-thinking-off",        "when": "thinkingDisabled() || reasoningEffortIs(\"none\")", "require": ["usage.thinking_counting"]},
        {"id": "k3-tool-choice-not-auto","when": "has(tool_choice) && tool_choice != \"auto\"",      "require": ["tools.choice_semantics"]},
        {"id": "k3-response-format",     "when": "responseFormatNotText()",                        "require": ["response_format.json"]},
        {"id": "k3-history-not-user",    "when": "!historyBeginsWithUserTurn()",                   "require": ["history.assistant_first"]},
        {"id": "k3-dynamic-tools",       "when": "messagesCarryDynamicTools()",                    "require": ["tools.dynamic_names"]}
      ],
      "behaviors": {
        "usage.thinking_counting":  {"class": "verdict"},
        "tools.choice_semantics":   {"class": "verdict"},
        "response_format.json":     {"class": "verdict"},
        "history.assistant_first":  {"class": "verdict"},
        "tools.dynamic_names":      {"class": "verdict"}
      },
      "unknown_mark_policy": "conservative",
      "empty_match_policy": "legacy_hard_pin_then_existing_error"
    }
  ]
}
```

- `match`：策略族归属；只允许匹配现有 mechanism registry 已注册的 family id。新增族仍需代码 registry 与消费者适配，不能仅新增 JSON。
- `rules[*].when`：expr 表达式，命中即表示"本请求需要 `require` 里的行为"（§6.3）。
- `behaviors[*].class`：`verdict`（判决类：宁失败不失真）/ `capability`（能力类：换渠道即可）/ `courtesy`（体感类：可降级并标注）。
- `unknown_mark_policy`：无标记的渠道如何处理——`conservative`（视为不支持，**标记当承诺用**）/ `permissive`。v1 默认 conservative；该默认值在 shadow 阶段冻结，未经单独验收不得改。
- `empty_match_policy`：收窄后为空时的口径。v1 固定 `legacy_hard_pin_then_existing_error`：先沿用现有 `official_fit_models ∪ 官方渠道类型` 硬 pin 集合；该集合为空时保留现有选择失败/官方错误语义，禁止回到普通未标记优先级池，禁止 fitpolicy 自造 4xx/5xx。`fail_verbatim` / `fallthrough_priority` 不属于 v1，未来若启用必须另立指标、回滚演练和用户确认。
- **未验证与过期标记**：`unknown`、`stale`、`supported:false` 必须是可区分状态；过期或撤销不能被当作 `supported:true`，也不能静默转成官方集合。
- 写时 schema 校验 + 引用完整性 + policy version 单调递增 + capability-row CAS/revision + 审计（谁、何时、变更摘要）。

### 6.2 渠道标记 `channel_fit_capabilities`（逻辑 API；渠道级物理表）

**升级而非替换**：`official_fit_models` 保留（兼容 + 粗粒度），新增独立的 `channel_fit_capabilities` 记录表达“哪些行为已验证”。逻辑上仍是渠道内标记；物理上不把可并发更新、审计和套件 provenance 塞进现有 `settings` JSON，以免通用 `UpdateChannel` 整行覆盖。`official_fit_capabilities` 可作为只读兼容投影，不是写入入口：

```json
{
  "official_fit_models": "kimi-k3",
  "official_fit_capabilities": {
    "kimi-k3": {
      "tools.dynamic_names":   {"supported": true,  "source": "suite",  "suite": "cdp-k3", "cases": "30/30", "rounds": 3, "at": "2026-09-24T02:11:00+08:00"},
      "history.assistant_first":{"supported": true, "source": "suite",  "suite": "cdp-k3", "cases": "12/12", "rounds": 3, "at": "2026-09-24T02:11:00+08:00"},
      "tools.choice_semantics": {"supported": false, "source": "suite", "suite": "cdp-k3", "cases": "2/8", "rounds": 3, "at": "2026-09-24T02:11:00+08:00"},
      "usage.thinking_counting":{"supported": true,  "source": "manual", "by": "user", "at": "2026-09-26T11:02:00+08:00", "expires_at": null}
    }
  }
}
```

- **存储选择的理由**：用户要求“多在渠道中加标记”，标记的语义归属仍是渠道；但因为现有 `channel.settings` 是整行 JSON 更新，本设计采用独立 `channel_fit_capabilities` 表（每行 `channel_id + family/model + behavior` 唯一），并由 `InitChannelCacheAndNotify()` 在提交后刷新能力索引。`official_fit_models` 继续从渠道行读取，作为兼容的粗粒度硬 pin 来源。
- **最小物理字段**：`channel_id`、`family`、`model`、`behavior`、`supported`、`source`、`suite`、`cases`、`rounds`、`at`、`expires_at`、`policy_version`、`policy_hash`、`baseline_hash`、`report_id`、`run_id`、`force`、`revision`、`updated_at`；数据库唯一键保证同一渠道/模型/行为只有一条当前记录，历史审计另写 `AuditLog`/受控审计表，不把完整 body 或凭据落库。
- **数据库门禁**：新增表/索引必须同时支持 SQLite、MySQL ≥5.7.8、PostgreSQL ≥9.6；fresh DB、上一版本升级和二次启动迁移都要验证，SQLite 不使用不兼容的 `ALTER COLUMN`。行锁统一用现有 `lockForUpdate(tx)`，CAS 冲突返回 409。
- **写入授权与并发**：专用 endpoint 区分 `capability.write` 与 `capability.force`；suite 通过独立受限 service identity/受控 applier 调用，不能持有通用 AdminAuth。请求必须携带 `expected_revision`、`report_id/run_id`，服务端在事务内锁定能力行、校验状态机并原子递增 revision；冲突不覆盖。`force` 仅 suite identity 或明确特权角色可用，人工项的覆盖、过期和恢复由服务端处理。
- **审计最小字段**：channel_id、family/model、behavior、before/after 摘要、source/by、suite/run/report id、force、expires_at、expected/new revision、结果和失败原因；禁止写入请求 body、token、完整响应或凭据。
- **冲突语义（C4）**：实测为默认来源；人工写入即 sticky，套件覆盖人工必须显式 `force`；人工项可带 `expires_at`，到期自动回退到最新实测值（防"人工遗毒"）。
- **新鲜度**：`source=suite` 且 `at` 超过 `stale_after_days`（v1 默认 30 天）即为 `suite_stale`；conservative 策略下 stale 不满足能力。时间基准由网关服务端 UTC 判定；控制台与采样日志显示 stale。
- **兼容**：`official_fit_models` 仍按现语义参与硬 pin 候选并集（`ChannelIsOfficialFitForModel`）；`channel_fit_capabilities` 缺省或索引不可用时行为完全不变。
- **能力状态机**：每个 `family/model × behavior` 独立维护 `unknown`、`suite_fresh`、`suite_stale`、`suite_failed`、`manual_active`、`manual_expired`；`supported:false` 是明确的否定结果，不等同 unknown。`supportsAll` 只接受 `suite_fresh` 或未过期 `manual_active` 的 true；stale/failed/expired 一律不满足 conservative。套件报告必须绑定 `policy_version` 与规则 hash；规则或官方基线变化会使旧报告 stale，不能自动恢复。探针失败立即产生新的 suite_failed 结果；连续两轮通过只能恢复最近一次同版本 suite 结果，不能覆盖未过期人工 sticky 项。

### 6.3 表达式契约（`expr-lang/expr`，**已在 go.mod 依赖中**）

- env（只读、无 I/O、无时间函数）：`model`、`messages`、`tools`、`tool_choice`、`response_format`、`stream`、`max_tokens`、`temperature`、`top_p`、`n`、`logprobs`、`top_logprobs`、`thinking`、`reasoning_effort`、`user_agent`、以及 gjson 视图 `body`。实现必须限制 body 字节数、JSON 深度、数组/消息/工具数量和字符串长度；畸形 JSON、超限输入和未知字段按确定性的求值失败处理，不把原始 body 写入 trace。
- 注册函数（`pkg/fitpolicy/registry.go`，**fork 独有，唯一需要发版的扩展点**）：`thinkingDisabled()`、`reasoningEffortIs(s)`、`historyBeginsWithUserTurn()`、`messagesCarryDynamicTools()`、`responseFormatNotText()`、`toolChainResolvable()`、`promptTokenEstimate()`、`hasImagePart()`。**这些就是今天 `kimiK3RequestNeedsOfficial` / DS 三类谓词的原样搬迁**（先等价搬家，后改数据）。
- 编译：按 `version` 整集编译（形态参考现有 `pkg/billingexpr/compile.go`，但使用独立的 fitpolicy env、函数白名单和资源上限）；编译失败 → **拒绝写入**。fitpolicy 表达式不得调用 billing expr 的 usage/quota 环境。
- 求值：每请求 `expr.Run`，预算默认 2ms，超时按求值失败处理（→ 兜底链）。

## 7. 标记怎么来：套件实测写回 + 人工编辑

**写回链路（默认来源）**

```
tools/cdp-bench (Bun/TS, src/suites/{ds-v4,kimi-k3,glm-v53}.ts)
  └─ 隔离验证：把目标渠道临时挂到专用分组 fit-verify-<chid>，临时 token 绑该分组
     └─ 官方 LIVE 基准（唯一基准）→ 判定维度 SEVERE / WORDING / SHAPE / PROTOCOL / INTEGRITY
        └─ 3 轮 × 目标用例（cost plan 口径：200-class，每渠道总消耗 < ¥2），三轮全 0 diff 才算 supported
           └─ 输出 fit-marks 报告（JSON：model × behavior × supported × cases × rounds × at）
              └─ 输出报告 → 受控 applier → 专用 capability endpoint（CAS 合并）→ InitChannelCacheAndNotify → 健康 Redis 下全节点 P99 ≤2s
```

- **套件只对照官方端点**（与当前路由无关）→ 不存在“测量被路由污染”的反馈环。
- **写回采用两段式**：套件默认只产出带 `schema_version`、`report_id`、官方基线指纹、目标渠道/模型、行为结果和签名摘要的报告；独立的受控 applier 校验报告、权限、幂等键和当前 capability-row revision 后才写入。v1 不允许套件进程持有通用 AdminAuth，也不允许无条件覆盖整条渠道 settings。
- **并发与幂等**：写回必须使用 capability-row compare-and-swap（`expected_revision`），冲突即拒绝并重新读取；同一 `report_id` 重放不得重复产生审计事件。人工编辑与 suite 写回都必须保留 before/after 摘要、操作者、原因、来源、`force`、`expires_at` 和关联报告。
- **持续校准**：cron 每 6h 轻探针（3-5 例）；任一失败 → 立即把对应行为标 `supported:false` + 告警；连续 2 轮通过 → 自动恢复（沿用 cost plan §1.2 的校准纪律与自动摘除/恢复）。自动恢复只能恢复到最近一次通过的 suite 结果，不能覆盖未过期人工 sticky 项。
- **人工编辑（C4）**：同一字段可由管理员/控制台直接改写；人工项 sticky，套件需 `force` 才覆盖；带 `expires_at` 的临时覆盖到期回落到最新实测值；每次写入留 provenance + 审计。报告签名/权限/版本校验失败时保持 last-known-good，不改变线上标记。

## 8. 决策流（pin 仍由 `route` 触发，标记只决定"谁有资格"）

```
func Decide(req) Decision:
    cfg := snapshot(); if !cfg.Enabled || cfg.Shadow: shadowRecord(); return NoOpinion
    profile := req.UserSetting.OfficialFitProfileFor(req.Model)
    if !profile.Route: return NoOpinion
    fam := strategyFamily(req.Model) // 只引用不可热替换的 mechanism registry
    if fam == nil: return NoOpinion
    feats := evalFeatureExprs(req, fam)                     // expr，输入有大小/深度上限
    reqMarks := union(rule.require for rule in fam.rules if rule.when(feats))
    if len(reqMarks) == 0: return NoOpinion                 // 既有 route predicate 未命中
    base := buildCandidates(group, model, aliases, filters, blocked, saturation, affinity)
    officialBase := [c for c in base if isOfficialBehaviorChannel(c, fam, req.Model)]
    marked := [c for c in officialBase if supportsAll(marks(c), reqMarks, fam.unknown_mark_policy)]
    if len(marked) > 0: return Decision{Constraints: allowOnly(marked), RequiredMarks: reqMarks, Snapshot: cfg.version}
    return Decision{Constraints: allowOnly(officialBase), RequiredMarks: reqMarks, EmptyReason: "no marked channel; preserve legacy hard pin", Snapshot: cfg.version}
```

- **候选构造顺序必须固定**：先按 group/model/request filter、model alias/path、group policy、blocked、saturation 和已有 affinity/preferred 规则构造候选；fitpolicy 只增加 allow-only 能力约束，不能重新引入被禁用或已排除渠道。`base` 为该阶段的当前候选；`hardPinBase = base ∩ (official_fit_models ∪ 官方渠道类型)`。
- **现有 pin 不变量与策略**：`route=true` 且请求命中既有族 predicate 时，若 `base ∩ capabilities(supportsAll RequiredMarks)` 非空，先只在此子集内按原 priority/weight 选路；若为空，必须使用 `hardPinBase`，不能无候选约束地退回普通池；若 `hardPinBase` 也为空，保留现有 `nil`/selection-error 结果，不能降级到普通聚合池。对于未参与该族 predicate 的请求，fitpolicy 返回无意见，完全沿用现有路由。
- **重试状态机**：每次选择前消费同一 `DecisionContext`，先从行为约束和硬 pin 约束中排除已尝试渠道，再按既有 selector/priority 选择；仍有合格渠道才 retry。约束集合耗尽时执行既有错误语义；fitpolicy 不生成新的 4xx/5xx，也不把未标记渠道当作官方行为渠道。v1 contract tests 覆盖 HTTP 首选、HTTP affinity、归一化模型、auto-group、blocked/saturation、普通 retry；Responses WS、显式固定 pin 和任务插件路径只做“fitpolicy 不介入”的回归测试。
- **重试判定**：只检查上下文中排除已尝试渠道后的剩余 `FitRequirement`/hard-pin 约束，不重新推导 `RequiredMarks`；有则允许换渠道，无则按原有 stop/retry 结果返回。只有“仍有能力合格渠道”时才绕过现有单一官方渠道假设，修复 D5。

## 9. 即时生效（复用既有链路，不新增传播代码）

| 变更 | 传播路径 | 生效时延 |
|---|---|---|
| 规则集（options） | DB 提交前 schema/ref/type 编译校验通过 → `model/option.go` 写入 → `NotifyConfigChanged()` → Redis `config_epoch:v1` INCR → 各节点 watcher → `applyConfigReload` → 显式注册的 fitpolicy reload hook 重建快照 | 健康 Redis、N 节点 P99 ≤2s |
| 渠道标记（独立表） | 专用 capability endpoint 的事务/CAS 原子合并 → 刷新 channel capability index → `InitChannelCacheAndNotify()` → 同上 | 健康 Redis、N 节点 P99 ≤2s |
| Redis 不可用 | epoch 读不到 → 退回 `SYNC_FREQUENCY`（默认 60s）同步循环 | 数十秒（**正确性不受影响**，`config_epoch.go` 明确 fail-open） |

- 本实例写入后**下一个请求**即用新快照（原子指针交换）；DB 事务提交前拒绝非法规则，不允许先落库再异步发现错误。
- 编译/求值失败 → 保留节点内 last-known-good，并记录版本与失败原因；节点重启加载同一 last-known-good/旧行为，不把半编译状态传播出去。
- Redis 不可用时不得宣称 ≤2s：节点保持当前快照，按现有同步周期获取最新配置；验收必须分别覆盖健康与故障条件。
- **不新增传播机制**：`config_epoch` 已由 fork 于 2026-09-25 落地，本设计复用它的 reload hook，但必须验证包 import 保活、单次注册和慢 reload 行为。

## 10. 护栏（吸收 2026-09-26 夜间三次误报的教训）

1. 写入即 schema + 引用完整性校验（`require` 引用的行为必须在 `behaviors` 声明；族必须存在）。
2. 任何规则/标记变更先 **shadow**（§11）再放行；shadow 结论入 `logs.other.fitpolicy`，可按规则 id 聚合"若启用影响多少请求"。
3. 一键回滚：`official_fit.policy.enabled=false`（秒级回旧行为）或回滚到指定 `version`。
4. 判定纪律（夜间教训）：**慢≠死**（无完成≠池全灭）、**请求侧≠渠道侧**（渠道文案默认可疑，先对齐官方基准）、**未知标记语义显式**（默认 conservative）。
5. 标记新鲜度：`stale` 标记 + 6h 校准 + 人工项过期；`force` 覆盖留审计。
6. 观测：Trace 只写规则 id、policy/mark revision、requiredMarks 哈希、候选/选择渠道 id、状态和 shadow 结果；请求 body、messages、tools、token、完整响应和凭据禁止入日志。按采样率记录并设保留期；巡检脚本增加“收窄集为空比率”、候选绕过率和 DecisionContext 缺失率指标。

## 11. 影子、灰度与卸载

- **影子**：`shadow=true` → 全量评估并记录，**绝不改路由**；用于用真实流量校准规则与标记。
- **灰度**：按族启用 → 按渠道补标记 → 全量；每步独立回退。
- **旧机制共存**：`official_fit_models` 与能力标记并存；`route` 与 `validate/errors/shape` 完全不动。规则集缺失或 fitpolicy 失败时返回无意见，行为 = 今天；`official_fit_models ∪ 官方渠道类型` 的硬 pin 基线不被移除。
- **卸载**：先将 `official_fit.policy.enabled=false` 并确认所有节点使用旧快照，再删除 policy 和 `channel_fit_capabilities` 数据；最后移除显式启动注册、selector consumers 和 `pkg/fitpolicy/`。`official_fit_models` 与既有 route 逻辑不能被卸载步骤删除。

## 12. Fork 存活与可见（C5）

- **侵入面 = fork 独有包 + 明确的 selector consumers + 显式启动注册**；不能再用“两个 hook”概括全部接线。每个上游修改点都要有 `// tokeness-fitpolicy:begin/end` 或符号/AST 锚点。
  ```go
  // tokeness-fitpolicy:begin  （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
  fitpolicy.ApplyRequirement(c, request)
  // tokeness-fitpolicy:end
  ```
- **登记**（实现时同批提交）：
  - `FORK-CHANGES.md` 新增一节；
  - `scripts/fork-invariants/manifest.json` 新增 entry，锚点至少覆盖：`kind:file`（`pkg/fitpolicy/*.go`）、`kind:symbol`（`fitpolicy.Current` / `Decide` / `CompileRules` / `ApplyRequirement`）、显式启动注册、每个 selector consumer、能力表/endpoint 服务和测试名（`TestFitPolicyHookWired` / `TestFitPolicyFallbackToLegacy` / `TestFitPolicyMarksConflict`）。
  - 门禁：`node scripts/fork-invariants/main.mjs --check manifest`，每次上游 sync 后必跑（该门禁正因 rc35 丢 5 项、rc39 丢 video-token 块而立）。
- **语义门禁**：除 file/symbol 锚点外，必须有 contract tests 验证 active policy 确实改变已知 fixture 的候选集、shadow 写 trace、reload 后版本变化、retry 消费 `RequiredMarks`，并验证 init registration/import 只发生一次。merge CI 对真实 `upstream/main` 做 clean/conflict rehearsal 后运行这些测试；仅保留注释或符号不算通过。
- **定位**：`grep -rn "tokeness-fitpolicy"` 仅用于人工导航，不是完整语义证明。
- **数据在 DB**：规则/标记数据不随上游 merge，但所有 selector 入口、专用写回接口和注册表适配器仍需纳入 fork inventory。

## 13. 迁移计划（风险递增，每步可停）

| 步 | 内容 | 验收 |
|---|---|---|
| P0（已上线） | 四维 profile + 形状 pin + `official_fit_models` 白名单 + 亲和不对称 | `official-fit-mode.md` §验收 |
| **A（前置）** | 冻结 v1 调用图与非目标；为已注册族建立 route predicate adapter、`FitRequirement/DecisionContext` 和所有 v1 selector consumers；`shadow=true` 跑 ≥48h，旧 Go predicate 与新策略逐例对照 | HTTP 首选/affinity、随机、auto-group、normalized model、blocked/saturation、retry 全入口覆盖；Responses WS/显式 pin/任务插件保持现状；无上下文绕过；差异为零或逐条批准 |
| **B** | 建立 `channel_fit_capabilities` 表、迁移/索引、专用 capability DTO/endpoint、CAS/审计/状态机；接入 suite 报告校验与 6h 校准；shadow 下能力筛选只观察不改路 | SQLite/MySQL/PostgreSQL fresh+upgrade+二次启动通过；权限/force/冲突/幂等测试通过；旧人工项不被覆盖；收窄空集和重试状态机可观测 |
| **C** | 先以 kimi-k3 单族启用；旧 `official_fit_models` 与官方类型继续作为硬 pin 基线；仅在 healthy Redis SLO 下逐步放量 | 24h 内拟合违约 0、可避免失败 0；故障注入时保持旧快照且不制造新错误；无标记候选时绝不落到普通聚合池 |
| **D** | DS-V4 / GLM-5.3 迁入；明确热策略与代码 mechanism registry 边界；新增族仍需代码 registry/consumer 变更 | 逐族 shadow 等价；registry adapter、wire shape、channel type、exact model consumer 全覆盖；非目标协议无行为变化 |
| **E** | §16 契约面数据化，顺序 = **F1 errors → F2 shape-A → F3 validate → F4 B 档 → F5 具名契约**；F1/F2 必须先完成语料前置 | 每阶段按对应等价标准、golden fixtures、脱敏回放/账务门禁通过；不以未定义的“全部逐字节”作为默认承诺 |

## 14. 验收

**功能与等价性**
- deterministic CI：规则编译/schema/ref/type 在 DB 提交前拒绝；fake epoch、显式注册只执行一次、last-known-good、删除配置卸载、能力表 CAS/权限/force/幂等、空集/已尝试渠道/首选/归一化/affinity/HTTP/retry contract tests 全通过；Responses WS、显式固定 pin、任务插件路径有保持现状的回归测试，不宣称由 fitpolicy 覆盖。
- 规则/标记写入后：在明确的 N 节点、Redis healthy、节点健康条件下传播 P99 ≤2s；Redis/节点/慢 reload 故障时保持旧快照并记录可观测故障，不把 ≤2s 当作无条件承诺。
- `enabled=false` 或删除 `official_fit.policy` 与 `channel_fit_capabilities` 后，行为与未安装本层一致，不产生新 4xx/5xx；`official_fit_models` 仍按既有 route 逻辑工作。
- 非法规则写入被拒，不影响线上快照；求值异常 → last-known-good → 旧行为。
- shadow 日志只含脱敏元数据、哈希和采样 trace，且有保留期限；不记录 body/凭据。
- 卸载时删除的是 `channel_fit_capabilities` 表中当前标记；`official_fit_capabilities` 只读投影不作为独立写入数据源。
- 标记状态机（人工 sticky/过期、suite stale/failed/恢复、policy/baseline 变化）均有测试。
- F1/F2 的准入前置：必须有短期双写/哈希差异捕获或经批准的 semantic baseline；报告样本量、fixture、差异分类和产物路径，缺失语料不得放行。

**CDP 双轨（既有的唯一验收口径）**
- A 轨：`tools/cdp-bench` 严格跑通过（全部执行用例 PASS、无未声明跳过、SEVERE/WORDING/SHAPE/PROTOCOL/INTEGRITY 全零）；基线对比是门禁的一部分。
- B 轨：KV cache ≥90%、有效成功率 ≥99%（HTTP 200 空内容算失败）、TTFT P90 <30s、TPOT P90 <40ms、TPM 满足所选 profile。

**工程与 fork**
- `go test ./pkg/fitpolicy/...`、selector/constraint/capability endpoint 聚焦测试、根目录全量 `go test ./...`、`go vet ./...`、`git diff --check`、web `bun run typecheck`。
- 涉及 `relaykit/` DTO 时另跑 `cd relaykit && GOWORK=off go build ./...`；新增 capability 表/事务时必须跑真实 SQLite、MySQL 和 PostgreSQL fresh/upgrade/idempotency 矩阵，并记录版本/命令/结果。
- `node scripts/fork-invariants/main.mjs --check manifest` 通过；对 `upstream/main` 做一次真实 merge rehearsal 后门禁和 contract tests 仍绿。
- 决策延迟：先以编译期表达式复杂度/输入大小上限约束，再用固定 fixture benchmark 验收 P50/P95/P99；2ms 是目标而非可依赖的强制中断机制，超预算按失败安全路径处理并有测试证据。

## 15. Risks

- **标记即信任，写错=静默违约**（既有已知限制）：缓解 = 套件写回 + 6h 校准 + stale 提示 + shadow 先行 + 人工项过期。
- **数据即代码**：规则错误即时影响线上 → schema 校验 + shadow + 版本回滚 + 按族灰度 + 审计。
- **表达式沙箱**：env 只读、无 I/O、无时间函数、函数白名单；以编译期复杂度和输入上限控制资源，固定 fixture benchmark 验证延迟，超预算按失败安全路径处理，不依赖无法可靠中断的运行时 CPU timeout。
- **未知标记歧义**：v1 conservative 可能让更多流量落官方集（成本上升）；用 shadow 数据评估后再决定是否对特定行为放开 permissive。
- **钩子与上游冲突**：路由决策有 2 处主要 fork hook，但 selector 入口、专用写回接口和 registry adapter 也属于存活面；所有入口由调用图、fork inventory 和 contract tests 覆盖。上游重写 hook 所在函数时按 `FORK-CHANGES.md` 重挂。
- **等价迁移风险**：谓词从 Go 搬到 expr 若有偏差，会改变 pin 判定 → A 步必须逐例一致，且 shadow 观察期内不得启用。
- **依赖既有机制**：`config_epoch` 需 Redis；不可用时退化为数十秒同步（正确性不变，仅传播变慢）。

## 16. 演进：把 validate / errors / shape / route 归一为契约面（F1–F5，即 §13 的步 E）

四维是同一份保真契约的四个面，可统一为 **需求侧（用户契约）× 供给侧（渠道标记）→ 决策**。边界必须如实：**策略可数据化，机制仍需注册表**。

| 维度 | 可数据化（热） | 必须留代码 |
|---|---|---|
| route | 谓词、候选过滤、空集口径（本文件主体） | 提取器函数 |
| validate | 规则表：字段/比较/官方文案模板、族开关 | 语义检查（tool 链可解析性、tokenization 失败识别） |
| errors | 文案策略：是否附 request id、按来源/错误类分流 | 错误提取与拼装点 |
| shape | 变换选择：哪些变换对哪族/哪渠道生效 | 变换器本体（SSE 拼接、usage 映射、字段剥离） |

**阶段（按 §16.1 的三档重排，风险递增）**：**F1 errors**（几乎纯 A 档：`error_text` / `media_type` / 是否附网关 request id 全部数据化；先做 **shadow 原型**，与 `IsStrictFitValidationMessage` + `controller/relay.go` 门控逐字节对照，差异必须为 0 或逐条列明批准）→ **F2 shape 的 A 档键操作**（键集与映射外提为数据，raw-byte 外壳留代码）→ **F3 validate 规则表**（Go 校验器降级为执行器，旧表保留一键回退；tool 链可解析性等语义检查留代码）→ **F4 B 档**（`usage` 数值推导：键名映射可数据，数值来源留代码，规则须打 `affects_billing: true` 并与计费回归同批验证）→ **F5 具名契约**（四布尔 → `fit_contract: "official-k3-v3"`，旧数据零迁移）；**C 档（跨消息状态与时序）永不数据化**，只登记为命名变换器。准入标准按操作类型分别采用有限字节级 golden 或 canonical semantic JSON；差异逐条列明，基线用 CDP 双轨与脱敏回放。四维属客户可感知契约，比路由更危险：路由层与契约层开关必须互相独立。

### 16.1 体变换契约：哪些请求体 / 响应体改动能接住

判据只有一条：**该改动是否只依赖单条消息自身的结构**。

| 档 | 判据 | 能否数据化 | 现网对应实现（已在此抽象层级） |
|---|---|---|---|
| **A 单条消息内结构操作** | 只读一条 message / event 的键集与值 | ✅ **纯数据** | `deleteNonAllowedTopLevelKeys(data, allowed)`、`stripOfficialChoiceKeysInPlace`、`ensureOfficialChoiceKeys`、`stripNonOfficialStreamKeys`、`deepSeekV4OfficialTopLevelKeys` / `deepSeekV4OfficialMessageKeys`（**本来就是白名单表**） |
| **B 数值推导与计费口径** | 需要计算 / 聚合数值 | ⚠️ **半数据**（键名映射=数据，数值来源=代码） | `deepSeekV4UsageJSON`、`normalizeDeepSeekV4Usage`、`kimiK3UsageJSON(usage, cacheWrite)`、`cacheWriteFromRows` |
| **C 跨消息状态与时序** | 依赖前序消息 / 终止 / 时序 | ❌ **必须代码**（注册为命名变换器） | `FitDeepSeekV4StreamEventForAdapters`、`fitKimiK3StreamEvent(..., final)`、`FitKimiK3StreamUsageOnlyChunk`、`kimiK3FirstChoiceUsage`、流式首事件判决与 `[DONE]`/EOF 终止 |

**op 集（数据，仅 A 档，全部作用在原始字节上）**：`strip_keys`（白名单/黑名单）、`rename_key`、`ensure_keys`（按模板补齐）、`set_value`（常量）、`coerce_type`、`key_order`（显式键序模板）、`media_type`、`error_text`（来源或错误类 → 官方原文 + 是否附网关 request id + Content-Type）。每条 op 带 `scope`（顶层 / `choices[]` / `message` / `delta` / per-event）与顺序号；**不接受任意脚本、正则替换或模板求值**。

**两条硬性约束**

1. **先区分字节级与语义级等价**：不能宣称所有 `gjson`/`sjson` 操作都保持原始字节。F1 只允许已用 golden fixtures 证明可保持字节的有限操作；`rename_key`、`coerce_type`、`ensure_keys`、`key_order` 等默认按 canonical semantic JSON 验收，除非引入专用保序/保词法 parser 并单独证明。必须定义 path grammar、数组/重复 key/缺失路径、冲突、幂等和错误 fallback。SSE framing、首事件、usage 尾块、`[DONE]`/EOF 归 C 档，数据 op 不得触碰。
2. **不新增无审查的上游侵入点**：体变换发生在 relay/adaptor 层，不在路由钩子里。复用既有 fork 变换文件（`relay/channel/openai/deepseek_v4_fit.go`、`kimi_k3_fit.go`），把已验证的表外提为数据，代码壳留在原处；任何新增入口必须进入 fork inventory、调用图和 merge contract tests。

**对 §16 阶段顺序的调整**：errors（几乎纯 A 档、风险最低，直接对应 CDP 的 WORDING / SHAPE 失败）→ shape 的 A 档键操作 → validate 的规则表 → B 档带计费门禁 → **C 档永不数据化**。route 仍是第一步（§13 的 A 步）。

**B 档的额外门禁**：任何触及 `usage` 的规则必须打 `affects_billing: true`，并与计费回归同批验证——拟合形状改了，账不能跟着变。

## 17. Implementation Preconditions (Not Open Design Choices)

1. **套件写回协议（已确定）**：报告 + 受控 applier + 专用 capability endpoint；实现前冻结 DTO、权限、CAS、幂等和审计字段；不允许直接复用通用 `PUT /api/channel/`。
2. **标记存储（已确定）**：逻辑归属渠道，物理存储 `channel_fit_capabilities` 独立表；`official_fit_models` 继续保留为粗粒度硬 pin。不得隐式改回 options 或把新字段写进通用 settings patch。
3. **v1 选路入口（已确定）**：只覆盖 HTTP chat 的 direct/affinity/preferred/random/auto-group/normalized-model/blocked/saturation/retry；Responses WS、显式 fixed pin、任务插件选路保持现状，并在实现前提交调用图与非回归测试清单。
4. **机制注册表边界（已确定）**：`pkg/fitpolicy/` 只热加载已注册族的策略；family id、channel type、wire shape、exact model names 和跨模块机制仍由代码 registry 提供。新增族仍需代码变更。
5. **`unknown_mark_policy`（已确定）**：v1 固定 `conservative`；`legacy_hard_pin_then_existing_error` 是唯一空集策略，未经独立验收不得切换。
6. **F1/F2 语料前置（未满足即不得开工）**：现有 logs 不留 body，必须先选择并记录批准证据：短期双写仅落哈希/差异且脱敏，或批准 semantic baseline + golden fixtures；CDP 官方语料不能替代旧网关输出语料。
7. **热生效 SLO（实现前冻结）**：冻结 N 节点、Redis healthy/fault、慢 reload、重启恢复的测量方案；≤2s 仅适用于健康条件，故障条件验收旧快照与告警。
8. **日志与隐私（实现前冻结）**：冻结 shadow/审计字段、采样率、哈希方式和保留期；禁止记录 body、工具内容、token、完整响应和凭据。
9. **权限命名（实现前冻结）**：优先复用现有 `authz.ChannelWrite` 作为人工 capability.write、`authz.ChannelSensitiveWrite` 或受控 service identity 作为 force 写入；只有在确认现有权限粒度不足时才新增 capability 专用 action，并同步 authz 资源定义与测试。
