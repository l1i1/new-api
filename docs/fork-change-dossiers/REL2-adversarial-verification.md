===REL2-BEGIN===
# 对抗式验证与落地清单（第 2 轮）

## 0. 结论摘要
- 图谱可信度：高。REL1 的 27 条契约（C01–C27）在当前树（tokeness/main @ 97b83c2c0）逐条 grep，**全部锚点真实存在**；行号抽查 30+ 处全部命中（含 :262/:283/:449/:721/:1196/:1466/:1484/:1657/:1962/:2095/:2510 等精确行号）。
- 硬错误：**2 处**，均在档案侧而非图谱侧——①G06 引用的测试名 `TestGlm53OrphanRuleIsFamilyScoped` 在当前树不存在（已被 49e6ce52f 重写为 `TestGlm53OrphanRuleProfileGate`，REL1 §4.1 判断属实）；②REL1 C13 写「spec.go:181-236 共 5 条 glm-5.3 行」，该区间实为 **6 条**（:181/:193/:204/:215/:226/:236），其中 :236 的 family.whole 是 1f068e545 之前已有（spec.go:177 注释自证），提交本身新增 5 条——表述需修正。
- 提交覆盖：76/76 恰好一次、无重复、全部可达、5 个 merge 父子关系逐一对账属实；无归组错误。
- 上游基线：抽检全部 22 条 fork-only/fork-ahead 判词（远超 1/3），**0 条推翻**。
- 最终要不要新增建档：**要**。给出 11 条新 id + 1 条修订；另有 5 类不建议新增（§8）。

## 1. 锚点验证结果（逐条：项 / 命令形态 / 结果 / 纠正）
通用命令：`grep -n '<符号>' <文件>`、`git cat-file -e upstream/main:<路径>`、`git grep <符号> upstream/main`。逐条结果（仅列非「命中属实」项；其余全部属实）：

| 项 | 验证 | 结果 | 纠正 |
| --- | --- | --- | --- |
| G06 `TestGlm53OrphanRuleIsFamilyScoped` | `grep -n` glm53_customer_contract_test.go | **0 命中**；现名 `TestGlm53OrphanRuleProfileGate`(:105) | G06 该名作废；manifest 引用的 3 个测试名全部存在（:36/:157/:186 复核），门禁无碍 |
| REL1 C13「5 条 glm 行」 | `grep -c 'Family: "glm-5.3"' scripts/fit-probe/spec.go` → 6（区间 181–236 内） | 6 条 | 提交新增 5 条 + 既有 family.whole 1 条；锚点区间写成「:181–236（含既有 family.whole）」 |
| G03 行号 `relay.go:1029/:1060` | `grep -n retryFoundNoPinnedCandidate\|recordNoCandidateRetryError controller/relay.go` | 实为 :999（func）/:375（调用）/:1046 | 行号漂移，符号真实；G03 应更新为现树行号 |
| G02「manifest :2621 起」 | `grep -n retry-error-fidelity` manifest.json | 实为 :2620 | 漂移 1 行，无碍 |
| REL1 §0 命令 `.44..7ba7e0ab1` | `git rev-parse .44` | **无效引用**（fatal: 需要一个单独的版本） | 本仓库不存在 `.44` ref；用等价命令（§3）替代 |
| `shrink_hold_write`（REL1 §3 草稿锚点） | `grep -n shrink_hold_write deploy.sh` | :2003/:2117 命中 | 存在，可作锚点（REL1 未给行号，补 :2003） |
| `verify_no_extra_removals` | 同上 | :2510 命中 | 存在 |
| C22「release-local.sh:145-155 比对块」 | `sed -n '145,165p'` | imagetools inspect :145；digest 读取实际在 :162 | 锚点区间修正为 :145–:162 |
| C08 缺口「新增头无直接断言」 | `grep -c 'x-goog-api-key\|mj-api-secret\|sec-websocket' middleware/request_capture_test.go` | 0 | G05 的缺口记录属实，维持 |
| C24 测试锚点 | deploy-test.sh:1362（case）/ :1373（`not exactly OldestInstance`） | 命中 | REL1 的 :1362/:1373 两个写法都真实 |
| guard-test.sh 挂入 CI | `grep -n guard-test tests/deploy-test.sh` → :1850 | 命中 | 属实 |
| `TestZZPrintCandidateHash`、`TestR20DeepSeekProbesPinTheThinkingState`(:2351) | grep | 命中；后者确无 glm 断言（:2363-2400 仅 deepseek/kimi） | G07 缺口记录属实 |

其余 C01–C27 的全部文件/符号/测试名（`NormalizeOpenAIStreamError`、`ResponseCommitStateOf`、`retry_error_fidelity.go`、`ChannelSelectionUnprovableExhaustion`:72、`filterAbilitiesByVideoCapability`:282、`officialFitPinKeepsVerdict`:150、`isCredentialExhaustedFailure`:252、`anotherCredentialAvailable`:281、`RelayRequestContextDone`:28、`request_context_done`:46、四个 `Unwrap()`（relay.go:721/request_capture.go:31/audit.go:46/nonstream_keepalive.go:292）、`SHUTDOWN_TIMEOUT_SECONDS`:276、`budget already exhausted`:304、`option.Key != "ForceRetryStatusCodes"`:283、`glm53OrphanToolMessage`:1681、`applyGlm53ToolChoiceNone`:1722、`IsStrictGlmValidationMessage`:1785、`TestZZPrintCandidateHash`:10、deploy.sh 15 个函数、guard 4 个符号、manifest `removed_started` 锚点 :2530、i18n 7 locale overrides、fork-overlay.test.ts:100、merge-allowlist.json 2 条 reason、main.test.mjs:175 allowlist 用例）——**全部逐条 grep 命中，无一编造**。REL1 断言的「已删/已消失」项（`c.Status(streamErr`、`relayClientGone`、`client_gone` 调用点、`relay_sticky_status_repro_test.go`、`TestDecideRelayRetryStopsWhenClientGone`、`TestSuiteResultHonorsItsOwnExpiry` 函数体）均复核为 0 命中，属实。

## 2. 上游基线抽检（维持 22 条 / 推翻 0 条）
命令：`git show upstream/main:<path>`、`git grep <符号> upstream/main`（upstream/main = 42e694eb6，与图谱声明一致；`git rev-list --count 42e694eb6..upstream/main` = 0，上游未前进）。
| 判词 | 证据（本轮实测） | 判定 |
| --- | --- | --- |
| C01 fork-only | upstream relay-openai.go 仅 371 行、只调 `StreamScannerHandler`，无 NormalizeOpenAIStreamError/ResponseCommitStateOf/流内 error 循环（grep 全 0） | 维持 |
| C03 fork-only | `RetryErrorFidelity` 上游 0 命中；上游 `DecideRelayRetry`(:15-58) 仅裸 ErrorCodeGetChannelFailed 形态 | 维持 |
| C04 fork-only | `ChannelSelectionUnprovableExhaustion` 0 命中 | 维持 |
| C05 fork-only | `officialFitPin`/`credentialExhausted`/`auth_unavailable` 上游全 0；上游 pin 确为 `dto.ChannelPin`（dto/channel_constraints.go:29）+ `PinRetrySingleAttempt`——G04 所述「另一形态」属实 | 维持 |
| C06 fork-ahead | 上游 DecideRelayRetry 无上下文检查；`pkg/perf_metrics/outcome.go:27-30` 确把 canceled 记 OutcomeIgnored（机制在结果分类层，非重试决策层）——G04 表述精确 | 维持 |
| C07/C08 fork-only | 上游 audit.go Unwrap=0；`middleware/request_capture.go` 不存在 | 维持 |
| C09/C10 fork-only | 上游 main.go 仅 `SHUTDOWN_TIMEOUT_SECONDS`(:235)+Shutdown，无 drain 循环（BACKGROUND_DRAIN=0）；上游 option.go `loadOptionsFromDatabase` 无条件全量 apply、无 ForceRetryStatusCodes 豁免 | 维持 |
| C11/C12 fork-only | 上游 valid_request.go/distributor.go 中 glm/GLM/Zhipu/1211 全 0 | 维持 |
| C13/C14 fork-only | 上游无 scripts/、无 pkg/fitpolicy | 维持 |
| C15–C20 fork-only | `git ls-tree upstream/main` 顶层无 deployment/ | 维持 |
| C21/C22 fork-only | 上游无 release-local.sh/release-on-master.sh；上游 Dockerfile:7 `COPY ./VERSION`（跟踪文件直 COPY，无构建时写+恢复契约）——与图谱「上游把 VERSION 作为跟踪文件直接 COPY」一致 | 维持 |
| C26 fork-only/fork-ahead | 上游无 scripts/fork-invariants、无 i18n overlay；上游 zh.json 确含 `Disabled {{count}} keys`（128089fa7） | 维持 |
| C27 fork-only | 上游无 scripts/ | 维持 |
**推翻 0 条**。附带收获：REL1 不确定项 9 部分解决——fork 的 `.cnb.yml` 自身含 digest 认证链（:207-227，CNB 构建→digest 固定→发布前再对账），属 fork-only 且加强 C22。

## 3. 提交覆盖核对（命令 + 结果）
```
awk -F'\t' '{n=split($3,a," "); for(i=1;i<=n;i++) print a[i]}' groups.tsv | wc -l   # → 76
sort | uniq -d | wc -l                                                              # → 0
while read s; do git cat-file -e $s^{commit} || echo MISSING; done                   # → 全部存活
# 可达性（.44 无效，用等价命令）：
while read s; do git merge-base --is-ancestor $s 97b83c2c0 || echo NOT-ANC; done     # → 0 条输出
while read s; do git merge-base --is-ancestor $s 7ba7e0ab1  || echo NOT-ANC; done    # → 0 条输出
git rev-list 42e694eb6..7ba7e0ab1 --not upstream/main | cut -c1-10 | sort            # 869 个提交；76 个 sha 各出现恰一次（无 0 次、无 >1 次）
git rev-list --count --merges 42e694eb6..7ba7e0ab1 --not upstream/main              # 90（76 中仅 5 个是 merge，均已对父）
```
- merge 父子对账：`26998b80e`=203ce0128+baf4273c1、`9945d1e6b`=dff1b2d7f+**282180e99**（`git merge-base --is-ancestor c709d6571 9945d1e6b` rc=1，虚报属实）、`2f6e80ff2`=955c624ee+c709d6571、`2bf08ab3f`=63f6631a8+2f6e80ff2、`b7ac05af2`=9653d6975+5104746c5——REL1 §0/§2 全部属实。
- `6e21c76cb`/`dca044b9e`：deploy.sh blob hash 同为 `af68157c2`（git hash-object 复核），互不为祖先（双向 rc=1）——REL1 的「平行落点」判定无法进一步证伪，维持不确定项。
- 归组抽查：G01（502 线 5 sha）、G04（事故包 7 sha）、G08/G09/G10（发版线 32 sha，subject 全部 2026-10-10 deploy 域）、G11（7 sha 全 release 链路）、G12（7 sha 含 merge）与组名一致。**遗漏 0 / 重复 0 / 归组错误 0**。
- `7ba7e0ab1..97b83c2c0` = 3 个 docs 提交（97b83c2c0/f78a48287/da7c24962），groups.tsv 未纳入——正确（非发版审查范围）。

## 4. 契约合并/拆分判定
| 图谱条目 | 判定 | 理由 |
| --- | --- | --- |
| C02并入C01 | **维持合并** | C02 仅是 C01 的 E2E 验收入口（同 2 个测试），单独立条无锚点增量 |
| C03 与 C04 | **维持分立** | 现树同时存在 `retryFoundNoPinnedCandidate`（relay.go:999，C03 链终态）与 `ChannelSelectionUnprovableExhaustion`（channel_select.go:72，C04 终态）；manifest 两 id 锚点互不重叠（routing_error 行 vs exhaustion kind），G02/G03 分界（三态开关 vs 证据纪律）成立 |
| C05（含 282180e99 本地计费排除） | **维持一条** | 282180e99 只是给 `isCredentialExhaustedFailure`(:252，含 `ErrorTypeNewAPIError` 检查 :256) 加例外，同一函数同一契约，拆开会造成「逃生口不完整」的假条目 |
| C06（v1→v2） | **维持一条** | v1 符号已灭（0 命中），v2 是唯一终态 |
| C15 与「回滚走同协议」（158cbcf91） | **维持合并** | 回滚调用的 `drain_before_scale_in`(:897) 就是 C15 的锚点，共享同一批 tests；G09 建议的 `cn-rollback-through-parked-set-protocol` 不单列，并入本条 title |
| C16 与 C25 | **维持合并进一个 id** | 同一互斥/fail-closed 协议的发布侧与守卫侧，锚点分属 deploy.sh/ecs-shrink-guard 但共用 guard-test.sh；G09 建议的 `cn-drain-marker-leased-and-owned` 并入本条与 C15 的锚点集，不单列 |
| C19 与 C20 | **维持拆分** | C19（旋转闸门 instance_streams_busy/drain_window_with_quiet_check，无专项测试）与 C20 前半（oldest_park_ids + tie 测试套）锚点、测试、事故形态均不同 |
| C21 与 C22 | **维持拆分** | 构建树契约 vs registry 协议契约，文件与失败模式不同 |
| C12 的「ID 状态机」 | **不拆新 id，修订既有条目**（见 §7） | 与 C11 共享 `glm-group-contract-and-cdp-instruction-following`；单列会造成同函数两 id |

## 5. 处置一致性判定
| 分歧 | 档案 vs 图谱 | 最终判定 |
| --- | --- | --- |
| C23（master 本机 rollout） | G08「建议补断言」vs REL1「可弃用」 | **可弃用（脚本）/ 检查表项（语义）**——与 gateway-migration-inventory §8「只迁语义」一致；无测试锚点、单机拓扑特有，不建档 |
| C13/C14（测量工装） | G07 与 REL1 一致（建议/可弃用） | 维持：C14 不建档（语义已被 `POST /api/fit-policy/validate` 覆盖，controller/fit_policy.go:197）；C13 随拍板、低优先建档 |
| C26（门禁+i18n） | G13「可弃用脚本/建议继承语义」vs REL1「不迁移脚本本体」 | 一致，维持 |
| G09 的 4 条建议 id vs REL1 的 3–4 条 | 边界与命名不同 | **按 REL1 边界收编**（§4 判定）：marker lease 并入 drain/mutex 两条，rollback 并入 C15 条，victim-selection 独立保留 |
| G05/G12 的 `kill -0` 演进 | 7c2c8311f 引 `$PPID` → 58f8c5cd0 改 `$$` | 复核属实（现树 deploy.sh:213 为 `kill -0 "$$"`，:210 注释自证原因）——无矛盾，维持 REL1 §4.5 |

## 6. 落地清单 A：新增/修订 FORK-CHANGES 行（可直接照抄）
追加到 §8「运营、合作方与部署」表（deploy 域）与 §5（Go 域）。格式同现有表：| 改造 | 关键文件 | 检测 |

```markdown
| 发版互斥与 fail-closed 读：一次只许一个 release（hold 即锁：flock 下 check-and-acquire、三态退出码、fresh 外来 hold 拒绝、不可解析即拒绝发布）；所有远端读「失败≠没有」，守卫与发布写同一 marker/hold 协议，Modify 前在共享锁段复查所有权 | `deployment/tokeness-cn/deploy.sh`、`deployment/tokeness-cn/ecs-shrink-guard` | `deployment/tokeness-cn/tests/deploy-test.sh`（`release-mutex`、`release-no-headroom`）、`deployment/tokeness-cn/tests/guard-test.sh`（经 deploy-test.sh:1850 挂入 CI：idle park owner/lease 三行断言、foreign marker 不删） |
| scale-in 告警生命周期端到端 fail-closed：查询失败≠无告警、禁用前记住原状态、恢复失败重试 3 次后保留 hold、退役 pending 时告警保持关闭 | `deployment/tokeness-cn/deploy.sh`（`scale_in_alarm_id`、`suspend_scale_in_guard`、`resume_scale_in_guard`、`RETIREMENT_PENDING`） | `deployment/tokeness-cn/tests/deploy-test.sh`：`scale-in-alarm-restore-fail`、`retire-pending`、`scale-in-alarm-missing`、`scale-in-alarm-guard`、`scale-in-alarm-pre-disabled` |
| relay 排空 park 协议：先 park 再排空、marker 为 owner+lease v2（写/清/刷新同锁）、按 ID 等全量离场才清 marker、回滚走同一 park 协议、park 不了就大声失败 | `deployment/tokeness-cn/deploy.sh`（`oldest_instance_ips`、`relay_drain_write/clear/refresh`、`wait_drain_applied`、`drain_before_scale_in`） | `deployment/tokeness-cn/tests/deploy-test.sh`：`rollout-all-scale-in-race`（"parked the retiring instance"）、"a relay drain marker was left behind after the release"、`rollout-all-batch` |
| ESS 受害者选择「不可预测就不猜」：秒级 CreatedTime 取整 n 个最旧、边界平局拒绝本轮退役、策略非 OldestInstance 拒绝、平局 park 整个 tie 集合 | `deployment/tokeness-cn/deploy.sh`（`oldest_instance_ids`、`oldest_park_ids`） | `deployment/tokeness-cn/tests/deploy-test.sh`：`oldest-policy-refused`（"not exactly OldestInstance" + eci-old 仍在）、`rollout-batch-created-time-tie`、`release-created-time-tie` |
| 零切流旋转闸门：摘流前 `nginx -T` 交叉验证上游成员、排空窗静默检查、退役预算 `removed_started` 计入在途 | `deployment/tokeness-cn/deploy.sh`（`instance_streams_busy`、`drain_window_with_quiet_check`、`ecs_upstream_members_running`、`scale_group_from_set`） | 专项断言缺（G12 记录属实）；依赖 `release-created-time-tie` 等 run_deploy 套件间接覆盖——验收入口缺口 |
| registry digest 先对账再部署；release check 接受 401 为「可达」并证明拉取授权 | `deployment/tokeness-cn/release-local.sh`、`deployment/tokeness-cn/release-on-master.sh`、`.cnb.yml`（tag_push digest 认证链） | `deployment/tokeness-cn/tests/deploy-test.sh`：`release-certified-digest`；401/pull-auth 无自动化测试（缺口） |
| 本机构建发版：构建树必须干净（dirty 拒绝、untracked 仅 warn）、release tag 不可变（重复 tag 拒绝）、GOPROXY 默认官方（大陆加速走参数）、VERSION 构建时写+恢复 | `deployment/tokeness-cn/release-local.sh`、`Dockerfile.tokeness` | 无自动化测试（缺口，记录在案） |
| 测试假件与生产同边界（fake ssh/aliyun 的 fleet-sync 派生、Modify 边界、TOKENESS_TEST_* 钩子） | `deployment/tokeness-cn/tests/fake-bin/ssh`、`tests/fake-bin/aliyun` | 经 deploy-test.sh/guard-test.sh 全套间接验证；判据照搬、测试重写（AGPL） |
```
**修订既有行**：§5「GLM 组契约与 cdp 指令遵循」行的检测列补 `TestGlm53OrphanRuleProfileGate`（现树 :105，替代档案旧名）。

## 7. 落地清单 B：新增/修订 manifest.json 条目（可直接照抄）
全部锚点已在本轮逐条 grep 验证存在。测试名用 case 目录/断言字符串前缀（与既有 `tie-retirement-budget-` 前缀写法同风格）。

```json
{ "id": "cn-release-mutex-fail-closed-reads", "title": "发版互斥：hold 即锁（flock 下 check-and-acquire、三态退出码、fresh 外来 hold 拒绝、不可解析拒绝发布），所有远端读 fail-closed，守卫与发布同协议", "risk": "high", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/deploy.sh", "deployment/tokeness-cn/ecs-shrink-guard", "deployment/tokeness-cn/tests/deploy-test.sh", "deployment/tokeness-cn/tests/guard-test.sh"],
  "anchors": [
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"MARKER_LOCK_PATH"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"release_hold_is_fresh"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"RELEASE_OWNER"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"suspend_scale_in_guard"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"shrink_hold_write"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"verify_no_extra_removals"},
    {"kind":"symbol","file":"deployment/tokeness-cn/ecs-shrink-guard","name":"DRAIN_OWNER"},
    {"kind":"symbol","file":"deployment/tokeness-cn/ecs-shrink-guard","name":"shrink_section"}],
  "tests": [
    {"file":"deployment/tokeness-cn/tests/deploy-test.sh","names":["release-mutex","release-no-headroom"]},
    {"file":"deployment/tokeness-cn/tests/guard-test.sh","names":["an idle tier above the floor did not park a drain marker","a live foreign drain marker was deleted while a release held the tier"]}] }

{ "id": "cn-scale-in-alarm-lifecycle-fail-closed", "title": "scale-in 告警生命周期端到端 fail-closed：查询失败≠无告警、恢复失败重试后保留 hold、退役 pending 时告警保持关闭", "risk": "high", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/deploy.sh", "deployment/tokeness-cn/tests/fake-bin/aliyun"],
  "anchors": [
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"scale_in_alarm_id"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"resume_scale_in_guard"},
    {"kind":"contains","file":"deployment/tokeness-cn/deploy.sh","contains":"RETIREMENT_PENDING"}],
  "tests": [{"file":"deployment/tokeness-cn/tests/deploy-test.sh","names":["scale-in-alarm-restore-fail","retire-pending","scale-in-alarm-missing","scale-in-alarm-pre-disabled"]}] }

{ "id": "deploy-relay-drain-park-before-hold", "title": "relay 排空 park 协议：先 park 再排空、marker 为 owner+lease v2、按 ID 等全量离场才清、回滚走同一 park 协议", "risk": "high", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/deploy.sh", "deployment/tokeness-cn/tests/fake-bin/ssh"],
  "anchors": [
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"oldest_instance_ips"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"relay_drain_write"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"relay_drain_refresh"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"wait_drain_applied"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"drain_before_scale_in"}],
  "tests": [{"file":"deployment/tokeness-cn/tests/deploy-test.sh","names":["rollout-all-scale-in-race","parked the retiring instance","a relay drain marker was left behind after the release"]}] }

{ "id": "cn-drain-victim-selection-refuses-unpredictable", "title": "ESS 受害者选择不可预测就不猜：秒级 CreatedTime 取整 n 个、边界平局拒绝、策略非 OldestInstance 拒绝、平局 park 整个 tie 集合", "risk": "medium", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/deploy.sh"],
  "anchors": [
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"oldest_instance_ids"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"oldest_park_ids"}],
  "tests": [{"file":"deployment/tokeness-cn/tests/deploy-test.sh","names":["oldest-policy-refused","not exactly OldestInstance","rollout-batch-created-time-tie"]}] }

{ "id": "cn-zero-cut-rotation-gates", "title": "零切流旋转闸门：nginx -T 交叉验证上游成员、排空窗静默检查、removed_started 计入在途退役", "risk": "medium", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/deploy.sh"],
  "anchors": [
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"instance_streams_busy"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"drain_window_with_quiet_check"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"ecs_upstream_members_running"},
    {"kind":"symbol","file":"deployment/tokeness-cn/deploy.sh","name":"scale_group_from_set"}],
  "tests": [] }

{ "id": "release-registry-digest-confirmed-before-deploy", "title": "registry digest 先对账再部署（本机构建、.cnb.yml tag_push 与发布两段均复核）", "risk": "medium", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/release-local.sh", "deployment/tokeness-cn/deploy.sh", ".cnb.yml"],
  "anchors": [
    {"kind":"contains","file":"deployment/tokeness-cn/release-local.sh","contains":"imagetools inspect"},
    {"kind":"symbol","file":"deployment/tokeness-cn/release-local.sh","name":"registry_digest"}],
  "tests": [{"file":"deployment/tokeness-cn/tests/deploy-test.sh","names":["release-certified-digest"]}] }

{ "id": "release-check-accepts-registry-401-and-proves-pull-auth", "title": "release --check 的 registry 探针接受 401 为可达（-f 会把 401 误报为不可达），可选 tag 转 docker manifest inspect 证明拉取授权", "risk": "medium", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/release-on-master.sh"],
  "anchors": [
    {"kind":"contains","file":"deployment/tokeness-cn/release-on-master.sh","contains":"grep -qE '200|401'"},
    {"kind":"contains","file":"deployment/tokeness-cn/release-on-master.sh","contains":"docker manifest inspect"}],
  "tests": [] }

{ "id": "release-local-dirty-tree-and-immutable-tag", "title": "本机构建：dirty 树拒绝（untracked 仅 warn）、重复 tag 拒绝、VERSION 构建时写+trap 恢复、GOPROXY 默认官方走参数覆盖", "risk": "medium", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["deployment/tokeness-cn/release-local.sh", "Dockerfile.tokeness"],
  "anchors": [
    {"kind":"contains","file":"deployment/tokeness-cn/release-local.sh","contains":"--untracked-files=no"},
    {"kind":"symbol","file":"deployment/tokeness-cn/release-local.sh","name":"restore_version"},
    {"kind":"contains","file":"Dockerfile.tokeness","contains":"ARG GOPROXY="}],
  "tests": [] }

{ "id": "merge-allowlist-visible-supersession", "title": "merge-divergence 的有意取代必须可见登记：allowlist 条目强制 reason，且在门禁输出中计数为 acknowledged 而非隐藏", "risk": "medium", "source": "FORK-CHANGES.md › 8. 运营、合作方与部署",
  "files": ["scripts/fork-invariants/merge-allowlist.json"],
  "anchors": [{"kind":"contains","file":"scripts/fork-invariants/merge-allowlist.json","contains":"\"reason\""}],
  "tests": [{"file":"scripts/fork-invariants/main.test.mjs","names":["an allowlisted path stops failing but stays visible"]}] }

{ "id": "audit-test-fixture-owns-its-connections", "title": "审计测试夹具只关自有连接（不误关被测外层连接），不迁移但需 fork 侧回归", "risk": "low", "source": "FORK-CHANGES.md › 5. 路由与可靠性",
  "files": ["controller/access_token_audit_test.go", "controller/relay_routing_failure_test.go"],
  "anchors": [{"kind":"symbol","file":"controller/access_token_audit_test.go","name":"newAuditTestDatabase"}],
  "tests": [{"file":"controller/relay_routing_failure_test.go","names":["TestNewAuditTestDatabaseLeavesTheCallersConnectionsAlone","TestRecordNoCandidateRetryErrorNeedsARequest"]}] }

{ "id": "fit-probe-glm53-expectation-rows", "title": "fit-probe 的 glm-5.3 期望行（5 条新增 + 既有 family.whole）与 thinking-ON 探测分支", "risk": "low", "source": "FORK-CHANGES.md › 5. 路由与可靠性",
  "files": ["scripts/fit-probe/spec.go", "scripts/fit-probe/probe.go"],
  "anchors": [
    {"kind":"contains","file":"scripts/fit-probe/spec.go","contains":"Family:         \"glm-5.3\""},
    {"kind":"contains","file":"scripts/fit-probe/probe.go","contains":"family == \"deepseek-v4\" || family == \"glm-5.3\""}],
  "tests": [{"file":"scripts/fit-probe/regression_test.go","names":["TestR20DeepSeekProbesPinTheThinkingState"]}] }
```
**修订既有条目** `glm-group-contract-and-cdp-instruction-following`（不新增 id）：
- anchors 追加：`{"kind":"symbol","file":"relay/helper/valid_request.go","name":"glm53OrphanToolMessage"}`(:1681)、`{"kind":"symbol","file":"relay/helper/valid_request.go","name":"applyGlm53ToolChoiceNone"}`(:1722)。
- tests 追加（glm53_customer_contract_test.go）：`TestGlm53OrphanRuleProfileGate`(:105)、`TestGlm53ToolChoiceNoneWithholdsToolDeclarations`(:186)。
理由：G06 建议的 `glm-orphan-tool-id-state-machine` 与本条是同一函数同一契约，单列会出现「一条契约两个 id」。

## 8. 不新增的项与理由
| 项 | 理由 |
| --- | --- |
| C23 `deploy-master-local-scope-only` | 单机拓扑特有、tests/ 0 命中（复核属实）、无测试锚点可通过门禁的测试列；gateway-migration-inventory §8 已判「只迁语义」，作检查表项即可 |
| C14 `fitpolicy-candidate-hash-prebind` | 一次性运维助手；语义已被 `POST /api/fit-policy/validate`（controller/fit_policy.go:197 `validateFitPolicyDocument`，编译即返回 hash）覆盖；G07 自己也判「可弃用」 |
| C26 脚本本体建档 | 门禁/overlay 是本仓库配套机制不迁移；键让位与锚点同步语义已被既有 id（`卡片本地化…-7-语言键集合契约…`、overlay 机制行）覆盖 |
| 三个 merge 提交单独建档（G04 建议 `incident-2026-10-10-merge-chain`） | merge 无行为契约；9945d1e6b 虚报事实已固化在 merge-allowlist.json reason 里，再建 id 是重复登记 |
| C18 假件独立 id | 假件是测试基建，随 `cn-release-mutex…`/`deploy-relay-drain…` 两条的 tests 列覆盖；单独建档会把「测试夹具」升格为「行为契约」，且 AGPL 下新网关必须重写而非移植 |
| C22 的「缓存源/--no-cache」独立 id | 属构建参数选择（release-local.sh:119 默认 goproxy.cn、Dockerfile 默认官方），非协议事实，已并入 `release-local-dirty-tree…` 的 title |

## 9. 仍不确定的（缺什么证据）
1. **全部测试未运行**（纪律禁跑 go test / deploy-test.sh / main.mjs）：shell 用例的运行期依赖（rendezvous、共享 mktemp、顺序）只经静态阅读；`release-mutex` 的会合逻辑（:975-1000）理论上可 flake，未实证。
2. **事故数字全部是二阶陈述**：136 条 502、1.881%/110 条、80 条 type=5、96% 假失败、p99 765s 等——无 access log 原件可核。
3. **`.44..7ba7e0ab1` 命令无法执行**：本仓库无 `.44` ref；本轮用 `42e694eb6..7ba7e0ab1 --not upstream/main`（869/779）等价替代，76 个 sha 覆盖结论不受影响，但任务原文命令本身待澄清。
4. **`cn-zero-cut-rotation-gates` 无测试锚点**（tests=[]）：G12 已如实记录「代码在、断言缺」；登记后应把补断言列为待办，否则该条只由锚点守护。
5. **上游 42e694eb6 之后是否吸收 C01/C03**：`git rev-list 42e694eb6..upstream/main` = 0，上游尚未前进，该问题暂无对象；下次 rcNN 同步时需重跑 §2 的 grep。
6. **`6e21c76cb`/`dca044b9e` 平行落点的成因**：blob/author/date 全同、父不同、互不为祖先、无 cherry-pick 痕迹——git 证据已穷尽，属不可判定项。
7. **生产「无 Content-Type」报文等价性**：仅 httptest 抓线（`TestWriteZhipuBusinessErrorOmitsContentTypeOnTheWire`）佐证，未抓真实网关包。
8. **i18n 7 个 locale 的 overrides 译文**：本轮抽查 en/zh/ja/ru 形态（−2/+2、尾部 overrides），fr/vi/zh-TW 未逐行核对译文文案。
===REL2-END===
