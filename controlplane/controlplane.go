// Package controlplane 控制平面的**最小契约 + 进程内参考实现 + HTTP 适配面**。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 它补的是哪一段（issue #115）
//
// 本仓此前最接近控制面的是 taskboard.Check——判一份快照的不变量，宿主随时
// 可跑。但它不回答「现在跑到哪了」（运行状态查询），更不提供任何**人工介入**
// 动作：强制释放、裁决未知结果、终止运行。缺这一面的代价是：线上出问题时
// 宿主只能自己拼命令去查、自己去改存储——这些动作**绕过了运行时的判据**，
// 改完可能与运行中的持有者打架。
//
// # 契约给什么
//
//   - **查询**：任务 / 运行的状态视图 + 不变量视图（taskboard.Check 的
//     投影，TaskView / RunView）；
//   - **介入**：强制释放（ForceRelease）、裁决未知结果（Adjudicate）、
//     终止运行（Terminate）；
//   - **权限边界**：介入动作必须走**与运行时同一套判据**，不绕闸——
//     强制释放是「经闸换持有者」（旧令牌当场作废，迟到提交凭
//     taskboard.ErrLeaseLost 被拒；下一次认领铸造的代次严格大于被释放的
//     那一代，下游栅栏据此拒收被释放者的迟到写入）；裁决走 sideeffect 的
//     对账语义（凭下游事实不凭持有权，先到者为准）；终止凭**运行代次**
//     （ExpectedGen 不匹配即 ErrStaleRun——「改完与运行中的持有者打架」
//     正是这条要拦的形态）。租户上下文经 tenant.Require fail-closed：
//     介入是写路径，必须可归责到租户与主体（issue #114 判据面的落点）；
//   - **留痕**：每次介入必须产生一条 AuditEntry（谁、什么时候、依据什么
//     结论），留痕面缺位 ⇒ 介入一律拒绝（ErrNoAuditor）——没有留痕的
//     介入无法回答「谁在什么时候依据什么改了运行状态」。
//
// # 边界
//
// 控制平面是**接口面**，不是 UI；具体界面归宿主。本包给三样：
//
//  1. Plane 契约（宿主包着自己的 taskboard.Board / sideeffect Runner /
//     运行状态存储实现它）；
//  2. 进程内参考实现（MemPlane + MemAuditor——判据怎么落的演示体，
//     也是测试与演练的最小运行时面）；
//  3. net/http JSON 适配面（httpapi.go——鉴权钩子由宿主注入，内核不鉴权）。
package controlplane

import (
	"context"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// RunID 运行标识（一轮编排；宿主生成，与 contract.RunContext.ID 同源）。
type RunID string

// Plane 控制面契约：查询 + 介入。存储中立——实现方是宿主。
//
// 实现方必须保证（三条硬契约，缺一即破）：
//
//   - 介入**走运行时的同一套判据**（令牌 / 代次 / 栅栏），不绕闸；
//   - 介入**留痕**：Auditor 缺位时一律拒绝（ErrNoAuditor），不允许
//     「先介入、留痕以后补」——补的留痕无法证明发生在动作之前；
//   - 介入请求**可归责**：Operator 与 Basis 必填（各请求的 Validate 判）。
//
// 查询面无此约束：查询不改变状态，未注入租户上下文时由宿主自行裁剪
// （httpapi 的 Authorizer 是宿主裁剪的落点）。
type Plane interface {
	// ── 查询 ──

	// TaskView 任务的运行状态视图（快照 + 该任务身上的不变量违反）。
	// 不存在 → ErrNotFound。
	TaskView(ctx context.Context, id taskboard.TaskID) (TaskView, error)
	// RunView 运行的状态视图（状态、代次、全部任务、全 run 不变量视图）。
	// 不存在 → ErrNotFound。
	RunView(ctx context.Context, id RunID) (RunView, error)

	// ── 介入 ──

	// ForceRelease 强制释放任务认领（契约见 ForceRelease）。
	ForceRelease(ctx context.Context, req ForceRelease) error
	// Adjudicate 裁决未知结果（契约见 Adjudication）。
	Adjudicate(ctx context.Context, req Adjudication) error
	// Terminate 终止运行（契约见 Termination）。
	Terminate(ctx context.Context, req Termination) error
}

// TaskView 任务的运行状态视图。
type TaskView struct {
	// Task 任务快照（状态 / owner / 租约 / 代次——与运行时读到的同一份）。
	Task taskboard.Task
	// Problems 该任务身上的不变量违反（run 级 taskboard.Check 的投影；
	// 空 = 干净）。控制面不止报状态，还报「这份状态是否自相矛盾」——
	// 人工介入的结论（Basis）往往正是一条 Problem。
	Problems []taskboard.Problem
}

// RunView 运行的状态视图。
type RunView struct {
	// RunID 运行标识。
	RunID RunID
	// Status 运行状态。取值建议用 contract 的状态常量（内核不解释词表；
	// 终态词表与 contract 保持一致，宿主自定义状态自行登记）。
	Status string
	// Gen 运行代次：运行级介入的乐观并发判据。Terminate 携带的
	// ExpectedGen 必须等于它，不等即 ErrStaleRun——介入方先查视图再介入，
	// 两次介入之间状态若已前进，旧视图上的动作必须重查。
	Gen uint64
	// Tasks 该 run 的全部任务快照（按任务 id 排序，确定性输出）。
	Tasks []taskboard.Task
	// Problems 全 run 的不变量视图（taskboard.Check）。
	Problems []taskboard.Problem
}

// ForceRelease 强制释放任务认领。
//
// # 契约：不是绕过闸，是经闸换持有者
//
// 实现必须按「接管」处理：被释放任务的旧令牌**当场作废**，任务回到可认领
// 态。于是旧持有者（崩溃的进程、失联的 worker）迟到的 Complete / Renew
// 一切动作都凭 taskboard.ErrLeaseLost 被拒；下一次认领铸造的接管代次
// （taskboard.Fence）严格大于被释放的那一代——宿主的下游资源凭代次比较
// 拒收被释放者的迟到写入（与 taskboard / sideeffect 的栅栏同一纪律）。
//
// 危险实现（契约禁止）：只改状态不清租约——旧令牌仍然匹配，幽灵持有者的
// 迟到提交会照常落账。
//
// 对不在认领态的任务强释 → 包装 taskboard.ErrLeaseLost 的错误（该任务
// 的租约已经不在了；介入重试撞上它说明第一次介入已生效）。
type ForceRelease struct {
	// TaskID 要释放的任务。
	TaskID taskboard.TaskID
	// Operator 谁在介入（留痕必填）。
	Operator string
	// Basis 依据什么结论（留痕必填：证据 / 判据 / 单号，宿主词表）。
	Basis string
}

// Verdict 对「结果未知」副作用的裁决结论（与 sideeffect.Resolution 同口径）。
type Verdict string

const (
	// VerdictApplied 已生效：Key 记为完成并留存结果，之后同一 Key 的执行
	// 拿回这份结果（重放）。
	VerdictApplied Verdict = "applied"
	// VerdictNotApplied 未生效：清除未知标记，该 Key 重新可被执行。
	VerdictNotApplied Verdict = "not_applied"
)

// Adjudication 裁决未知结果（sideeffect.Reconcile 同口径）。
//
// # 契约：裁决凭下游事实，不凭持有权
//
// 裁决**不校验栅栏 / 令牌**：它的依据是宿主查到的下游事实——原持有者可能
// 已经死亡、租约早已过期，而对账恰恰要在这种情况下进行（sideeffect
// 同一条契约）。同一 Key 的多次裁决**先到者为准**：结论已定后再来裁决
// → sideeffect.ErrNotUncertain（不覆盖先到的结论）。
type Adjudication struct {
	// EffectKey 副作用幂等键（sideeffect.Action.Key；宿主映射）。
	EffectKey string
	// Verdict 裁决结论（词表见 Verdict）。
	Verdict Verdict
	// ForceRetryable 与 sideeffect.Resolution.ForceRetryable 同口径：
	// 不可重试（Unsafe）的动作凭「未生效」的裁决不自动开门——重新打开
	// 施加的门必须是显式决策，不能是「对账流程跑完了」的自然结果。
	ForceRetryable bool
	// Operator 谁在介入（留痕必填）。
	Operator string
	// Basis 依据什么结论（留痕必填：通常是「查了下游的什么、看到什么」）。
	Basis string
}

// Termination 终止运行。
//
// # 契约：终止凭运行代次（乐观并发）
//
// ExpectedGen 必须等于当前 RunView.Gen：不等即 ErrStaleRun。终止本身就是
// 一次代次前进——两个介入者拿着同一份旧视图并发终止，先到者生效，后到者
// 凭代次被拒，不会出现「两次终止都以为自己终了这次运行」。终止是运行级
// 动作：任务租约的清理走 ForceRelease，两级判据不混。
type Termination struct {
	// RunID 要终止的运行。
	RunID RunID
	// ExpectedGen 介入方视图里的运行代次（来自 RunView.Gen）。
	ExpectedGen uint64
	// Operator 谁在介入（留痕必填）。
	Operator string
	// Basis 依据什么结论（留痕必填）。
	Basis string
}

// 错误哨兵（宿主判定走 errors.Is，不要各造一套）。
var (
	// ErrNotFound 查询目标不存在（任务 / 运行）。
	ErrNotFound = errors.New("controlplane: 目标不存在")
	// ErrInvalidRequest 介入请求不完整（Operator / Basis / 结论词表不合格）。
	// 无归责不介入。
	ErrInvalidRequest = errors.New("controlplane: 介入请求不完整——无归责不介入")
	// ErrNoAuditor 留痕面未装配。留痕是介入的硬前提：没有留痕的介入
	// 无法回答「谁在什么时候依据什么改了运行状态」，一律拒绝（fail-closed）。
	ErrNoAuditor = errors.New("controlplane: 留痕面未装配，拒绝介入")
	// ErrStaleRun 运行代次已前进：介入基于过期视图（Terminate 的乐观并发闸）。
	ErrStaleRun = errors.New("controlplane: 运行代次已前进，介入基于过期视图")
	// ErrTerminalRun 运行已是终态：无需也无法终止（介入重试撞上它说明
	// 第一次介入已生效）。
	ErrTerminalRun = errors.New("controlplane: 运行已是终态，无需终止")
)

// Validate 介入请求的留痕完整性判定（Operator 与 Basis 必填——无归责不介入）。
func (r ForceRelease) Validate() error {
	return validateIntervention(string(r.TaskID), r.Operator, r.Basis)
}

// Validate 介入请求的留痕完整性 + 结论词表判定。
func (r Adjudication) Validate() error {
	if err := validateIntervention(r.EffectKey, r.Operator, r.Basis); err != nil {
		return err
	}
	switch r.Verdict {
	case VerdictApplied, VerdictNotApplied:
		return nil
	default:
		return errors.Wrap(ErrInvalidRequest, "裁决结论不在词表内: "+string(r.Verdict))
	}
}

// Validate 介入请求的留痕完整性判定。
func (r Termination) Validate() error {
	return validateIntervention(string(r.RunID), r.Operator, r.Basis)
}

// validateIntervention 三类介入请求共用的留痕完整性判据（一个真相）。
func validateIntervention(target, operator, basis string) error {
	switch {
	case target == "":
		return errors.Wrap(ErrInvalidRequest, "介入目标为空")
	case operator == "":
		return errors.Wrap(ErrInvalidRequest, "缺介入者（Operator）")
	case basis == "":
		return errors.Wrap(ErrInvalidRequest, "缺介入依据（Basis）")
	}
	return nil
}
