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
          │  问：还有没有标记渠道能为本请求的必需行为服务？
          └─ 有 → 允许换渠道；无且判决类 → 保留官方判决（stop）
```

- **两处钩子**：均为委托调用、不含逻辑，用 `// tokeness-fitpolicy:begin/end` 标记（§12）。
- **reload 自注册**：`pkg/fitpolicy` 在自己的 `init()` 里调 `model.RegisterConfigReloadHook(...)` → 配置 epoch 前进时重建规则快照。**不必改 `main.go`**（钩子数保持 2）。
- **兜底链**：未配置 / `official_fit.policy` 缺失 / 编译失败 / 求值失败 → `HasOpinion=false` → 完全走今天的实现（pin 由 `route` + `official_fit_models` 决定），永不 500。

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

- `match`：族归属（前缀/正则），**新增族 = 新增一条 JSON**（D6 解）。
- `rules[*].when`：expr 表达式，命中即表示"本请求需要 `require` 里的行为"（§6.3）。
- `behaviors[*].class`：`verdict`（判决类：宁失败不失真）/ `capability`（能力类：换渠道即可）/ `courtesy`（体感类：可降级并标注）。
- `unknown_mark_policy`：无标记的渠道如何处理——`conservative`（视为不支持，**标记当承诺用**）/ `permissive`。v1 默认 conservative。
- `empty_match_policy`：收窄后为空时的口径（`official_set_then_fail_verbatim` / `fail_verbatim` / `fallthrough_priority`）。
- 写时 schema 校验 + 版本单调递增 + 审计（谁、何时、变更摘要）。

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

- **存储选择的理由**：用户要求"多在渠道中加标记"，且 `official_fit_models` 本就住在渠道行；渠道写入路径已有 `InitChannelCacheAndNotify()`（重建渠道缓存 + bump epoch）→ **同样 ~2s 全网生效**。`official-fit-cost-optimization-plan.md` 曾提议放在 options（`official_fit.channel_capabilities` + Redis 广播）；二者传播能力等价，本项目选**渠道行**（语义归属正确、与既有字段同处、套件写回走同一个 `PUT /api/channel/`）。
- **冲突语义（C4）**：实测为默认来源；人工写入即 sticky，套件覆盖人工必须显式 `force`；人工项可带 `expires_at`，到期自动回退到最新实测值（防"人工遗毒"）。
- **新鲜度**：`source=suite` 且 `at` 早于 `stale_after_days`（规则集配置，默认 30 天）→ 控制台与采样日志标 `stale`，可选降级为 unknown（默认关）。
- **兼容**：`official_fit_models` 仍按现语义参与 pin 候选并集（`ChannelIsOfficialFitForModel`）；新增能力字段缺省时行为完全不变。

### 6.3 表达式契约（`expr-lang/expr`，**已在 go.mod 依赖中**）

- env（只读、无 I/O、无时间函数）：`model`、`messages`、`tools`、`tool_choice`、`response_format`、`stream`、`max_tokens`、`temperature`、`top_p`、`n`、`logprobs`、`top_logprobs`、`thinking`、`reasoning_effort`、`user_agent`、以及 gjson 视图 `body`。
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
              └─ 写回：PUT /api/channel/（渠道行 official_fit_capabilities）→ InitChannelCacheAndNotify → ~2s 全网生效
```

- **套件只对照官方端点**（与当前路由无关）→ 不存在"测量被路由污染"的反馈环。
- **持续校准**：cron 每 6h 轻探针（3-5 例）；任一失败 → 立即把对应行为标 `supported:false` + 告警；连续 2 轮通过 → 自动恢复（沿用 cost plan §1.2 的校准纪律与自动摘除/恢复）。
- **人工编辑（C4）**：同一字段可由管理员/控制台直接改写；人工项 sticky，套件需 `force` 才覆盖；带 `expires_at` 的临时覆盖到期回落到实测值；每次写入留 provenance + 审计。

## 8. 决策流（pin 仍由 `route` 触发，标记只决定"谁有资格"）

```
func Decide(req) Decision:
    cfg := snapshot(); if !cfg.Enabled || cfg.Shadow: shadowRecord(); return NoOpinion
    fam := firstFamilyMatching(model); if fam == nil: return NoOpinion
    feats := evalFeatureExprs(req, fam)                     // expr，预算 2ms
    reqMarks := union(rule.require for rule in fam.rules if rule.when(feats))
    if len(reqMarks) == 0: return NoOpinion                 // 普通优先级池（默认路径）
    cands  := candidates(group, model)
    marked := [c for c in cands if supportsAll(marks(c), reqMarks, fam.unknown_mark_policy)]
    case len(marked) == len(cands): return NoOpinion         // 收窄无意义
    case len(marked) > 0:  return Decision{Filter: marked, RequiredMarks: reqMarks}   // 标记渠道照常吃量
    default:               return Decision{Filter: officialSet(fam), Degrade: classPolicy(...)}
```

- **与既有实现的关系**：`Filter` 的结果与今天一样交给 `preferOfficialFitChannels/Abilities` 按 priority 选路；差别只是候选集由**行为标记**而非**二值模型白名单**决定。
- **hook#2（重试判定）**：判据从"本族是否只有一条官方渠道"改为"**还有没有标记渠道能服务 `RequiredMarks`**"——有则允许换渠道，无且判决类则 stop（修 D5）。

## 9. 即时生效（复用既有链路，不新增传播代码）

| 变更 | 传播路径 | 生效时延 |
|---|---|---|
| 规则集（options） | `model/option.go` 写入 → `NotifyConfigChanged()` → Redis `config_epoch:v1` INCR → 各节点 2s watcher → `applyConfigReload` → `RegisterConfigReloadHook`（本包自注册）重建快照 | **~2s** |
| 渠道标记（渠道行） | `PUT /api/channel/` → `InitChannelCacheAndNotify()` → 同上 | **~2s** |
| Redis 不可用 | epoch 读不到 → 退回 `SYNC_FREQUENCY`（默认 60s）同步循环 | 数十秒（**正确性不受影响**，`config_epoch.go` 明确 fail-open） |

- 本实例写入后**下一个请求**即用新快照（原子指针交换）。
- 编译/求值失败 → 保留 last-known-good → 再退旧行为。
- **不新增任何传播机制**：`config_epoch` 已由 fork 于 2026-09-25 落地，专为"配置变更 ~1s 到其他节点"而建，本设计只是它的一个 reload hook。

## 10. 护栏（吸收 2026-09-26 夜间三次误报的教训）

1. 写入即 schema + 引用完整性校验（`require` 引用的行为必须在 `behaviors` 声明；族必须存在）。
2. 任何规则/标记变更先 **shadow**（§11）再放行；shadow 结论入 `logs.other.fitpolicy`，可按规则 id 聚合"若启用影响多少请求"。
3. 一键回滚：`official_fit.policy.enabled=false`（秒级回旧行为）或回滚到指定 `version`。
4. 判定纪律（夜间教训）：**慢≠死**（无完成≠池全灭）、**请求侧≠渠道侧**（渠道文案默认可疑，先对齐官方基准）、**未知标记语义显式**（默认 conservative）。
5. 标记新鲜度：`stale` 标记 + 6h 校准 + 人工项过期；`force` 覆盖留审计。
6. 观测：Trace 写 `logs.other`（规则 id、requiredMarks、候选集、shadow）；巡检脚本增加"收窄集为空比率"指标。

## 11. 影子、灰度与卸载

- **影子**：`shadow=true` → 全量评估并记录，**绝不改路由**；用于用真实流量校准规则与标记。
- **灰度**：按族启用 → 按渠道补标记 → 全量；每步独立回退。
- **旧机制共存**：`official_fit_models` 与能力标记并存；`route` 与 `validate/errors/shape` 完全不动。规则集缺失时 `Decide` 返回无意见，行为 = 今天。
- **卸载**：删除 `official_fit.policy` 与 `official_fit_capabilities` → 完全回到今天；`pkg/fitpolicy/` 整目录可删。

## 12. Fork 存活与可见（C5）

- **侵入面 = 2 处钩子 + 0 个其他上游文件改动**（reload 由包 `init()` 自注册，无需动 `main.go`）：
  ```go
  // tokeness-fitpolicy:begin  （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
  if d := fitpolicy.Current().Decide(c); d.HasOpinion() { applyDecision(c, d) }
  // tokeness-fitpolicy:end
  ```
- **登记**（实现时同批提交）：
  - `FORK-CHANGES.md` 新增一节；
  - `scripts/fork-invariants/manifest.json` 新增 entry，锚点四层：`kind:file`（`pkg/fitpolicy/*.go`）、`kind:symbol`（`fitpolicy.Current` / `Decide` / `CompileRules`）、`kind:symbol`（两处钩子所在上游函数）、测试名（`TestFitPolicyHookWired` / `TestFitPolicyFallbackToLegacy` / `TestFitPolicyMarksConflict`）。
  - 门禁：`node scripts/fork-invariants/main.mjs --check manifest`，每次上游 sync 后必跑（该门禁正因 rc35 丢 5 项、rc39 丢 video-token 块而立）。
- **定位**：`grep -rn "tokeness-fitpolicy"` = 全部侵入点。
- **数据在 DB**：merge 碰不到；唯一可被 merge 影响的只有 2 处钩子，且已被锚点锁定。

## 13. 迁移计划（风险递增，每步可停）

| 步 | 内容 | 验收 |
|---|---|---|
| P0（已上线） | 四维 profile + 形状 pin + `official_fit_models` 白名单 + 亲和不对称 | `official-fit-mode.md` §验收 |
| **A（本文件第一步）** | 把既有族谓词**等价搬家**为 expr 规则 + 注册函数；`shadow=true` 跑 ≥48h | 与今天的 pin 判定**逐例一致**（差异为零或逐条列明批准） |
| **B** | 引入 `official_fit_capabilities`；套件写回 + 6h 校准；hook#2 守卫改为标记判定 | shadow 下"收窄集为空比率"、换渠道成功率可观测 |
| **C** | 按族启用（kimi-k3 先行），旧 `official_fit_models` 仍作并集参与 | 24h 内拟合违约 0、可避免失败 0 |
| **D** | DS-V4 / GLM-5.3 迁入；GLM 整族 pin 拆分为形状谓词 | 逐族 shadow 等价 |
| **E（另议）** | §16 四维归一（validate/errors/shape 数据化 → 具名契约） | 各阶段字节级等价 |

## 14. 验收

**功能与等价性**
- 规则/标记写入后：本实例瞬时、全节点 ≤2s 生效；`enabled=false` 后行为与今天逐字节一致。
- 删除 `official_fit.policy` 与 `official_fit_capabilities` 后，行为与未安装本层完全一致。
- 非法规则（schema/引用/类型）写入被拒，不影响线上快照；求值异常 → last-known-good → 旧行为，全程无 5xx。
- shadow 模式下路由与今天一致，且 `logs.other.fitpolicy` 含完整 Trace。
- 标记冲突三条（人工 sticky / expires 回退 / suite force）均有测试。

**CDP 双轨（既有的唯一验收口径）**
- A 轨：`tools/cdp-bench` 严格跑通过（全部执行用例 PASS、无未声明跳过、SEVERE/WORDING/SHAPE/PROTOCOL/INTEGRITY 全零）；基线对比是门禁的一部分。
- B 轨：KV cache ≥90%、有效成功率 ≥99%（HTTP 200 空内容算失败）、TTFT P90 <30s、TPOT P90 <40ms、TPM 满足所选 profile。

**工程与 fork**
- `go test ./pkg/fitpolicy/...`、聚焦 hook 测试、根目录全量 `go test ./...`、`go vet ./...`、`git diff --check`、web `bun run typecheck`。
- `node scripts/fork-invariants/main.mjs --check manifest` 通过；对 `upstream/main` 做一次 merge 演练后门禁仍绿。
- 决策 P99 增量 ≤2ms/请求（快照内 expr，无 I/O）。

## 15. Risks

- **标记即信任，写错=静默违约**（既有已知限制）：缓解 = 套件写回 + 6h 校准 + stale 提示 + shadow 先行 + 人工项过期。
- **数据即代码**：规则错误即时影响线上 → schema 校验 + shadow + 版本回滚 + 按族灰度 + 审计。
- **表达式沙箱**：env 只读、无 I/O、无时间函数、2ms 预算、函数白名单。
- **未知标记歧义**：v1 conservative 可能让更多流量落官方集（成本上升）；用 shadow 数据评估后再决定是否对特定行为放开 permissive。
- **钩子与上游冲突**：仅 2 处，已被门禁锚点与测试覆盖；上游重写钩子所在函数时按 `FORK-CHANGES.md` 重挂。
- **等价迁移风险**：谓词从 Go 搬到 expr 若有偏差，会改变 pin 判定 → A 步必须逐例一致，且 shadow 观察期内不得启用。
- **依赖既有机制**：`config_epoch` 需 Redis；不可用时退化为数十秒同步（正确性不变，仅传播变慢）。

## 16. 演进（另议，不在本文件范围）：把 validate / errors / shape / route 归一为契约面

四维是同一份保真契约的四个面，可统一为 **需求侧（用户契约）× 供给侧（渠道标记）→ 决策**。边界必须如实：**策略可数据化，机制仍需注册表**。

| 维度 | 可数据化（热） | 必须留代码 |
|---|---|---|
| route | 谓词、候选过滤、空集口径（本文件主体） | 提取器函数 |
| validate | 规则表：字段/比较/官方文案模板、族开关 | 语义检查（tool 链可解析性、tokenization 失败识别） |
| errors | 文案策略：是否附 request id、按来源/错误类分流 | 错误提取与拼装点 |
| shape | 变换选择：哪些变换对哪族/哪渠道生效 | 变换器本体（SSE 拼接、usage 映射、字段剥离） |

阶段：P1 validate 数据化（Go 校验器降级为执行器，旧表保留一键回退）→ P2 errors → P3 shape 拆策略/变换器 → P4 四布尔收敛为**具名契约**（如 `fit_contract: "official-k3-v3"`，旧数据零迁移）。准入一律"字节级等价 + 逐条列明差异"。四维属客户可感知契约，比路由更危险：路由层与契约层开关必须互相独立。

## 17. Open Questions

1. **套件写回的落点**：由 `tools/cdp-bench` 直接 `PUT /api/channel/`（需要 admin 凭证），还是输出 JSON 由网关闭环消费？后者更隔离，前者更省事。
2. **标记存储**：确认采用渠道行 `official_fit_capabilities`（本文件选择，理由见 §6.2），还是沿用 cost plan 的 options `official_fit.channel_capabilities`。
3. **钩子位置**：`middleware/distributor.go` + `service/relay_error.go` 两处是否认可。
4. **包名** `pkg/fitpolicy/`、文档名 `docs/fitpolicy-tech-spec.md`（既有文档同名会冲突吗：`official-fit-mode.md` 是现状 spec，本文件是 Phase 3，建议保留并列）。
5. **`unknown_mark_policy` 默认值**：v1 conservative 会让未标记渠道退出拟合收窄（更安全、更贵），是否接受先用 shadow 数据决定。
