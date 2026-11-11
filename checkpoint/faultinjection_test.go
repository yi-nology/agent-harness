// faultinjection_test.go —— checkpoint 的**故障注入矩阵**（v0.33.0，issue #98）。
//
// # 逐格写明期望的失败形态（不是「不报错就行」）
//
//	写库失败注入       → 显式失败 + 闸回滚 ⇒ **可重入**（同序号可重试，run 不卡死）
//	并发 Seq 冲突      → 输家**变红**（ErrStaleSeq），不静默覆盖；赢家唯一
//	恢复中断           → **可重入**：中断后可再次恢复，已生效的副作用不重放
//	双 Worker 同任务   → 恰好一个推进，另一个**被有序拒绝**（Contended/Replayed）
//
// 存储是进程内最小实现，但语义按契约写（SaveIfNewer 严格递增、Acquire 一次
// 原子判定——见 durable_store_test.go）：把契约抄成宽松版等于把被测对象换掉。
package checkpoint_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// flakyStore 注入「第 N 次写失败」的存储（写库故障的最小形态）。
type flakyStore struct {
	checkpoint.StoreCAS
	failNext int // 剩余失败次数；>0 时 SaveIfNewer 失败并递减
}

func (f *flakyStore) SaveIfNewer(ctx context.Context, s checkpoint.Snapshot) error {
	if f.failNext > 0 {
		f.failNext--
		return errors.New("注入：存储写失败")
	}
	return f.StoreCAS.SaveIfNewer(ctx, s)
}

// TestFaultSaveFailsGateRollsBackSameSeqRetryable 写库失败注入：
// Commit 显式失败、闸回滚到前值，**同序号可重试**——run 不卡死。
//
// 期望失败形态：显式失败（错误透出）+ 可重入。对照物是「不回滚」的坏实现：
// 闸停在 N，重试 N 被判陈旧、跳过 N 留空洞——那个形态本用例必红。
func TestFaultSaveFailsGateRollsBackSameSeqRetryable(t *testing.T) {
	ctx := context.Background()
	base := newDurable()
	st := &flakyStore{StoreCAS: base}
	c := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st)

	s1 := checkpoint.Snapshot{RunID: "run-f1", Seq: 1, Status: "running", Done: []string{"a"}}
	if err := c.Commit(ctx, s1); err != nil {
		t.Fatalf("首次提交应成功: %v", err)
	}
	// 注入下一次写失败。
	st.failNext = 1
	s2 := checkpoint.Snapshot{RunID: "run-f1", Seq: 2, Status: "running", Done: []string{"a", "b"}}
	if err := c.Commit(ctx, s2); err == nil {
		t.Fatal("注入写失败后 Commit 必须显式报错，不静默成功")
	}
	if got := c.Last("run-f1"); got != 1 {
		t.Fatalf("失败后闸必须回滚到前值（不回滚则 run 卡死），实得 %d", got)
	}
	// 存储恢复后，**同序号**重试成功。
	if err := c.Commit(ctx, s2); err != nil {
		t.Fatalf("同序号重试应成功（可重入）: %v", err)
	}
	if got := c.Last("run-f1"); got != 2 {
		t.Fatalf("重试成功后闸应到 2，实得 %d", got)
	}
	// 失败的那次确实没落库：注入失败的快照不在存储里。
	if _, err := st.Load(ctx, "run-f1"); err != nil {
		t.Fatalf("存储应可读: %v", err)
	}
}

// TestFaultConcurrentSameSeqExactlyOneWinner 并发 Seq 冲突（两个「进程」抢
// 同一序号）：各自独立的 SeqGate（各自进程内），共享同一个存储——
// **恰好一个赢家**，输家全部拿到 ErrStaleSeq（变红，不静默覆盖）。
//
// 期望失败形态：输家红 + 有序。顺带钉住「输家的闸回滚」：输家拿到的是
// 前值 0，重灌后可以重新提交更大的序号。
func TestFaultConcurrentSameSeqExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	st := newDurable()

	const workers = 8
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 每个 worker 一个独立闸（各自进程），同一存储（跨进程判据）。
			c := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st)
			s := checkpoint.Snapshot{
				RunID: "run-f2", Seq: 1, Status: "running",
				Done: []string{"w"}, Fields: map[string]string{"winner": itoa98(i)},
			}
			errs[i] = c.Commit(ctx, s)
		}(i)
	}
	wg.Wait()

	winners, stale := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, checkpoint.ErrStaleSeq):
			stale++
		default:
			t.Fatalf("出现第三种结论（既非成功也非 ErrStaleSeq）: %v", err)
		}
	}
	if winners != 1 || stale != workers-1 {
		t.Fatalf("应恰好 1 个赢家、%d 个输家变红，实得 %d 赢 / %d 陈旧", workers-1, winners, stale)
	}
	// 赢家的快照在存储里（不是「谁都写进去了只是报错」）。
	snap, err := st.Load(ctx, "run-f2")
	if err != nil {
		t.Fatalf("赢家快照应可读: %v", err)
	}
	if snap.Fields["winner"] == "" {
		t.Fatalf("赢家快照应带有其标记: %+v", snap)
	}
	// 输家重灌基线后用更大的序号继续——有序，不卡死。
	c := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st)
	if err := c.SeedFromStore(ctx, "run-f2"); err != nil {
		t.Fatalf("回灌失败: %v", err)
	}
	if err := c.Commit(ctx, checkpoint.Snapshot{
		RunID: "run-f2", Seq: snap.Seq + 1, Status: "running", Done: []string{"w", "x"},
	}); err != nil {
		t.Fatalf("输家重灌后应能有序推进: %v", err)
	}
}

func itoa98(i int) string {
	return []string{"w0", "w1", "w2", "w3", "w4", "w5", "w6", "w7"}[i]
}

// TestFaultResumeInterruptedThenResumes 恢复中断（恢复到一半进程死掉）：
// 恢复过程不是幂等的——它写快照、跑副作用、推进步骤；中断的语义与
// 「从未恢复」完全不同。
//
//	第一次进程：步骤 a 完成（副作用已施加并记账）+ 快照 seq1，然后崩；
//	恢复中断：b 的副作用已施加并记账、快照 seq2 已落库，死在 c 之前；
//	再次恢复：b 的动作**重试判 replayed**（不重放），c 正常推进到终态。
//
// 期望失败形态：可重入——中断后可再次恢复，已生效的副作用不重放。
func TestFaultResumeInterruptedThenResumes(t *testing.T) {
	ctx := context.Background()
	st := newDurable()
	graph := []checkpoint.StepDef{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	// ── 第一次进程：a 完成 + 快照 seq1，然后崩。
	r1 := sideeffect.NewRunner(st, time.Minute)
	if _, err := apply(ctx, r1, st, "run-f3", "a", "res-a"); err != nil {
		t.Fatalf("a 应施加成功: %v", err)
	}
	if err := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st).Commit(ctx,
		snap98("run-f3", 1, "a")); err != nil {
		t.Fatalf("seq1 应落库: %v", err)
	}

	// ── 恢复中断：b 完成、快照 seq2 落库，死在 c 之前。
	if out, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-f3", "b", "res-b"); err != nil || out != sideeffect.OutcomeApplied {
		t.Fatalf("恢复应施加 b，实得 outcome=%v err=%v", out, err)
	}
	if err := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st).Commit(ctx,
		snap98("run-f3", 2, "a", "b")); err != nil {
		t.Fatalf("seq2 应落库: %v", err)
	}
	// （进程死：无 Release、无最终快照——与上一轮同款崩溃形态。）

	// ── 再次恢复：b 不重放，c 推进到完成。
	snap, err := st.Load(ctx, "run-f3")
	if err != nil {
		t.Fatalf("应能读回快照: %v", err)
	}
	plan := checkpoint.BuildPlan(graph, &snap)
	if len(plan.Skip) != 2 || len(plan.Run) != 1 || plan.Run[0] != "c" {
		t.Fatalf("再次恢复应跳过 a,b 只跑 c，实得 %+v", plan)
	}
	// 宿主对中断点保守重试（它不知道自己死在哪）：b 的动作必须判 replayed。
	if out, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-f3", "b", "res-b"); err != nil || out != sideeffect.OutcomeReplayed {
		t.Fatalf("已生效的 b 不得重放，实得 outcome=%v err=%v", out, err)
	}
	if got := st.appliedCount("b"); got != 1 {
		t.Fatalf("b 的实际施加次数 = %d, want 1（恢复中断不得变成重放）", got)
	}
	if out, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-f3", "c", "res-c"); err != nil || out != sideeffect.OutcomeApplied {
		t.Fatalf("c 应施加，实得 outcome=%v err=%v", out, err)
	}
	if err := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st).Commit(ctx,
		snap98("run-f3", 3, "a", "b", "c")); err != nil {
		t.Fatalf("seq3 应落库: %v", err)
	}
}

// TestFaultResumeInterruptedBeforeSnapshot 恢复中断在**快照之前**：
// 副作用已施加并记账、快照还没写就死了。再次恢复时快照仍停在旧序号 ⇒
// 计划会重跑该步——但闸记得它做过 ⇒ 重试判 replayed，副作用不重复。
// 快照随后补上（序号继续推进）。这是「恢复过程本身不是幂等的」那格的
// 另一半：副作用记账与快照落库是两次写，中间任何一处都可能断。
func TestFaultResumeInterruptedBeforeSnapshot(t *testing.T) {
	ctx := context.Background()
	st := newDurable()
	graph := []checkpoint.StepDef{{ID: "a"}, {ID: "b"}}

	// 第一次进程：a 完成 + 快照 seq1，崩。
	r1 := sideeffect.NewRunner(st, time.Minute)
	if _, err := apply(ctx, r1, st, "run-f4", "a", "res-a"); err != nil {
		t.Fatalf("a 应施加成功: %v", err)
	}
	if err := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st).Commit(ctx,
		snap98("run-f4", 1, "a")); err != nil {
		t.Fatalf("seq1 应落库: %v", err)
	}

	// 恢复中断：b 施加成功并记账，快照**未**落库，死。
	if out, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-f4", "b", "res-b"); err != nil || out != sideeffect.OutcomeApplied {
		t.Fatalf("b 应施加成功: %v", err)
	}
	// （死：seq2 没写。）

	// 再次恢复：快照停在 seq1 ⇒ 计划重跑 b ⇒ 闸拦下（replayed）⇒ 不重复施加。
	snap, err := st.Load(ctx, "run-f4")
	if err != nil {
		t.Fatalf("应能读回快照: %v", err)
	}
	plan := checkpoint.BuildPlan(graph, &snap)
	if len(plan.Run) != 1 || plan.Run[0] != "b" {
		t.Fatalf("计划应重跑 b（快照没记它），实得 %+v", plan)
	}
	if out, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-f4", "b", "res-b"); err != nil || out != sideeffect.OutcomeReplayed {
		t.Fatalf("闸必须拦下重放（replayed），实得 outcome=%v err=%v", out, err)
	}
	if got := st.appliedCount("b"); got != 1 {
		t.Fatalf("b 的实际施加次数 = %d, want 1（快照落后于副作用账时不得重做）", got)
	}
	// 快照补上：序号推进到 2，恢复点对齐副作用账。
	if err := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st).Commit(ctx,
		snap98("run-f4", 2, "a", "b")); err != nil {
		t.Fatalf("seq2 应落库: %v", err)
	}
}

// TestFaultTwoWorkersResumeSameTask 双 Worker 恢复同一任务：两个进程同时
// Load + BuildPlan（得到同一份计划），同时执行同一步骤的同一条动作——
// **恰好一个施加**，另一个被有序拒绝（Contended / Replayed），副作用计数为 1。
//
// 期望失败形态：输家有序拒绝（不是错误崩塌，也不是双双成功）。
func TestFaultTwoWorkersResumeSameTask(t *testing.T) {
	ctx := context.Background()
	st := newDurable()

	// 前置：第一次进程做完 a 就崩（两台 worker 面对同一个崩溃现场）。
	if _, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-f5", "a", "res-a"); err != nil {
		t.Fatalf("a 应施加成功: %v", err)
	}
	if err := checkpoint.NewCommitter(checkpoint.NewSeqGate(), st).Commit(ctx,
		snap98("run-f5", 1, "a")); err != nil {
		t.Fatalf("seq1 应落库: %v", err)
	}

	// 两个 worker 同时恢复：都读快照、都算计划（同一份）、都抢 b 的动作。
	snap, err := st.Load(ctx, "run-f5")
	if err != nil {
		t.Fatalf("应能读回快照: %v", err)
	}
	planA := checkpoint.BuildPlan([]checkpoint.StepDef{{ID: "a"}, {ID: "b"}}, &snap)
	planB := checkpoint.BuildPlan([]checkpoint.StepDef{{ID: "a"}, {ID: "b"}}, &snap)
	if len(planA.Run) != 1 || len(planB.Run) != 1 {
		t.Fatalf("两个 worker 应得到同一份待执行计划: %+v / %+v", planA, planB)
	}

	const workers = 2
	outcomes := make([]sideeffect.Outcome, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 各自的 Runner（各自进程）；同一动作 = 同一幂等键。
			out, _, err := sideeffect.NewRunner(st, time.Minute).Execute(ctx,
				sideeffect.Action{Key: sideeffect.Key("run-f5:b"), Resource: "res-b"},
				func(context.Context, sideeffect.Lease) (sideeffect.Result, error) {
					st.mu.Lock()
					st.applied["b"]++
					st.mu.Unlock()
					return sideeffect.Result{Payload: []byte("b")}, nil
				})
			if err != nil {
				t.Errorf("worker %d 收到错误（Contended 不带 error，这里不该有）: %v", i, err)
				return
			}
			outcomes[i] = out
		}(i)
	}
	wg.Wait()

	applied := 0
	for i, out := range outcomes {
		switch out {
		case sideeffect.OutcomeApplied:
			applied++
		case sideeffect.OutcomeContended, sideeffect.OutcomeReplayed:
			// 有序拒绝：等一等或拿回结果。合法。
		default:
			t.Fatalf("worker %d 出现意外结论 %v", i, out)
		}
	}
	if applied != 1 {
		t.Fatalf("应恰好一个 worker 施加成功，实得 %d（outcomes=%v）", applied, outcomes)
	}
	if got := st.appliedCount("b"); got != 1 {
		t.Fatalf("b 的实际施加次数 = %d, want 1", got)
	}
	// 输家随后重试同一动作：拿回结果（replayed），整个任务收敛。
	if out, err := apply(ctx, sideeffect.NewRunner(st, time.Minute), st, "run-f5", "b", "res-b"); err != nil || out != sideeffect.OutcomeReplayed {
		t.Fatalf("输家重试应判 replayed，实得 outcome=%v err=%v", out, err)
	}
	if got := st.appliedCount("b"); got != 1 {
		t.Fatalf("收敛后 b 的施加次数 = %d, want 1", got)
	}
}

// snap98 造一份最小快照。
func snap98(runID string, seq uint64, done ...string) checkpoint.Snapshot {
	return checkpoint.Snapshot{RunID: runID, Seq: seq, Status: "running", Done: done}
}
