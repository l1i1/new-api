# 提交级变更档案 · mainland.49 的 76 个提交

**生成**：2026-10-13 · **基线**：`tokeness/main @ 97b83c2c0`（发版 `v1.0.0-rc.40-tokeness-mainland.49` + 本轮建档提交）· **官方基线**：`upstream/main @ 42e694eb6`
**生成方式**：13 个组各由一次独立的 `pi -p --model Tokeness-CN/glm-5.3-flash --thinking max` 只读考古产出（每组建档一个目录文件），随后两轮关系分析：`REL1-contract-graph.md`（契约图谱 C01–C27 + 覆盖缺口）→ `REL2-adversarial-verification.md`（对抗式验证 + 可落地清单）。全部读同一棵只读树，禁止写仓库、禁止跑测试。

## 这份目录解决什么

`FORK-CHANGES.md` 是**行为契约的现状清单**（人读表格），`manifest.json` 是**门禁锚点**（机读）。两者都以"域"为单位，读不出"某一个提交到底改了什么、上游有没有对应物、这条在不在契约里"。本目录补的就是这一层：**逐提交**记录意图、改动（`文件:行`）、**官方基线四分类判词**、行为契约、证据（测试/符号）、事故数字与迁移建议。

不在本目录内：上游自带功能、纯文档、纯发布流程的脚本实现细节（只记语义）。

## 目录

| 组 | 主题 | 提交 | 档案 |
| --- | --- | --- | --- |
| G01 | 502 状态污染修复线 | 5 | [G01.md](G01.md) |
| G02 | 重试失败保真三态 | 3 | [G02.md](G02.md) |
| G03 | pin/拟合「候选耗尽」必须可证明 + 饱和豁免 | 5 | [G03.md](G03.md) |
| G04 | 2026-10-10 事故包：凭据逃生口 + 停链 v2 | 7 | [G04.md](G04.md) |
| G05 | 观测/审计/写超时/预算加固（B1/B3/B7/B8/B9/B10/B11） | 4 | [G05.md](G05.md) |
| G06 | GLM 组契约漂移 + cdp 指令遵循 | 3 | [G06.md](G06.md) |
| G07 | 测量工装：fit-probe 与 fitpolicy 测试收编 | 2 | [G07.md](G07.md) |
| G08 | 发版：排空/park/hold 核心协议 | 10 | [G08.md](G08.md) |
| G09 | 发版：退役选择/回滚/lease/hold 精化 | 12 | [G09.md](G09.md) |
| G10 | 发版：guard/alarm fail-closed（review 6–13） | 10 | [G10.md](G10.md) |
| G11 | 发版：本地构建优先/registry digest/镜像代理/master 上跑 rollout | 7 | [G11.md](G11.md) |
| G12 | 发版：平局退役预算与完成证明 + 8 项切流整改 | 7 | [G12.md](G12.md) |
| G13 | 发版门禁与 i18n 收口 | 1 | [G13.md](G13.md) |
| — | 契约图谱（第 1 轮：C01–C27 + 缺口 + 一致性告警） | — | [REL1-contract-graph.md](REL1-contract-graph.md) |
| — | 对抗式验证 + 落地清单（第 2 轮） | — | [REL2-adversarial-verification.md](REL2-adversarial-verification.md) |

## 验证结论（第 2 轮，对抗式）

- **提交覆盖**：76/76 恰好一次、无重复、全部在 HEAD 可达；5 个 merge 的父子关系逐一对账属实。
- **上游基线**：抽检 22 条 `fork-only`/`fork-ahead` 判词（远超 1/3），**0 条被推翻**。档案内逐提交判词统计：`fork-only` 99、`fork-ahead` 9、`无法判定` 1。
- **锚点**：图谱 C01–C27 的文件/符号/测试名逐条 grep **全部命中**（含 30+ 个精确行号）；发现 **2 处档案侧硬错误**并在 REL2 中纠正：
  1. G06 引用的 `TestGlm53OrphanRuleIsFamilyScoped` 在当前树不存在（49e6ce52f 重写为 `TestGlm53OrphanRuleProfileGate`）——已按现树改正，并把该名补进 §5 行与 manifest 的 tests。
  2. REL1 称 `spec.go:181-236` 有 5 条 glm-5.3 期望行，实为 6 条（其中 `family.whole` 在 `1f068e545` 之前已存在）——已在 REL2 记录。
- **落地**：11 条新 id + 1 条修订已并入建档（见下），门禁 **75 条 / 360 锚点 / 5-5 绿**（此前 64 条 / 324 锚点）。

| 新增 manifest id | 组 |
| --- | --- |
| `cn-release-mutex-fail-closed-reads` | G08/G09/G10 |
| `cn-scale-in-alarm-lifecycle-fail-closed` | G10 |
| `deploy-relay-drain-park-before-hold` | G08/G09 |
| `cn-drain-victim-selection-refuses-unpredictable` | G09 |
| `cn-zero-cut-rotation-gates` | G12 |
| `release-registry-digest-confirmed-before-deploy` | G11 |
| `release-check-accepts-registry-401-and-proves-pull-auth` | G11 |
| `release-local-dirty-tree-and-immutable-tag` | G11 |
| `merge-allowlist-visible-supersession` | G01/G13 |
| `audit-test-fixture-owns-its-connections` | G02 |
| `fit-probe-glm53-expectation-rows` | G07 |

修订：`glm-group-contract-and-cdp-instruction-following` 追加 `glm53OrphanToolMessage`、`applyGlm53ToolChoiceNone`、`IsStrictGlmValidationMessage` 三个锚点与 `TestGlm53OrphanRuleProfileGate`。
**有意不新增**（REL2 §8 给理由）：`deploy-master-local-scope-only`（单机拓扑特有）、`fitpolicy-candidate-hash-prebind`（一次性运维助手，语义已被 `/api/fit-policy/validate` 覆盖）、门禁/i18n 脚本本体、三个 merge 单独建档、测试假件独立 id、构建参数选择。

## 怎么用（迁移到自研网关）

1. 先读 [REL1 契约图谱](REL1-contract-graph.md) 的 C01–C27：那是**契约级**视图，含迁移处置（必须/建议/可弃用/不迁移）。
2. 要落到某个提交的原始证据时读对应 `G*.md`：每条含 `文件:行`、上游对比命令形态、测试名。
3. **许可边界**：这些档案是**行为规格**，不是移植指南——新系统不得复制 new-api 的代码，测试代码同样算（AGPL）。判据照搬、测试重写。
4. **纪律**：改行为 → 同批更新 `FORK-CHANGES.md` 表格 + `manifest.json` 锚点；本目录的对应小节与 `docs/gateway-migration-inventory.md` 在下一次导出时同步。
5. 已知缺口（REL2 §9）：`cn-zero-cut-rotation-gates` 与两条 release-* 条目**没有测试锚点**（据实登记，补断言列为待办）；全部测试**未运行**（纪律禁跑）；事故数字为二阶陈述（提交信息/值守记录）。

## 复现

```bash
# 只读树
git worktree add /tmp/wt-dossiers tokeness/main
cd /tmp/wt-dossiers
# 单组建档（示例：502 线）
pi -p --model Tokeness-CN/glm-5.3-flash --thinking max --exclude-tools write,edit \
  @/home/vhbs/workos/scratch/fcdoc/prompts/G01.md
# 两轮关系分析见同目录 PROMPT_REL1.md / PROMPT_REL2.md；组定义见 groups.tsv
```

审计时看这三样即可：本目录的 `G*.md`（原始档案）、`REL1`/`REL2`（收敛与证伪）、`manifest.json`（门禁是否仍守得住）。
