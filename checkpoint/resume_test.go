package checkpoint

// 恢复计划的行为钉住。合成图：step-alpha / step-beta / step-gamma。

import (
	"reflect"
	"testing"

	"github.com/yi-nology/agent-harness/contract"
)

func graph(ids ...string) []StepDef {
	out := make([]StepDef, 0, len(ids))
	for _, id := range ids {
		out = append(out, StepDef{ID: id, Label: "环节 " + id})
	}
	return out
}

// TestFirstRunNoSnapshot 首次执行：全量跑，无异常。
func TestFirstRunNoSnapshot(t *testing.T) {
	p := BuildPlan(graph("step-alpha", "step-beta"), nil)
	if len(p.Run) != 2 || len(p.Skip) != 0 || len(p.Anomalies) != 0 {
		t.Fatalf("首次执行应全量无异常，实得 %+v", p)
	}
	if p.Blocked() {
		t.Fatal("首次执行不应被阻断")
	}
}

// TestPrefixDoneIsSkipped 正常路径：完成集是前缀 → 续跑剩余，不报异常。
func TestPrefixDoneIsSkipped(t *testing.T) {
	s := snap("run", 2, contract.StatusRunning, "step-alpha", "step-beta")
	p := BuildPlan(graph("step-alpha", "step-beta", "step-gamma"), &s)
	if !reflect.DeepEqual(p.Skip, []string{"step-alpha", "step-beta"}) {
		t.Fatalf("已完成应被跳过，实得 %v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-gamma"}) {
		t.Fatalf("未完成应续跑，实得 %v", p.Run)
	}
	if len(p.Anomalies) != 0 {
		t.Fatalf("正常路径不应报异常，实得 %+v", p.Anomalies)
	}
}

// TestHoleNotTrusted 完成集有空洞 → 空洞之后的完成**不采信**，整体重跑并报异常。
// 这是本包最关键的判定：采信一半等于承认一个已失序的快照仍然可信。
func TestHoleNotTrusted(t *testing.T) {
	s := snap("run", 3, contract.StatusRunning, "step-gamma")
	p := BuildPlan(graph("step-alpha", "step-beta", "step-gamma"), &s)
	if len(p.Skip) != 0 {
		t.Fatalf("空洞存在时不应采信任何完成项，实得 %v", p.Skip)
	}
	if !reflect.DeepEqual(p.Run, []string{"step-alpha", "step-beta", "step-gamma"}) {
		t.Fatalf("失序后应全量重跑，实得 %v", p.Run)
	}
	if !p.HasAnomaly(AnomalyDoneOutOfOrder) {
		t.Fatalf("应报告失序，实得 %+v", p.Anomalies)
	}
	if p.Blocked() {
		t.Fatal("失序不阻断：内核已按最保守解释给出可执行计划")
	}
}

// TestDoneNotInGraph 快照里的步骤在当前图中不存在 → 报告但不重跑。
func TestDoneNotInGraph(t *testing.T) {
	s := snap("run", 4, contract.StatusRunning, "step-alpha", "step-removed")
	p := BuildPlan(graph("step-alpha", "step-beta"), &s)
	if !reflect.DeepEqual(p.Run, []string{"step-beta"}) {
		t.Fatalf("只应续跑图内未完成项，实得 %v", p.Run)
	}
	found := false
	for _, a := range p.Anomalies {
		if a.Kind == AnomalyDoneNotInGraph && a.Step == "step-removed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应点名报告图外完成项，实得 %+v", p.Anomalies)
	}
}

// TestDuplicateStepInGraph 图内重复 ID → 去重并报告，且保持首次出现位置。
func TestDuplicateStepInGraph(t *testing.T) {
	p := BuildPlan(graph("step-alpha", "step-beta", "step-alpha"), nil)
	if !reflect.DeepEqual(p.Run, []string{"step-alpha", "step-beta"}) {
		t.Fatalf("重复 ID 应只留首次出现处，实得 %v", p.Run)
	}
	if !p.HasAnomaly(AnomalyDuplicateStep) {
		t.Fatalf("应报告重复步骤，实得 %+v", p.Anomalies)
	}
}

// TestEmptyStepIDDropped 空 ID 从图里剔除并报告（不进 Run）。
func TestEmptyStepIDDropped(t *testing.T) {
	p := BuildPlan(graph("", "step-alpha", ""), nil)
	if !reflect.DeepEqual(p.Run, []string{"step-alpha"}) {
		t.Fatalf("空 ID 不应进 Run，实得 %v", p.Run)
	}
	if !p.HasAnomaly(AnomalyEmptyStepID) {
		t.Fatalf("应报告空 ID，实得 %+v", p.Anomalies)
	}
}

// TestTerminalNotAutoReopened 快照标记已终结但仍有未完成步骤 → 阻断且不重开。
func TestTerminalNotReopened(t *testing.T) {
	s := snap("run", 9, contract.StatusCancelled, "step-alpha")
	p := BuildPlan(graph("step-alpha", "step-beta", "step-gamma"), &s)
	if !p.Blocked() {
		t.Fatal("已终结运行有待执行项时必须阻断")
	}
	if len(p.Run) != 0 || len(p.Skip) != 0 {
		t.Fatalf("被阻断的计划不应给出半截 Run/Skip: Run=%v Skip=%v", p.Run, p.Skip)
	}
	if !p.HasAnomaly(AnomalyCompleteWithPending) {
		t.Fatalf("应报告待执行项冲突，实得 %+v", p.Anomalies)
	}
}

// TestTerminalFullyDone 终态且全部完成 → 干净跳过，不阻断不报异常。
func TestTerminalFullyDone(t *testing.T) {
	s := snap("run", 12, contract.StatusSucceeded, "step-alpha", "step-beta")
	p := BuildPlan(graph("step-alpha", "step-beta"), &s)
	if len(p.Run) != 0 {
		t.Fatalf("已完成不该有 Run，实得 %v", p.Run)
	}
	if !reflect.DeepEqual(p.Skip, []string{"step-alpha", "step-beta"}) {
		t.Fatalf("全部完成应全跳过，实得 %v", p.Skip)
	}
	if p.Blocked() || len(p.Anomalies) != 0 {
		t.Fatalf("干净完成不应阻断或报异常: blocked=%v %+v", p.Blocked(), p.Anomalies)
	}
}

// TestTerminalStatusesAllBlock 等价终态都要被认出来（含 failed / timed_out）。
func TestTerminalStatusesAllBlock(t *testing.T) {
	for _, st := range []string{
		contract.StatusSucceeded, contract.StatusFailed,
		contract.StatusCancelled, contract.StatusTimedOut,
	} {
		s := snap("run", 1, st)
		if !BuildPlan(graph("step-alpha", "step-beta"), &s).Blocked() {
			t.Fatalf("状态 %q 应被认作终态并阻断", st)
		}
	}
}

// TestNonTerminalStatusStillRuns 待审批不是终态 → 正常续跑剩余。
func TestNonTerminalStatusStillRuns(t *testing.T) {
	s := snap("run", 3, contract.StatusWaitingApproval, "step-alpha")
	p := BuildPlan(graph("step-alpha", "step-beta"), &s)
	if p.Blocked() {
		t.Fatal("待审批不是终态，不应阻断")
	}
	if !reflect.DeepEqual(p.Run, []string{"step-beta"}) {
		t.Fatalf("应续跑剩余，实得 %v", p.Run)
	}
}

// TestEmptyDoneIgnored 快照 Done 里的空串不参与完成判定。
func TestEmptyDoneIgnored(t *testing.T) {
	s := snap("run", 2, contract.StatusRunning, "", "step-alpha")
	p := BuildPlan(graph("step-alpha", "step-beta"), &s)
	if !reflect.DeepEqual(p.Skip, []string{"step-alpha"}) {
		t.Fatalf("空串不应造成空洞，实得 %v", p.Skip)
	}
	if p.HasAnomaly(AnomalyDoneOutOfOrder) {
		t.Fatalf("空串不应被当成图外完成项: %+v", p.Anomalies)
	}
}

// TestPlanDeterministic 同一输入必得同一计划（编排可复现的基础）。
func TestPlanDeterministic(t *testing.T) {
	g := graph("step-alpha", "step-beta", "step-gamma", "step-alpha")
	s := snap("run", 5, contract.StatusRunning, "step-alpha", "step-beta")
	first := BuildPlan(g, &s)
	for i := 0; i < 20; i++ {
		if got := BuildPlan(g, &s); !reflect.DeepEqual(got, first) {
			t.Fatalf("第 %d 次结果不一致:\n%+v\n%+v", i, first, got)
		}
	}
}

// TestSkipRunNoAlias 结果切片不得与入参共享底层数组（调用方改了会串）。
func TestSkipRunNoAlias(t *testing.T) {
	ids := []string{"step-alpha", "step-beta", "step-gamma"}
	s := snap("run", 1, contract.StatusRunning, "step-alpha")
	p := BuildPlan(graph(ids...), &s)
	p.Skip[0] = "mutated"
	if ids[0] != "step-alpha" {
		t.Fatalf("Skip 与编排图共享了底层数组: %v", ids)
	}
}
