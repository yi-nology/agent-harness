// resume_reconcile_e2e_test.go —— 跨包冲突链（v0.33.0，issue #90）：
// 「快照说已完成 + 副作用结果未知」同时成立 ⇒ 恢复必须阻塞并给出可读原因。
//
// # 为什么单包单测盖不住这件事
//
// checkpoint.BuildPlan 只看快照（Done 含该步 ⇒ 跳过），sideeffect 闸只看
// 自己的账（结果未知 ⇒ 该 Key 硬阻塞）。两套判据各自全绿时，坏组合照样
// 畅通：宿主在副作用的结论落定**之前**乐观落了快照、然后进程死了——恢复
// 按快照跳过该步，那个副作用**既没对账也没重做**，悬在中间，没有任何东西
// 会报。只有把两侧行到同一条恢复流程里跑一遍才看得见。
//
// 本文件就是那条流程：乐观落库 → 崩溃 → 重启 → 对账判阻塞 → 裁决 → 解阻。
// 存储沿用 durable（语义按契约写，见 durable_store_test.go）。
package checkpoint_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// reconcileProbe 宿主的闸侧试探：对「快照说已完成且有副作用」的步骤，
// 用同一 Action 触一次闸，按 Outcome 映射成对账视图（映射口径见
// checkpoint/reconcile.go 的接线说明）。
//
// 观察口是 Store.Acquire（sideeffect 包唯一的观察面）。返回 Applied 说明
// 闸认为「没做过」——试探即授予，故探针**立即 Abandon** 把 Key 还原成
// 可执行态（宿主不想真执行时的标准做法）；Abandon 失败说明存储侧此刻
// 不可信，按 GateUnknown 报（判不了不是放行的理由）。
func reconcileProbe(t *testing.T, st sideeffect.Store, act sideeffect.Action) (checkpoint.GateView, sideeffect.Outcome) {
	t.Helper()
	outcome, lease, _, err := st.Acquire(context.Background(), act, time.Minute)
	if err != nil {
		return checkpoint.GateView{Step: stepOfKey(act), State: checkpoint.GateUnknown,
			Detail: "Key " + string(act.Key) + " 闸侧状态查询失败：" + err.Error()}, outcome
	}
	switch outcome {
	case sideeffect.OutcomeReplayed:
		return checkpoint.GateView{Step: stepOfKey(act), State: checkpoint.GateSettled,
			Detail: "闸记了已完成并有结果"}, outcome
	case sideeffect.OutcomeUncertain:
		return checkpoint.GateView{Step: stepOfKey(act), State: checkpoint.GateUncertain,
			Detail: "Key " + string(act.Key) + " 处于结果未知态"}, outcome
	case sideeffect.OutcomeContended:
		return checkpoint.GateView{Step: stepOfKey(act), State: checkpoint.GateInFlight,
			Detail: "Key " + string(act.Key) + " 有未裁决尝试或被他人持有"}, outcome
	case sideeffect.OutcomeApplied:
		if aerr := st.Abandon(context.Background(), act, lease, "恢复对账试探：确认闸态后撤销"); aerr != nil {
			return checkpoint.GateView{Step: stepOfKey(act), State: checkpoint.GateUnknown,
				Detail: "试探后撤销失败（存储侧不可信）：" + aerr.Error()}, outcome
		}
		return checkpoint.GateView{Step: stepOfKey(act), State: checkpoint.GateUnrecorded,
			Detail: "闸认为该动作从未做过（已授予执行权，试探后已撤销）"}, outcome
	default:
		return checkpoint.GateView{Step: stepOfKey(act), State: checkpoint.GateUnknown,
			Detail: "未预期的闸侧结论"}, outcome
	}
}

// stepOfKey 宿主的键构造约定：Key = runID + ":" + step（与 durable 链路同款）。
func stepOfKey(act sideeffect.Action) string {
	if i := strings.LastIndex(string(act.Key), ":"); i >= 0 {
		return string(act.Key[i+1:])
	}
	return string(act.Key)
}

// TestE2ESnapshotDoneButEffectUncertainBlocksResume 制造冲突并走完恢复弧：
//
//	① 宿主乐观落库：副作用的结论没落定（结果未知）就写了「已完成」快照；
//	② 崩溃重启，BuildPlan 说跳过该步；
//	③ 恢复前三方对账 ⇒ **阻塞** + 可读原因（点名步骤与闸侧状态）；
//	④ 宿主对账裁决（下游确认已生效）⇒ 闸记为已完成；
//	⑤ 重建计划 ⇒ 不再阻塞，快照的完成说法此时有了账面支撑。
func TestE2ESnapshotDoneButEffectUncertainBlocksResume(t *testing.T) {
	ctx := context.Background()
	st := newDurable()
	graph := []checkpoint.StepDef{{ID: "enrich"}, {ID: "publish"}}
	act := sideeffect.Action{
		Key:      sideeffect.Key("run-9:enrich"),
		Resource: "store-main",
	}

	// ── 第一次进程：施加失败（响应丢失）+ 乐观落库，然后崩。
	runner := sideeffect.NewRunner(st, time.Minute)
	_, _, err := runner.Execute(ctx, act, func(context.Context, sideeffect.Lease) (sideeffect.Result, error) {
		return sideeffect.Result{}, errors.New("下游响应丢失（可能已生效）")
	})
	if !errors.Is(err, sideeffect.ErrOutcomeUnknown) {
		t.Fatalf("前置：施加失败应转入结果未知，实得 err=%v", err)
	}
	committer := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st)
	// 乐观落库：结论没落定就写「已完成」——坏组合的制造点。
	if err := committer.Commit(ctx, checkpoint.Snapshot{
		RunID: "run-9", Seq: 1, Status: "running", Done: []string{"enrich"},
	}); err != nil {
		t.Fatalf("前置：快照应落库: %v", err)
	}

	// ── 重启：只剩持久化事实。
	snap, err := st.Load(ctx, "run-9")
	if err != nil {
		t.Fatalf("重启后应能读回快照: %v", err)
	}
	plan := checkpoint.BuildPlan(graph, &snap)
	if len(plan.Skip) != 1 || plan.Skip[0] != "enrich" {
		t.Fatalf("② 快照说已完成 ⇒ 计划应跳过该步，实得 %+v", plan)
	}

	// ③ 恢复前三方对账：闸侧试探 → 未知态 ⇒ 冲突 ⇒ 阻塞。
	restarted := sideeffect.NewRunner(st, time.Minute)
	view, outcome := reconcileProbe(t, st, act)
	if outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("③ 闸侧应报结果未知，实得 %v", outcome)
	}
	blocked := checkpoint.Reconcile(plan, []checkpoint.GateView{view})
	if !blocked.Blocked() {
		t.Fatal("③ 冲突必须阻塞：按快照跳过会让这个副作用既不对账也不重做")
	}
	if len(blocked.Run) != 0 || len(blocked.Skip) != 0 {
		t.Fatalf("③ 阻塞时不得给出半截计划: %+v", blocked)
	}
	found := false
	for _, a := range blocked.Anomalies {
		if a.Kind == checkpoint.AnomalySideEffectConflict && a.Step == "enrich" &&
			strings.Contains(a.Detail, "uncertain") {
			found = true
		}
	}
	if !found {
		t.Fatalf("③ 冲突报告应点名步骤并写明闸侧状态: %+v", blocked.Anomalies)
	}

	// ④ 宿主对账：查下游事实（已生效）→ 裁决。
	if _, _, err := restarted.Reconcile(ctx, act, sideeffect.Resolution{
		Applied: true, Result: sideeffect.Result{Payload: []byte("enriched")}, Reason: "下游确认已生效",
	}); err != nil {
		t.Fatalf("④ 对账裁决失败: %v", err)
	}

	// ⑤ 重建计划 + 复对账：闸已记账 ⇒ 快照可信 ⇒ 放行。
	view2, outcome2 := reconcileProbe(t, st, act)
	if outcome2 != sideeffect.OutcomeReplayed {
		t.Fatalf("⑤ 裁决后闸应判 replayed，实得 %v", outcome2)
	}
	plan2 := checkpoint.BuildPlan(graph, &snap)
	ok2 := checkpoint.Reconcile(plan2, []checkpoint.GateView{view2})
	if ok2.Blocked() {
		t.Fatalf("⑤ 对账完成后不应再阻塞: %+v", ok2.Anomalies)
	}
	if len(ok2.Skip) != 1 || len(ok2.Run) != 1 || ok2.Run[0] != "publish" {
		t.Fatalf("⑤ 放行后的计划应与快照一致: %+v", ok2)
	}
}

// TestE2ESnapshotDoneButEffectNeverHappenedStillBlocks 反向弧：
// 裁决说「未生效」之后，闸对同一动作会**重新授予执行权**（unrecorded）——
// 快照的完成说法仍没有账面支撑，对账必须**继续阻塞**：只有把该步真正重做
// （或宿主显式修正快照），恢复才可继续。
//
// 这一条钉住「unrecorded 也是冲突」：放行它等于采信一条账面对不上的快照。
func TestE2ESnapshotDoneButEffectNeverHappenedStillBlocks(t *testing.T) {
	ctx := context.Background()
	st := newDurable()
	graph := []checkpoint.StepDef{{ID: "enrich"}, {ID: "publish"}}
	act := sideeffect.Action{
		Key:      sideeffect.Key("run-8:enrich"),
		Resource: "store-main",
	}

	runner := sideeffect.NewRunner(st, time.Minute)
	if _, _, err := runner.Execute(ctx, act, func(context.Context, sideeffect.Lease) (sideeffect.Result, error) {
		return sideeffect.Result{}, errors.New("下游响应丢失")
	}); !errors.Is(err, sideeffect.ErrOutcomeUnknown) {
		t.Fatalf("前置：施加失败应转入结果未知，实得 err=%v", err)
	}
	if err := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st).Commit(ctx, checkpoint.Snapshot{
		RunID: "run-8", Seq: 1, Status: "running", Done: []string{"enrich"},
	}); err != nil {
		t.Fatalf("前置：快照应落库: %v", err)
	}

	snap, err := st.Load(ctx, "run-8")
	if err != nil {
		t.Fatalf("重启后应能读回快照: %v", err)
	}
	restarted := sideeffect.NewRunner(st, time.Minute)
	if v, oc := reconcileProbe(t, st, act); oc != sideeffect.OutcomeUncertain || v.State != checkpoint.GateUncertain {
		t.Fatalf("前置：重启后闸侧应报结果未知，实得 %v / %s", oc, v.State)
	}

	// 宿主查下游：确认**没有**生效 → 裁决清除未知态。
	if _, _, err := restarted.Reconcile(ctx, act, sideeffect.Resolution{
		Applied: false, Reason: "下游确认未收到该请求",
	}); err != nil {
		t.Fatalf("对账裁决失败: %v", err)
	}

	// 复对账：闸现在会重新授予执行权（unrecorded）——快照的完成说法仍无支撑，继续阻塞。
	view2, outcome2 := reconcileProbe(t, st, act)
	if outcome2 != sideeffect.OutcomeApplied {
		t.Fatalf("裁决未生效后闸应重新授予（试探即 Applied），实得 %v", outcome2)
	}
	blocked := checkpoint.Reconcile(checkpoint.BuildPlan(graph, &snap), []checkpoint.GateView{view2})
	if !blocked.Blocked() {
		t.Fatal("unrecorded 也是冲突：账面对不上的快照不得被采信")
	}

	// 宿主修正进度（该步真正重做）之后：闸记为已完成 ⇒ 快照可信 ⇒ 放行。
	if out, err := apply(ctx, restarted, st, "run-8", "enrich", "store-main"); err != nil || out != sideeffect.OutcomeApplied {
		t.Fatalf("重做该步应施加成功，实得 outcome=%v err=%v", out, err)
	}
	if got := st.appliedCount("enrich"); got != 1 {
		t.Fatalf("重做应恰好施加一次，实得 %d", got)
	}
	view3, _ := reconcileProbe(t, st, act)
	ok := checkpoint.Reconcile(checkpoint.BuildPlan(graph, &snap), []checkpoint.GateView{view3})
	if ok.Blocked() {
		t.Fatalf("重做完成并记账后不应再阻塞: %+v", ok.Anomalies)
	}
}
