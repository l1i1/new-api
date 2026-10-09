# 提案：「关思考」应成为可验证的渠道能力，而不是方言里的一个假定常量

- 日期：2026-10-09
- 状态：**待批准**（涉及路由与准入行为，需用户明确要求后才实施）
- 触发：ch77 上线 18 分钟内 21 次 `400 未知请求字段：chat_template_kwargs`
- 影响面：所有「非官方型」聚合渠道；向客户承诺「关思考」的语义正确性与计费正确性

## 一、问题

平台的聚合渠道方言把「关思考」翻译成 `reasoning_effort=minimal`：

```go
// relay/channel/openai/adaptor.go
const kimiK3AggregatorDisabledEffort = "minimal"
```

`applyKimiK3DisabledThinkingDialect` 对非官方型渠道，把调用方的关思考意图统一改写成
`minimal`，并注释说明其依据是 2026-10-02 对 **NeurVibe 一家**的实测（推理量从
~1000-2000 塌到 ~5）。

问题是：**这个常量被当成了「聚合渠道类」的性质，而它只是一次单点测量。** 在不同聚合渠道上，
`minimal` 可能完全不关思考，此时平台仍然"认为"自己兑现了承诺。

## 二、实测证据（2026-10-09，同一道推理题）

题：`一个笼子里有鸡和兔共35只，脚共94只，鸡兔各几只？`

| 渠道 / 参数 | reasoning 字符数 |
| --- | --- |
| ch77 默认（无 effort） | 665 |
| ch77 `reasoning_effort=minimal` | **1059** |
| ch77 `reasoning_effort=high` | 1270 |
| ch77 `thinking.type=disabled`（官方轴） | **1106** |
| ch77 `reasoning_effort=none`（官方轴） | **574** |
| **官方 ch8 `reasoning_effort=none`** | **0** |
| 官方 ch8 `chat_template_kwargs.enable_thinking=false` | 936（**该字段官方也忽略**） |

两个结论：

1. **在 ch77（YJDY 上游）上，`minimal` 不是"关"**——它比默认还多；连官方轴
   （`none` / `thinking.type=disabled`）在这里也关不掉。
2. **`chat_template_kwargs` 在任何渠道上都是无效元数据**：官方忽略它，ch77/ch49 直接
   400 拒绝整个请求。

## 三、后果

| 环节 | 现状 |
| --- | --- |
| 客户体验 | 明确要求"不要思考"，仍收到推理内容；若平台按官方契约剥离推理，客户还会为看不见的 token 付费 |
| 计费 | 推理 token 照跑照计费，平台以为自己已换成"关"的语义 |
| 可用性 | 携带该字段的请求在不认它的上游直接 400（ch77 实测 5.6% 失败） |

这不是某一个渠道的配置问题，而是**能力建模缺失**：平台没有任何"该渠道能否真正关思考"的
可验证标记，却对客户做出了这层承诺。

## 四、现有扩展点（提案的落点）

`pkg/fitpolicy` 已经具备所需机制：

- **behaviour（能力标记）**：稳定标识符，如 `usage.thinking_counting`、`response_format.json`；
- **rule**：请求特征 → 必需能力，例如现有
  `{ID: "k3-thinking-off", When: 'ThinkingDisabled() || ReasoningEffortIs("none") || ReasoningEffortIs("minimal")', Require: []string{BehaviorThinkingCounting}}`；
- **AdmissionBattery**：非官方型渠道必须携带的实测能力集（`AdmissionSource: measured`）；
- **UnknownMarkPolicy: UnknownMarkConservative**：能力缺失时保守处理（这正是"缺少标记就不要
  装作兑现"所需要的语义）。

注意现有 `k3-thinking-off` 要求的是 `usage.thinking_counting`（会不会**统计**推理 token），
**不是**"请求关思考时是否真的不产生推理"——后者今天没有任何标记。

## 五、提案

### 5.1 新增能力标记

```
reasoning.disable_honoured        // 关思考意图在该渠道上被真正兑现
```

纳入 `familyKimiK3` 的 `Behaviors`，并**加入该族的 AdmissionBattery**：非官方型渠道必须通过
探针实测才能获得该标记。

### 5.2 探针定义（交给 CDP 套件测）

对以下三种"关思考"表达各发一次请求，断言 `completion` 中推理 token 数为 0（或低于噪声阈值）：

1. `reasoning_effort=none`
2. `thinking={"type":"disabled"}`
3. `chat_template_kwargs={"enable_thinking":false}`

任一为假 ⇒ 不打该标记。这样"能否关思考"变成**实测事实**，而不是方言里的假定。

### 5.3 规则调整

`k3-thinking-off` 的 Require 增加 `reasoning.disable_honoured`。渠道没有该标记时，按
`UnknownMarkConservative` 走保守路径：**要么路由到有该标记的渠道**（如官方 ch8），
**要么向调用方返回明确错误**——而不是静默多思考、多计费。

### 5.4 识别第三条轴（代码改动）

`RequestView.ThinkingDisabled()` 与 `kimiK3DisabledThinkingRequest()` 目前只认官方两轴。
应把 `chat_template_kwargs.enable_thinking=false` 也识别为关思考意图，使"识别 → 路由 → 兑现"
三者一致。当前它在任何渠道都不生效，识别它不会改变已兑现的行为，只会让路由与计费判断正确。

### 5.5 方言常量按渠道取值

`kimiK3AggregatorDisabledEffort` 应从"全局常量"变为"有该标记的渠道才使用 minimal 翻译"：
没有标记的渠道不做翻译，避免制造"已兑现"的假象。

## 六、已实施的临时措施（2026-10-09，已生效并验证）

给 ch77、ch49 的 `param_override` 各追加：

```json
{"mode":"delete","path":"chat_template_kwargs"}
```

- 效果：该字段不再到达上游；ch77 失败 5.6% → **0%**，平台整体失败率 2.25% → **0.02%**
- 依据：该字段在任何渠道上都不生效（官方 ch8 实测同样忽略），删除它使这两个渠道与**官方渠道
  的实际行为对齐**，而不是发明一个承诺
- **明确未做**：不附加 `set reasoning_effort=minimal`——那会把"诚实的 400"变成"静默多计费"

## 七、风险与取舍

| 选项 | 优点 | 代价 |
| --- | --- | --- |
| 本提案（能力探针 + 路由约束） | 承诺与事实一致；计费正确 | 需改 fit 策略与 CDP 探针；部分渠道会失去关思考流量 |
| 只做 5.4（识别第三轴） | 改动小 | 仍无能力标记，路由无从判断 |
| 保持现状 + 文档说明 | 零改动 | 客户继续为要求关闭的推理付费 |

**推荐**：5.1 + 5.2 + 5.3（能力化与路由），5.4/5.5 作为配套。实施前需要明确：没有该标记的
渠道在收到关思考请求时，是**改路由**还是**报错**——这是产品决策，不是实现细节。

## 八、教训

把一个上游的实测结论固化成"这一类渠道"的常量，会让平台在能力缺失时仍然对外承诺——
**单点实测只能支撑单点结论；要支撑类级别的承诺，必须有类级别的可验证标记。**
