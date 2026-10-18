package checkpoint

import "github.com/yi-nology/agent-harness/contract"

// StepDef 编排图中的一个步骤。
//
// 内核只认 **ID 与图内顺序**，不认语义：Label 是给宿主/用户看的，
// 内核既不解释也不校验。
type StepDef struct {
	ID    string
	Label string
}

// AnomalyKind 编排图与快照的不一致类别。
type AnomalyKind string

const (
	// AnomalyEmptyStepID 步骤 ID 为空，已从编排图剔除。
	AnomalyEmptyStepID AnomalyKind = "empty_step_id"
	// AnomalyDuplicateStep 编排图内步骤 ID 重复，只保留首次出现处。
	AnomalyDuplicateStep AnomalyKind = "duplicate_step"
	// AnomalyDoneNotInGraph 快照记为已完成，但当前编排图里没有这一步
	// （典型成因：宿主换过编排包，图收缩了）。
	AnomalyDoneNotInGraph AnomalyKind = "done_not_in_graph"
	// AnomalyDoneOutOfOrder 已完成集合不是编排图的前缀（含空洞）。
	AnomalyDoneOutOfOrder AnomalyKind = "done_out_of_order"
	// AnomalyCompleteWithPending 快照标记运行已终结，编排图里却仍有未完成步骤。
	AnomalyCompleteWithPending AnomalyKind = "complete_with_pending"
)

// Anomaly 一条不一致报告。
//
// **只报告不裁决**（与 watchdog 同款哲学）：除终态重开外，
// 内核都按最保守的一种解释继续跑，而不是停下来问宿主。
type Anomaly struct {
	Kind   AnomalyKind
	Step   string
	Detail string
}

// Plan 恢复计划：续跑而非重放。
type Plan struct {
	// Skip 本次**不执行**的步骤（已完成），按编排图顺序。
	Skip []string
	// Run 本次**要执行**的步骤，按编排图顺序。
	Run []string
	// Anomalies 编排图与快照的不一致，按检出顺序。
	Anomalies []Anomaly
}

// Blocked 该计划是否含内核**拒绝自动决定**的异常。
//
// 唯一一种：快照说这次运行已终结，图里却还有未完成步骤。
// 内核不会擅自重开一个已终结的运行——是否重开是宿主的决定。
// 其余异常都不阻断：内核已按最保守解释给出可执行计划。
func (p Plan) Blocked() bool {
	for _, a := range p.Anomalies {
		if a.Kind == AnomalyCompleteWithPending {
			return true
		}
	}
	return false
}

// HasAnomaly 是否报告了指定类别的不一致。
func (p Plan) HasAnomaly(kind AnomalyKind) bool {
	for _, a := range p.Anomalies {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

func (p *Plan) addAnomaly(kind AnomalyKind, step, detail string) {
	p.Anomalies = append(p.Anomalies, Anomaly{Kind: kind, Step: step, Detail: detail})
}

// isTerminal 该状态是否为运行终态。取自 contract 的状态常量集——
// 不在本包另立一份，否则两个包会各自漂移出不同的终态定义。
func isTerminal(status string) bool {
	switch status {
	case contract.StatusSucceeded, contract.StatusFailed,
		contract.StatusCancelled, contract.StatusTimedOut:
		return true
	default:
		return false
	}
}

// BuildPlan 由编排图与（可为 nil 的）进度快照算出恢复计划。纯函数，无副作用。
//
// # snap 为 nil
//
// 首次执行：全量 Run，不报异常。
//
// # 已完成的可信边界：图内**连续已完成的前缀**
//
// 这是本包最关键的一条判定。一步的完成，只有在它**图内的前驱也都已完成**时
// 才可信。若快照的完成集有空洞（后面几步完成了、前面几步没完成），
// 说明推进本身已经失序——此时采信其中一部分，等于承认一个已失序的快照仍然可信，
// 而那正是续跑唯一危险的前提。故空洞之后的完成**一律不采信**，整体重跑，
// 并报 AnomalyDoneOutOfOrder 让宿主知道发生过失序。
//
// 代价是「失序时会多跑几步」。这个方向是刻意选的：多跑可恢复，重复副作用往往不可。
//
// # 图收缩
//
// 快照里的某步在当前图中不存在时，报 AnomalyDoneNotInGraph。
// 内核不重跑它（压根不在图里），但必须报告——宿主可能刚换过编排包，
// 无声跳过会让「换包后少做了事」彻底不可见。
//
// # 终态不自动重开
//
// 快照标记运行已终结、图里却有未完成步骤时，计划置为 Blocked 且 Run/Skip 均留空：
// 内核拒绝替宿主决定是否重开，也不给一个「看起来能跑」的半截计划。
func BuildPlan(graph []StepDef, snap *Snapshot) Plan {
	var p Plan

	// 净化编排图：剔空 ID、去重（保留首次出现处 → 保证同一图必得同一计划）。
	seen := make(map[string]bool, len(graph))
	ids := make([]string, 0, len(graph))
	for _, s := range graph {
		switch {
		case s.ID == "":
			p.addAnomaly(AnomalyEmptyStepID, "", "步骤 ID 为空，已从编排图剔除")
		case seen[s.ID]:
			p.addAnomaly(AnomalyDuplicateStep, s.ID, "编排图内步骤 ID 重复，只保留首次出现处")
		default:
			seen[s.ID] = true
			ids = append(ids, s.ID)
		}
	}

	if snap == nil {
		p.Run = ids
		return p
	}

	done := make(map[string]bool, len(snap.Done))
	for _, id := range snap.Done {
		if id != "" {
			done[id] = true
		}
	}
	for _, id := range snap.Done {
		if id != "" && !seen[id] {
			p.addAnomaly(AnomalyDoneNotInGraph, id, "快照记为已完成，但当前编排图无此步骤")
		}
	}

	n := 0
	for ; n < len(ids) && done[ids[n]]; n++ {
	}
	p.Skip = ids[:n:n]
	p.Run = ids[n:]

	// 空洞检测：未完成的前缀之后，还有被记为已完成的步骤。
	if n < len(ids) {
		for _, id := range ids[n:] {
			if done[id] {
				p.addAnomaly(AnomalyDoneOutOfOrder, "",
					"已完成步骤不是编排图的前缀，其后的完成记录不采信")
				break
			}
		}
	}

	// 已终结的运行不被自动重开：把已算出的计划作废，交给宿主显式决定。
	if isTerminal(snap.Status) && len(p.Run) > 0 {
		p.addAnomaly(AnomalyCompleteWithPending, "",
			"快照标记运行已终结但仍有未完成步骤，内核不自动重开，需宿主显式决定")
		p.Skip, p.Run = nil, nil
	}

	return p
}
