// Package policy 操作审计门薄转发：核心已沉淀 oagentkit/toolgate/policy（含
//
// 生命周期: Stable（v0.33.0 定级）——只允许 additive 变更（新增导出符号 /
// 新增方法且给零值默认实现 / 新增错误值）；改签名、改语义、改默认值、
// 删导出符号都算 breaking，判定规则与变更流程见 docs/api-compat.md，
// 逐包理由见 api-lifecycle.yaml。
// 本包是门面：调用面稳定，判据语义跟随上游 github.com/oagentkit/toolgate 演进。
//
// WithAuditGate 工具装饰器），此处以类型别名/函数转发维持既有调用点稳定。
// _shared/operation_policy.yaml 路径约定固化在 LoadOverrides 转发层。
package policy

import (
	"io/fs"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/guardx/invoke"

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

// Mutating 返回指向 b 的指针，供在 Op / Rule 字面量里内联填写变更标记：
// `policy.Op{..., Mutating: policy.Mutating(true)}`。
//
// # 为什么门面必须转发它（issue #64）
//
// toolgate 把 `Op.Mutating` 从 bool 三态化为 `*bool`（nil=未知）后，宿主经
// 门面写 `Op{Mutating: mutating}` 直接编译不过（bool 不能作 *bool），配套构造器
// 又只有 toolgate 侧有——宿主被迫直引 toolgate/policy，同一文件里出现两个
// policy 别名，「薄转发让宿主只 import 门面」的包设计在这里断掉。
//
// 三态的判定语义在 kit 侧（nil 时变异守卫 fail-safe 升人审，不得默认按非变更
// 放行）：本仓只保证构造器经门面可达，不复制判定。
func Mutating(b bool) *bool { return policykit.Mutating(b) }

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

// Stage 把审计门接成**判定环上的一环**（invoke.Stage）。
//
// # 为什么门面原先缺这一项
//
// 审计门有两个接入口：
//   - `WithAuditGate`——把**单个工具**包起来（eino 的 InvokableTool 形态）；
//   - `Stage`——把门接成**判定环的一环**（invoke 链的形态）。
//
// 本仓门面原先只转发了前者。后者恰恰是「一处装配、处处生效」的形态：
// 工具调用、程序内调用（`ptc` 的 `host.<tool>()`）、子进程侧调用，
// 全部走同一条 invoke 链 ⇒ 审批规则**只装配一次**。
//
// 缺了它，宿主想接审批进判定环只能去 import 上游 toolgate ——
// 那就绕过了本仓门面，**调用面逐字兼容**的承诺断在这里。
//
// # 为什么 `invoke` 要登记进 allowed_imports
//
// `Stage` 的返回类型是 `invoke.Stage`（来自 `github.com/oagentkit/guardx/invoke`）。
// 它是**签名本身要求的**，不提供任何实现——与既有的 `io/fs`、
// `eino/components/tool` 同款。
func Stage(g *Gate, op Op, onDecision func(Decision)) invoke.Stage {
	return policykit.Stage(g, op, onDecision)
}
