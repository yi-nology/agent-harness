package checkpoint

import "fmt"

// 依赖语义层：把「有序步骤链」与「并行组」区分开的那一面。
//
// # 为什么需要这一层
//
// BuildPlan 原本只认一种图：**有序步骤链**——一步的完成，只有在它图内的前驱
// 都已完成时才可信，完成集必须是连续前缀。这条规则对有序链是对的（前驱没完成
// 则后继不可信），但对 **fan-out 并行组**是错的：并行组里各步骤彼此独立、
// 乱序完成，「第 1 个还没完成、第 3 个完成」是**合法完成集**，不是失序。
//
// 按前缀规则判，合法完成集被整体作废重跑——本该逐个续跑的并行阶段被迫整批重来，
// 下游只能绕开本包自建一套 checkpoint。本仓已知实证：某宿主的并行插件阶段
// 正是因此放弃了 BuildPlan、自建了逐插件存取。
//
// # 三种声明形态（字段语义见 StepDef，完整推导见 docs/CHECKPOINT.md §三之 1）
//
//	StepDef{ID: "a"}                           // 未声明：位置隐含前驱（有序链，零值语义）
//	StepDef{ID: "a", Isolated: true}           // 显式无前驱：可乱序完成
//	StepDef{ID: "a", DependsOn: []string{"b"}} // 显式前驱集：取代位置隐含前驱
//
// 未声明即有序链，是**零值语义**：既有调用方逐字不变（见 BuildPlan 的两种口径）。
//
// # 为什么显式声明是「取代」而不是「叠加」
//
// 叠加（声明只能加约束）表达不了并行组：组内第二步的位置前驱包含第一步，
// 叠加语义下它仍然依赖第一步——并行组退化成有序链。故显式声明**取代**位置
// 隐含的前驱：列出的就是**全部直接前驱**，间接前驱由可信性的传递闭包自动带上。
//
// 代价是作者必须列全直接前驱，漏列等于抹掉那条约束。这条代价是刻意接受的：
// **叠加语义的代价是「并行组表达不出来」**（本层要解决的正是这个）。
//
// ⚠️ 漏列**检不出来**，本包不假装能兜住它：内核不知道作者的真实意图，
// 「漏了」的形态是「**过早采信**一个本不该采信的完成记录」——**静默**，
// 且方向正好是续跑最危险的那一侧（重复副作用往往不可）。
// 能机械检出的是另一头：「声明了依赖但没满足」——那种完成不采信，并点名报告。
// ⇒ 漏列这条风险只能靠**文档 + 宿主自审**兜（见 docs/CHECKPOINT.md §三之 1）。
//
// # 混图的坑：声明的前驱别指向更晚的链式步骤
//
// 位置隐含前驱是「排在它之前的全部步骤」，于是「靠后的步骤」反过来依赖
// 「靠前的步骤」。若靠前的步骤显式声明依赖靠后的**链式**步骤，两者构成环：
// 环内步骤恒不可信（恒重跑）。声明依赖**更早**的步骤、或依赖显式声明为
// 独立（Isolated）的步骤，都不会成环。

// depSpec 一步的依赖声明（StepDef 的解析形态）。
type depSpec struct {
	// isolated 本步显式声明无前驱。
	isolated bool
	// explicit 本步显式前驱集（DependsOn）。非空时**取代**位置隐含的前驱。
	explicit []string
}

// declared 该步是否显式声明过依赖。
func (s depSpec) declared() bool { return s.isolated || len(s.explicit) > 0 }

// resolveSpec 解析一步的依赖声明。
//
// DependsOn 非空时以它为准（列了名字就是全部）；否则 Isolated 生效（无前驱）。
// 两者都设不矛盾：DependsOn 更具体，且它给出的约束更严——
// **严的那边赢**，免得一个漏填的名字换来一条被忽略的依赖。
func resolveSpec(s StepDef) depSpec {
	return depSpec{isolated: s.Isolated, explicit: s.DependsOn}
}

// anyDeclared 图里是否出现过任何依赖声明。决定用哪种完成集口径。
func anyDeclared(specs []depSpec) bool {
	for _, s := range specs {
		if s.declared() {
			return true
		}
	}
	return false
}

// computeSatisfied 算「可信完成集」。
//
// 一步的完成可信 ⟺ 它自己已完成 **且** 它的全部前驱也都可信完成
// （传递闭包：前驱本身不可信时，靠它完成的这一步同样不可信——
// 采信它等于承认一条已失序的快照仍然可信，而那正是续跑唯一危险的前提）。
//
// 未声明依赖的步骤走**位置隐含前驱**（图内排在它之前的全部步骤），
// 于是本判定在纯有序链上与「连续已完成前缀」逐字等价。
//
// 环上的步骤恒不可信：可信集是 fixed point，环里的每一步都在等对方先完成，
// 于是谁也完成不了。这正是「多跑可恢复」那一侧——环是编排图的写法错误，
// 表现是这几步恒重跑，而不是恒被采信。
func computeSatisfied(ids []string, specs []depSpec, done map[string]bool) map[string]bool {
	satisfied := make(map[string]bool, len(ids))
	for changed := true; changed; {
		changed = false
		// prefixOK：ids[0..i-1] 是否全部可信完成（位置隐含前驱的判定面）。
		prefixOK := true
		for i, id := range ids {
			if satisfied[id] {
				continue
			}
			if done[id] && stepTrusted(specs[i], satisfied, prefixOK) {
				satisfied[id] = true
				changed = true
				continue
			}
			prefixOK = false
		}
	}
	return satisfied
}

// stepTrusted 一步在当前可信集下是否可信完成。
//
// prefixOK 是「图内排在它之前的步骤是否全部可信完成」——位置隐含前驱的判定面。
func stepTrusted(spec depSpec, satisfied map[string]bool, prefixOK bool) bool {
	switch {
	case len(spec.explicit) > 0:
		for _, p := range spec.explicit {
			if !satisfied[p] {
				return false
			}
		}
		return true
	case spec.isolated:
		return true // 显式无前驱：自己完成即可信
	default:
		return prefixOK // 未声明：位置隐含前驱全部可信完成才可信
	}
}

// predStatus 前驱「未满足」的三种状态。
//
// 三种要分清：文案说错方向，宿主就照着错方向去查——那是**报告在制造噪音**，
// 比不报更费时间。
type predStatus int

const (
	// predSatisfied 前驱已满足（不该出现在报告里）。
	predSatisfied predStatus = iota
	// predNotInGraph 悬空依赖：前驱 ID 根本不在编排图内（编排图的写法错误）。
	predNotInGraph
	// predNotDone 前驱压根没记完成。
	predNotDone
	// predUntrusted 前驱**记了完成**但它自己不可信（它的前驱没满足，
	// 或它与本步成环）。此时说它「未完成」是事实错误。
	predUntrusted
)

// firstUnsatisfiedPred 返回步骤 i 的第一个未满足前驱及其状态。
//
// 返回的 predID 为空表示「没有未满足的前驱」。
func firstUnsatisfiedPred(i int, ids []string, specs []depSpec, satisfied map[string]bool, done map[string]bool, index map[string]int) (predID string, status predStatus) {
	spec := specs[i]
	switch {
	case len(spec.explicit) > 0:
		for _, p := range spec.explicit {
			if !satisfied[p] {
				return p, predState(p, done, index)
			}
		}
		return "", predSatisfied
	case spec.isolated:
		return "", predSatisfied
	default:
		for j := 0; j < i; j++ {
			if !satisfied[ids[j]] {
				return ids[j], predState(ids[j], done, index)
			}
		}
		return "", predSatisfied
	}
}

// predState 判定一个未满足的前驱属于哪一类。
func predState(pred string, done map[string]bool, index map[string]int) predStatus {
	if _, inGraph := index[pred]; !inGraph {
		return predNotInGraph
	}
	if !done[pred] {
		return predNotDone
	}
	return predUntrusted
}

// untrustedDetail 一条「已完成但不可信」的说明文案。
func untrustedDetail(id, predID string, status predStatus) string {
	switch status {
	case predNotInGraph:
		return fmt.Sprintf("步骤 %s 已完成，但其前驱 %s 不在编排图内（悬空依赖），完成记录不采信", id, predID)
	case predUntrusted:
		return fmt.Sprintf("步骤 %s 已完成，但其前驱 %s 虽已完成仍不可信（该前驱自己的前驱未满足），完成记录不采信", id, predID)
	case predNotDone:
		return fmt.Sprintf("步骤 %s 已完成，但其前驱 %s 未完成，完成记录不采信", id, predID)
	default:
		return fmt.Sprintf("步骤 %s 已完成但前驱未满足，完成记录不采信", id)
	}
}

// topoRun 把待执行步骤排成依赖拓扑序。
//
// 规则：显式依赖先满足；未声明依赖的步骤沿用位置隐含前驱（排在它之前的待执行步骤
// 都得先跑）。同层（彼此无先后约束）保持编排图顺序——于是「编排图顺序本来就
// 与依赖一致」的常见情形下，输出与编排图顺序逐字相同。
//
// 依赖成环时：环内步骤排不进任何序。内核不替宿主裁决环（那会变成第二套
// 语义），剩余步骤按编排图顺序追加——它们恒不可信，故恒在待执行集合里。
func topoRun(specs []depSpec, index map[string]int, run []string) []string {
	m := len(run)
	pos := make(map[string]int, m)
	for i, id := range run {
		pos[id] = i
	}
	emitted := make([]bool, m)
	order := make([]string, 0, m)
	for len(order) < m {
		picked := -1
		for k := 0; k < m; k++ {
			if emitted[k] {
				continue
			}
			if runStepReady(specs[index[run[k]]], pos, emitted, k) {
				picked = k
				break
			}
		}
		if picked < 0 {
			// 成环：剩下的按编排图顺序追加（内核不替宿主裁决环）。
			for k := 0; k < m; k++ {
				if !emitted[k] {
					order = append(order, run[k])
					emitted[k] = true
				}
			}
			break
		}
		emitted[picked] = true
		order = append(order, run[picked])
	}
	return order
}

// runStepReady 待执行集合里的第 k 步是否已具备执行条件。
func runStepReady(spec depSpec, pos map[string]int, emitted []bool, k int) bool {
	switch {
	case len(spec.explicit) > 0:
		for _, p := range spec.explicit {
			if j, ok := pos[p]; ok && !emitted[j] {
				return false
			}
		}
		return true
	case spec.isolated:
		return true
	default:
		// 位置隐含前驱：排在它之前的待执行步骤都得先跑。
		for j := 0; j < k; j++ {
			if !emitted[j] {
				return false
			}
		}
		return true
	}
}
