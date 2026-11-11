// reconcile.go —— 恢复判定与副作用记录的**对账**（v0.33.0，issue #90）。
//
// # 缺口在哪
//
// checkpoint 与 sideeffect 各答一半的问题，此前**互不感知**：
//
//	checkpoint.BuildPlan —— 「哪几步不用再跑」（依据：快照 Done）
//	sideeffect 闸        —— 「这个动作做过没有 / 结果是否未知」（依据：闸的账）
//
// 于是「快照说已完成、闸说结果未知」可以**同时成立**：宿主在副作用的结论
// 落定**之前**就把快照写了（乐观落库），然后进程死了。恢复时按快照跳过该步
// ⇒ 那个「结果未知」的副作用**既没对账也没重做**——它悬在中间，而恢复判定
// 认为一切正常。「续跑而非重放」的前提（快照可信）被单方面破坏，且失效是
// **静默**的。
//
// # 修法：恢复前三方对账，判据落 checkpoint 侧
//
// Reconcile 把「计划要跳过的步骤」逐个对上**闸侧视图**：
//
//	快照 Done 含该步（⇒ 计划 Skip 它） + 闸侧不是「已结算」也不是「无记录」
//	⇒ 冲突 ⇒ 快照不可信 ⇒ **阻塞**（不给看起来能跑的半截计划），不得静默跳过。
//
// 判据是纯函数，可机械复跑；两侧的状态由宿主在恢复流程里注入——本包不
// import sideeffect（依赖方向保持单向，sideeffect 的接口面由它自己的车道
// 演进），判据本身却收在**本包**：各宿主各写一遍对账口径必然漂移。
//
// # 闸侧视图怎么来（宿主接线口径）
//
// sideeffect.Store 只有一个观察口：Acquire。宿主对每个「快照说已完成且有
// 副作用」的步骤，用**同一 Action**（同一幂等键）试探一次，按 Outcome 映射：
//
//	OutcomeReplayed  → GateSettled    （闸记了「已完成」并有结果：与快照一致）
//	OutcomeUncertain → GateUncertain  （结果未知：与快照冲突）
//	OutcomeContended → GateInFlight   （有未裁决的尝试 / 他人持有：冲突）
//	OutcomeApplied   → GateUnrecorded （闸认为「没做过」：与快照冲突——
//	                                    步骤的副作用从未记账，快照的「完成」
//	                                    说法没有账面支撑）
//	查询本身失败      → GateUnknown    （判不了：按冲突处理，不默认放行）
//
// 注意试探的代价：OutcomeApplied 会授予执行权。宿主若不想真的执行，应在
// 试探后 Abandon（声明了 RetrySafetySafe 才能干净重试）——或者干脆不试探、
// 直接按 GateUnknown 报冲突，让对账走 sideeffect.Runner.Reconcile。
// **没有动作的步骤（纯计算）不需要视图**：无副作用就无对账可言。
package checkpoint

// GateState 闸侧对一个步骤的副作用状态视图（宿主从 sideeffect 对账后注入）。
type GateState string

const (
	// GateSettled 已记账且有结论：动作已完成并有结果（Replayed）。
	GateSettled GateState = "settled"
	// GateUncertain 结果未知：可能已生效，必须对账（Uncertain）。
	GateUncertain GateState = "uncertain"
	// GateInFlight 有一次尚未裁决的尝试，或资源正被他人持有（Contended）。
	GateInFlight GateState = "in_flight"
	// GateUnrecorded 闸认为「没做过」：会授予执行权（Applied）——
	// 快照的「已完成」没有账面支撑。
	GateUnrecorded GateState = "unrecorded"
	// GateUnknown 判不了：查询失败 / 宿主无从观测。按冲突处理——
	// **判不了不是放行的理由**，与 archcheck 门禁「读不到就显式报错」同一条口径。
	GateUnknown GateState = "unknown"
	// GateAbsent 闸侧无记录：该步骤没有副作用动作（纯计算），或动作从未经闸。
	// 不构成冲突。
	GateAbsent GateState = "absent"
)

// gateStateConflicts 快照「已完成」与哪些闸侧状态互斥。
//
// GateSettled / GateAbsent 与快照一致或无关；其余四种都说明
// 「快照的完成说法缺少账面支撑」。
func gateStateConflicts(s GateState) bool {
	switch s {
	case GateSettled, GateAbsent:
		return false
	case GateUncertain, GateInFlight, GateUnrecorded, GateUnknown:
		return true
	default:
		// 未登记的状态值：按冲突处理（沉默按最弱，方向是阻塞那一侧）。
		return true
	}
}

// GateView 闸侧对**一个步骤**的副作用状态视图。
type GateView struct {
	// Step 步骤 ID（与编排图 / 快照 Done 同一词表）。
	Step string
	// State 闸侧状态。
	State GateState
	// Detail 可读原因（冲突报告里原样回显，如「Key run-1:publish 处于结果未知态」）。
	// 宿主填写的观测依据——排障时「凭什么判的」比「判了什么」更值钱。
	Detail string
}

// anomalyKindSideEffectConflict 恢复对账冲突的异常类别。
const anomalyKindSideEffectConflict = AnomalyKind("side_effect_conflict")

// AnomalySideEffectConflict 恢复对账冲突的异常类别：
// 计划要跳过的步骤，其副作用在闸侧不是「已结算」——快照的完成记录不可信。
const AnomalySideEffectConflict = anomalyKindSideEffectConflict

// Reconcile 恢复前对账：把闸侧视图对进恢复计划（纯函数，无副作用）。
//
// 判据：计划要 **Skip** 的每一步，闸侧必须是 GateSettled（已结算）或
// GateAbsent（无副作用动作）。其余一律冲突——快照说「已完成」、闸说
// 「未知 / 未裁决 / 没做过 / 判不了」都可以同时成立，而按快照跳过会让那个
// 副作用**既没对账也没重做**。
//
// # 冲突的处置：阻塞，且不作壁上观
//
// 任一冲突即判**快照不可信**：计划整体作废（Skip/Run 清空）并报
// AnomalySideEffectConflict（逐冲突点名，Detail 原样回显）。宿主先对账
// （sideeffect.Runner.Reconcile）、修好账面或快照，再重新 BuildPlan +
// Reconcile。不给「看起来能跑」的半截计划——与终态重开同一条哲学。
//
// # 只对 Skip 侧判
//
// 计划要 **Run** 的步骤带着闸侧 Uncertain/InFlight 是**设计的**：执行时闸
// 自己会拒绝（OutcomeUncertain / Contended），那是「至多一次」的正常运转，
// 不是恢复判定要拦的冲突。闸是执行路径的判据，本函数只补「按快照跳过」
// 这一侧的协调。
func Reconcile(p Plan, views []GateView) Plan {
	conflicts := 0
	for _, v := range views {
		if !gateStateConflicts(v.State) {
			continue
		}
		if !planSkips(p, v.Step) {
			continue // 不是「按快照跳过」的步骤：闸在执行路径上自己会判
		}
		conflicts++
		p.addAnomaly(anomalyKindSideEffectConflict, v.Step, reconcileConflictDetail(v))
	}
	if conflicts == 0 {
		return p
	}
	// 快照不可信 ⇒ 整个计划作废：宿主先对账，再重建。半截计划比没有计划更危险
	// ——它会让人以为「只有那几步有问题」，而冲突说明的是**账面与快照脱节**。
	p.Skip, p.Run = nil, nil
	return p
}

// planSkips 计划是否正要跳过该步骤。
func planSkips(p Plan, step string) bool {
	for _, id := range p.Skip {
		if id == step {
			return true
		}
	}
	return false
}

// reconcileConflictDetail 一条冲突的可读原因。
//
// 状态名直接回显（宿主按状态查处置），Detail 是宿主写的观测依据。
// 文案要能让人**照着做**：先对账，再重建计划。
func reconcileConflictDetail(v GateView) string {
	base := ""
	switch v.State {
	case GateUncertain:
		base = "副作用结果未知（可能已生效），按快照跳过会让它既不对账也不重做"
	case GateInFlight:
		base = "该动作有一次尚未裁决的尝试（或资源被他人持有），完成记录与账面脱节"
	case GateUnrecorded:
		base = "闸侧没有任何「已做过」的记录（再动它会重新授予执行权），快照的完成说法缺少账面支撑"
	case GateUnknown:
		base = "闸侧状态无法判定（查询失败），不得按快照默认放行"
	default:
		base = "闸侧状态与快照的完成记录冲突"
	}
	if v.Detail != "" {
		return "快照记该步已完成，但闸侧视图为 " + string(v.State) + "：" + base + "（" + v.Detail + "）"
	}
	return "快照记该步已完成，但闸侧视图为 " + string(v.State) + "：" + base
}
