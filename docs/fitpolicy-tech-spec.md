# Fit Policy：数据驱动的拟合路由、渠道标记与热可编辑规则

> English abstract: a detachable, data-driven replacement for the fork's hard
> "official-fit pin". Required request behaviors are matched against per-channel
> capability marks (suite-measured, human-editable) instead of pinning whole
> shape classes to the official set. Rules and marks are hot-reloadable data
> (expr-lang expressions + JSON, no rebuild, no restart; fleet-wide effect via
> config-epoch). The entire layer lives in one fork-only package plus at most
> two tagged hooks in upstream files, registered in `FORK-CHANGES.md` and the
> `scripts/fork-invariants` gate so upstream syncs cannot drop it silently.

## 1. Goal

用「请求所需行为 × 渠道实测能力」的匹配取代今天的「模型族形状类 → 硬收窄到官方等价集」，让每条渠道在自己能忠实服务的形状上尽量吃量（物尽其用），只在**没有任何候选渠道能服务某项必需行为**时才收窄到官方等价集。

同时满足用户的全部硬约束（§3）。

## 2. 背景：2026-09-26 夜间事故暴露的三类结构性缺陷

1. **硬 pin 的单点化**：pin 候选集 {ch19, ch8, ch41} 在官方端点 ch8 余额停用后实质塌缩；pin 永不降级 → 23:02 出现 312 条路由失败 500（客户端可见）。
2. **标记无语义粒度**：`official_fit_models` 只有「是/否」。ch41 被标了但实测 3/51，无法表达「它对哪些行为是可信的」；ch46 仅对某一类 tool 链严格，其余形状完全可用——二值标记对两者都失效。
3. **守卫假设与配置脱节**：`officialFitPinKeepsVerdict` 假设「该族只有一条官方渠道」，与实际三条不符，把 86 次**本可成功的换渠道**拦成客户端 429。

附：运维侧的三次误报教训（已并入 §10 护栏）：判定规则必须区分 **慢≠死**（无完成≠池全灭）、**请求侧≠渠道侧**（渠道文案默认可疑，先对齐官方基准）、**未知标记的语义必须显式**（不能默认可用）。

## 3. 硬约束（用户逐条提出，逐条对应设计）

| # | 约束 | 设计兑现点 |
|---|---|---|
| C1 | pin 条件更细、少一刀切、渠道加标记、物尽其用 | 行为级规则 + 行为级标记（§6、§7）；默认留在优先级池（§8） |
| C2 | 可拆卸、高灵活、尽量可编辑 | 单包 + ≤2 钩子 + 纯数据规则（§4、§5）；删除配置即卸载（§11） |
| C3 | 通用（族无关），改拟合一律即时生效、不编译重启 | 族定义/谓词/策略全数据；expr 表达式热编译 + config-epoch 秒级传播（§6、§9） |
| C4 | 标记由一致性套件实测写回、可被人工编辑 | provenance + 人工 sticky + 可过期 + 审计（§10） |
| C5 | 合并上游时不能丢，要独立和可见 | fork 独有包 + 标记钩子 + fork-invariants 门禁锚点（§12） |

## 4. 范围 / 非目标

**范围**
- OpenAI 兼容 `/v1/chat/completions` 文本中继的**选路前**与**重试判定前**两个决策点。
- 族：deepseek-v4、kimi-k3、glm-5.3 及后续新增族（族定义为数据，新增族不发版）。
- 渠道标记的声明、实测写回、人工编辑与审计。

**非目标（v1 明确不做）**
- 不动用户契约 `official_fit.profile` 的四个维度语义（validate/errors/shape/route 保持现状；本层只为 route 提供更细的候选决策）——把四个维度归一化的长期路线见 §16。
- 不动计费、配额、并发限流。
- 不做视频/图片/Responses 中继的行为判定（沿用现有 `supports_video` 机制）。
- 控制台可视化编辑器（v1 用既有 `PATCH /api/option/request_policy` 与 `PUT /api/channel/` 直写；UI 单独立项）。

## 5. 架构

```
                    ┌────────────────────────────────────────────┐
  请求 ──► hook#1 ──►│  pkg/fitpolicy                             │
  distributor.go     │    Current().Decide(reqCtx) → Decision      │
                    │      ├─ snapshot: rules vN (expr 程序, 原子)  │
  错误 ──► hook#2 ──►│      ├─ env: gjson 视图 + extractor 注册表   │
  relay_error.go     │      ├─ marks: channel fit_capabilities      │
                    │      └─ shadow: 只记录不生效                  │
                    └──────┬──────────────────────────┬───────────┘
                           │ options.FitPolicyRules    │ channels.other_settings.fit_capabilities
                           ▼                           ▼
                    PATCH /api/option            PUT /api/channel/
                    （套件写回 / 人工编辑）        （套件写回 / 人工编辑）
                           │                           │
                           └────────► bump config_epoch:v1 ──► 全部节点 1–2s 内重载
```

- **hook#1（middleware/distributor.go，选渠道前）**：返回候选过滤（为空时按行为类的降级口径）与判决口径。
- **hook#2（service/relay_error.go，重试判定前）**：返回该请求在重试时应保留官方判决（stop）还是允许换渠道（retry），替代今天 `officialFitPinKeepsVerdict` 的族级假设。
- 两处钩子均为**委托调用**，不含逻辑，并用 `// tokeness-fitpolicy:begin/end` 标记（§12）。

**Decision 类型**

```go
type Decision struct {
    HasOpinion     bool          // false = 本层无意见，完全走今天的老路径（默认/兜底）
    FilterOfficial bool          // true = 候选收窄到官方等价集（official_type ∪ 标记该行为的渠道）
    RequiredMarks  []string      // 收窄时要求的行为标记（本请求真正需要的）
    KeepVerdict    bool          // 重试判定用：true = 保留官方判决（stop），false = 允许换渠道
    Degrade        DegradePolicy // never | mark_response | failover（按行为类）
    Shadow         bool          // true = 只记录，绝不生效
    Trace          Trace         // 命中的规则 id / 特征 / 候选集（写入 logs.other，可观测）
}
```

**兜底链（任一步失败都向旧行为退化，永不 500）**：
未配置 / `enabled=false` / 编译失败 / 求值失败 / 规则为空 → `HasOpinion=false` → 今天的原逻辑逐字节不变。快照替换失败 → last-known-good。

## 6. 数据模型

### 6.1 规则集 `options.FitPolicyRules`（版本化 JSON）

```json
{
  "version": 12,
  "enabled": true,
  "shadow": true,
  "families": [
    {
      "id": "kimi-k3",
      "match": "lower(model) startsWith \"kimi-k3\"",
      "official_type": 25,
      "behaviors": {
        "tools.dynamic_names":      {"class": "capability"},
        "tools.resolvable_chain":   {"class": "capability"},
        "history.assistant_first":  {"class": "capability"},
        "stream.reasoning_content": {"class": "courtesy",  "degrade": "mark_response"},
        "verdict.passthrough":      {"class": "verdict",   "degrade": "never"}
      },
      "rules": [
        {
          "id": "k3-tool-choice-not-auto",
          "when": "has(tool_choice) && tool_choice != \"auto\"",
          "require": ["tools.resolvable_chain"],
          "then": "restrict_to_marked"
        },
        {
          "id": "k3-assistant-first-history",
          "when": "size(messages) > 0 && messages[0].role == \"assistant\"",
          "require": ["history.assistant_first"],
          "then": "restrict_to_marked"
        }
      ],
      "empty_match_policy": "official_set_then_fail_verbatim",
      "unknown_mark_policy": "conservative"
    }
  ]
}
```

- `match`：族归属表达式（模型 id 前缀/正则），新增族=新增一条 JSON。
- `behaviors[*].class`：`verdict`（判决类，宁失败不失真）/ `capability`（能力类，换渠道即可）/ `courtesy`（体感类，可降级并标注）。
- `rules[*].when`：**expr 表达式**（见 §6.3），返回 bool。
- `then`：`restrict_to_marked`（候选收窄到「标记支持 require 中全部行为」的渠道；若收窄结果==全部候选则视为无意见）；`official_set`（收窄到官方等价集）；`ignore`。
- `empty_match_policy`：收窄后为空时的口径（`official_set_then_fail_verbatim` / `fail_verbatim` / `fallthrough_priority`）。
- `unknown_mark_policy`：渠道对某必需行为**没有标记**时如何处理——`conservative`（视为不支持，排除出收窄集；「标记当承诺用」）/ `permissive`（视为可用）。v1 默认 conservative。
- 全文件 schema 校验（写时拒绝非法规则）；`version` 单调递增；写操作记录审计（谁、何时、diff 摘要）。

### 6.2 渠道标记 `channels.other_settings.fit_capabilities`

```json
{
  "kimi-k3": {
    "marks": {
      "tools.resolvable_chain":  {"value": true,  "source": "suite",  "suite": "48case@2026-09-24", "evidence": "41/48", "at": "2026-09-24T02:11:00+08:00"},
      "codex_cli":               {"value": false, "source": "manual", "by": "user", "at": "2026-09-26T10:46:00+08:00", "expires_at": null},
      "history.assistant_first": {"value": true,  "source": "suite",  "suite": "48case@2026-09-24", "at": "2026-09-24T02:11:00+08:00"}
    },
    "context_max": {"value": 1048576, "source": "suite", "at": "2026-09-24T02:11:00+08:00"},
    "usage_counting": {"value": "official_exact", "source": "manual", "by": "user", "at": "..."}
  }
}
```

- 每个维度一个对象：`value` + `source`（suite|manual|import）+ 溯源（suite 名称/证据/at）+ 人工项可选 `expires_at`。
- **冲突语义（C4）**：实测为默认来源；人工写入即 sticky，套件想覆盖人工必须显式 `force`；人工项到期自动回退到最新实测值（防「人工遗毒」）。
- **新鲜度**：`source=suite` 且 `at` 早于 `stale_after_days`（规则集级配置，默认 30 天）→ 控制台与采样日志里标 `stale`，可选降级为 unknown（off by default）。
- 每次写入（套件或人工）都带 provenance + 审计，行为变更可回溯到「哪台套件 / 哪个人 / 何时」。

### 6.3 表达式契约（expr-lang/expr，已在依赖中）

- 求值环境 env：
  - `model`、`messages`、`tools`、`tool_choice`、`response_format`、`stream`、`max_tokens`、`temperature`、`top_p`、`n`、`presence_penalty`、`frequency_penalty`、`logprobs`、`top_logprobs`、`reasoning_effort`、`thinking`、`user_agent`、`headers`（经 gjson 视图安全暴露，只读）
  - 注册函数（pkg/fitpolicy/registry.go，fork 独有，**唯一需要发版的扩展点**）：`historyBeginsWithUserTurn()`, `messagesCarryDynamicTools()`, `toolChainResolvable()`, `promptTokenEstimate()`, `hasVideoPart()`, `familyOf(model)` …（今天的 5 个 kimi-k3 谓词与视频谓词原样迁为函数，先注册后改数据，行为等价）
- 编译：按 `version` 整集编译（仿 `pkg/billingexpr/compile.go`：env 固定、AST patch、类型断言 bool）；编译失败 → 拒绝写入。
- 求值：每请求 `expr.Run`，单请求预算（默认 2ms，超时按求值失败处理）；env 只读、无 I/O、无时间函数 → 可沙箱。
- 简单维度可直接用 gjson 路径表达式，不必注册函数（如 `size(messages)>0 && messages[0].role=="assistant"`）。

## 7. 渠道标记匹配（物尽其用的核心）

收窄集 = 「group+model 候选」 ∩ 「`require` 全部行为的标记为 value:true 的渠道」。三类来源可进收窄集：

1. 官方渠道类型（`official_type`）——恒视为「全行为支持」；
2. 标记 `value:true` 的渠道（含 suite 实测与人工声明）；
3. （可选）规则显式豁免的渠道。

收窄后仍按 priority/weight 在集内竞争 —— 标记渠道照常吃量，**pin 只发生在一个请求真正需要、且普通候选里没有标记支持者的形状上**。

## 8. 决策流（hook#1 伪码）

```
func Decide(req) Decision:
    cfg := snapshot()                       // 原子读取；无配置 → NoOpinion
    if !cfg.Enabled: return NoOpinion
    fam := firstFamilyMatching(model)
    if fam == nil: return NoOpinion
    feats := evalFeatureExprs(req, fam)     // expr env，预算 2ms
    reqMarks := union(rule.require for rule in fam.rules if rule.when(feats))
    if len(reqMarks) == 0: return NoOpinion          // 普通优先级池（默认路径）
    cands := candidates(group, model)
    marked := [c for c in cands if supportsAll(marks(c), reqMarks, fam.unknown_mark_policy)]
    switch:
    case len(marked) == len(cands): return NoOpinion // 收窄无意义
    case len(marked) > 0:  return Decision{FilterOfficial: marked, RequiredMarks: reqMarks, Trace}
    default:               return Decision{FilterOfficial: officialSet(fam), RequiredMarks: reqMarks,
                                           Degrade: classPolicy(fam, reqMarks), Trace}
```

hook#2（重试判定）只问一个问题：**「还有没有标记渠道能为 reqMarks 服务？」**——有则允许换渠道（retry），没有且 verdict 类 → `KeepVerdict=true`（stop，保留官方判决原文）。这直接修复「守卫假设单官方渠道」的缺陷：判据来自标记而非族假设。

## 9. 即时生效（不编译、不重启）

- **本实例**：PATCH 成功即替换内存快照（原子指针交换），下一个请求生效。
- **全 fleet**：复用 `model/config_epoch.go`（Redis `config_epoch:v1`，watcher 2s，下限 250ms）：写入规则或标记后 bump epoch，所有节点 1–2s 内重建快照/标记视图。**承诺口径：规则与标记变更 1–2 秒全网生效；不作亚秒承诺。**
- **失败语义**：新快照编译失败 → 全 fleet 保留 last-known-good；channel 标记读取失败 → 该渠道按 unknown 处理（由 unknown_mark_policy 决定），不影响其他渠道。
- 对无法走 config-epoch 的现有 channel 缓存路径：标记写入同时 bump epoch 并依赖既有 channel cache sync 兜底（SYNC_FREQUENCY 量级），两路孰先取先到。

## 10. 标记生命周期与护栏

- **写回通道**：一致性套件直连 `PUT /api/channel/`（已有鉴权）写入 `fit_capabilities`（§6.2）；套件必须传 `suite` 名与 `evidence`；v1 不做队列/文件通道。
- **防反馈环**：套件永远只对照**官方端点**实测（与当前路由无关），标记变化不污染测量。
- **护栏（吸收夜间误报教训）**：
  1. 写入即 schema + 引用完整性校验（require 引用的行为必须在 behaviors 声明；官方类型必须存在）。
  2. 任何规则/标记变更先 **shadow**（§11）再放行；shadow 结果入 `logs.other.fitpolicy`，可按规则 id 聚合「若启用会影响多少请求」。
  3. 一键回滚：`enabled=false`（秒级回旧行为）；或回滚到指定 `version`（版本化存储）。
  4. 采样/观测：决策 Trace 写 `logs.other`（规则 id、reqMarks、候选集、shadow）；夜间巡检脚本可加一条「收窄集为空的比率」指标。
  5. 人工标记默认 sticky + 可选 expires；套件覆盖人工需 `force`，全部留审计。

## 11. 影子、灰度与卸载

- **影子模式**：`shadow=true` → 全量评估并记录 Decision，**绝不改路由**；用于用真实流量校准规则与标记，直到 shadow 结论稳定。
- **灰度**：按族启用（families[] 逐条加）→ 按渠道启用（先只给已实测渠道打标记）→ 全量。每一步可独立回退。
- **旧机制 preset 化**：今天的三个族硬 pin（`deepSeekV4RequestNeedsOfficial`、`kimiK3RequestNeedsOfficial`、glm-5.3 全族）先以**等价规则 JSON** 注册为 preset（行为等价，可 A/B）；切换窗口内两路并行评估、仅一路生效（由 `enabled` 与 preset 开关控制）。
- **卸载**：删除 `options.FitPolicyRules` 与渠道 `fit_capabilities` → 钩子返回 NoOpinion → 完全回到今天的实现；`pkg/fitpolicy/` 整目录可删。

## 12. Fork 存活与可见（C5）

- **侵入面 = 2 处钩子 + 0 个其他上游文件改动**。钩子块：
  ```go
  // tokeness-fitpolicy:begin  （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
  if d := fitpolicy.Current().Decide(c); d.HasOpinion() { applyDecision(c, d) }
  // tokeness-fitpolicy:end
  ```
- **登记**（实现时同批提交）：
  - `FORK-CHANGES.md` 新增一节：标题、行为契约、文件清单、证据（本文件 + 测试）。
  - `scripts/fork-invariants/manifest.json` 新增 entry，锚点：
    - `kind: file` → `pkg/fitpolicy/{schema,compile,env,decide,snapshot,shadow,registry}.go`
    - `kind: symbol` → `fitpolicy.Current`、`fitpolicy.Decide`、`fitpolicy.CompileRules`
    - `kind: symbol` → 钩子所在上游函数（`middleware` 的 distributor 选路函数、`service` 的 `DecideRelayRetry`）
    - 测试名锚点 → `TestFitPolicyHookWired`、`TestFitPolicyFallbackToLegacy`、`TestFitPolicyMarksConflict`
  - 门禁命令：`node scripts/fork-invariants/main.mjs --check manifest`；上游 sync 后必跑。
- **定位 grep**：`grep -rn "tokeness-fitpolicy"` = 全部侵入点（除包目录本身）。
- 行为数据全部在 DB/options，**merge 无法触碰**；唯一能被 merge 影响的只有两处钩子，且已被门禁锚定。

## 13. 迁移计划（风险递增，每步可停）

1. **零发版（已具备）**：标记只在实测通过时打；渠道特有拒绝→关键词换渠道；窗口/严格度→优先级（2026-09-26 夜间已按此运营）。
2. **发版 A（纯影子）**：pkg/fitpolicy 落地 + 2 钩子 + preset 规则；`shadow=true` 全量观察 ≥48h，对比 shadow 结论与旧 pin 行为的差异率（目标：除预期收窄外差异为 0）。
3. **发版 B（按族启用）**：kimi-k3 先启用；旧 pin preset 保留可一键切回；观察「收窄集为空比率」「KeepVerdict 次数」「换渠道成功率」。
4. **全量**：deepseek-v4、glm-5.3 迁入；逐渠道补实测标记；旧 Go 谓词退役（fork 代码面缩小，manifest 条目相应退役）。
5. **之后（本文件不实施，路线见 §16）**：P1 validate → P2 errors → P3 shape → P4 契约具名化，把四个布尔收敛为具名契约。

## 14. Acceptance Criteria

- 规则/标记写入（PATCH / PUT）后：本实例即时生效，全 fleet ≤2s 生效；`enabled=false` 后行为与今天逐字节一致。
- 删除 `FitPolicyRules` 与全部 `fit_capabilities` 后，网关行为与未安装本层完全一致。
- 非法规则（schema/引用/类型错误）在写入时被拒绝，且不影响线上快照。
- 求值超时/异常 → 回退 last-known-good → 回退旧行为；全程无 5xx。
- 影子模式下路由行为与旧实现一致，且 `logs.other.fitpolicy` 含完整 Trace。
- 对 2026-09-26 事故重放：ch8 停用场景下，带标记的健康渠道可承接时不再出现 `stop/pinned_channel`；pin 候选全灭时按 `empty_match_policy` 处理。
- 冲突语义：人工 sticky、expires 到期回退实测、套件 `force` 覆盖，三条均有测试。
- fork-invariants 门禁在本层登记后通过；对 upstream/main 做一次 merge 演练后门禁仍绿。
- 性能：决策 P99 增量 ≤2ms/请求（快照内 expr，无 I/O）。
- 工程：`go test ./pkg/fitpolicy/...`、聚焦 hook 测试、根目录全量 `go test ./...`、`go vet ./...`、`git diff --check` 全绿；四节点灰度发布并经公网两路由验证。

## 15. Risks

- **数据即代码的风险放大**：规则错误会即时影响线上。缓解：schema 校验 + shadow + 版本回滚 + 审计 + 按族灰度。
- **表达式沙箱**：env 只读、无 I/O、无时间函数、预算限制；函数注册表是白名单。
- **未知标记歧义**：v1 取 conservative（无标记=不支持），可能导致收窄集变小、更多流量落官方端点；用 shadow 数据评估后再决定是否给特定维度放开 permissive。
- **标记腐化**：实测标记有 stale 提示；人工标记可过期；防「人工遗毒」。
- **钩子与上游冲突**：仅 2 处，已被门禁锚点与测试覆盖；若上游重写了钩子所在函数，sync 时按 FORK-CHANGES 记录重挂。
- **套件反馈环**：套件只对照官方端点实测，与路由无关（§10）。
- **config-epoch 依赖 Redis**：Redis 不可达时回退既有 SYNC_FREQUENCY 级传播（降级为「最多数十秒」，不影响正确性）。

## 16. 演进：把 validate / errors / shape / route 归一为「契约面 × 行为维度」

**结论：能。** 四个维度本质是同一份保真契约的四个面，可统一为

```
需求侧（用户契约 facets：validate / errors / shape / route）  ×  供给侧（渠道行为标记）  →  Decide()
```

两轴正交：契约说「用户要什么」，标记说「渠道能给什么」，决策由同一份数据模型产出。迁移分阶段，逐阶段可停可退。

**机制 vs 策略边界（必须如实）**

| 维度 | 可数据化（热生效，不发版） | 必须留代码（注册表 / 执行器） |
|---|---|---|
| route | 谓词、候选过滤、空集口径（本文件 v1 主体） | 提取器函数（tool 链可解析性等） |
| validate | 规则表：字段/比较/官方文案模板、族级开关、错误码 | 语义检查：tool 链可解析性、serde location 后缀、tokenization 失败识别 |
| errors | 文案策略：是否附 request id、按来源（上游/网关）与错误类分流 | 错误提取与拼装点 |
| shape | 变换选择：哪些变换对哪族/哪渠道生效（strip / assemble / usage 映射） | 变换器本体（SSE 拼接、usage 结构映射、扩展字段剥离） |

即：**策略可数据化，机制仍需注册表**——与 §5「钩子只委托、逻辑在 fork 独有包」同一条纪律。

**阶段（每阶段独立开关、独立回退、先 shadow）**

| 阶段 | 内容 | 准入条件（等价性证明） |
|---|---|---|
| P0 | 本文件 v1：只喂 `route`，其余三维不动 | §14 |
| P1 | `validate`：三大族校验表写成数据，Go 校验器降级为「执行器 + 语义函数」；旧 Go 表保留为一键回退 | 新旧判定在「2026-09-26 事故重放 + 套件样本」上**逐条一致**，差异逐条列明并经批准 |
| P2 | `errors`：文案策略数据化（来源 / 错误类 → 是否官方原文） | 同一批错误样本输出**字节一致** |
| P3 | `shape`：拆「策略（数据）」+「变换器注册表（代码）」，先把选择项开关化 | 同一批响应样本**字节一致**（含 SSE 分片边界） |
| P4 | 契约具名化：`profile` 四布尔 → **命名契约**（如 `fit_contract: "official-k3-v3"`）；四布尔保留为别名，旧数据**零迁移**；新族上线 = 新增一份契约数据 | 控制台与 API 向后兼容；旧用户行为不变 |

**护栏**

- 四个维度属**客户可感知契约**，比路由更危险：每阶段必须 shadow → 按用户/按族灰度 → 一键回旧表；**路由层与契约层开关互相独立**（任一层失效不影响另一层，满足 C2 可拆卸）。
- P4 之前不改四个布尔的语义与存储；P4 只做「别名 + 具名化」，**不做数据迁移**。
- 本演进**不进入 v1 范围**（§4 非目标保持），此处仅登记路线。

**收益**

- 一份数据模型同时回答「用户要什么」与「渠道能给什么」；新族 / 新维度 / 新官方文案不发版。
- fork 代码面继续缩小：三大族校验器与形状策略迁出 Go 表，上游 merge 更轻（C5 长期收益）。

## 17. Open Questions（待用户确认后进入实现）

1. **一致性套件的位置/命令**（实测写回的调用方；当前仓库内未找到该脚本）。
2. 钩子位置确认：`middleware/distributor.go` + `service/relay_error.go` 两处，还是存在更合适的既有扩展点。
3. 包名 `pkg/fitpolicy` 与文档名 `docs/fitpolicy-tech-spec.md` 是否采用。
4. 冲突语义：人工 sticky + 可选过期（本文件 §6.2）是否符合预期；是否需要「人工永久优先」开关。
5. 第一版是否把 config-epoch 的 1–2s 传播作为承诺口径（vs 仅依赖既有 channel sync 的数十秒）。
