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

### 0.0 当前代码基线（先读：区分“已存在”与“计划新增”）

截至本文件当前提交，**已经存在**的只有：

- legacy 渠道级 per-model allowlist `official_fit_models`（`channels.settings` JSON 里的 `[]string`，`relaykit/dto/channel_settings.go`）；
- 官方拟合的硬 pin 收窄（`model/channel_cache.go` 的 `preferOfficialFitChannels`、`model/ability.go` 的 `preferOfficialFitAbilities`，legacy model-level baseline）；
- `config_epoch` 与 `RegisterConfigReloadHook`（`main.go` 目前**只**显式注册 authz hook）；
- 现有多键凭据 revision/CAS（只覆盖 credential 行）与 Casbin authz（只有 channel read/operate/write/sensitive_write/secret_view）。

**完全不存在、属于本文件计划新增**的：`channel_fit_capabilities` model/migration/cache 索引、`FitRequirement`、`pkg/fitpolicy`、`ApplyRequirement`、capability endpoint/applier/CAS/provenance/state machine、`capability.write`/`capability.force` 权限或受限 service principal、channel 行级 revision、periodic 安装路径、行为级 capability 标记本身。

因此：**§6–§14 是实现目标与门禁，不是当前保证**；除 §0.1 明确标注“已核实”的 legacy 事实外，全篇出现“新增/必须/目标”语义的条目都不得被读成“复用既有基础设施”。文档中凡“复用”仅指复用 `config_epoch` 传播机制、`expr-lang/expr` 依赖、`recordManageAudit`/`AuditLog` 审计入口和 CDP 套件，不指复用尚未存在的 capability 设施。

### 0.1 已核实事实

1. 第一次 HTTP 选路在 `middleware.Distribute`，普通重试在 `controller/relay.go:getChannel`；实际选择下沉到 `service.SelectChannelForRequest`/`service.SelectRandomChannelForRequest` → `service.CacheGetRandomSatisfiedChannel` → 内存 `model.GetRandomSatisfiedChannelPinned` 或 DB `model.GetChannelWithBlockedChannelsPinned`/`getChannelWithFilters`，ability 回退是 `model.GetAbilities`。affinity/preferred 快径在 `Distribute` 和 `getChannel` 各有一份，且两者检查强度不同（`Distribute` 查 filter + `officialPinAllowsAffinity`；`getChannel` 不查 `ChannelSatisfiesFilters`）。
2. `middleware.Distribute` 是**通用**中间件：除 `/v1/chat/completions` 外还服务 `/pg/chat/completions`、`/v1/completions`、`/v1/messages`、`/v1/responses/compact`、Gemini、embedding/audio/rerank 和 realtime WS。现有 `markV4OfficialPinFromDistributor` 用的是 `strings.HasSuffix(path, "/chat/completions")`，因此**也会命中 `/pg/chat/completions`**。v1 必须按精确路径 + OpenAI chat relay 判定，不能用后缀匹配。
3. `ChannelSatisfiesFilters` 只在 `service.SelectRandomChannelForRequest` 内执行；`controller/relay.go:getChannel` 的重试直接调 `CacheGetRandomSatisfiedChannel`，该函数只接收 legacy 的 pin/video bool，**不读 `ChannelConstraints.Filters`**。因此“所有 selector 消费同一约束”在当前代码上不成立，必须显式扩接口。
4. `lockForUpdate` 在 SQLite 会跳过 `FOR UPDATE`（`model/locking.go`），所以“事务内 `SELECT ... FOR UPDATE` + 原子递增 revision”不能作为三库统一的 CAS 方案；通用做法必须是条件 `UPDATE ... WHERE revision = ?` 并检查 `RowsAffected`，冲突返回 409。
5. periodic `model.SyncOptions` → `loadOptionsFromDatabase` **不执行** `RegisterConfigReloadHook` 注册的 hook（hook 只在 `applyConfigReload`/epoch watcher 路径执行）。Redis 不可用时，其他节点的周期同步只更新 raw OptionMap，不会编译/安装 fitpolicy 快照；因此当前不能宣称“Redis 故障仅变慢、正确性不变”。
6. `dto.ChannelConstraints` 当前只有 pin/filter，没有行为能力和“marked 优先、official 回退”的结构；`official_fit_models` 硬 pin 由 `preferOfficialFitChannels`/`preferOfficialFitAbilities` 在选路内部即时过滤产生，**不是可复用的集合对象**，且候选为空时直接返回 `nil`。
7. 显式 `ChannelPin`（token/origin-task）在 `Distribute` 的 ResolvedPin 与 token-specific 分支于选路前直接取渠道，共享 selector 也先返回 pin；这与 fit 的 `pinOfficial` 候选收窄是两回事，不能混为一谈。
8. Responses WebSocket 通过 `relay/responses_websocket.go:selectResponsesWSChannel` 复用 `service.SelectChannelForRequest` 并注入 `FilterResponsesWebSocket`；它是独立协议路径，当前 Phase 3 v1 **不纳入**。
9. `RegisterConfigReloadHook` 是 append 注册、没有去重；`main.go` 目前用显式调用注册 authz hook。fitpolicy 采用同样的显式注册并要求只注册一次测试。
10. `model.UpdateOption(s)` 对现有 request policy 已有“构造完整快照 → DB transaction → 更新内存 → `NotifyConfigChanged()`”路径；fitpolicy 规则必须接入同一提交前校验和快照路径。
11. `channel.settings` 当前是 `relaykit/dto.ChannelOtherSettings` 的 JSON，通用 `UpdateChannel` 整行更新并调用 `InitChannelCacheAndNotify`，没有字段级 provenance/CAS 语义；因此能力标记不复用该写入路径。
12. 计费表达式代码在根模块 `pkg/billingexpr`（`expr.go` 同目录 `expr.md`）；usage/计费相关变换必须遵守 `.agents/rules/billing.md`，fitpolicy expr 与 billing expr 不是同一个运行时环境。
13. 现有 `GET/PUT /api/user/official-fit` 与 `/api/user/official-fit/families`（`router/api-router.go`、`controller/user.go`）只读写**用户** `official_fit` 四维 profile，与渠道 capability marks 无关，不能当作 capability API。
14. `officialFitPinKeepsVerdict`（`service/relay_error.go`）在 pin + 自动重试关键字命中时直接 stop（仅多键轮换例外）；`DecideRelayRetry` 才是决策点，重试准备在 `controller/relay.go` 之后。D5 必须改这个函数，不能只改候选集。
15. `common.ApiError`/`ApiErrorI18n`（`common/gin.go`）一律返回 HTTP 200；新 endpoint 的 409 必须显式 `c.JSON(http.StatusConflict, ...)`。
16. `migrateDB` 只在 `common.IsMasterNode` 时执行（`model/main.go`），当前 AutoMigrate 列表没有 capability model；现有迁移测试在缺 DSN 时会跳过，不能作为三库证据。
17. `Channel.Insert/Update/Delete`/`BatchDeleteChannels`（`model/channel.go`）都不处理 capability 行，也没有外键级联；能力表生命周期必须显式设计。



### 0.2 最终冻结范围

- **v1 只覆盖**精确路径 `POST /v1/chat/completions`、且走 OpenAI chat relay 格式的请求，并且该用户 `official_fit.profile[family].route=true`：首次选路、HTTP affinity/preferred 快径、普通重试和 capability/ability 候选收窄。
- **v1 明确排除**（这些路径即使经过 `middleware.Distribute` 也必须 no-op）：`/pg/chat/completions`、`/v1/completions`、`/v1/messages`、`/v1/responses/compact`、`/v1/alpha/search`、Gemini、embedding/audio/rerank、realtime WS、Responses WebSocket、异步任务/任务插件选路、显式 `ChannelPin`（token/origin-task）、非 chat relay 格式和响应级运行时拟合门。
- “同后缀就生效”不算精确判定：实现必须同时校验精确路径与 relay 格式，禁止沿用 `HasSuffix("/chat/completions")` 这类会命中 `/pg` 的写法。
- 任何未覆盖入口都必须保持现有行为；文档与日志不得声称 fitpolicy 已对“全部渠道/全部协议”生效。
- 契约面 F1–F5 是后续独立阶段，不与 v1 route 策略数据化同批上线；其中 F1/F2 的语料前置未满足时不得开工（见 §17.6）。


### 0.3 最终架构决定

- route 仍是唯一用户侧启用条件；`ApplyRequirement` 自身负责 gate：`route=false`、未知族、非 in-scope 路径/格式一律 no-op。
- fitpolicy 只增加“**marked 优先**”这一层约束，不替换 `official_fit_models`：算法是 selector 内的两阶段收窄——先取 `base ∩ officialBehavior ∩ marked`，非空则只在该集合内按原 priority/weight 选路；为空则取 `base ∩ officialBehavior`（即今天硬 pin 的等价结果）；仍为空时沿用现有 `nil`/selection-error 分类。**不存在“可复用的硬 pin 集合对象”**，该两阶段必须在同一处 selector 内实现。
- 显式 `ChannelPin`（token/origin-task）分支**永不附加也不评估** FitRequirement，保留现有 filter/policy/error/retry 行为；官方拟合的 `pinOfficial` 候选收窄与显式 `ChannelPin` 是两回事，文档与代码都不得混称“pin”。
- 决策快照放 gin context（immutable：policy version、family、RequiredMarks、legacy fallback 标志）；`RetryParam` 只从同一 context 取引用，不各自重算。attempted/excluded/saturation 作为独立 mutable 状态，不属于快照。
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
| D1 | 标记只有"是/否"、且按模型不按行为 | `official_fit_models` 是 `channels.settings`(即 `Channel.OtherSettings`) JSON 里的 per-model 字符串数组（`[]string`，`relaykit/dto/channel_settings.go`），声明即视为该模型的官方行为上游 |
| D2 | 标记无溯源、无时效、无自动校准 | `official-fit-mode.md` §已知限制："人工信任标记，不做运行时校验……上游行为变化后重新验证"（谁、何时、测了几例、几轮——都无处记录） |
| D3 | 族谓词是代码 | kimi-k3 五类形状、DS 三类、GLM 整族 pin，全在 Go 谓词里；加一类形状要发版 |
| D4 | 表达不了"部分合格" | ch41 实测 CDP 51 例仅 3 例通过；ch46 仅对 tool 链严格、其余形状可用——同一张二值表两者都表达不了 |
| D5 | 守卫假设"本族只有一条官方渠道" | `officialFitPinKeepsVerdict`（`service/relay_error.go`）在 `ContextKeyV4OfficialPin` 下对自动重试关键字直接 stop（只放行多键凭据轮换）→ 2026-09-26 夜间把本可成功的换渠道拦成客户端 429。**仅靠候选过滤修不掉它**：决策点在 `DecideRelayRetry`，而重试准备在 `controller/relay.go` 之后发生，必须扩展/替换该判定函数 |
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

**范围**：精确 `POST /v1/chat/completions`（OpenAI chat relay）、用户 `profile[family].route=true` 的文本中继：选路前候选收窄、HTTP affinity/preferred 快径、普通重试，以及 DS-V4 / kimi-k3 / GLM-5.3 已注册族的策略数据化、标记实测写回与人工编辑。

**非目标（v1）**
- 不改 `route` / `validate` / `errors` / `shape` 的**语义**（四维归一化见 §16，属后续阶段）；
- 不重做已上线的 pin 形状谓词，只把它们**搬家**（代码 → 等价数据，行为必须逐例一致，见 §13）；
- 不覆盖 `/pg/chat/completions`、`/v1/completions`、`/v1/messages`、`/v1/responses/compact`、Gemini/embedding/audio/rerank、realtime WS、Responses WebSocket、任务/任务插件选路；这些路径保持现状且必须有非回归测试；
- 不覆盖显式 `ChannelPin`（token/origin-task）与 `?channel_id=` 类固定渠道；这些分支永不评估 FitRequirement；
- 不恢复任何响应级拒绝门（2026-09-11 已因误伤删除）；
- 不做控制台可视化编辑器（v1 用现有 API 直写）；
- 不引入新依赖（表达式用已在依赖里的 `expr-lang/expr`，套件用既有 `tools/cdp-bench`）。

## 5. 架构

```
POST /v1/chat/completions (OpenAI chat relay only)
  ─► ApplyRequirement (middleware/distributor.go)
       │  gate: route=true + 已注册族 + in-scope path/format + 无显式 ChannelPin
       │  pkg/fitpolicy.Current().Decide(req) → FitRequirement（immutable，写 gin context）
       │    ├─ snapshot: 规则 vN（expr 程序，原子替换，显式 reload hook）
       │    ├─ marks: channel_fit_capabilities（official_fit_models 仍作官方行为判定）
       │    └─ shadow: 只记录不生效
  ─► selector 两阶段收窄（见 §8）：base∩official∩marked → base∩official → 现有 nil/error
HTTP retry (controller/relay.go:getChannel)
  ─► 从同一 gin context 取 FitRequirement，排除已尝试渠道后重复同一两阶段收窄
       └─ 无候选 → 按原有错误/重试语义，不制造新 4xx/5xx
out of scope (WS/任务/显式 pin/其他协议) ─► 完全不走 fitpolicy，保持现状
```

- **真实接线点（逐个符号，不假设现有 filter loop 覆盖 retry）**：`middleware/distributor.go` 的 direct / affinity / preferred 分支；`service.SelectChannelForRequest` 与其 pin/affinity 前置；`service.SelectRandomChannelForRequest` 的 filter 循环；`service.CacheGetRandomSatisfiedChannel`（**当前只传 `v4OfficialPin`/`videoRequestOnly` bool，必须扩展为接收 FitRequirement，否则重试路径不会被约束**）；内存 `model.GetRandomSatisfiedChannelPinned`；DB `model.GetChannelWithBlockedChannelsPinned`；ability 回退 `model.GetAbilities`；`controller/relay.go:getChannel` 的 retry / affinity / preferred 三处。
- **两套 affinity 快径的裁决（已定，不再开放）**：
  - `middleware/distributor.go` 的 affinity/preferred 分支**自己选路**（`channel = preferred`，不经过 `CacheGetRandomSatisfiedChannel`），会绕过 selector 内的两阶段收窄 → **必须纳入** fit eligibility（在该分支同样执行 `base∩official∩marked → base∩official → 现有错误`）。这是唯一会引入未受约束渠道的 fast path。
  - `controller/relay.go:getChannel` 的 affinity/preferred 分支只在 `retryParam.GetRetry()==0` 且无 preferred 时**复用 distributor 已选中的渠道**（`RetryParam.ContextKeyChannelId`/已选 channel），重试时因 `GetRetry()!=0` 直接跳过 → 它不引入新渠道。**v1 不改该分支**，但必须加回归测试证明：该分支只能返回 distributor 已做 fit 判定的同一渠道，任何情况下都不会把未满足 `RequiredMarks` 的渠道带进来；若测试证伪，则升级为必改项。
  - 真正的重试缺口是 `getChannel` 落空时直达 `service.CacheGetRandomSatisfiedChannel`，而该函数当前不读 `ChannelConstraints.Filters` → 已要求把它扩展为 fit-aware（§5、§13）。

- **请求级约束**：新增独立 `FitRequirement`（family/model、required marks、policy version、fallback 标志、empty reason），immutable，挂 gin context；`RetryParam` 只持有指向它的引用，禁止在 retry 中重算。
- **显式 pin 短路**：ResolvedPin(token/origin-task) 与 token-specific 分支在评估 FitRequirement **之前**完成，保持现有 filter/policy/error/retry；只在非显式 pin、in-scope HTTP chat 请求上评估。
- **显式启动注册**：`pkg/fitpolicy` 不依赖 `init()` 自动注册；由 `main.go` 在初始 options/channel cache 完成后显式调用一次 `fitpolicy.RegisterReloadHook()`，注册函数用 `sync.Once` 防重复，测试验证初始化顺序和单次注册。
- **兜底链**：未配置、`enabled=false`、`shadow=true`、`route=false`、非 in-scope 路径、规则缺失、编译/求值失败或能力索引不可用 → 不附加约束，完全走今天的实现；显式 pin 与 out-of-scope 路径一律不受影响。

> **请求内一致性约束**：首次选路把 `FitRequirement`（含 policy version、RequiredMarks、fallback 标志）写入 gin context；`RetryParam`、`DecideRelayRetry` 和后续 selector 从同一 context 读取；已尝试渠道/排除集合保持 mutable 且独立于快照。禁止在错误处理路径按当前热配置重新推导。

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

- **存储选择的理由**：用户要求“多在渠道中加标记”，标记的语义归属仍是渠道；但因为现有 `channel.settings` 是整行 JSON 更新，本设计新增独立 `channel_fit_capabilities` 表（每行 `channel_id + family/model + behavior` 唯一），并**新增**一个随 `InitChannelCacheAndNotify()` 刷新的能力索引（该索引当前不存在，属步 B 交付物）。`official_fit_models` 继续从渠道行读取，作为官方行为判定的粗粒度来源。
- **最小物理字段**：`channel_id`、`family`、`model`、`behavior`、`supported`、`source`、`suite`、`cases`、`rounds`、`at`、`expires_at`、`policy_version`、`policy_hash`、`baseline_hash`、`report_id`、`run_id`、`force`、`revision`、`updated_at`；数据库唯一键保证同一渠道/模型/行为只有一条当前记录，历史审计另写 `AuditLog`/受控审计表，不把完整 body 或凭据落库。
- **数据库门禁**：新增表/索引必须同时支持 SQLite、MySQL ≥5.7.8、PostgreSQL ≥9.6；fresh DB、上一版本升级和二次启动迁移都要验证，SQLite 不使用不兼容的 `ALTER COLUMN`。字段长度/类型/索引/唯一键在实现前冻结。
- **CAS 必须用条件 UPDATE，不是行锁**：`lockForUpdate` 在 SQLite 跳过 `FOR UPDATE`（`model/locking.go`），且 `channel` 行**没有** revision/UpdatedAt 字段（现有多键 credential revision 不覆盖它）。三库统一做法是：能力表自带 `revision`，`expected_revision` 必填；`UPDATE ... SET revision=revision+1, ... WHERE channel_id=? AND family=? AND model=? AND behavior=? AND revision=?` 并检查 `RowsAffected==1`；为 0 即冲突，返回 409。首次写入先按唯一键 INSERT，并处理并发 INSERT 的唯一键冲突（冲突后重读并返回 409/重试）。不得把“事务内 `SELECT ... FOR UPDATE`”或复用 credential revision 当成方案。
- **能力表当前不存在**：仓库目前没有 capability model、migration、cache index 或 authz action；`InitChannelCacheAndNotify()` 只做 `InitChannelCache()+epoch`（`model/config_epoch.go`），现有 cache 只读 Channel/Ability、不读新表。这些全部是**步 B 的实现前置**，不是既有能力。
- **删除/新建/更新全覆盖**：现有 `Channel.Insert`、`Channel.Update`、`Channel.Delete`、`BatchDeleteChannels`（`model/channel.go`）只处理 Channel/Ability 与凭据，不碰新表；步 B 必须决定外键级联或显式按 `channel_id` 清理，并在 create/update/delete/batch-delete 四条路径都有清理与测试，且 update 不得因整行保存而重置 capability revision。
- **迁移只在 master 执行**：`migrateDB` 仅在 `common.IsMasterNode` 时运行（`model/main.go`），且当前 AutoMigrate 列表里没有 capability model → 步 B 必须把它注册进迁移路径，并验证非 master 节点不建表/不报错。
- **审计跨库不是原子的**：`AuditLog` 写 `LOG_DB`（可配置为独立日志库/ClickHouse，`model/audit_log.go`），与主库的能力表不在同一事务；不得声称“标记写入与审计原子”。做法是主库先提交能力变更，再 best-effort 写审计，失败可观测、可重放，不能让审计失败回滚业务写入。
- **写入授权与并发**：专用 endpoint 区分 `capability.write` 与 `capability.force`；suite 通过独立受限 service identity/受控 applier 调用，不能持有通用 AdminAuth。**当前不存在受限 applier principal**：`RequirePermission` 只认 dashboard 用户/PAT，authz 只有 read/operate/write/sensitive_write/secret_view，channel 路由一律先 `AdminAuth`；generic `ChannelWrite` 还允许改任意非敏感字段，不能代替 capability action。步 B 必须在“dashboard 用户 + 专用 action”“HMAC/service principal”或“专用 Casbin subject”中选定一种并给出真实鉴权路径与测试。请求携带**必填** `expected_revision`、`report_id/run_id`，冲突不覆盖。

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

## 8. 决策流（route 触发，两阶段收窄在 selector 内完成）

**决策（产生 FitRequirement，immutable，写 gin context）**

```
func Decide(req) FitRequirement:
    cfg := snapshot(); if !cfg.Enabled || cfg.Shadow: shadowRecord(); return NoOpinion
    if req.Path != exact POST /v1/chat/completions || req.RelayFormat != OpenAI chat: return NoOpinion
    if hasExplicitChannelPin(req): return NoOpinion          // token/origin-task 短路，永不评估
    profile := req.UserSetting.OfficialFitProfileFor(req.Model)
    if !profile.Route: return NoOpinion
    fam := strategyFamily(req.Model)                          // 只引用 mechanism registry
    if fam == nil: return NoOpinion
    feats := evalFeatureExprs(req, fam)                       // expr，输入有大小/深度上限
    reqMarks := union(rule.require for rule in fam.rules if rule.when(feats))
    if len(reqMarks) == 0: return NoOpinion                   // 既有 route predicate 未命中
    return FitRequirement{RequiredMarks: reqMarks, PolicyVersion: cfg.version, Family: fam}
```

**收窄（selector-owned，每次尝试都执行同一两阶段）**

```
func narrow(candidates, fit) []Channel:
    official := base ∩ (official_fit_models ∪ 官方渠道类型)
    if fit == NoOpinion: return existingOfficialPinBehavior(candidates, official)
    marked := official ∩ supportsAll(marks, fit.RequiredMarks, conservative)
    if len(marked) > 0: return marked                          // 阶段 1：marked 优先
    return official                                            // 阶段 2：今天硬 pin 的等价结果
    // 阶段 3：official 为空 → 沿用现有 nil / selection-error 分类，不落普通池
```

- **候选构造（selector 职责，不含并发饱和）**：`base` = group/model 候选经 request filter、model alias/path、group policy、blocked channels、compact alias 解析后的结果；这就是 `service.CacheGetRandomSatisfiedChannel` 与 `model.GetRandomSatisfiedChannelPinned`/`GetChannelWithBlockedChannelsPinned` 现在负责的部分。
- **attempt admission（不是 selector 职责）**：并发饱和在 `controller/relay.go` 的 `AcquireChannelConcurrency` → `ExcludeSaturatedChannel`、以及 Responses WS 的对应分支处理；`narrow` 只接受 blocked/excluded 输入，不自己探测饱和。文档与实现都不得把饱和写成候选构造的一部分。
- **显式 pin 短路**：`ResolvedPin()`（token/origin-task）与 token-specific 固定渠道分支在 `narrow` **之前**返回，不附加、不评估 FitRequirement，保留现有 filter/policy/error/retry。fit 的 `pinOfficial` 候选收窄不是显式 `ChannelPin`。
- **重试状态机**：`RetryParam` 从同一 gin context 取 FitRequirement 引用；每次尝试先从约束集合排除已尝试渠道，再跑同一 `narrow`，仍有候选才 retry。耗尽时执行既有错误语义；fitpolicy 不生成新的 4xx/5xx。
- **重试判定（必须改代码，不是只改候选集）**：现有 `officialFitPinKeepsVerdict`（`service/relay_error.go`）在 pin 命中自动重试关键字时直接 `stop`，只放行多键凭据轮换；因此 D5 不能靠候选过滤修。实现必须扩展/替换该函数，让它消费 immutable `FitRequirement`，并且**仅在约束候选集里仍有未尝试的 marked official 渠道时**放行 retry；约束集耗尽（或没有 second candidate）时保持 `stop`。测试必须覆盖：存在第二个 marked official 渠道 → retry；约束集耗尽 → 仍 stop；多键轮换例外不被破坏。
- **`ApiError` 不是 409**：现有 `common.ApiError`/`ApiErrorI18n` 一律返回 HTTP 200（`common/gin.go`）；capability endpoint 的 CAS 冲突必须显式 `c.JSON(http.StatusConflict, ...)`，不得暗示复用现有错误 helper 就能得到 409。
- **in-scope contract tests**：HTTP 首选、`Distribute` affinity/preferred 选路（含 marked 命中和 official 回退）、归一化/compact model、auto-group、blocked/saturation、普通 retry、无 marked 时回 official、official 为空时保持原错误。**负面/边界测试**：`getChannel` 复用分支不得引入未受约束渠道；Responses WS、显式固定 pin、`/pg`、`/v1/completions`、`/v1/messages`、任务/任务插件、realtime WS 必须证明 fitpolicy 不介入且行为与今天一致。


## 9. 即时生效（复用既有链路，但必须补两个缺口）

| 变更 | 传播路径 | 生效时延 |
|---|---|---|
| 规则集（options） | **提交前** schema/ref/type 编译校验 → `model.UpdateOption(s)` 构造并安装 fitpolicy 快照 → DB transaction → `NotifyConfigChanged()` → Redis `config_epoch:v1` INCR → 各节点 watcher → `applyConfigReload` → fitpolicy hook 重建快照 | 健康 Redis、N 节点 P99 ≤2s |
| 渠道标记（独立表） | 专用 capability endpoint 的条件 UPDATE/CAS → 刷新能力索引 → `InitChannelCacheAndNotify()` → 同上 | 健康 Redis、N 节点 P99 ≤2s |
| Redis 不可用 | epoch 读不到 → 只能靠 `SYNC_FREQUENCY` 周期同步 | 数十秒且**不保证跟进**（见下） |

- **缺口 1：提交前编译必须真的接进 `UpdateOption(s)`**。当前 `validateOptionValue` 只有既有 key 分支、`UpdateOption(s)` 也没有 fitpolicy 快照；实现必须让 `official_fit.policy` 的 schema/引用/expr 编译失败在 DB transaction **之前**返回错误，禁止先落库再异步发现。
- **缺口 2：周期同步路径不执行 reload hook**。`model.SyncOptions` → `loadOptionsFromDatabase`（`model/option.go`）目前只更新 raw OptionMap 与 request policy 快照，**不运行** `RegisterConfigReloadHook` 注册的 hook；hook 只在 epoch watcher 的 `applyConfigReload`（`model/config_epoch.go`）里跑。因此 Redis 不可用时，其他节点不会安装新的 fitpolicy 快照。实现必须二选一：把 fitpolicy 的安装并入 `loadOptionsFromDatabase` 的既有快照路径，或让周期同步也调用同一个 `applyConfigReload`。否则不得宣称“Redis 故障仅变慢、正确性不变”。
- **Redis 故障下的真实语义**：节点保持**当前已安装的 last-known-good 快照**继续服务（不会退回 legacy，也不是“无影响”）——策略变更与**紧急回滚**都可能延迟到同步周期甚至更久。这是已知残余风险，必须进 §15 Risks 与巡检指标，不能写成“正确性不受影响”。
- **`applyConfigReload` 的 hook 失败语义（现状，不是保证）**：现有实现对 hook failure 只记日志、**不重试**，并仍然推进 epoch；`RegisterConfigReloadHook` 是 append 注册、没有去重（现有测试甚至故意注册多个 hook）。因此文档只承诺：fitpolicy hook 自己在**编译成功后才原子替换**快照，失败保留 LKG 并暴露失败指标；**不声称** `config_epoch` 会为重试失败 hook 或保证单次注册。
- **last-known-good 与重启**：快照版本与失败原因可观测；节点启动时若 DB 中的 policy 非法/不可编译，应安装“无意见”而非崩溃，并把上一次成功快照版本记录清楚。
- **不新增传播机制**：仍复用 `config_epoch` 与显式 `RegisterReloadHook()`；必须验证初始化顺序、单次注册和慢 reload 行为。


## 10. 护栏（吸收 2026-09-26 夜间三次误报的教训）

1. 写入即 schema + 引用完整性校验（`require` 引用的行为必须在 `behaviors` 声明；族必须存在）。
2. 任何规则/标记变更先 **shadow**（§11）再放行；shadow 结论入 `logs.other.fitpolicy`，可按规则 id 聚合"若启用影响多少请求"。
3. 一键回滚：`official_fit.policy.enabled=false`（秒级回旧行为）或回滚到指定 `version`。
4. 判定纪律（夜间教训）：**慢≠死**（无完成≠池全灭）、**请求侧≠渠道侧**（渠道文案默认可疑，先对齐官方基准）、**未知标记语义显式**（默认 conservative）。
5. 标记新鲜度：`stale` 标记 + 6h 校准 + 人工项过期；`force` 覆盖留审计。
6. 观测：Trace 只写规则 id、policy/mark revision、requiredMarks 哈希、候选/选择渠道 id、状态和 shadow 结果；请求 body、messages、tools、token、完整响应和凭据禁止入日志。按采样率记录并设保留期；巡检脚本增加“收窄集为空比率”、候选绕过率和 FitRequirement 缺失率指标。

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
  - `scripts/fork-invariants/manifest.json` 新增 entry，锚点至少覆盖：`kind:file`（`pkg/fitpolicy/*.go`）、`kind:symbol`（`fitpolicy.Current` / `Decide` / `CompileRules` / `ApplyRequirement`）、显式启动注册、精确路径 gate、每个 in-scope selector consumer、`UpdateOption(s)` precommit、周期安装路径、能力表/endpoint 服务和测试名（`TestFitPolicyHookWired` / `TestFitPolicyFallbackToLegacy` / `TestFitPolicyMarksConflict` / `TestFitPolicyOutOfScopeNoop`）。
  - 门禁：`node scripts/fork-invariants/main.mjs --check manifest`，每次上游 sync 后必跑（该门禁正因 rc35 丢 5 项、rc39 丢 video-token 块而立）。
- **语义门禁**：除 file/symbol 锚点外，必须有 contract tests 验证 active policy 确实改变已知 fixture 的候选集、shadow 写 trace、reload 后版本变化、retry 消费 `RequiredMarks`、out-of-scope 路径 no-op，并验证显式注册只发生一次。merge CI 对真实 `upstream/main` 做 clean/conflict rehearsal 后运行这些测试；仅保留注释或符号不算通过。
- **定位**：`grep -rn "tokeness-fitpolicy"` 仅用于人工导航，不是完整语义证明。
- **数据在 DB**：规则/标记数据不随上游 merge，但 in-scope selector consumers、精确路径 gate、`UpdateOption(s)` precommit、周期安装路径、专用写回接口和注册表适配器仍需纳入 fork inventory。

## 13. 迁移计划（风险递增，每步可停）

| 步 | 内容 | 验收 |
|---|---|---|
| P0（已上线） | 四维 profile + 形状 pin + `official_fit_models` 白名单 + 亲和不对称 | `official-fit-mode.md` §验收 |
| **A（前置）** | 冻结精确入口与非目标；为已注册族建立 route predicate adapter、`FitRequirement`（gin context）+ `RetryParam` 引用、`CacheGetRandomSatisfiedChannel` 的 fit-aware 接口、两阶段 `narrow`、`Distribute` affinity 分支的 fit eligibility；`shadow=true` 跑 ≥48h，旧 Go predicate 与新策略逐例对照 | 精确 `POST /v1/chat/completions` 的首选/`Distribute` affinity/随机/auto-group/normalized model/普通 retry 全覆盖；`getChannel` 复用分支有“不引入未受约束渠道”回归；`/pg`、`/v1/completions`、`/v1/messages`、WS、任务、显式 pin 保持现状（negative tests）；无上下文绕过；差异为零或逐条批准 |
| **B** | 建 `channel_fit_capabilities` model/migration（含删除级联或显式清理）/cache 索引/条件 UPDATE CAS/专用 endpoint/审计/状态机（当前全部不存在）；接入 suite 报告校验与 6h 校准 | SQLite/MySQL/PostgreSQL fresh+upgrade+二次启动；CAS 409/`RowsAffected`/幂等；`expected_revision` 必填；权限与受限 suite principal 落到真实鉴权路径；删除渠道无孤儿行；旧人工项不被覆盖；审计跨库失败可观测且不回滚业务写入 |
| **B2** | 把 fitpolicy 安装接入 `UpdateOption(s)` 提交前路径与周期同步路径（§9 缺口 1/2） | 非法 policy 在 DB 提交前被拒；Redis 故障下节点按周期安装/保持 last-known-good，并可实测 |
| **C** | 先以 kimi-k3 单族启用；旧 `official_fit_models` 与官方类型继续作为硬 pin 基线；仅在 healthy Redis SLO 下逐步放量 | 24h 内拟合违约 0、可避免失败 0；故障注入时保持旧快照且不制造新错误；无标记候选时绝不落到普通聚合池 |
| **D** | DS-V4 / GLM-5.3 迁入；明确热策略与代码 mechanism registry 边界；新增族仍需代码 registry/consumer 变更 | 逐族 shadow 等价；registry adapter、wire shape、channel type、exact model consumer 全覆盖；非目标协议无行为变化 |
| **E** | §16 契约面数据化，顺序 = **F1 errors → F2 shape-A → F3 validate → F4 B 档 → F5 具名契约**；F1/F2 必须先完成语料前置 | 每阶段按对应等价标准、golden fixtures、脱敏回放/账务门禁通过；不以未定义的“全部逐字节”作为默认承诺 |

## 14. 验收

**功能与等价性**
- deterministic CI：
  - 规则 schema/ref/type/expr 编译在 `UpdateOption(s)` 的 DB transaction **之前**拒绝；非法写入不影响线上快照。
  - 显式注册只执行一次、初始化顺序正确；`enabled=false`/删除配置/快照缺失 → 无意见。
  - 两阶段收窄：marked 优先；无 marked 回 official 等价结果；official 为空保持现有 nil/error 分类，永不落普通池。
  - 能力表 CAS：条件 UPDATE 的 `RowsAffected` 语义、409 冲突、`expected_revision` 必填、`report_id` 幂等、人工 sticky 不被 suite 覆盖；三库（SQLite/MySQL/PostgreSQL）fresh + upgrade + 二次启动。
  - 删除渠道（单条/批量）后能力行无孤儿；`AuditLog` 写独立日志库时能力写入仍提交、审计失败可观测且不被误报为业务失败。
  - **negative contract tests（必须）**：`/pg/chat/completions`、`/v1/completions`、`/v1/messages`、`/v1/responses/compact`、Gemini/embedding/audio/rerank、realtime WS、Responses WebSocket、显式 `ChannelPin`、token-specific 固定渠道 → 证明 fitpolicy 不介入且行为与今天一致。
- 规则/标记写入后：在明确的 N 节点、Redis healthy、节点健康条件下传播 P99 ≤2s；Redis 故障时节点保持当前已安装的 last-known-good 快照（策略变更与紧急回滚都可能延迟），必须实测并记录，不把 ≤2s 当作无条件承诺。
- `enabled=false` 或删除 `official_fit.policy` 与 `channel_fit_capabilities` 后，行为与未安装本层一致，不产生新 4xx/5xx；`official_fit_models` 仍按既有 route 逻辑工作。
- 求值异常/编译失败 → 保留 last-known-good → 旧行为；hook 失败不得推进到半编译状态。
- shadow 日志只含脱敏元数据、哈希和采样 trace，且有保留期限；不记录 body/凭据。
- 卸载时删除的是 `channel_fit_capabilities` 表中当前标记与索引；`official_fit_capabilities` 若作为只读投影存在，不作为独立写入数据源。
- 标记状态机（人工 sticky/过期、suite stale/failed/恢复、policy/baseline 变化）均有测试。
- F1/F2 的准入前置：必须有短期双写/哈希差异捕获或经批准的 semantic baseline；报告样本量、fixture、差异分类和产物路径，缺失语料不得放行。
- F4（usage/计费）另需 billing.md 全链路证据：validation → EstimateBilling/OtherRatios → `Quota*Checked` 转换 → pre-consume → settle/refund，含溢出/饱和/NaN 与前后账单差异。

**CDP 双轨（既有的唯一验收口径）**
- A 轨：`tools/cdp-bench` 严格跑通过（全部执行用例 PASS、无未声明跳过、SEVERE/WORDING/SHAPE/PROTOCOL/INTEGRITY 全零）；基线对比是门禁的一部分。
- B 轨：KV cache ≥90%、有效成功率 ≥99%（HTTP 200 空内容算失败）、TTFT P90 <30s、TPOT P90 <40ms、TPM 满足所选 profile。

**工程与 fork**
- `go test ./pkg/fitpolicy/...`、selector/constraint/capability endpoint 聚焦测试、根目录全量 `go test ./...`、`go vet ./...`、`git diff --check`、web `bun run typecheck`。
- 涉及 `relaykit/` DTO 时另跑 `cd relaykit && GOWORK=off go build ./...`；新增 capability 表/事务时必须跑真实 SQLite、MySQL 和 PostgreSQL fresh/upgrade/idempotency 矩阵，并记录版本/命令/结果。**当前仓库没有可援引的三库证据**：现有迁移测试主要是 synthetic/DSN-optional（例如 migration dialector 测试在缺 DSN 时跳过），所以这是**步 B 必须现场产出的 gate**，不能写成“已满足”。
- `node scripts/fork-invariants/main.mjs --check manifest` 通过；对 `upstream/main` 做一次真实 merge rehearsal 后门禁和 contract tests 仍绿。
- 决策延迟：先以编译期表达式复杂度/输入大小上限约束，再用固定 fixture benchmark 验收 P50/P95/P99；2ms 是目标而非可依赖的强制中断机制，超预算按失败安全路径处理并有测试证据。

## 15. Risks

- **标记即信任，写错=静默违约**（既有已知限制）：缓解 = 套件写回 + 6h 校准 + stale 提示 + shadow 先行 + 人工项过期。
- **数据即代码**：规则错误即时影响线上 → schema 校验 + shadow + 版本回滚 + 按族灰度 + 审计。
- **表达式沙箱**：env 只读、无 I/O、无时间函数、函数白名单；以编译期复杂度和输入上限控制资源，固定 fixture benchmark 验证延迟，超预算按失败安全路径处理，不依赖无法可靠中断的运行时 CPU timeout。
- **未知标记歧义**：v1 conservative 可能让更多流量落官方集（成本上升）；用 shadow 数据评估后再决定是否对特定行为放开 permissive。
- **钩子与上游冲突**：路由决策有 2 处主要 fork hook，但 selector 入口、专用写回接口和 registry adapter 也属于存活面；所有入口由调用图、fork inventory 和 contract tests 覆盖。上游重写 hook 所在函数时按 `FORK-CHANGES.md` 重挂。
- **等价迁移风险**：谓词从 Go 搬到 expr 若有偏差，会改变 pin 判定 → A 步必须逐例一致，且 shadow 观察期内不得启用。
- **依赖既有机制（已修正）**：`config_epoch` 需 Redis；不可用时节点只会保持**当前已安装的 last-known-good 快照**，策略变更与紧急回滚都可能延迟到同步周期甚至更久（`SyncOptions` 当前不跑 reload hook）。这不是“仅传播变慢”，必须作为残余风险监控，并在步 A/B 补上周期安装路径。
- **显式 pin 与 out-of-scope 路径的回归风险**：`FitRequirement` 若被放进通用 selector 而不做 transport/path/显式 pin 判定，会改变 `/pg`、`/v1/completions`、`/v1/messages`、WS、任务和固定渠道的错误与重试行为 → 必须用 negative contract tests 锁住“不介入”。
- **SQLite CAS**：`lockForUpdate` 在 SQLite 不生效；能力表写入必须用条件 UPDATE + `RowsAffected` + 唯一键竞态处理，否则并发写回会静默覆盖。

## 16. 演进：把 validate / errors / shape / route 归一为契约面（F1–F5，即 §13 的步 E）

四维是同一份保真契约的四个面，可统一为 **需求侧（用户契约）× 供给侧（渠道标记）→ 决策**。边界必须如实：**策略可数据化，机制仍需注册表**。

| 维度 | 可数据化（热） | 必须留代码 |
|---|---|---|
| route | 谓词、候选过滤、空集口径（本文件主体） | 提取器函数 |
| validate | 规则表：字段/比较/官方文案模板、族开关 | 语义检查（tool 链可解析性、tokenization 失败识别） |
| errors | 文案策略：是否附 request id、按来源/错误类分流 | 错误提取与拼装点 |
| shape | 变换选择：哪些变换对哪族/哪渠道生效 | 变换器本体（SSE 拼接、usage 映射、字段剥离） |

**阶段（按 §16.1 的三档重排，风险递增）**：**F1 errors**（几乎纯 A 档：`error_text` / `media_type` / 是否附网关 request id 全部数据化；先做 **shadow 原型**，与 `IsStrictFitValidationMessage` + `controller/relay.go` 门控逐字节对照，差异必须为 0 或逐条列明批准）→ **F2 shape 的 A 档键操作**（键集与映射外提为数据，raw-byte 外壳留代码；**F2 禁止触碰 `usage` 的任何键或数值**，否则必须升到 F4）→ **F3 validate 规则表**（Go 校验器降级为执行器，旧表保留一键回退；tool 链可解析性等语义检查留代码）→ **F4 B 档 usage/计费**（见下方完整门禁）→ **F5 具名契约**（四布尔 → `fit_contract: "official-k3-v3"`，旧数据零迁移）；**C 档（跨消息状态与时序）永不数据化**，只登记为命名变换器。准入标准按操作类型分别采用有限字节级 golden 或 canonical semantic JSON；差异逐条列明，基线用 CDP 双轨与脱敏回放。四维属客户可感知契约，比路由更危险：路由层与契约层开关必须互相独立。

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

**F4（B 档 usage/计费）的强制门禁**（`.agents/rules/billing.md` 是硬要求，不是提示）：

- 触及 `usage` 数值/键的规则**必须**按 billing.md 追踪完整链路：请求校验 → `EstimateBilling`/`OtherRatios` → quota 转换（只用 `common.Quota*`/`*Checked`，禁止裸 `int(...)` 转换）→ 预扣费 → 结算/退款；并覆盖饱和/溢出/NaN 与 `relayInfo.QuotaClamp` 审计。
- `affects_billing: true` 必须是**服务端准入校验**：缺该标记却引用 usage 数值的规则在写入时被拒，而不是只在报告里声明。
- 验收证据必须包含真实计费回归（pre-consume/settle/refund/overflow/saturation）与前后账单差异，不接受“形状看起来一致”。
- 动态/阶梯计费表达式另读 `pkg/billingexpr/expr.md`，并遵守其归一化与配额规则。


## 17. Closed Decisions and Remaining Delivery Gates

**已关闭（不再讨论，改动需重新评审）**

| # | 决定 |
|---|---|
| 1 | 标记存储 = 独立物理表 `channel_fit_capabilities`；`official_fit_models` 保留为官方行为判定来源；不写进通用 settings patch |
| 2 | v1 只覆盖精确 `POST /v1/chat/completions` + OpenAI chat relay；`/pg`、`/v1/completions`、`/v1/messages`、`/v1/responses/compact`、Gemini/embedding/audio/rerank、realtime WS、任务插件一律 no-op |
| 3 | Responses WebSocket 不在 v1 范围，只做“不介入”非回归测试 |
| 4 | 显式 `ChannelPin`（token/origin-task）与 token-specific 固定渠道短路 FitRequirement，保持现有 filter/policy/error/retry |
| 5 | 空集策略 = `legacy_hard_pin_then_existing_error`：marked 优先 → official 等价结果 → 现有 nil/error；永不落普通聚合池 |
| 6 | `unknown_mark_policy = conservative`（v1 固定） |
| 7 | 机制注册表边界：family id / channel type / wire shape / exact model names 仍是代码事实；新增族必须改代码 |
| 8 | 显式启动注册（`main.go`）+ `sync.Once`，不依赖 `init()` 自注册 |

**必须在开工前冻结的交付门禁**

1. **能力表落地细节**：model/字段类型/长度/索引/唯一键（含 `revision`）、migration（fresh + upgrade + 二次启动幂等，三库实测**并记录真实版本/命令/结果**）、注册进 `migrateDB`/master 迁移路径、cache 索引重建、条件 UPDATE 形式与显式 409、`expected_revision` 必填、create/update/delete/batch-delete 四条路径的级联或显式清理、审计字段与跨库失败语义；当前仓库完全没有这些，也没有可援引的三库证据。
2. **权限与 suite 身份**：必须选定 dashboard 用户 + 专用 action、HMAC/service principal 或专用 Casbin subject 之一，并给出真实鉴权路径与测试；现有 `ChannelWrite` 粒度过宽且不能代替 capability action，当前也没有受限 applier principal。
3. **提交前编译 + 周期安装路径**：`UpdateOption(s)` 的 precommit 编译接线，以及 Redis 故障下 `SyncOptions` 如何安装 fitpolicy（§9 缺口 1/2）。
4. **F1/F2 语料**（未满足即不得开工）：短期双写仅落哈希/差异且脱敏，或批准 semantic baseline + golden fixtures；CDP 官方语料不能替代旧网关输出语料。
5. **热生效 SLO 测量**：N 节点、Redis healthy/fault、慢 reload、重启恢复的测量方案与产物路径；≤2s 只在健康条件成立。
6. **日志与隐私**：shadow/审计字段、采样率、哈希方式、保留期；禁止 body/tools/token/完整响应/凭据。
