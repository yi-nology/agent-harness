// Package statemachine 环节状态机（PHASE1_PLAN §6.2）：合法转移表硬编码 + 全覆盖单测。
//
// 生命周期: Stable（v0.33.0 定级）——只允许 additive 变更（新增导出符号 /
// 新增方法且给零值默认实现 / 新增错误值）；改签名、改语义、改默认值、
// 删导出符号都算 breaking，判定规则与变更流程见 docs/api-compat.md，
// 逐包理由见 api-lifecycle.yaml。
//
// 所有状态迁移走条件 UPDATE（repo 层），影响行数=0 即竞态失败——standalone/cluster 同一写法。
package statemachine

import "fmt"

// StepStatus 环节状态。
type StepStatus string

// 无类型常量（批次二十八）：同一取值既可作 StepStatus 用（转移表/判定），也可作
// string 直传 repo.TransitionStep 等以字符串参数调用的位点——单一事实源不设两份。
const (
	StatusCreated         = "created"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusApproved        = "approved"
	StatusRejected        = "rejected"
	StatusExpired         = "expired"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	// StatusInterrupted 中断占位态（spec 2026-09-21-conversation-uplift P1-3）：环节因
	// 用户取消/会话超时/进程中断而中止——区别于失败（执行了但失败）。占位报告写明
	// 「被中断、无产出或不完整」，下一轮对话历史如实携带，LLM 自行决定重做或跳过。
	StatusInterrupted = "interrupted"
	StatusRolledBack  = "rolled_back"
	StatusSkipped     = "skipped"
	StatusCancelled   = "cancelled"
)

// StepType 环节类别；plan/execute 必经审批态，readonly 裁剪审批（AGENTS 治理闸门）。
type StepType string

const (
	TypeReadonly StepType = "readonly"
	TypePlan     StepType = "plan"
	TypeApproval StepType = "approval"
	TypeExecute  StepType = "execute"
	TypeVerify   StepType = "verify"
)

// transitions 合法转移表。
var transitions = map[StepStatus][]StepStatus{
	StatusCreated:         {StatusRunning, StatusSkipped},
	StatusRunning:         {StatusSucceeded, StatusFailed, StatusInterrupted, StatusWaitingApproval},
	StatusWaitingApproval: {StatusApproved, StatusRejected, StatusExpired, StatusCancelled},
	StatusApproved:        {StatusRunning},
	StatusFailed:          {StatusRolledBack},
	// rejected/expired/succeeded/interrupted/skipped/rolled_back/cancelled 为终态
}

// CanTransition 判定 from→to 是否合法。
func CanTransition(from, to StepStatus) bool {
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// MustTransition 非法转移返回错误（repo 条件 UPDATE 前的快速失败；DB 侧仍有条件守卫兜底）。
func MustTransition(from, to StepStatus) error {
	if CanTransition(from, to) {
		return nil
	}
	return fmt.Errorf("非法状态转移: %s → %s", from, to)
}

// RequiresApproval 该环节类别是否必须经过审批态。
func RequiresApproval(t StepType) bool {
	return t == TypePlan || t == TypeExecute
}

// IsTerminal 是否终态。
//
// # 词表边界（issue #159）
//
// 本函数是**本状态机**的终态判据，词表以转移表为准：failed 在这里不是终态
// （它还有出边 → rolled_back，回滚是失败后的正常去向），degraded 不在本包
// 词表中。contract.StepSink.Finish 的 status 是宿主自定义词表（那边举例
// succeeded/failed/degraded/skipped），与本表是**两套口径**——两包组合使用
// 时（如把本函数注入 commitgate 当终态判定），宿主须让落库的 status 取值
// 跟随本表，不要按 contract 注释的举例词表灌进来。
func IsTerminal(s StepStatus) bool {
	return s == StatusRejected || s == StatusExpired || s == StatusSucceeded ||
		s == StatusInterrupted ||
		s == StatusSkipped || s == StatusRolledBack || s == StatusCancelled
}
