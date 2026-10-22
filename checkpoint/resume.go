package checkpoint

import "github.com/yi-nology/agent-harness/contract"

// StepDef 编排图中的一个步骤。
//
// 内核只认 **ID、图内顺序、并行组**，不认语义：Label 是给宿主/用户看的，
// 内核既不解释也不校验。
type StepDef struct {
	ID    string
	Label string
	// ParallelGroup 并行组号（fan-out 语义）。
	//
	// 0（缺省）= 顺序步：依赖图内顺序在其前的**所有**步骤，完成集须是连续前缀。
	// 同一非 0 组号 = 一组**互不依赖**的并行步（fan-out）：组内成员可乱序完成，
	// 各自只依赖「组外、顺序在其前」的步骤——故某个成员完成即可信，
	// 不因兄弟成员尚未完成而作废。跨组/顺序步之间的前后依赖不变。
	//
	// 编排图应把**并发执行**的连续步骤放进同一组；内核不校验组内步骤是否真的相邻
	// （与「只认 ID 与顺序、不认语义」一致——图良构是宿主责任）。
	ParallelGroup int
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
// # 已完成的可信边界：依赖全部可信
//
// 这是本包最关键的一条判定。一步的完成，只有在它**依赖的前驱都已可信完成**时才可信。
// 依赖 = 图内顺序在其前、且不同并行组的步骤（ParallelGroup 见 StepDef）。
//
//   - **顺序步**（ParallelGroup==0，缺省）依赖其前所有步骤 → 退化为「连续已完成的
//     前缀」：完成集有空洞（后面完成、前面没完成）说明推进失序，空洞后的完成
//     一律不采信、整体重跑，并报 AnomalyDoneOutOfOrder。
//   - **fan-out 并行组**（同非 0 ParallelGroup）成员互不依赖：某个兄弟完成即可信，
//     不因其他兄弟未完成而作废——fan-out 乱序完成是常态而非失序。
//
// 采信一半失序快照是续跑唯一危险的前提，故默认（顺序）从严；fan-out 从宽是显式声明的。
// 代价是「顺序链失序时会多跑几步」。这个方向是刻意选的：多跑可恢复，重复副作用往往不可。
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
	steps := make([]StepDef, 0, len(graph))
	for _, s := range graph {
		switch {
		case s.ID == "":
			p.addAnomaly(AnomalyEmptyStepID, "", "步骤 ID 为空，已从编排图剔除")
		case seen[s.ID]:
			p.addAnomaly(AnomalyDuplicateStep, s.ID, "编排图内步骤 ID 重复，只保留首次出现处")
		default:
			seen[s.ID] = true
			steps = append(steps, s)
		}
	}

	if snap == nil {
		p.Run = stepIDs(steps)
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

	// 可信完成判定（fan-out 感知）：一步可信 = 自身已完成，且其**依赖全部可信**。
	// 依赖 = 图内顺序在它之前、且**不同并行组**的步骤；同组（ParallelGroup 相同且非 0）
	// = 互不依赖的 fan-out 兄弟，可乱序完成。全部 ParallelGroup==0 时退化为
	// 「连续前缀」语义（向后兼容）——顺序步依赖其前所有步骤。
	trusted := make([]bool, len(steps))
	skip := make([]string, 0, len(steps))
	run := make([]string, 0, len(steps))
	for i, s := range steps {
		if done[s.ID] && depsTrusted(steps, trusted, i) {
			trusted[i] = true
			skip = append(skip, s.ID)
			continue
		}
		run = append(run, s.ID)
		if done[s.ID] {
			// 自身已完成，却因依赖未齐而不可信 = 失序推进（跳过了前驱 / fan-out 兄弟
			// 尚未完成）。fan-out 兄弟互不依赖，故不会因此误报。
			p.addAnomaly(AnomalyDoneOutOfOrder, s.ID,
				"已完成但依赖未全部可信（前驱或同组外步骤未完成），该完成记录不采信")
		}
	}
	p.Skip, p.Run = skip, run

	// 已终结的运行不被自动重开：把已算出的计划作废，交给宿主显式决定。
	if isTerminal(snap.Status) && len(p.Run) > 0 {
		p.addAnomaly(AnomalyCompleteWithPending, "",
			"快照标记运行已终结但仍有未完成步骤，内核不自动重开，需宿主显式决定")
		p.Skip, p.Run = nil, nil
	}

	return p
}

// depsTrusted 步骤 i 的全部依赖是否已可信。依赖 = 图内顺序在 i 之前、且不同并行组的
// 步骤；同组（ParallelGroup 相同且非 0）的兄弟步骤互不依赖（fan-out），跳过。
// 顺序步（ParallelGroup==0）依赖其前**所有**步骤（含更早的顺序步），故同组跳过仅对
// 非 0 组生效——这是「全 0 组退化为连续前缀」的关键。i 的依赖都在 j<i，故单趟前向
// 扫描即可，无需递归。
func depsTrusted(steps []StepDef, trusted []bool, i int) bool {
	g := steps[i].ParallelGroup
	for j := 0; j < i; j++ {
		if g != 0 && steps[j].ParallelGroup == g {
			continue // 同一 fan-out 组的兄弟，互不依赖
		}
		if !trusted[j] {
			return false
		}
	}
	return true
}

// stepIDs 抽出步骤 ID 序列（建独立切片，不与调用方共享底层数组）。
func stepIDs(steps []StepDef) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.ID)
	}
	return out
}
