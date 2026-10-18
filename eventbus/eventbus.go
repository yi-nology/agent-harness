// Package eventbus 事件单源双写（PHASE1_PLAN §2 决策 7）：
// 引擎只向本总线 Publish 一次，订阅者分别落 bq_event（repo）与 SSE（handler）。
package eventbus

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/oagentkit/runtime/progress"
)

// Event 引擎事件（比 model.Event 多运行时字段；落库时转换）。
type Event struct {
	ID         uint64         `json:"id"`
	SessionID  string         `json:"session_id"`
	CaseID     string         `json:"case_id,omitempty"`
	StepID     *uint64        `json:"step_id,omitempty"`
	Type       string         `json:"type"`
	Level      string         `json:"level"` // info|warn|error
	Payload    map[string]any `json:"payload,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
}

// 事件类型词表（bq_event.type 同源）。
const (
	EventSessionCreated      = "session_created"
	EventIntentRouted        = "intent_routed"
	EventDispatchDenied      = "dispatch_denied"
	EventStepStarted         = "step_started"
	EventStepFinished        = "step_finished"
	EventReportValidated     = "report_validated"
	EventVerdictJudged       = "verdict_judged" // 结论合议裁决留痕（spec 2026-09-15-verdict-judge-design §8）
	EventReportViolation     = "report_violation"
	EventApprovalRequested   = "approval_requested"
	EventApprovalDecided     = "approval_decided"
	EventApprovalExpired     = "approval_expired"
	EventExecutionDryrun     = "execution_dryrun"
	EventExecutionDegraded   = "execution_degraded"   // 执行面缺失（MCP 池未装配），已批准方案降级 DryRun 的显式留痕
	EventRemediationExecuted = "remediation_executed" // 受审变更执行回执（批次九十四）：批准→执行完成后的汇总留痕（executor/change_ref/host/步骤数）
	EventSessionFinished     = "session_finished"
	// 轮次生命周期事件对（批次二百八十四，对标 DeepSeek harness turn 括号语义+「锁最后
	// 释放」纪律）：turn_started 在会话认领成功后立即落库，turn_ended 在收尾漏斗出口
	// 落库——「有 start 无 end」的残留=中断轮的可检测孤儿态（发版重启/进程死亡不再依赖
	// 事件静默启发式定性，时间线与诊断大脑可直接读出轮边界与中断点）。停机路径不落
	// turn_ended（轮括号留给恢复循环/启动对账以 closed_by=recovery|boot-reconcile 关闭，
	// 谎报「正常收尾」比留开放括号更伤审计）。
	EventTurnStarted = "turn_started"
	EventTurnEnded   = "turn_ended"
	// EventReportSuperseded 终报覆写版本尾迹（批次一百三十七）：成功收尾覆写 final_report
	// 前旧报告非空且不同时发布——bq_event 持久化留痕，控制台回放可拼历史版本链（载荷只
	// 带头尾摘录防大字段；旧报告全文仍以折叠进压缩面的路径为准）。
	EventReportSuperseded = "report_superseded"

	// 过程可观测事件（此前是调度器里的字符串字面量，收编进词表同源管理）：
	// reasoning/text 为 agent 过程思考与中间答复；tool_call/tool_result 经 call_id 配对；
	// llm_usage 为单次 LLM 调用真实用量（DeepSeek harness 式 token 面板的数据源）。
	// reasoning_delta/text_delta 为流式增量分片（agentkit v0.10.32 默认开流）：生成过程
	// 中按 ~400ms/200 字符节流发布（seq 单调、text=分片非快照），同轮权威 reasoning/
	// text 快照到达即吸收——真流式渲染的数据源，回放/对账以权威快照为准。
	EventReasoning      = "reasoning"
	EventAgentText      = "text"
	EventToolCall       = "tool_call"
	EventToolResult     = "tool_result"
	EventLLMUsage       = "llm_usage"
	EventReasoningDelta = "reasoning_delta"
	EventTextDelta      = "text_delta"

	// 报告增量流（SSE 真打字机）：终态成功收尾时把定稿报告按 markdown 行边界分片、
	// 节奏化发布（seq/total/text）。经 s.pub 落库保 bq_event 游标不变量；回放重放
	// 无害（同 seq 同文，前端按 seq 归并、终态权威报告覆盖）。
	EventReportDelta = "report_delta"

	// 用户输入事件（多轮对话的用户侧留痕）：创建/注入时由 SessionSvc 经调度器出口发布，
	// 时间线可见，同时是下一轮对话历史（ListEventsByType）的数据源。
	EventUserInput = "user_input"

	// 上下文构成事件（批次八十一，对标 Octop ContextWindowRing）：每环节装配完成后
	// 由 runner OnContext 回调发布，payload.segs=段名→字符数（soul/prompt/defense/
	// skills/skills_list/memory/env_notes/todo/query/tools）——前端构成条的数据源。
	// 批次二百八十七扩压力读数四字段（pressure_tokens/pressure_baseline/window_tokens/
	// window_pct，dsh token-meter 对标：usage 锚定与启发式取大）。
	EventContextComposition = "context_composition"

	// 上下文压力高水位告警（批次二百八十七，pressureHighWater=0.85）：请求压力达窗口
	// 高水位时 warn 级显形（压力是事实不是错误，不阻断）；同时是压缩面预折叠闸的
	// 触发信号（compact.go pressureHot）。
	EventContextPressure = "context_pressure"

	// 协议级重试留痕（批次二百九十五，dsh assistant/attempt 等价物）：首跑失败决定
	// 回喂重试时 warn 级显形（payload.reason=失败原因摘要）——时间线可解释「两段近似
	// 重复环节」（此前无标记，排障误读为模型重复劳动）。agentkit 内部 LLM 调用级重试
	// 不在本面（实现侧在 agentkit，挂账）。
	EventAttemptRetried = "attempt_retried"
	// EventPtcRun 程序化工具编排明细（批三百零五）：一次 run_program 执行的账目面——
	// bindings 调用清单/各预算余量/中断原因（程序把大输出「洗」成小结果时账目不缺）。
	EventPtcRun = "ptc_run"

	// 任务清单事件（执行进程浮层，对标 ZCode 进程面板）：todo_plan 全量替换成功后由
	// runner OnTodos 回调发布，payload.todos=完整清单（[{content,status}]）、done/total
	// =完成数/总数——控制台会话页右上角进度浮层数据源；经 s.pub 落库，回放自水合。
	EventTodoUpdated = "todo_updated"

	// 记忆召回事件（批次一百三十五，「诊断大脑」观测台一期缺口补齐）：分层记忆召回
	// 装配非空时由 runner OnMemoryRecall 回调发布，payload.chars=召回块字符数、
	// preview=首 240 字——此前召回只有 DB Hits 滞后计数无事件，脑图海马区活跃度数据源。
	EventMemoryRecalled = "memory_recalled"

	// 排队输入生命周期事件（真队列化 P1-1/P1-2/P2-3）：abandoned=失败/预算禁续等收尾
	// 未消费的明示留痕；removed=队列管理面单条移除（气泡「已移除」判定依据）。
	EventQueueAbandoned = "queue_abandoned"
	EventQueueRemoved   = "queue_removed"

	// 选模悬空回落事件（批次二百五十二，spec dialog-zcode-alignment §3）：会话设了
	// model_profile 但档案链已不在已装配快照（档案被删/停用/密钥摘除/mock 形态）——
	// 本轮按平台角色路由继续（回落是设计行为，悬空不得炸会话），事件显形「你选的模型
	// 已不可用」防静默换脑。payload=profile/returned_model。每轮一条（轮级判据）。
	EventModelFallback = "model_fallback"

	// 目标驻留事件（P3-1/P3-2）：set=目标绑定；verified=judge 完成验证器裁决留痕
	// （passed/next_action/iteration 随 payload）；completed=目标达成收敛。
	EventGoalSet       = "goal_set"
	EventGoalVerified  = "goal_verified"
	EventGoalCompleted = "goal_completed"

	// 跨轮历史压缩事件（批次三十六，对标 ZCode CompactBoundary）：【历史压缩】环节把
	// 旧摘要+近轮结论折叠为结构化摘要后发布——before/after_tokens 为估算口径
	// （EstimateTokens），summary_chars/pending_count 供时间线卡与排障。
	EventHistoryCompacted = "history_compacted"

	// 跨副本取消控制事件（P2-1）：Cancel API 在本副本信号未命中时发布，各副本监视器
	// 轮询 bq_event 命中本副本取消句柄即协作中止（append-only 幂等：非执行副本 no-op）。
	EventSessionCancelRequested = "session_cancel_requested"

	// 澄清反问观测事件（P3-5）：反问轮此前仅 intent_routed 的 question 字段可辨，
	// 无独立可观测事件——补一条显式留痕（keyword/question 随 payload）。
	EventClarifyAsked = "clarify_asked"

	// Spawn 原语生命周期事件（批次二百四十一 R4 笼子四补位）：dispatch_agent 单派与
	// dispatch_explore 各路勘探子任务的起止结构化留痕——spawn_id 配对两事件，
	// caller/target/mode/status/latency_ms 随 payload。此前 spawn 子任务只能靠父环节
	// tool_call/tool_result 的文本痕迹观察，无结构化父子归因；本事件是前端「进行中
	// spawn」进行式卡与审计面的数据源（StepID=父环节，落库/广播经调度器 pub 单源）。
	EventSpawnStarted  = "spawn_started"
	EventSpawnFinished = "spawn_finished"

	// 会话产物登记（批次八十六，spec COMPETENCE_UPLIFT_SPEC §5）：版本化交付物
	// 落库即发布（artifact_id/kind/title/seq/mime/size 随 payload）——时间线产物卡
	// 与 SSE 实时刷新同一事件源。
	EventArtifactRegistered = "artifact_registered"

	// 平台化插拔审计事件（spec §4 操作审计）：无会话归属的系统级事件——启停/重载
	// 的操作者/时间/slug 落台账，与决策树留痕同一套台账哲学。
	EventExpertToggled   = "expert_toggled"
	EventExpertsReloaded = "experts_reloaded"
	EventMcpToggled      = "mcp_toggled"    // MCP server 运行时启停（页面管理）
	EventMcpGovernance   = "mcp_governance" // MCP 治理变更（批次一百八十七：包声明激活位/凭证绑定覆盖，热生效）
	EventMCPPoolError    = "mcp_pool_error" // MCP 池建连/列举失败留痕（此前完全静默，agent 拿空工具表无从排查）

	// 凭证平面审计事件（spec 2026-09-11 §9.1）：CRUD/启停/测试为操作留痕；
	// staged 为 MCP spawn 装载留痕（只记名字与键名，不记值）。
	EventSkillUpdated = "skill_updated" // 技能编辑留痕（mode/正文写回；批次十补审计缺口）

	EventCredentialUsed    = "credential_used" // 会话级凭证使用归因（host 命中凭证实例名时随会话发布）
	EventCredentialCreated = "credential_created"
	EventCredentialUpdated = "credential_updated"
	EventCredentialDeleted = "credential_deleted"
	EventCredentialToggled = "credential_toggled"
	EventCredentialTested  = "credential_tested"
	EventCredentialOAuth   = "credential_oauth" // OAuth2 授权流留痕（批次一百九十七 R8：authorized|failed|refreshed|refresh_failed）
	EventCredentialsStaged = "credentials_staged"

	// IM 通道审计事件（批次九十三 DB 化）：通道 CRUD/启停/测试留痕（bq_event，
	// 经 /api/v1/audit?type=im_channel_ 前缀过滤可见；credentials 同款纪律）。
	EventImChannelCreated = "im_channel_created"
	EventImChannelUpdated = "im_channel_updated"
	EventImChannelDeleted = "im_channel_deleted"
	EventImChannelToggled = "im_channel_toggled"
	EventImChannelTested  = "im_channel_tested"

	// 自动化审计事件（spec 2026-09-11-automation）：规则 CRUD/启停/触发留痕（bq_event，
	// 经 /api/v1/audit?type=automation_ 前缀过滤可见）。
	EventAutomationCreated   = "automation_created"
	EventAutomationUpdated   = "automation_updated"
	EventAutomationDeleted   = "automation_deleted"
	EventAutomationToggled   = "automation_toggled"
	EventAutomationTriggered = "automation_triggered"
	EventAutomationFired     = "automation_fired"     // 排期拍到点实跑（与 TriggerNow 的 manual 区分）
	EventAutomationSuggested = "automation_suggested" // FR-11：巡检成功后建议定时化（进会话时间线，前端渲染建议卡）

	// 链模板审计事件（spec 2026-09-18-chain-v2-workflow §10）：模板 CRUD/启停/触发留痕
	//（bq_event，经 /api/v1/audit?type=chain_ 前缀过滤可见）；节点级事件在 scheduler 侧。
	EventChainCreated        = "chain_created"
	EventChainUpdated        = "chain_updated"
	EventChainDeleted        = "chain_deleted"
	EventChainToggled        = "chain_toggled"
	EventChainTriggered      = "chain_triggered"
	EventChainCondition      = "chain_condition"       // 链内 condition 节点判定留痕（field/op/value/命中分支）
	EventChainGateRequested  = "chain_gate_requested"  // 链内 approval 节点提请人工闸门
	EventChainNotifySkipped  = "notify_skipped"        // notify 节点降级（未配置/失败，观测有损不阻塞）
	EventChainExecuteBlocked = "chain_execute_blocked" // execute 节点被拦（平台总闸关/黑名单/主机缺失；降级不阻塞链）
	EventChainParallel       = "chain_parallel"        // 并行子图起跑标记（branches 名单随 payload）
	EventChainNode           = "chain_node"            // 链节点执行留痕（path/节点类型/状态；path 与控制台画布节点 id 同构，运行时拓扑据此高亮）

	// 操作审计门事件（spec 2026-09-14-policy-modes-audit-gate-design）：方案提审裁决与
	// 工具调用面逐次审计留痕——verdict/decider/rule_id 随 payload，审计可答「谁裁的、依据什么」。
	EventOperationAudited = "operation_audited"

	// 告警驱动面事件（spec 2026-09-14-aiops-uplift-design §2）：外部告警接入即留痕
	// （fingerprint/severity/deduped/merged 随 payload），会话时间线渲染「来源告警」卡。
	EventAlertReceived = "alert_received"

	// 告警路由规则面事件（批次一百一十一 DB 化管理面）：CRUD/启停审计留痕。
	EventAlertRouteCreated = "alert_route_created"
	EventAlertRouteUpdated = "alert_route_updated"
	EventAlertRouteToggled = "alert_route_toggled"
	EventAlertRouteDeleted = "alert_route_deleted"

	// 工具身份守卫事件（spec 2026-09-15-tool-identity-otel-guard-design P4）：
	// violation 为热路径守卫实锤（requested/resolved/checksum/violation 随 payload）；
	// pairing_leak 为被动 watcher 配对不变量告警（reason=declared_not_executed|duplicate_result）。
	EventToolIdentityViolation = "tool_identity_violation"
	EventToolPairingLeak       = "tool_pairing_leak"
	// 身份违规 IM 推送已处理标记（identity-watch 通知扫描兜底的幂等锚点；payload {"ref":<源事件ID>}）。
	EventIdentityViolationNotified = "identity_violation_notified"
)

// Bus 进程内事件总线（单实例；cluster 多副本 Phase③ 换 NATS 扇出）。
type Bus struct {
	inner *progress.Bus[Event]
	seq   atomic.Uint64
}

// New 构造（序号从 0 起；仅测试或全新库使用）。
func New() *Bus {
	return &Bus{inner: progress.NewBus[Event]()}
}

// Broadcast 广播一个已构建事件（发布路径先 repo.InsertEvent 落库拿 bq_event 自增 ID，再由此广播）。
// SSE after 游标从此只有 bq_event ID 一个单调空间——跨重启、跨副本（PG 序列全局唯一）都成立，
// 取代旧的 NewWithSeq 启动播种方案（bus 内部序号与回放 ID 是两个空间，多副本下必然错位）。
func (b *Bus) Broadcast(e Event) {
	if e.Level == "" {
		e.Level = "info"
	}
	b.inner.Publish(e)
}

// Publish 赋 ID/时间戳后广播（未落库的临时序号；生产路径改用 InsertEvent+Broadcast）。
func (b *Bus) Publish(sessionID, caseID string, stepID *uint64, typ, level string, payload map[string]any) Event {
	e := Event{
		ID:         b.seq.Add(1),
		SessionID:  sessionID,
		CaseID:     caseID,
		StepID:     stepID,
		Type:       typ,
		Level:      level,
		Payload:    payload,
		OccurredAt: time.Now(),
	}
	if e.Level == "" {
		e.Level = "info"
	}
	b.inner.Publish(e)
	return e
}

// Subscribe 订阅事件流；cancel 释放订阅（SSE 断开/bq_event writer 停止时调用）。
func (b *Bus) Subscribe(ctx context.Context) (<-chan Event, func()) {
	return b.inner.Subscribe(ctx)
}

// Dropped 慢订阅丢弃计数（可观测）。
func (b *Bus) Dropped() int64 { return b.inner.Dropped() }
