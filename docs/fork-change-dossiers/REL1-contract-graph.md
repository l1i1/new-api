===REL1-BEGIN===
# 契约图谱（第 1 轮）· mainland.49 的 76 个提交

## 0. 提交覆盖核对

核对命令与结果（在 `/home/vhbs/wt-fcdoc`，`tokeness/main @ 97b83c2c0`）：

```bash
awk -F'\t' '{n=split($3,a," "); for(i=1;i<=n;i++) print a[i]}' groups.tsv | wc -l   # → 76
awk -F'\t' '{...}' groups.tsv | sort | uniq -d | wc -l                               # → 0（无重复）
while read s; do git cat-file -e $s^{commit} || echo MISSING; git merge-base --is-ancestor $s 97b83c2c0 || echo NOT-ANCESTOR; done  # → 全部存活、全部在 HEAD 可达
```

- 13 组 sha 总数 = 76，两两去重后仍 76，无遗漏、无重复（组间无交叉归属）。
- 其中 **5 个是 merge 提交**：`26998b80e`、`9945d1e6b`、`2f6e80ff2`、`2bf08ab3f`、`b7ac05af2`（父提交关系已逐一复核，见 §4）。merge 无独立行为契约，归入其带入的行为契约。
- 补充核对：`git show -s --format='%P'` 复核了 G04 档案关于 `9945d1e6b` 第二父 = `282180e99`、且 `c709d6571` 不是其祖先的声明，**属实**。

## 1. 契约图谱

（上游基线统一为 `upstream/main @ 42e694eb6`；「已登记」均指在 `scripts/fork-invariants/manifest.json` 64 条里 grep 到）

### C01 失败尝试的状态码不得污染客户响应（尝试级失败 ≠ 请求级结局）
- 提交：`0d1f999d1`（修复）→ `dad2d762a`（E2E 回归）→ `baf4273c1`（清理）→ `26998b80e`（merge 固化）
- 文件/符号：`relay/channel/openai/relay-openai.go:262`（「只把错误交回重试循环」注释块，grep 确认；`c.Status(streamErr` 已不存在）、`service.NormalizeOpenAIStreamError`、`helper.ResponseCommitStateOf`
- 测试锚点：`stream_status_commit_test.go::TestFailedStreamAttemptDoesNotPoisonLaterSuccessfulResponse`(:33)、`TestInBandStreamErrorStillYieldsTerminal5xxWhenNothingRetries`(:90)；`relay_stale_status_e2e_test.go::TestE2EStickyStatusRetryLoopServes200AfterInBandError`(:168)、`TestE2EStickyStatusAllFailuresYield5xx`(:182)——全部 grep 确认
- 上游基线：`fork-only`（证据：`git grep NormalizeOpenAIStreamError|ResponseCommitStateOf upstream/main` exit 1；上游无流内 error 事件识别 + writer 状态机）
- 迁移处置：**必须实现**（各档案一致；丢掉即重演「客户 502+正文、平台计费成功」事故）
- 建档：manifest id `relay-status-commit-fidelity-502` ｜ 附带 `merge-allowlist.json` 两条 allowlist（见 C27）

### C02 端到端换道回归验收集
- 提交：`dad2d762a`（主契约同 C01，此条是它的验收入口）
- 文件/符号：`controller/relay_stale_status_e2e_test.go`、`model.InitChannelCache`
- 测试锚点：同 C01 的 2 个 E2E 用例
- 上游基线：`fork-only`（文件在上游不存在）
- 迁移处置：必须（行为契约的验收）
- 建档：已并入 `relay-status-commit-fidelity-502` 的 tests

### C03 重试失败保真三态（routing_error 入库 + 上游文本脱敏）
- 提交：`d2a125b7b`（建立）→ `08176dfbd`（判据收窄 + `%w` 包装 + admin 域）→ `45dd3774b`（无 kind 不算无候选 + 夹具修复）
- 文件/符号：`setting/operation_setting/retry_error_fidelity.go`（atomic.Value，grep 确认）、`RetryErrorFidelityModeOrDefault`、`controller/relay.go:375 retryFoundNoPinnedCandidate`、`model/log_other.go:27 "last_upstream_error"`
- 测试锚点：`service/relay_error_test.go::TestRetryErrorFidelityGuard`(:512)、`relay_routing_failure_test.go::TestRetryRoutingFailureReadsTheSelectionKind`(:36)、`TestNoCandidateRetryLogRowKeepsTheUpstreamTextOutOfUserScope`(:140)、`TestNoCandidateRetryErrorRowPersistsOnEveryDialect`(:196)、`TestRecordNoCandidateRetryErrorNeedsARequest`(:285)——grep 确认
- 上游基线：`fork-only`（`git grep RetryErrorFidelity upstream/main` exit 1；上游同问题域只有裸 `ErrorCodeGetChannelFailed`）
- 迁移处置：**必须实现**（错误语义：真实裁决优先 + 失败入库 + 脱敏）；pin 相关判定随 fit/pin 产品决策
- 建档：manifest id `retry-error-fidelity-mode-three-states` ｜ 夹具部分**未覆盖**（见 §3 缺口 5）

### C04 「候选耗尽」必须可证明 + 饱和豁免
- 提交：`c66ed310e`（pin 前置判据）→ `9653d6975`（video 过滤器错误传播）→ `24ddf1e56`（pin/fit 路径拒绝推断）→ `d992051da`（专用 kind + 透传 + 豁免，**语义终态**）→ `99abc4dde`（豁免回归测试）
- 文件/符号：`service/channel_select.go:72 ChannelSelectionUnprovableExhaustion`、`classifyChannelSelectionFailure`、`HasSaturatedChannel`、`model/ability.go:282 filterAbilitiesByVideoCapability`（返回 error，grep 确认）、`controller/relay.go:375`
- 测试锚点：`service/zz_pin_exhaustion_test.go::TestClassifyChannelSelectionFailureRefusesExhaustionUnderOfficialPin`(:19)、`TestClassifyChannelSelectionFailureKeepsSaturationKindUnderOfficialPin`(:38)、`model/channel_video_test.go::TestVideoCapabilityFilterReportsAnUnreadableCapability`(:100)、`relay_fitpolicy_reuse_test.go::TestGetChannelReuseBranchCannotIntroduceUnconstrainedChannel`(:39)——grep 确认
- 上游基线：`fork-only`（`git grep ChannelSelectionUnprovableExhaustion upstream/main` exit 1；上游 DB `GetChannel` 无该缺陷形态——见 §4 告警 3）
- 迁移处置：**必须实现**（语义部分：证据纪律与是否 pin 无关）；pin/fit 宿主随产品决策
- 建档：manifest id `provable-channel-exhaustion-under-fit-pin`

### C05 官方 pin 的凭据/额度耗尽逃生口（含本地计费排除）
- 提交：`c376c422c`（引入）→ `282180e99`（排除 `ErrorTypeNewAPIError`，**终态**）→ `9945d1e6b`（merge）
- 文件/符号：`service/relay_error.go:150 officialFitPinKeepsVerdict`、`:252 isCredentialExhaustedFailure`、`:281 anotherCredentialAvailable`——grep 确认
- 测试锚点：`service/relay_error_test.go::TestOfficialFitPinCredentialExhaustionFailover`(:402)、`TestOfficialFitPinKeepsUpstreamVerdict`(:230)
- 上游基线：`fork-only`（`git grep ContextKeyV4OfficialPin upstream/main` exit 1；上游 pin 是 `dto.ChannelPin` 另一形态）
- 迁移处置：**必须实现**（1.881% 可见失败率事故）；两条必须捆绑迁移
- 建档：manifest id `official-pin-credential-exhaustion-failover`

### C06 请求上下文取消后停链（v2：取消不计入渠道失败/禁用）
- 提交：`c709d6571`（v1 `client_gone`，**已被取代**）→ `63f6631a8`（v2 `request_context_done`，**终态**）→ `2f6e80ff2`/`2bf08ab3f`（merge 收敛）
- 文件/符号：`service/relay_error.go:28 RelayRequestContextDone`、`:46 Reason:"request_context_done"`、`controller/relay.go:449`、`channelobservability.RecordAttempt`——grep 确认；`relayClientGone`/`client_gone` 在当前树已不存在（grep 空，v1 取代属实）
- 测试锚点：`TestDecideRelayRetryStopsWhenRequestContextDone`(:472)、`relay_openai_stream_usage_test.go::TestOaiStreamHandlerTreatsClientGoneAsNonError`(:1008)
- 上游基线：`fork-ahead`（上游 `DecideRelayRetry` 无上下文检查；仅 `pkg/perf_metrics/outcome.go` 把 canceled 记 ignored——机制不同层）
- 迁移处置：**必须实现**（按 v2 形态）
- 建档：manifest id `request-context-done-stops-the-retry-chain`

### C07 四层 ResponseWriter 包装器 Unwrap 链（写截止期）
- 提交：`8a381ba85`（2 层）→ `e5fec146f`（补 2 层 + 3 份回归，**终态**）
- 文件/符号：四个 `Unwrap()`——`controller/relay.go:721`、`middleware/request_capture.go:31`、`middleware/audit.go:46`、`relay/helper/nonstream_keepalive.go:292`——grep 确认
- 测试锚点：`TestObservationWriterSetWriteDeadlineReachesConn`(:19)、`TestCaptureWriterSetWriteDeadlineReachesConn`(:18)、`TestAuditWriterSetWriteDeadlineReachesConn`(:44)、`TestKeepAliveGuardedWriterSetWriteDeadlineReachesConn`(:18)
- 上游基线：`fork-only`（上游 audit/capture 文件无 Unwrap；`nonstream_keepalive.go` 不存在）
- 迁移处置：**必须实现**（慢下游占死并发槽）
- 建档：manifest id `response-writer-unwrap-chain`

### C08 请求/响应捕获默认脱敏（多值头全量）
- 提交：`8a381ba85`（扩表）→ `7f0b029ca`（`sec-websocket-protocol` 无条件，**终态**）
- 文件/符号：`middleware/request_capture.go`（`x-goog-api-key`/`mj-api-secret`/`Set-Cookie` 锚点，manifest 命中）
- 测试锚点：`TestCaptureRequestHeadersRedactCredentialsByDefault`（仅覆盖 authorization/x-api-key；**新增头无直接断言**，G05 已记录为验收入口缺口）
- 上游基线：`fork-only`（`request_capture.go` 在上游不存在）
- 迁移处置：**必须实现**（凭据落盘/泄密）
- 建档：manifest id `capture-redaction-defaults`

### C09 master 停止预算（共享预算 + 耗尽仍停收 + 10s 硬顶）
- 提交：`8a381ba85` → `7f0b029ca` → `58f8c5cd0`（goroutine+10s，**终态**）
- 文件/符号：`main.go`（drain + `SHUTDOWN_TIMEOUT_SECONDS` + 「budget already exhausted」锚点，manifest 命中）、`model/utils.go:96`（根因 `<-ctx.Done(): <-done`）
- 测试锚点：无测试锚点（manifest tests=[]）
- 上游基线：`fork-only`（上游 main.go 无 drain 循环，grep BACKGROUND_DRAIN=0）
- 迁移处置：**必须实现**（关机排空被平台掐断）
- 建档：manifest id `master-shutdown-budget-is-shared`

### C10 选项周期同步「值未变即跳过」+ ForceRetryStatusCodes 豁免
- 提交：`8a381ba85` → `7f0b029ca`（豁免但键名写错）→ `58f8c5cd0`（改为 `"ForceRetryStatusCodes"`，**终态**）
- 文件/符号：`model/option.go:283`（`if option.Key != "ForceRetryStatusCodes"`，grep 确认）
- 测试锚点：无测试锚点（manifest tests=[]）
- 上游基线：`fork-only`（上游无条件全量 apply，`grep ForceRetry upstream/main:model/option.go` 零输出）
- 迁移处置：**必须实现**（反射重写竞争 panic / legacy 键静默消失）
- 建档：manifest id `option-periodic-sync-skips-unchanged-values`

### C11 GLM-5.3 契约漂移对齐（采样/1211 文案/无 Content-Type）
- 提交：`4fc847d8e`
- 文件/符号：`middleware/distributor.go:784`（1211，grep 确认）、`relay/helper/valid_request.go:1595 WriteZhipuBusinessError`
- 测试锚点：`glm53_official_fields_test.go::TestGlm53ModelNotFoundTextMatchesLiveContract`(:140)、`TestWriteZhipuBusinessErrorOmitsContentTypeOnTheWire`(:149)、`TestGlm53OfficialFieldsAccept`(:74)
- 上游基线：`fork-only`（`git grep glm53|WriteZhipu upstream/main` exit 1）
- 迁移处置：必须（对齐官方现网，防 fit 误挂）
- 建档：manifest id `glm-group-contract-and-cdp-instruction-following`

### C12 GLM cdp 指令遵循（孤儿 tool + tool_choice=none）——终态 49e6ce52f
- 提交：`6be69e4d2`（引入）→ `49e6ce52f`（ID 级状态机 + Zhipu 错误形态 + body 重写，**终态**）
- 文件/符号：`valid_request.go:1681 glm53OrphanToolMessage`、`:1722 applyGlm53ToolChoiceNone`、`IsStrictGlm53ValidationMessage` 前缀表
- 测试锚点：`glm53_customer_contract_test.go::TestGlm53OrphanToolMessageRejected`(:36)、`TestGlm53OrphanRuleProfileGate`(:105)、`TestGlm53OrphanErrorUsesZhipuWireShape`(:157)、`TestGlm53ToolChoiceNoneWithholdsToolDeclarations`(:186)——grep 确认（注意：档案 G06 写的 `TestGlm53OrphanRuleIsFamilyScoped` 当前树不存在，被 49e6ce52f 重写为 ProfileGate，见 §4 告警 1）
- 上游基线：`fork-ahead`（上游无任何工具链状态机校验）
- 迁移处置：必须（买家验收项）；必须与 49e6ce52f 一起迁
- 建档：manifest id `glm-group-contract-and-cdp-instruction-following` ｜ ID 状态机/body 重写**未覆盖**（见 §3 缺口 7）

### C13 fit-probe glm-5.3 期望行 + thinking-ON 探测分支
- 提交：`1f068e545`
- 文件/符号：`scripts/fit-probe/spec.go:181-236`（5 条 glm-5.3 行，grep 确认）、`probe.go`（thinking-ON 分支）
- 测试锚点：无测试锚点（`TestR20DeepSeekProbesPinTheThinkingState`:2351 只断言 deepseek/kimi，不覆盖 glm 分支——G07 记录的缺口属实）
- 上游基线：`fork-only`（上游无 scripts/ 目录）
- 迁移处置：建议（随 fitpolicy 拍板）
- 建档：**未覆盖**（建议 id `fit-probe-glm53-expectation-rows`，见 §3 缺口 1）

### C14 fitpolicy 候选哈希预绑定助手
- 提交：`71799b99c`
- 文件/符号：`pkg/fitpolicy/zz_candidate_hash_test.go:10 TestZZPrintCandidateHash`（grep 确认）
- 测试锚点：`TestZZPrintCandidateHash`
- 上游基线：`fork-only`
- 迁移处置：可弃用（语义并入 `POST /api/fit-policy/validate`）
- 建档：**未覆盖**（建议 id `fitpolicy-candidate-hash-prebind`，见 §3 缺口 2）

### C15 发版 park-协议：先 park 再排空、按 ID 等全量离场
- 提交：`e97d98ea2` → `859facc41` → `ca8e650e4` → `f5c680e83` → `95b7afbb3`（按 ID 固定 retire set，终态）→ `158cbcf91`（回滚走同一协议）→ `850372036`（计数先于使用 + 测试要求 parking 行）
- 文件/符号：`deploy.sh:1657 oldest_instance_ips`、`:1675 relay_drain_write`、`:1722 relay_drain_clear`、`:1888 wait_drain_applied`、`:897 drain_before_scale_in`——grep 确认
- 测试锚点：`tests/deploy-test.sh` 的 rollout-all-race（parked the retiring instance）、`a relay drain marker was left behind after the release`、`h1_policy_case`(:1362) 等
- 上游基线：`fork-only`（上游整个 `deployment/` 不存在，`git ls-tree upstream/main` 顶层确认）
- 迁移处置：必须（语义：先 park 再排空、失败走同协议回滚）
- 建档：**未覆盖**（建议 id `deploy-relay-drain-park-before-hold` + `cn-rollback-through-parked-set-protocol`，见 §3 缺口 3）

### C16 release hold 互斥链（新鲜度拒第二次 → owner 绑定 → 原子 verify-on-write → 三态消费 → flock 真互斥）
- 提交：`1cc70139d` → `142271264` → `0f6d84398` → `32151964a` → `68ce5c54f` → `92cee8a26` → `b41402cbd` → `8e73cfd90` → `ce8882964` → `b1142a679` → `30162cdf2` → `e11a44c08`（round 6–13）
- 文件/符号：`deploy.sh:1962 release_hold_is_fresh`、`:1998 RELEASE_OWNER`、`:119 MARKER_LOCK_PATH`、`:2095 suspend_scale_in_guard`、`:2211 resume_scale_in_guard`、guard 侧 `DRAIN_OWNER`(ecs-shrink-guard:80)、`shrink_section`
- 测试锚点：`deploy-test.sh` release-mutex(:968)、guard-test.sh 全套（park/hold/janitor/…，经 deploy-test.sh:1850 挂入 CI）
- 上游基线：`fork-only`
- 迁移处置：必须（语义：一次只许一个 release、锁 owner-bound、不可解析即 fail-closed）
- 建档：**未覆盖**（建议 id `cn-release-mutex-fail-closed-reads` + `cn-release-hold-atomic-verify-on-write`，见 §3 缺口 3）

### C17 scale-in 告警生命周期端到端 fail-closed
- 提交：`9ec0a61cc` → `98a9e8e28` → `dff1b2d7f` → `955c624ee`（收尾形态）
- 文件/符号：`deploy.sh:1519 scale_in_alarm_id`、`RETIREMENT_PENDING`（:2430 等多处，grep 确认）
- 测试锚点：`scale-in-alarm-restore-fail`(:1504)、`retire-pending`(:1527)、`scale-in-alarm-missing`(:1553)、`release-no-headroom`(:1046)、`scale-in-external-scaleout`(:1592)——grep 确认
- 上游基线：`fork-only`
- 迁移处置：必须（语义）
- 建档：**未覆盖**（建议 id `cn-scale-in-alarm-lifecycle-fail-closed`，见 §3 缺口 3）

### C18 测试假件与生产同边界（fake ssh/aliyun 收紧）
- 提交：`1e1b5826a`、`6fd971977`
- 文件/符号：`tests/fake-bin/ssh`（派生式 fleet-sync）、`tests/fake-bin/aliyun`（Modify 边界、`TOKENESS_TEST_NO_ALARM` 等）
- 测试锚点：rollout-all-batch、release-no-headroom、scale-in-external-scaleout 等
- 上游基线：`fork-only`
- 迁移处置：建议（判据照搬，测试重写——AGPL 约束）
- 建档：**未覆盖**（建议 id `cn-test-fakes-match-production-boundaries`，见 §3 缺口 3）

### C19 零切流旋转闸门（nginx -T 交叉验证 + 满排空窗 + 回滚不切流）
- 提交：`8865d6ca5`（8 项整改）→ `7c2c8311f`（孤儿心跳 + 未知计数）
- 文件/符号：`deploy.sh:776 instance_streams_busy`、`:801 drain_window_with_quiet_check`、`ecs_upstream_members_running`、`scale_group_from_set:1451`（`removed_started` 预算，grep 确认）
- 测试锚点：无新增专项用例（G12 记录「代码在、断言缺」属实；依赖既有 tie/套件间接覆盖）
- 上游基线：`fork-only`
- 迁移处置：必须（语义）
- 建档：**未覆盖**（建议 id `cn-zero-cut-rotation-gates` + `cn-heartbeat-orphan-guard-and-unknown-count-restore`，见 §3 缺口 3）

### C20 CreatedTime 平局 park 集合 + 退役预算与完成证明
- 提交：`6e21c76cb`/`dca044b9e`（**同一 blob 的两个平行提交**，hash `af68157c2` 相同，已复核）→ `83669b675`（预算 v1 departed）→ `5104746c5`（v2 removed_started + 超预算拒绝 + pending 保护，**终态**）→ `b7ac05af2`（merge 合流）
- 文件/符号：`deploy.sh:1632 oldest_park_ids`、`:1466 removed_started`、`:1484 budget_left=$(( retire_count - removed_started ))`——grep 确认
- 测试锚点：`rollout-batch-created-time-tie`(:1129)、`release-created-time-tie`(:1201)、`tie-retirement-budget-*`(:1231 起)——grep 确认
- 上游基线：`fork-only`
- 迁移处置：必须（「宁可不缩容也不删错实例」+ 退役已开始即计入预算）
- 建档：manifest id `cn-tied-retirement-budget-and-completion`（锚点已是 removed_started 终态）｜ 平局 park 集合构造部分**未覆盖**（建议 id `cn-created-time-tie-park-set`，见 §3 缺口 3）

### C21 本机构建优先发版路径（release-local.sh）
- 提交：`70ab27224`（新建）+ `a873bc621` → `6c39c796d`（dirty-tree 终态）+ `fe8a2f738`（GOPROXY 回退 + VERSION 恢复）
- 文件/符号：`release-local.sh:79`（`--untracked-files=no`）、`:116 restore_version`、`:145 imagetools inspect`、`:119 --build-arg GOPROXY`、`Dockerfile.tokeness:33 ARG GOPROXY=https://proxy.golang.org,direct`——grep 确认
- 测试锚点：无测试锚点（tests/ grep 0 命中）
- 上游基线：`fork-only`（上游把 VERSION 作为跟踪文件直接 COPY，无构建时写+恢复契约）
- 迁移处置：建议（语义：构建树干净、tag 不可变、构建产物不污染源树）
- 建档：**未覆盖**（建议 id `release-local-dirty-tree-and-immutable-tag`，见 §3 缺口 4）

### C22 registry digest 对账 + 拉取授权证明 + 401 可达语义
- 提交：`b30a22490`（M2 digest）+ `821ea7ce5`（401）+ `0e71a879e`（M1 pull-auth + L1 缓存源/--no-cache）+ `ab7fa4c73`/`fe8a2f738`（GOPROXY 两轮，终态=官方默认）
- 文件/符号：`release-local.sh:145-155`（registry digest 比对）、`release-on-master.sh:38`（`grep -qE '200|401'`）、`:58 docker manifest inspect`
- 测试锚点：`deploy-test.sh::release-certified-digest`（deploy.sh 侧 digest pin 有测试）；release-on-master 侧无测试
- 上游基线：`fork-only`
- 迁移处置：401 可达 + digest 对账 = **必须**（语义，协议事实）；缓存源/--no-cache = 建议
- 建档：**未覆盖**（建议 id `release-registry-digest-confirmed-before-deploy` + `release-check-accepts-registry-401-and-proves-pull-auth`，见 §3 缺口 4）

### C23 master 本机 rollout（MASTER_LOCAL 限定 + release-on-master --check）
- 提交：`5244cd7a4`（新建）+ `ee524e4ad`（H1：仅 MASTER_HOST 本机执行，终态）
- 文件/符号：`deploy.sh`（`MASTER_LOCAL == "1" && host == MASTER_HOST` 分支）、`release-on-master.sh check()`
- 测试锚点：无测试锚点（MASTER_LOCAL 在 tests/ 中 0 命中，G08 诚实记录）
- 上游基线：`fork-only`
- 迁移处置：可弃用（单机拓扑特有）；「本机执行必须核对目标主机」语义可作检查表项
- 建档：**未覆盖**（建议 id `deploy-master-local-scope-only`，见 §3 缺口 4）

### C24 ESS 受害者选择「不可预测就不猜」（秒级 CreatedTime + 策略/可用区守卫）
- 提交：`7357dd897`
- 文件/符号：`deploy.sh:1603 oldest_instance_ids`
- 测试锚点：`h1_policy_case`（`not exactly OldestInstance` + `eci-old` 仍在，:1373）——grep 确认
- 上游基线：`fork-only`
- 迁移处置：必须（语义）
- 建档：**未覆盖**（建议 id `cn-drain-victim-selection-refuses-unpredictable`，见 §3 缺口 3）

### C25 守卫与发布同协议（ecs-shrink-guard v2 重写）
- 提交：`c4e39ff1e`
- 文件/符号：`ecs-shrink-guard:80 DRAIN_OWNER`、`:82 hold_is_stale`；新增 `tests/guard-test.sh`（经 deploy-test.sh:1850 挂入）
- 测试锚点：guard-test.sh 的 idle park owner/lease 三行断言（:126-135）、foreign marker 不删（:145-147）等——grep 确认
- 上游基线：`fork-only`
- 迁移处置：必须（语义：守卫与发布协议同源）
- 建档：随 C16/C15 建议条目覆盖

### C26 发版门禁收口与 i18n 键让位
- 提交：`203ce0128`（无行为代码）
- 文件/符号：`manifest.json`（锚点 departed→removed_started，grep 确认）、7 个 `web/src/i18n/overlay/*.json`（`Disabled {{count}} keys` 进 overrides，grep 确认）
- 测试锚点：`fork-overlay.test.ts::a key is either fork-only or an override, never both`(:100)——grep 确认
- 上游基线：`fork-only`（门禁）/`fork-ahead`（键让位：上游 `128089fa7` 已采纳同键）
- 迁移处置：不迁移脚本本体；「门禁不绿不发」「锚点随改名同步」语义建议继承
- 建档：已覆盖（`fitpolicy-渠道能力表与写回链路-2`、`cn-tied-retirement-budget-and-completion`、i18n 契约 id）

### C27 merge-divergence 门禁的可见登记（allowlist 契约）
- 提交：`7ba7e0ab1`
- 文件/符号：`scripts/fork-invariants/merge-allowlist.json`（2 条 reason，已读全文，登记 502 修复对旧写法的有意取代）
- 测试锚点：无测试锚点（门禁自测在 `scripts/fork-invariants/main.test.mjs`，未逐函数核验）
- 上游基线：`fork-only`（上游无 scripts/）
- 迁移处置：建议（「有意取代必须可见登记」纪律）
- 建档：**未覆盖**（建议 id `merge-allowlist-visible-supersession`，见 §3 缺口 6）

## 2. 关系拓扑

- **链式（同一缺陷多轮整改，最终形态标★）**：
  - C01：0d1f999d1→dad2d762a→baf4273c1★（删零判别力用例 + 可溯源注释）→26998b80e 固化；被取代中间产物 `relay_sticky_status_repro_test.go` 已删（当前树确认）。
  - C03：d2a125b7b→08176dfbd→45dd3774b★（判据收窄到 kind==temporarily_unavailable）；组后 c66ed310e（G03）再改名为 `retryFoundNoPinnedCandidate`，整链真终态在 C04 判据。
  - C04：c66ed310e→9653d6975→24ddf1e56→d992051da★（unprovable_exhaustion 语义）+ 99abc4dde（验收）；c66/9653 的判据与错误传播未被替代，仍是组成部分。
  - C05：c376c422c→282180e99★（本地计费排除）。
  - C06：c709d6571(v1)→63f6631a8★(v2)；v1 符号已从树中消失，2bf08ab3f 是显式替换点。
  - C07/C08/C09/C10：8a381ba85→e5fec146f（C07★）→7f0b029ca（C08★）→58f8c5cd0（C09/C10★）——四域第一版各有缺陷（单位错误、条件脱敏、预算饿死、豁免键名错），靠独立复审逐轮收敛。
  - C12：6be69e4d2→49e6ce52f★（pending 计数→ID 级状态机；顶层 DTO 清理→body 重写）。
  - C15–C19：G08/G09/G10/G12 的 30+ 发版提交是同一缺陷面（切流/互踩/锁失效）的**超长整改链**，链内多级替代：hold 锁从「新鲜度」→「owner 行」→「原子 verify-on-write」→「flock 真互斥」★（ce8882964+后续 round 7–13）；drain marker 从裸 IP→owner+lease v2★（f8399176f）；retire 集从「按数 gate」→「按 ID 固定」★（95b7afbb3）；预算从 departed→removed_started★（5104746c5）。**一律以现树（97b83c2c0）为准**，迁移时不看中间提交。
  - C21/C22：70ab27224+5244cd7a4 新建 → M1–M4/L1 五处整改 → a873bc621/6c39c796d 续固（dirty-tree 终态 6c39c796d★）。
- **替代（同一修复两份提交，指出哪份生效）**：
  - `6e21c76cb` vs `dca044b9e`：同一 deploy.sh blob（hash 相同，已复核 `git hash-object`）在两条平行分支的双落点；生效路径 = dca044b9e → 83669b675 → 5104746c5 → b7ac05af2 进 main。
  - `9945d1e6b` 合并信息虚报「包含 c709d6571」（`git merge-base --is-ancestor` 为假，已复核），由 2f6e80ff2 补并、2bf08ab3f 以 v2 替换。
  - `ab7fa4c73`（GOPROXY 默认 goproxy.cn）被 fe8a2f738 部分回退为官方默认——**Dockerfile.tokeness:33 现值 proxy.golang.org 生效**，大陆加速移到 release-local.sh 传参。
  - v1 停链（`TestDecideRelayRetryStopsWhenClientGone`）被 v2 替换，测试名已不存在。
- **依赖（必须先 A 后 B）**：C11 → C12（同一 `validateGlm53OfficialFields` 入口）；C03 → C04（routing_error 写入链宿主）；C05 ↔ C03/C04（同属 pin/保真族，逃生口是 `provable` 判据的例外族）；H4 心跳（0f6d84398）→ H3 互斥（1cc70139d）——「新鲜 hold=在跑 release」依赖心跳成立；f8399176f（marker lease）→ c4e39ff1e（守卫同协议）→ C19 的全部闸门；6e21/dca（平局 park 集合）→ 83669b675/5104746c5（预算）→ 203ce0128（门禁锚点同步）；203ce0128 → 7ba7e0ab1（门禁先收口再登记 allowlist，first-parent 顺序已复核）。
- **独立**：C13/C14（测量工装两翼）；C23（H1，仅同轮复审发现）；C26（下游收口）；C27（门禁配套）。

## 3. 覆盖缺口清单（按优先级）

排序依据：会不会重演事故 > 是否对外契约 > 是否 new-api 特有。以下 id 在 manifest.json 64 条中 grep 全部 0 命中。

1. **`cn-release-mutex-fail-closed-reads`**（C16+C25）——发版互斥与 fail-closed 读是防「两发布同持伸缩组/transport 失败读成无 hold」切流事故的核心；优先级最高。草稿：title「release 互斥：flock 下 check-and-acquire、三态退出码、所有远端读 fail-closed」；files `deployment/tokeness-cn/deploy.sh`、`deployment/tokeness-cn/ecs-shrink-guard`；anchors `MARKER_LOCK_PATH`(:119)、`release_hold_is_fresh`(:1962)、`shrink_hold_write`、`suspend_scale_in_guard`(:2095)、`verify_no_extra_removals`、guard `DRAIN_OWNER`(:80)/`shrink_section`；tests `deploy-test.sh::release-mutex`(:968) + `guard-test.sh` 全套（经 deploy-test.sh:1850 挂入）。
2. **`cn-scale-in-alarm-lifecycle-fail-closed`**（C17）——「告警开着时发版/告警永久不恢复」是客户可见切流事故。anchors `scale_in_alarm_id`(:1519)、`resume_scale_in_guard`(:2211)、`RETIREMENT_PENDING`；tests `scale-in-alarm-restore-fail`(:1504)、`retire-pending`(:1527)、`scale-in-alarm-missing`(:1553)。
3. **`deploy-relay-drain-park-before-hold` / `cn-drain-victim-selection-refuses-unpredictable` / `cn-zero-cut-rotation-gates` / `cn-created-time-tie-park-set`**（C15/C24/C19/C20 前半）——零切流排空与「不可预测就不猜」。anchors：`oldest_instance_ips`(:1657)、`relay_drain_write/clear/refresh`(:1675/1722/1747)、`wait_drain_applied`(:1888)、`oldest_instance_ids`(:1603)、`oldest_park_ids`(:1632)、`drain_before_scale_in`(:897)、`instance_streams_busy`(:776)、`drain_window_with_quiet_check`(:801)；tests：rollout-all-race（parked the retiring instance）、`h1_policy_case`(:1362)、`rollout-batch-created-time-tie`(:1129)、`release-created-time-tie`(:1201)。
4. **`release-registry-digest-confirmed-before-deploy` / `release-check-accepts-registry-401-and-proves-pull-auth` / `release-local-dirty-tree-and-immutable-tag`**（C21/C22）——digest 对账与 401 是协议事实，丢了重演假阴性阻断/部署不可识别制品。anchors：`release-local.sh` imagetools 比对块(:145-155)、`untracked-files=no`(:79)、`restore_version`(:116)、`release-on-master.sh` `grep -qE '200|401'`(:38)、`docker manifest inspect`(:58)；tests：`release-certified-digest`（仅 digest 一条有测试，其余为缺口需补）。
5. **`audit-test-fixture-owns-its-connections`**（C03 附带）——测试基建，不迁移但需 fork 侧回归；属测试夹具契约。anchors `controller/relay_routing_failure_test.go`（`TestNewAuditTestDatabaseLeavesTheCallersConnectionsAlone`:312、`TestRecordNoCandidateRetryErrorNeedsARequest`:285，grep 确认）；tests 同名。
6. **`merge-allowlist-visible-supersession`**（C27）——fork 基础设施契约；「有意取代必须可见登记」。anchors：`scripts/fork-invariants/merge-allowlist.json` 的 `paths[].reason`；tests：`scripts/fork-invariants/main.test.mjs` 中 allowlist 计数用例（函数名未核验，登记前需补 grep）。
7. **`glm-orphan-tool-id-state-machine`**（C12 细粒度）——manifest 现有条目只到文件级锚点；建议补 anchors `glm53OrphanToolMessage`(:1681)、`applyGlm53ToolChoiceNone`(:1722)，tests `TestGlm53OrphanRuleProfileGate`(:105)、`TestGlm53ToolChoiceNoneWithholdsToolDeclarations`(:186)。
8. **`fit-probe-glm53-expectation-rows` / `fitpolicy-candidate-hash-prebind`**（C13/C14）——测量工装，随 fitpolicy 拍板；优先级最低。anchors：`scripts/fit-probe/spec.go:181-236`、`probe.go` thinking-ON 分支、`pkg/fitpolicy/zz_candidate_hash_test.go:10`；tests：`TestZZPrintCandidateHash`（fit-probe 侧 glm 断言为缺口）。

## 4. 一致性告警

1. **G06 引用了不存在的测试名**：G06 契约证据写 `TestGlm53OrphanRuleIsFamilyScoped`（:148「在本提交中创建」），且 §1 中 6be69e4d2 与 49e6ce52f 都引用它。当前树 grep 该名**零命中**；该文件现测试为 `TestGlm53OrphanRuleProfileGate`(:105)。判定：G06 写的是 6be69e4d2 时点的旧名，49e6ce52f 重写测试时更名/合并，G06 的 49e6ce52f 段未同步——**以代码为准**，档案测试名已过时。manifest 引用的 3 个测试名（OrphanToolMessageRejected/OrphanErrorUsesZhipuWireShape/ToolChoiceNoneWithholdsToolDeclarations）全部存在，无碍门禁。
2. **G01 提到 `TestOaiStreamHandlerAllowsRetryBeforeAnyUpstreamData` 在 manifest `relay-status-commit-fidelity-502` 名下（:971）**：当前树该测试存在（manifest tests 含它），但 G01 自己也指出其主契约（incomplete stream 结算）不属本线——**边界归属含糊**，判定：作为旁证可接受，但它不是 502 契约的验收锚点。
3. **G03 描述的缺陷形态「先分层后过滤」与上游实现无关**：G03 档案已自查（上游 DB `GetChannel` 是「全量查询→过滤→分层」，不必然有该形态）。判定：`provable-channel-exhaustion-under-fit-pin` 的迁移定位应是「自研网关自身的证据纪律」而非「上游兼容修复」，与 G03 迁移处置一致，无矛盾。
4. **G02 与 G03 的判据演进链描述一致但分界线易误读**：G02 说「最终形态是 45dd3774b」又立即指出 c66ed310e 是「真正终态」；G03 把 c66ed310e 列为链首。判定：两档案实为同一条链的两段（G02 是三态开关与 routing_error 行，G03 是「无候选」判据），当前树 `controller/relay.go:375/999` 的 `retryFoundNoPinnedCandidate` + `unprovable_exhaustion` 是联合终态，manifest 两条 id 锚点均已反映终态——无实质矛盾，建档时按 C03/C04 分立即可。
5. **G05 与 G12 关于 `kill -0` 的演进**：G12 说 7c2c8311f 引入 `kill -0 "$PPID"`，G05 说 58f8c5cd0 改为 `kill -0 "$$"`。两者都自认是链中一环，当前树 deploy.sh:1998 `RELEASE_OWNER=...-$$` 与心跳存活检查为 `$$` 形态（58f8c5cd0 终态生效）。无矛盾，但 C19/C10 迁移必须读现树函数体。
6. **G08 与 G09/G10 对同一批符号的行号互相漂移**（如 `release_hold_is_fresh`、`wait_retired`）：属预期（中间形态 vs 现树），已按现树行号统一（C16/C20）。
7. **G04 与 G13 对 7ba7e0ab1/203ce0128 的时序**：G13 档案称自己是「发版前门禁收口」，G01 范围含 7ba7e0ab1（更晚，10-12 23:08）。二者是两次门禁动作（203ce 收口 manifest+i18n，7ba7 登记 allowlist），first-parent 顺序 `203ce0128 → 7ba7e0ab1` 已复核，不冲突。
8. **G12 平行提交 `6e21c76cb`/`dca044b9e`**：blob hash、提交信息、author date 完全相同（已复核），但父不同（08176dfbd vs 58f8c5cd0）且互不为祖先。档案判定「最可能平行落盘」——无法进一步取证，维持该不确定项。

## 5. 我核对不了的（缺什么证据）

1. **全部测试均未运行**（任务纪律禁止 `go test`/`node scripts/fork-invariants/main.mjs`/deploy-test）：提交信息声称的「5/5 门禁」「deploy-test 1620 行全绿」「三库矩阵 PASS」均未独立复现；shell 用例间的运行期依赖（顺序敏感、共享 mktemp）未验证。
2. **事故数字不可复核**：136 条 502、1.881%/110 条、80 条 type=5、ch8 96% 假失败、p95 181s/max 461s、p99 765s/stop 2132s、「second 1899 被切断」——全部来自提交信息/FORK-CHANGES/docs 的二阶陈述；入口哨兵聚合「客户可见 5xx=3」@20:29:42 同样不可复核（baf4273c1 自己声明逐条 access log 未归档）。
3. **上游 42e694eb6 之后的吸收情况**：基线是 2026-10-11 快照；上游此后是否以别的形态吸收 C01/C03 等修复未验证。
4. **门禁对这两条路径触发「取上游弃 fork」的精确机制**（G01 不确定项 2）：7ba7e0ab1 allowlist 的两个路径为何被 merge-divergence 报出（大概率因上游 128089fa7 大改 + 三方启发式误标）未逐行复现，需实际跑门禁。
5. **`scripts/fork-invariants/main.test.mjs` 中 allowlist 计数用例的函数名**：未逐函数核验，§3 缺口 6 的测试锚点登记前需补 grep。
6. **C13 的 prompt_tokens 基线是否已实测**（README §9 称经中转取得，数值未验证）；glm-5.3 thinking-ON 分支无回归断言是缺口还是有意省略。
7. **生产「无 Content-Type」报文等价性**：仅由 httptest 测试佐证，未抓包验证。
8. **`6e21c76cb`/`dca044b9e` 双落点是否有 cherry-pick/rebase 痕迹**：`git log --grep` 未命中，无法从 git 证据判定刻意还是事故性重复。
9. **`.cnb.yml`/上游 GitHub Actions 内是否有等价 digest/pull-auth 逻辑**：未逐文件检查（上游无 `deployment/` 目录、Dockerfile 无相关逻辑，fork-only 判词不受影响，但「上游 Actions 里有没有类似门禁」缺证据）。
10. **`71799b99c` 所述「v6→v7 suite_stale 全量回落官方」的影响面**：scratch 目录不在只读树内，无法核实。

===REL1-END===
