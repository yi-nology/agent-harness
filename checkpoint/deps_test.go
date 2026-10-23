package checkpoint

// 依赖语义的行为钉住。合成图：并行组 / 有序链 / 混图。
//
// 这批用例钉的是**两条同时成立的承诺**：
//  1. 声明过依赖的图：并行组的乱序完成是合法完成集，按各自前驱采信；
//  2. 没人声明依赖的图（零值）：与旧口径逐字一致（连续前缀 + 单条不点名异常）。
//
// 只钉第 1 条会让「零值语义」悄悄漂掉；只钉第 2 条则并行组无从验收。

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yi-nology/agent-harness/contract"
)

// depGraph 造一张带依赖声明的图（复制一份，避免调用方后续改动影响用例）。
func depGraph(steps ...StepDef) []StepDef {
	out := make([]StepDef, 0, len(steps))
	out = append(out, steps...)
	return out
}

// isolated 造一个显式无前驱的步骤。
func isolated(id string) StepDef {
	return StepDef{ID: id, Label: "并行 " + id, Isolated: true}
}

// depends 造一个显式声明前驱集的步骤。
func depends(id string, preds ...string) StepDef {
	return StepDef{ID: id, Label: "依赖 " + id, DependsOn: preds}
}

// TestFanOutOutOfOrderIsTrusted 并行组的乱序完成是**合法完成集**，不是失序。
//
// 这是本轮修复的核心：旧口径（连续前缀）会把「第 1 个没完成、第 3 个完成」
// 整体作废重跑——并行阶段被迫整批重来，下游被迫自建 checkpoint。
func TestFanOutOutOfOrderIsTrusted(t *testing.T) {
	g := depGraph(isolated("plug-a"), isolated("plug-b"), isolated("plug-c"))
	s := snap("run", 3, contract.StatusRunning, "plug-c")

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Skip, []string{"plug-c"}) {
		t.Fatalf("并行组里已完成的成员应被采信，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"plug-a", "plug-b"}) {
		t.Fatalf("其余并行成员应逐个续跑，实得 Run=%v", p.Run)
	}
	if len(p.Anomalies) != 0 {
		t.Fatalf("合法乱序完成不应报异常，实得 %+v", p.Anomalies)
	}
}

// TestFanOutMembersSharePredecessor 并行组成员共享同一前置时，各自只依赖它。
//
// 声明是「取代位置隐含前驱」：plug-b 若沿用位置前驱，它会依赖 plug-a，
// 并行组就退化成有序链——那正是本轮要修的病。
func TestFanOutMembersSharePredecessor(t *testing.T) {
	g := depGraph(
		StepDef{ID: "prep"},
		depends("plug-a", "prep"),
		depends("plug-b", "prep"),
	)
	s := snap("run", 3, contract.StatusRunning, "prep", "plug-b")

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Skip, []string{"prep", "plug-b"}) {
		t.Fatalf("前置与已完成的并行成员都应被采信，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"plug-a"}) {
		t.Fatalf("只应续跑剩下的并行成员，实得 Run=%v", p.Run)
	}
	if len(p.Anomalies) != 0 {
		t.Fatalf("并行组的合法乱序不应报异常，实得 %+v", p.Anomalies)
	}
}

// TestDeclaredDependencyUnsatisfiedIsNotTrusted 有依赖却失序：完成记录不采信，
// 且**点名**报告是哪一步。
func TestDeclaredDependencyUnsatisfiedIsNotTrusted(t *testing.T) {
	g := depGraph(isolated("plug-a"), depends("plug-b", "plug-a"))
	s := snap("run", 2, contract.StatusRunning, "plug-b")

	p := BuildPlan(g, &s)
	if len(p.Skip) != 0 {
		t.Fatalf("前驱未完成的完成记录不该被采信，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"plug-a", "plug-b"}) {
		t.Fatalf("两步都该重跑，实得 Run=%v", p.Run)
	}
	if !p.HasAnomaly(AnomalyDoneOutOfOrder) {
		t.Fatalf("应报告失序，实得 %+v", p.Anomalies)
	}
	var named bool
	for _, a := range p.Anomalies {
		if a.Kind == AnomalyDoneOutOfOrder && a.Step == "plug-b" {
			named = true
			if !strings.Contains(a.Detail, "plug-a") {
				t.Fatalf("报告应点名未满足的前驱，实得 %q", a.Detail)
			}
		}
	}
	if !named {
		t.Fatalf("失序异常应点名 plug-b，实得 %+v", p.Anomalies)
	}
}

// TestDeclaredDependencyIsTransitive 可信性走传递闭包：前驱自己不可信，
// 靠它完成的那一步同样不可信。
func TestDeclaredDependencyIsTransitive(t *testing.T) {
	g := depGraph(isolated("step-a"), depends("step-b", "step-a"), depends("step-c", "step-b"))
	s := snap("run", 3, contract.StatusRunning, "step-a", "step-c")

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Skip, []string{"step-a"}) {
		t.Fatalf("只应采信直接完成且前驱满足的步骤，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-b", "step-c"}) {
		t.Fatalf("step-c 的前驱 step-b 未完成，step-c 也不该被采信，实得 Run=%v", p.Run)
	}
}

// TestTransitiveTrustNotDirectDone 可信性看的是前驱**可信完成**，不是「前驱记为完成」。
//
// step-b 自己记为完成，但它的隐含前驱 step-a 没完成 ⇒ step-b 不可信；
// 依赖 step-b 的 step-c 同样不可信。若实现只查前驱的完成记录（不看可信性），
// step-c 会被采信——那等于承认一条已失序的快照仍然可信。
func TestTransitiveTrustNotDirectDone(t *testing.T) {
	g := depGraph(
		StepDef{ID: "step-a"},
		StepDef{ID: "step-b"},
		depends("step-c", "step-b"),
	)
	s := snap("run", 3, contract.StatusRunning, "step-b", "step-c")

	p := BuildPlan(g, &s)
	if len(p.Skip) != 0 {
		t.Fatalf("前驱不可信时不该采信下游的完成记录，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-a", "step-b", "step-c"}) {
		t.Fatalf("三步都该重跑，实得 Run=%v", p.Run)
	}
}

// TestDanglingDependencyNeverTrusted 依赖指向图外的步骤 = 悬空依赖：
// 恒不采信（恒重跑），且报告口径要能看出是「悬空」而不是「没完成」。
func TestDanglingDependencyNeverTrusted(t *testing.T) {
	g := depGraph(depends("step-a", "step-ghost"))
	s := snap("run", 1, contract.StatusRunning, "step-a")

	p := BuildPlan(g, &s)
	if len(p.Skip) != 0 {
		t.Fatalf("悬空依赖不该被采信，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-a"}) {
		t.Fatalf("悬空依赖的步骤应恒重跑，实得 Run=%v", p.Run)
	}
	found := false
	for _, a := range p.Anomalies {
		if a.Kind == AnomalyDoneOutOfOrder && a.Step == "step-a" {
			found = true
			if !strings.Contains(a.Detail, "悬空依赖") {
				t.Fatalf("悬空依赖的报告口径应与「前驱未完成」区分，实得 %q", a.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("应点名报告 step-a，实得 %+v", p.Anomalies)
	}
}

// TestDependencyCycleNeverTrusted 依赖成环：环内步骤互为前驱，谁也完成不了，
// 于是恒不采信（恒重跑）。内核不替宿主裁决环。
func TestDependencyCycleNeverTrusted(t *testing.T) {
	g := depGraph(depends("step-a", "step-b"), depends("step-b", "step-a"))
	s := snap("run", 2, contract.StatusRunning, "step-a", "step-b")

	p := BuildPlan(g, &s)
	if len(p.Skip) != 0 {
		t.Fatalf("环内步骤不该被采信，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-a", "step-b"}) {
		t.Fatalf("环内步骤应恒在待执行集合里，实得 Run=%v", p.Run)
	}
	if !p.HasAnomaly(AnomalyDoneOutOfOrder) {
		t.Fatalf("环内步骤记为完成时应报告失序，实得 %+v", p.Anomalies)
	}
}

// TestUndeclaredStepKeepsPositionChain 混图里，没声明依赖的步骤仍按位置隐含前驱。
//
// 「声明 = 取代位置前驱」只对**声明过的那一步**生效；别人没声明，就还是链。
func TestUndeclaredStepKeepsPositionChain(t *testing.T) {
	g := depGraph(isolated("plug-a"), StepDef{ID: "tail"})
	s := snap("run", 2, contract.StatusRunning, "plug-a", "tail")

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Skip, []string{"plug-a", "tail"}) {
		t.Fatalf("前驱已完成时，未声明的步骤应被采信，实得 Skip=%v", p.Skip)
	}

	// 前驱没完成时，未声明的步骤不该被采信（位置隐含前驱仍生效）。
	s2 := snap("run", 2, contract.StatusRunning, "tail")
	p2 := BuildPlan(g, &s2)
	if !reflect.DeepEqual(p2.Skip, []string{}) {
		t.Fatalf("前驱未完成时不该采信任何完成项，实得 Skip=%v", p2.Skip)
	}
	if !reflect.DeepEqual(p2.Run, []string{"plug-a", "tail"}) {
		t.Fatalf("两步都该重跑，实得 Run=%v", p2.Run)
	}
}

// TestDependsOnOverridesPosition 显式声明取代位置隐含前驱：
// 声明了 prep 的步骤，不再依赖它位置之前的那些步骤。
//
// 这张图能区分「取代」与「叠加」：step-c 位置在 step-b 之后，若语义是叠加，
// 它的前驱里会多出 step-b——step-b 未完成时 step-c 就不该被采信。
func TestDependsOnOverridesPosition(t *testing.T) {
	g := depGraph(
		StepDef{ID: "step-a"},
		StepDef{ID: "step-b"},
		depends("step-c", "step-a"),
	)
	s := snap("run", 2, contract.StatusRunning, "step-a", "step-c")

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Skip, []string{"step-a", "step-c"}) {
		t.Fatalf("显式声明的前驱满足时该步应被采信（不该被位置前驱拖累），实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-b"}) {
		t.Fatalf("只该续跑未完成的步骤，实得 Run=%v", p.Run)
	}
	if len(p.Anomalies) != 0 {
		t.Fatalf("显式声明满足时不该报失序，实得 %+v", p.Anomalies)
	}
}

// TestRunFollowsDependencyOrder 待执行顺序按依赖拓扑序：
// 声明的前驱排在自己的后面时，待执行列表必须把它排前面。
func TestRunFollowsDependencyOrder(t *testing.T) {
	g := depGraph(
		depends("tail-step", "mid-step"),
		depends("mid-step", "head-step"),
		isolated("head-step"),
	)
	s := snap("run", 1, contract.StatusRunning)

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Run, []string{"head-step", "mid-step", "tail-step"}) {
		t.Fatalf("待执行顺序应满足依赖拓扑序，实得 Run=%v", p.Run)
	}
}

// TestRunOrderMatchesGraphWhenConsistent 图顺序本就与依赖一致时，
// 待执行顺序与编排图顺序逐字相同（不做无谓的重排）。
func TestRunOrderMatchesGraphWhenConsistent(t *testing.T) {
	g := depGraph(
		isolated("head-step"),
		depends("mid-step", "head-step"),
		depends("tail-step", "mid-step"),
	)
	s := snap("run", 1, contract.StatusRunning)

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Run, []string{"head-step", "mid-step", "tail-step"}) {
		t.Fatalf("图顺序与依赖一致时应原样输出，实得 Run=%v", p.Run)
	}
}

// TestZeroValueGraphKeepsLegacyPlanExactly 零值语义：没人声明依赖时，
// 计划与旧口径**逐字一致**（含异常条数、异常文案与 Step 为空）。
func TestZeroValueGraphKeepsLegacyPlanExactly(t *testing.T) {
	g := graph("step-alpha", "step-beta", "step-gamma")
	s := snap("run", 3, contract.StatusRunning, "step-gamma")

	want := Plan{
		Skip: []string{},
		Run:  []string{"step-alpha", "step-beta", "step-gamma"},
		Anomalies: []Anomaly{{
			Kind:   AnomalyDoneOutOfOrder,
			Step:   "",
			Detail: "已完成步骤不是编排图的前缀，其后的完成记录不采信",
		}},
	}
	got := BuildPlan(g, &s)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("零值语义应与旧口径逐字一致:\n实得 %+v\n期望 %+v", got, want)
	}
}

// TestZeroValueGraphPrefixStillSkips 零值语义：完成集是前缀时照常跳过、不报异常。
func TestZeroValueGraphPrefixStillSkips(t *testing.T) {
	g := graph("step-alpha", "step-beta", "step-gamma")
	s := snap("run", 2, contract.StatusRunning, "step-alpha", "step-beta")

	p := BuildPlan(g, &s)
	if !reflect.DeepEqual(p.Skip, []string{"step-alpha", "step-beta"}) {
		t.Fatalf("前缀完成应被跳过，实得 Skip=%v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-gamma"}) {
		t.Fatalf("剩余步骤应续跑，实得 Run=%v", p.Run)
	}
	if len(p.Anomalies) != 0 {
		t.Fatalf("前缀完成不应报异常，实得 %+v", p.Anomalies)
	}
}

// TestDeclaredModeDeterministic 声明过依赖的图：同一输入必得同一计划。
func TestDeclaredModeDeterministic(t *testing.T) {
	g := depGraph(isolated("plug-a"), depends("plug-b", "plug-a"), isolated("plug-c"))
	s := snap("run", 2, contract.StatusRunning, "plug-c")
	first := BuildPlan(g, &s)
	for i := 0; i < 20; i++ {
		if got := BuildPlan(g, &s); !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次结果不一致:\n%+v\n%+v", i, first, got)
		}
	}
}

// TestDeclaredModeSkipRunNoAlias 声明模式下结果切片同样不得与入参共享底层数组。
func TestDeclaredModeSkipRunNoAlias(t *testing.T) {
	steps := []StepDef{isolated("plug-a"), isolated("plug-b")}
	s := snap("run", 1, contract.StatusRunning, "plug-b")
	p := BuildPlan(steps, &s)
	p.Skip[0] = "mutated"
	p.Run[0] = "mutated"
	if steps[0].ID != "plug-a" || steps[1].ID != "plug-b" {
		t.Fatalf("结果切片与编排图共享了底层数组: %+v", steps)
	}
}

// TestIsolatedAndDependsOnBothSet 两个都设时以 DependsOn 为准（更具体、约束更严）。
func TestIsolatedAndDependsOnBothSet(t *testing.T) {
	g := depGraph(
		StepDef{ID: "plug-a", Isolated: true},
		StepDef{ID: "plug-b", Isolated: true, DependsOn: []string{"plug-a"}},
	)
	s := snap("run", 2, contract.StatusRunning, "plug-b")

	p := BuildPlan(g, &s)
	if len(p.Skip) != 0 {
		t.Fatalf("DependsOn 非空时应以其为准（plug-a 未完成则不可信），实得 Skip=%v", p.Skip)
	}
}
