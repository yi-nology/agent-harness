// Package policy 操作审计门薄转发：核心已沉淀 oagentkit/toolgate/policy（含
// WithAuditGate 工具装饰器），此处以类型别名/函数转发维持既有调用点稳定。
// _shared/operation_policy.yaml 路径约定固化在 LoadOverrides 转发层。
package policy

import (
	"io/fs"

	"github.com/cloudwego/eino/components/tool"

	policykit "github.com/oagentkit/toolgate/policy"
)

type (
	Mode     = policykit.Mode
	Verdict  = policykit.Verdict
	OpType   = policykit.OpType
	Decider  = policykit.Decider
	Op       = policykit.Op
	Decision = policykit.Decision
	Policy   = policykit.Policy
	Rule     = policykit.Rule
	Arbiter  = policykit.Arbiter
	Gate     = policykit.Gate
)

const (
	ModeConfirm = policykit.ModeConfirm
	ModeAuto    = policykit.ModeAuto
	ModePlan    = policykit.ModePlan
	ModeFull    = policykit.ModeFull

	VerdictAuto      = policykit.VerdictAuto
	VerdictNeedHuman = policykit.VerdictNeedHuman
	VerdictDeny      = policykit.VerdictDeny
	VerdictPlanOnly  = policykit.VerdictPlanOnly
	VerdictUnknown   = policykit.VerdictUnknown

	OpToolCall    = policykit.OpToolCall
	OpExecuteStep = policykit.OpExecuteStep
	OpPlanSubmit  = policykit.OpPlanSubmit

	DeciderRule   = policykit.DeciderRule
	DeciderLLM    = policykit.DeciderLLM
	DeciderHuman  = policykit.DeciderHuman
	DeciderSystem = policykit.DeciderSystem
)

func Normalize(s string) Mode { return policykit.Normalize(s) }
func ValidMode(s string) bool { return policykit.ValidMode(s) }

// NewGate 构造审计门（p 为 nil 用内置基座）。
func NewGate(p *Policy, a Arbiter) *Gate { return policykit.NewGate(p, a) }

// LoadOverrides 读 experts 根的外置策略（缺文件回内置基座，阈值只降不升）。
func LoadOverrides(fsys fs.FS) (*Policy, error) {
	return policykit.LoadOverrides(fsys, "_shared/operation_policy.yaml")
}

// WithAuditGate 工具面审计装饰器转发。kit 侧符号为 AuditGate——With* 前缀保留给
// option applier，故本仓对外出口名维持 WithAuditGate 不变（宿主在用）。
func WithAuditGate(g *Gate, op Op, onAudit func(Op, Decision), t tool.InvokableTool) tool.InvokableTool {
	return policykit.AuditGate(g, op, onAudit, t)
}
