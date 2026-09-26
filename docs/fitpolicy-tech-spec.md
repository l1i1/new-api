# Fit Policy（Phase 3）：行为级渠道标记 × 热可编辑拟合规则

> English abstract: Phase 3 of the official-fit line. Today's channel whitelist
> (`channel.settings.official_fit_models`) is a binary, per-model, human-trust
> mark with no provenance, no expiry and no automatic calibration, and the
> per-family pin predicates live in Go. This spec upgrades the mark to a
> per-behaviour, suite-measured, human-editable capability record, and moves the
> family predicates and pin triggers into hot-reloadable data (expr-lang
> expressions, no rebuild, no restart, ~2s fleet-wide via the existing config
> epoch). The pin/route model itself, the four user profile dimensions, and the
> shipped P0 behaviour are unchanged. The whole layer is one fork-only package
> plus two tagged hooks, registered in `FORK-CHANGES.md` and the
> `scripts/fork-invariants` gate so an upstream sync cannot drop it silently.

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
- 改拟合行为 = 改数据（options / 渠道行），**不编译、不重启**，全节点 ~2s 生效；
- 规则与标记层**可整层拆卸**，上游 merge **不可能悄悄丢**（门禁锚点）。

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
| C2 | 可拆卸、高灵活、尽量可编辑 | 单包 + ≤2 钩子 + 纯数据规则（§5、§11）；删配置即卸载 |
| C3 | 通用（族无关）、改拟合即时生效不编译不重启 | 族/谓词/策略全数据；expr 热编译 + 既有 config epoch（§6、§9） |
| C4 | 标记由一致性套件实测写回、可人工编辑 | 套件写回协议（§7）+ sticky/expiry/force + 审计 |
| C5 | 合并上游不能丢，要独立可见 | fork 独有包 + 标记钩子 + `fork-invariants` 锚点（§12） |

## 4. 范围 / 非目标

**范围**：`/v1/chat/completions` 文本中继的「选路前候选决策」与「重试判定」两个决策点；DS-V4 / kimi-k3 / GLM-5.3 及后续家族的规则数据化；标记的实测写回与人工编辑。

**非目标（v1）**
- 不改 `route` / `validate` / `errors` / `shape` 的**语义**（四维归一化见 §16，属后续阶段）；
- 不重做已上线的 pin 形状谓词，只把它们**搬家**（代码 → 等价数据，行为必须逐例一致，见 §13）；
- 不恢复任何响应级拒绝门（2026-09-11 已因误伤删除）；
- 不做控制台可视化编辑器（v1 用现有 API 直写）；
- 不引入新依赖（表达式用已在依赖里的 `expr-lang/expr`，套件用既有 `tools/cdp-bench`）。

## 5. 架构

```
请求 ─► hook#1 (middleware/distributor.go, 选路前)
          │  pkg/fitpolicy.Current().Decide(req) → Decision
          │    ├─ snapshot: 规则 vN（expr 程序，原子替换，自注册 reload hook）
          │    ├─ marks: 渠道 official_fit_capabilities（+ 兼容 official_fit_models）
          │    └─ shadow: 只记录不生效
错误 ─► hook#2 (service/relay_error.go, 重试判定前)
          │  读取本次请求的 DecisionContext，判断是否还有未尝试且标记满足必需行为的渠道？
          └─ 有 → 允许换渠道；无 → 按原有错误/重试语义，不制造新 4xx/5xx
```

- **接入面不是“两处就够”**：两个 fork hook 只负责产生/消费 fitpolicy 决策，Decision 必须成为请求级不可变的 `ChannelConstraints`，由所有实际选路入口统一消费：HTTP 首次选路、preferred/affinity 快径、auto-group/request-filter/blocked/saturation 过滤后的再次选择、每次 retry，以及 Responses WebSocket 独立选路。实现前必须产出调用图和入口清单；任何无法覆盖的入口必须从 v1 范围明确移除，且不得宣称全局生效。
- **请求内一致性**：hook#1 写入 `RequiredMarks`、policy snapshot/version、候选构造阶段、已尝试渠道和降级等级；hook#2 及后续 selector 只能消费同一上下文，不得按当前热配置重新推导。
- **reload 自注册**：`pkg/fitpolicy` 在自己的 `init()` 里注册 reload hook，但必须由实际启动包显式 import 保活，并测试只注册一次及初始化顺序；不能以“无需改 main.go”替代验证。
- **兜底链**：未配置 / `official_fit.policy` 缺失 / 编译失败 / 求值失败 → `HasOpinion=false` → 完全走今天的实现（pin 由 `route` + `official_fit_models` 决定），永不 500。

> **请求内一致性约束**：hook#1 必须把 `RequiredMarks`、候选快照版本、已尝试渠道集合和降级等级写入本次 relay context；hook#2 只能消费该上下文。重试时沿用同一上下文并追加已尝试渠道，禁止在错误处理路径按当前请求重新计算规则，否则配置热更新会在同一请求内改变语义。

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
      "empty_match_policy": "official_set_then_fail_verbatim"
    }
  ]
}
```

- `match`：策略族归属；只允许匹配现有 mechanism registry 已注册的 family id。新增族仍需代码 registry 与消费者适配，不能仅新增 JSON。
- `rules[*].when`：expr 表达式，命中即表示"本请求需要 `require` 里的行为"（§6.3）。
- `behaviors[*].class`：`verdict`（判决类：宁失败不失真）/ `capability`（能力类：换渠道即可）/ `courtesy`（体感类：可降级并标注）。
- `unknown_mark_policy`：无标记的渠道如何处理——`conservative`（视为不支持，**标记当承诺用**）/ `permissive`。v1 默认 conservative；该默认值在 shadow 阶段冻结，未经单独验收不得改。
- `empty_match_policy`：收窄后为空时的口径（`official_set_then_fail_verbatim` / `fail_verbatim` / `fallthrough_priority`）。v1 只允许 `official_set_then_fail_verbatim`：官方行为集合也为空时必须回到既有优先级/错误语义，禁止 fitpolicy 自造 4xx/5xx；其他策略只能在有独立指标和回滚演练后启用。
- **未验证与过期标记**：`unknown`、`stale`、`supported:false` 必须是可区分状态；过期或撤销不能被当作 `supported:true`，也不能静默转成官方集合。
- 写时 schema 校验 + 引用完整性 + 版本单调递增 + 渠道行 CAS + 审计（谁、何时、变更摘要）。

### 6.2 渠道标记 `channel.settings.official_fit_capabilities`

**升级而非替换**：`official_fit_models` 保留（兼容 + 粗粒度），新增结构化字段表达"哪些行为已验证"：

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

- **存储选择的理由**：用户要求"多在渠道中加标记"，且 `official_fit_models` 本就住在渠道行；渠道写入路径已有 `InitChannelCacheAndNotify()`（重建渠道缓存 + bump epoch）。本项目仍选**渠道行**（语义归属正确、与既有字段同处），但不复用通用 `PUT /api/channel/` 作为写回契约：必须新增专用 capability DTO/endpoint，禁止普通 channel patch 写入该字段，避免整行覆盖和绕过 provenance 规则。
- **写入授权与并发**：专用接口区分 `capability.write` 与 `capability.force`；suite 使用独立 service identity，不能持有通用 AdminAuth。请求必须携带 `expected_revision`、`report_id/run_id`，服务端以 CAS 原子合并单个行为键；冲突返回 409 且不覆盖。`force` 仅 suite identity 或明确特权角色可用，人工项的覆盖、过期和恢复由服务端按状态机处理。
- **审计最小字段**：channel_id、family/model、behavior、before/after 摘要、source/by、suite/run/report id、force、expires_at、expected/new revision、结果和失败原因；禁止写入请求 body、token、完整响应或凭据。
- **冲突语义（C4）**：实测为默认来源；人工写入即 sticky，套件覆盖人工必须显式 `force`；人工项可带 `expires_at`，到期自动回退到最新实测值（防"人工遗毒"）。
- **新鲜度**：`source=suite` 且 `at` 早于 `stale_after_days`（规则集配置，默认 30 天）→ 控制台与采样日志标 `stale`，可选降级为 unknown（默认关）。
- **兼容**：`official_fit_models` 仍按现语义参与 pin 候选并集（`ChannelIsOfficialFitForModel`）；新增能力字段缺省时行为完全不变。
- **能力状态机**：每个 `family/model × behavior` 独立维护 `unknown`、`suite_fresh`、`suite_stale`、`suite_failed`、`manual_active`、`manual_expired`；`supported:false` 是明确的否定结果，不等同 unknown。`supportsAll` 只接受 `suite_fresh` 或未过期 `manual_active` 的 true；stale/failed/expired 一律不满足 conservative。套件报告必须绑定 `policy_version` 与规则 hash；规则或官方基线变化会使旧报告 stale，不能自动恢复。探针失败立即产生新的 suite_failed 结果；连续两轮通过只能恢复最近一次同版本 suite 结果，不能覆盖未过期人工 sticky 项。

### 6.3 表达式契约（`expr-lang/expr`，**已在 go.mod 依赖中**）

- env（只读、无 I/O、无时间函数）：`model`、`messages`、`tools`、`tool_choice`、`response_format`、`stream`、`max_tokens`、`temperature`、`top_p`、`n`、`logprobs`、`top_logprobs`、`thinking`、`reasoning_effort`、`user_agent`、以及 gjson 视图 `body`。实现必须限制 body 字节数、JSON 深度、数组/消息/工具数量和字符串长度；畸形 JSON、超限输入和未知字段按确定性的求值失败处理，不把原始 body 写入 trace。
- 注册函数（`pkg/fitpolicy/registry.go`，**fork 独有，唯一需要发版的扩展点**）：`thinkingDisabled()`、`reasoningEffortIs(s)`、`historyBeginsWithUserTurn()`、`messagesCarryDynamicTools()`、`responseFormatNotText()`、`toolChainResolvable()`、`promptTokenEstimate()`、`hasImagePart()`。**这些就是今天 `kimiK3RequestNeedsOfficial` / DS 三类谓词的原样搬迁**（先等价搬家，后改数据）。
- 编译：按 `version` 整集编译（形态照 `pkg/billingexpr/compile.go`：固定 env + 类型断言 bool）；编译失败 → **拒绝写入**。
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
- **写回采用两段式**：套件默认只产出带 `schema_version`、`report_id`、官方基线指纹、目标渠道/模型、行为结果和签名摘要的报告；独立的受控 applier 校验报告、权限、幂等键和当前渠道行版本后才写入。v1 不允许套件进程持有通用 AdminAuth，也不允许无条件覆盖整条渠道 settings。
- **并发与幂等**：写回必须使用 compare-and-swap（期望的渠道行版本/updated_at），冲突即拒绝并重新读取；同一 `report_id` 重放不得重复产生审计事件。人工编辑与 suite 写回都必须保留 before/after 摘要、操作者、原因、来源、`force`、`expires_at` 和关联报告。
- **持续校准**：cron 每 6h 轻探针（3-5 例）；任一失败 → 立即把对应行为标 `supported:false` + 告警；连续 2 轮通过 → 自动恢复（沿用 cost plan §1.2 的校准纪律与自动摘除/恢复）。自动恢复只能恢复到最近一次通过的 suite 结果，不能覆盖未过期人工 sticky 项。
- **人工编辑（C4）**：同一字段可由管理员/控制台直接改写；人工项 sticky，套件需 `force` 才覆盖；带 `expires_at` 的临时覆盖到期回落到最新实测值；每次写入留 provenance + 审计。报告签名/权限/版本校验失败时保持 last-known-good，不改变线上标记。

## 8. 决策流（pin 仍由 `route` 触发，标记只决定"谁有资格"）

```
func Decide(req) Decision:
    cfg := snapshot(); if !cfg.Enabled || cfg.Shadow: shadowRecord(); return NoOpinion
    fam := strategyFamily(req.model) // 热策略只引用不可热替换的 mechanism registry
    if fam == nil: return NoOpinion
    feats := evalFeatureExprs(req, fam)                     // expr，输入有大小/深度上限
    reqMarks := union(rule.require for rule in fam.rules if rule.when(feats))
    if len(reqMarks) == 0: return NoOpinion                 // 普通优先级池（默认路径）
    base := buildCandidates(group, model, filters, blocked, saturation, affinity)
    marked := [c for c in base if supportsAll(marks(c), reqMarks, fam.unknown_mark_policy)]
    if len(marked) == len(base): return Decision{Constraints: none}
    if len(marked) > 0: return Decision{Constraints: allowOnly(marked), RequiredMarks: reqMarks, Snapshot: cfg.version}
    official := [c for c in base if isOfficialBehaviorChannel(c, fam, model)]
    if len(official) > 0: return Decision{Constraints: allowOnly(official), RequiredMarks: reqMarks, Degrade: classPolicy(...), Snapshot: cfg.version}
    return Decision{Constraints: preserveLegacySelection, RequiredMarks: reqMarks, EmptyReason: "no eligible official behavior channel", Snapshot: cfg.version}
```

- **候选构造顺序必须固定**：先按 group/model/request filter 构造候选，再应用 blocked、saturation、affinity 的既有约束，形成 `base`；fitpolicy 只能在此集合内增加 allow-only 约束，不能重新引入被禁用或已排除渠道。`official` 指 `base` 中官方渠道类型与匹配 `official_fit_models` 的并集，不是全库查询结果。
- **重试状态机**：每次选择前消费同一 `DecisionContext`，先从约束集合排除已尝试渠道，再按既有 selector/priority 选择；仍有合格渠道才 retry。约束集合耗尽时执行既有错误语义；fitpolicy 不生成新的 4xx/5xx，也不把未标记渠道当作官方行为渠道。首选、归一化模型、affinity 命中、自动分组和 Responses WS 必须各有 contract test。
- **hook#2（重试判定）**：只检查上下文中排除已尝试渠道后的剩余约束，不重新推导 `RequiredMarks`；有则允许换渠道，无则按原有 stop/retry 结果返回（修 D5）。

## 9. 即时生效（复用既有链路，不新增传播代码）

| 变更 | 传播路径 | 生效时延 |
|---|---|---|
| 规则集（options） | DB 提交前 schema/ref/type 编译校验通过 → `model/option.go` 写入 → `NotifyConfigChanged()` → Redis `config_epoch:v1` INCR → 各节点 watcher → `applyConfigReload` → `RegisterConfigReloadHook` 重建快照 | 健康 Redis、N 节点 P99 ≤2s |
| 渠道标记（渠道行） | 专用 capability endpoint 的 CAS 原子合并 → `InitChannelCacheAndNotify()` → 同上 | 健康 Redis、N 节点 P99 ≤2s |
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
- **旧机制共存**：`official_fit_models` 与能力标记并存；`route` 与 `validate/errors/shape` 完全不动。规则集缺失时 `Decide` 返回无意见，行为 = 今天。
- **卸载**：删除 `official_fit.policy` 与 `official_fit_capabilities` → 完全回到今天；`pkg/fitpolicy/` 整目录可删。

## 12. Fork 存活与可见（C5）

- **侵入面 = 2 处上游 hook + fork 独有包**；“0 个其他上游文件改动”不成立，且所有实际 selector 入口必须纳入调用图/测试。reload 由包注册，但启动包必须显式 import 保活，不能仅依赖未使用的 init。
  ```go
  // tokeness-fitpolicy:begin  （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
  if d := fitpolicy.Current().Decide(c); d.HasOpinion() { applyDecision(c, d) }
  // tokeness-fitpolicy:end
  ```
- **登记**（实现时同批提交）：
  - `FORK-CHANGES.md` 新增一节；
  - `scripts/fork-invariants/manifest.json` 新增 entry，锚点四层：`kind:file`（`pkg/fitpolicy/*.go`）、`kind:symbol`（`fitpolicy.Current` / `Decide` / `CompileRules`）、`kind:symbol`（两处钩子所在上游函数）、测试名（`TestFitPolicyHookWired` / `TestFitPolicyFallbackToLegacy` / `TestFitPolicyMarksConflict`）。
  - 门禁：`node scripts/fork-invariants/main.mjs --check manifest`，每次上游 sync 后必跑（该门禁正因 rc35 丢 5 项、rc39 丢 video-token 块而立）。
- **语义门禁**：除 file/symbol 锚点外，必须有 contract tests 验证 active policy 确实改变已知 fixture 的候选集、shadow 写 trace、reload 后版本变化、retry 消费 `RequiredMarks`，并验证 init registration/import 只发生一次。merge CI 对真实 `upstream/main` 做 clean/conflict rehearsal 后运行这些测试；仅保留注释或符号不算通过。
- **定位**：`grep -rn "tokeness-fitpolicy"` 仅用于人工导航，不是完整语义证明。
- **数据在 DB**：规则/标记数据不随上游 merge，但所有 selector 入口、专用写回接口和注册表适配器仍需纳入 fork inventory。

## 13. 迁移计划（风险递增，每步可停）

| 步 | 内容 | 验收 |
|---|---|---|
| P0（已上线） | 四维 profile + 形状 pin + `official_fit_models` 白名单 + 亲和不对称 | `official-fit-mode.md` §验收 |
| **A（前置）** | 建立 selector 调用图、`ChannelConstraints/DecisionContext`、入口 contract tests；把既有族谓词等价搬家为 expr 规则 + 注册函数；`shadow=true` 跑 ≥48h | HTTP/WS/affinity/retry 全入口覆盖；与今天 pin 判定逐例一致；无上下文绕过；差异为零或逐条批准 |
| **B** | 建立专用 capability DTO/endpoint、CAS/审计/状态机；接入 suite 报告校验与 6h 校准；shadow 下 hook#2 消费标记判定 | 权限/force/冲突/幂等测试通过；旧人工项不被覆盖；收窄空集和重试状态机可观测 |
| **C** | 按族启用（kimi-k3 先行），旧 `official_fit_models` 仍作兼容并集；只在 healthy Redis SLO 下逐步放量 | 24h 内拟合违约 0、可避免失败 0；故障注入时保持旧快照且不制造新错误 |
| **D** | DS-V4 / GLM-5.3 迁入；明确“热策略 family/rules”与“代码机制 registry”边界；新增族仍需注册表代码变更，除非完成所有消费者的数据化证明 | 逐族 shadow 等价；registry adapter、wire shape、channel type、exact model consumer 全覆盖 |
| **E** | §16 契约面数据化，顺序 = **F1 errors → F2 shape-A → F3 validate → F4 B 档 → F5 具名契约**；F1/F2 必须先完成语料前置 | 每阶段按对应等价标准、golden fixtures、回放/账务门禁通过；不以未定义的“全部逐字节”作为默认承诺 |

## 14. 验收

**功能与等价性**
- deterministic CI：规则编译/schema/ref/type 在 DB 提交前拒绝；fake epoch、last-known-good、删除配置卸载、CAS/权限/force/幂等、空集/已尝试渠道/首选/归一化/affinity/HTTP/Responses WS/retry contract tests 全通过。
- 规则/标记写入后：在明确的 N 节点、Redis healthy、节点健康条件下传播 P99 ≤2s；Redis/节点/慢 reload 故障时保持旧快照并记录可观测故障，不把 ≤2s 当作无条件承诺。
- `enabled=false` 或删除 `official_fit.policy` 与 `official_fit_capabilities` 后，行为与未安装本层一致，不产生新 4xx/5xx。
- 非法规则写入被拒，不影响线上快照；求值异常 → last-known-good → 旧行为。
- shadow 日志只含脱敏元数据、哈希和采样 trace，且有保留期限；不记录 body/凭据。
- 标记状态机（人工 sticky/过期、suite stale/failed/恢复、policy/baseline 变化）均有测试。
- F1/F2 的准入前置：必须有短期双写/哈希差异捕获或经批准的 semantic baseline；报告样本量、fixture、差异分类和产物路径，缺失语料不得放行。

**CDP 双轨（既有的唯一验收口径）**
- A 轨：`tools/cdp-bench` 严格跑通过（全部执行用例 PASS、无未声明跳过、SEVERE/WORDING/SHAPE/PROTOCOL/INTEGRITY 全零）；基线对比是门禁的一部分。
- B 轨：KV cache ≥90%、有效成功率 ≥99%（HTTP 200 空内容算失败）、TTFT P90 <30s、TPOT P90 <40ms、TPM 满足所选 profile。

**工程与 fork**
- `go test ./pkg/fitpolicy/...`、聚焦 hook 测试、根目录全量 `go test ./...`、`go vet ./...`、`git diff --check`、web `bun run typecheck`。
- `node scripts/fork-invariants/main.mjs --check manifest` 通过；对 `upstream/main` 做一次 merge 演练后门禁仍绿。
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

## 17. Open Questions

1. **套件写回协议**：本计划已选择“报告 + 受控 applier + 专用 capability endpoint”，待实现时冻结 DTO、权限、CAS、幂等和审计字段；不允许直接复用通用 `PUT /api/channel/`。
2. **标记存储**：本计划选择渠道行 `official_fit_capabilities`；若改回 options，必须重新审查语义归属、并发和传播验收，不能在实现中隐式切换。
3. **实际选路入口**：必须先完成 HTTP、Responses WS、affinity、首选/归一化模型、自动分组、blocked/saturation 和 retry 的调用图；覆盖不了的入口从 v1 移除，不得以两个 hook 宣称全局。
4. **机制注册表边界**：`pkg/fitpolicy/` 只热加载策略；family id、channel type、wire shape、exact model names 和跨模块机制仍由代码 registry 提供。新增族仍需代码变更，除非另立完整数据化项目。
5. **`unknown_mark_policy` 默认值**：v1 固定 `conservative`，先用 shadow 观察成本与空集率；未经独立验收不得切换 `permissive`。
6. **F1/F2 语料前置**：现有 logs 不留 body，不能直接宣称旧实现逐字节等价。F1/F2 开工前必须二选一并记录批准证据：短期双写仅落哈希/差异且脱敏，或明确批准 semantic baseline + golden fixtures；CDP 官方语料不能替代旧网关输出语料。
7. **热生效 SLO**：实现前冻结 N 节点、Redis healthy/fault、慢 reload、重启恢复的测量方案；≤2s 仅适用于健康条件，故障条件验收旧快照与告警。
8. **日志与隐私**：冻结 shadow/审计字段、采样率、哈希方式和保留期；禁止记录 body、工具内容、token、完整响应和凭据。
