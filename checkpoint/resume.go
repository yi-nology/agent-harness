package checkpoint

import "github.com/yi-nology/agent-harness/contract"

// StepDef 编排图中的一个步骤。
//
// 内核只认 **ID、图内顺序与依赖声明**，不认语义：Label 是给宿主/用户看的，
// 内核既不解释也不校验。
//
// # 依赖声明（零值 = 有序步骤链）
//
// 图默认是**有序步骤链**：一步的完成，只有在它图内的前驱也都已完成时才可信。
// 并行组（fan-out）需要显式声明，否则合法的乱序完成集会被当成失序而整体重跑：
//
//	StepDef{ID: "a"}                           // 未声明：位置隐含前驱（有序链）
//	StepDef{ID: "a", Isolated: true}           // 显式无前驱：可乱序完成
//	StepDef{ID: "a", DependsOn: []string{"b"}} // 显式前驱集：取代位置隐含前驱
//
// 未声明即有序链，是**零值语义**：既有调用方逐字不变。依赖语义的完整推导、
// 声明写法与混图的坑见 deps.go。
type StepDef struct {
	ID    string
	Label string

	// DependsOn 本步的显式前驱集（步骤 ID）。非空时**取代**图内位置隐含的前驱。
	//
	// 列出的是**全部直接前驱**（间接前驱由可信性的传递闭包自动带上）。
	// 指向不在图内的 ID = 悬空依赖：该步恒不可信（恒重跑），不静默放行。
	//
	// ⚠️ **空切片不等于「无前驱」**：`DependsOn: []string{}` 走的是位置隐含前驱
	// （有序链语义），不是「本步无前驱」——要声明无前驱请用 `Isolated: true`。
	// 这条刻意不做「nil 与空切片分义」：宿主若动态拼装前驱集，拼出空切片时
	// 悄悄变成链语义（更严）尚可，拼成 nil 时变成「无前驱」（更松）才是事故。
	// 两种空值都按「未声明」处理，方向恒为保守的那一侧。
	DependsOn []string

	// Isolated 本步不依赖任何其他步骤（与执行环境的隔离无关）：
	// 它自己完成即可信，可与任何步骤乱序完成。
	//
	// 注意它只改变**本步**的前驱集合：排在它之后的未声明步骤，仍按位置把它当前驱
	// ——「本步不依赖别人」≠「别人不依赖本步」。
	Isolated bool
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
	// AnomalyDoneOutOfOrder 完成记录与依赖不符：零值语义下是「完成集合不是编排图的
	// 前缀（含空洞）」；声明依赖后收窄为「该步有依赖却失序」（它的前驱没满足）。
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
	// Skip 本次**不执行**的步骤（已完成且可信），按编排图顺序。
	Skip []string
	// Run 本次**要执行**的步骤。
	//
	// 图里没人声明依赖时按**编排图顺序**；声明过依赖时按**依赖拓扑序**
	// （显式依赖先满足，同层保持编排图顺序）——图顺序本就与依赖一致时两者相同。
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
// # 已完成的可信边界：按**依赖**判定
//
// 这是本包最关键的一条判定。一步的完成可信 ⟺ 它自己已完成 **且** 它的全部前驱
// 也都可信完成（传递闭包：前驱本身不可信时，靠它完成的这一步同样不可信——
// 采信它等于承认一个已失序的快照仍然可信，而那正是续跑唯一危险的前提）。
//
// **零值语义（图里没人声明依赖）= 图内连续已完成的前缀。**
// 一步的完成只有在它图内的前驱也都已完成时才可信；若快照的完成集有空洞
// （后面几步完成了、前面没完成），说明推进本身已经失序——故空洞之后的完成
// **一律不采信**，整体重跑，并报 AnomalyDoneOutOfOrder 让宿主知道发生过失序。
//
// **声明过依赖时，按各自声明的前驱判定。** 并行组（fan-out）里
// 「第 1 个还没完成、第 3 个完成」是**合法完成集**，不是失序：前置都满足的
// 那一步自己完成即可信，不必等图内更早的步骤。此时失序判定收窄到
// 「有依赖却失序」——某步自己完成了、但它的前驱没满足，那种完成不采信，
// 并点名报告（Anomaly.Step 是那一步的 ID）。
//
// 两种口径下「代价」的方向一致：**多跑可恢复，重复副作用往往不可。**
// 有依赖却失序时，宁可重跑那一步，也不采信一条说不清来历的完成记录。
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
	steps := make([]StepDef, 0, len(graph))
	for _, s := range graph {
		switch {
		case s.ID == "":
			p.addAnomaly(AnomalyEmptyStepID, "", "步骤 ID 为空，已从编排图剔除")
		case seen[s.ID]:
			p.addAnomaly(AnomalyDuplicateStep, s.ID, "编排图内步骤 ID 重复，只保留首次出现处")
		default:
			seen[s.ID] = true
			ids = append(ids, s.ID)
			steps = append(steps, s)
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

	specs := make([]depSpec, len(steps))
	for i, s := range steps {
		specs[i] = resolveSpec(s)
	}
	index := make(map[string]int, len(ids))
	for i, id := range ids {
		index[id] = i
	}

	// 可信完成集。未声明依赖时它就是「连续已完成的前缀」，与旧口径逐字等价。
	satisfied := computeSatisfied(ids, specs, done)

	skip := make([]string, 0, len(ids))
	run := make([]string, 0, len(ids))
	for _, id := range ids {
		if satisfied[id] {
			skip = append(skip, id)
		} else {
			run = append(run, id)
		}
	}
	p.Skip, p.Run = skip, run

	if anyDeclared(specs) {
		// 声明过依赖：逐点点名——谁完成了但前驱没满足。
		for i, id := range ids {
			if !done[id] || satisfied[id] {
				continue
			}
			pred, status := firstUnsatisfiedPred(i, ids, specs, satisfied, done, index)
			p.addAnomaly(AnomalyDoneOutOfOrder, id, untrustedDetail(id, pred, status))
		}
		// 待执行顺序：依赖拓扑序。图顺序本就与依赖一致时，与图顺序逐字相同。
		p.Run = topoRun(specs, index, run)
	} else {
		// 纯有序链：旧口径——一条异常、不点名、文案逐字不变。
		for _, id := range ids {
			if done[id] && !satisfied[id] {
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
