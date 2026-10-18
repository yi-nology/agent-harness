// Package statemachine 环节状态机（PHASE1_PLAN §6.2）：合法转移表硬编码 + 全覆盖单测。
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
func IsTerminal(s StepStatus) bool {
	return s == StatusRejected || s == StatusExpired || s == StatusSucceeded ||
		s == StatusInterrupted ||
		s == StatusSkipped || s == StatusRolledBack || s == StatusCancelled
}
