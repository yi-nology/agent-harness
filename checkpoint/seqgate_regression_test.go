package checkpoint

import "testing"

// TestReleaseRestoresPreviousHighWaterMark 钉住 Release 的回滚语义。
//
// 背景：Release 早先走 delete(g.last, runID)，只为了让同序号可重试。但 last 只存
// 最大值，删记录等于把**上一个已成功落库的序号**一起抹掉——闸的地板从 5 跌回
// 「无记录」，seq=3 的陈旧慢写便畅通无阻，恢复点静默回退，正是 SeqGate 存在的
// 理由被自己打开。本测试走完整条路径，不直接调内部字段。
func TestReleaseRestoresPreviousHighWaterMark(t *testing.T) {
	g := NewSeqGate()
	const run = "r1"

	// 首个快照成功落库：地板 = 5。
	if err := g.Accept(Snapshot{RunID: run, Seq: 5}); err != nil {
		t.Fatalf("首个快照应通过: %v", err)
	}
	// 第二个快照接受后落库失败 → 必须回滚。
	if err := g.Accept(Snapshot{RunID: run, Seq: 7}); err != nil {
		t.Fatalf("更大的序号应通过: %v", err)
	}
	if !g.Release(run, 7) {
		t.Fatalf("Release 应认领 seq=7（last 恰为 7）")
	}

	if got := g.Last(run); got != 5 {
		t.Fatalf("回滚后地板应是上一个已落库的 5，实得 %d——回滚成删记录会让地板跌到 0", got)
	}

	// 关键断言：陈旧的 seq=3 必须仍被拒。
	if err := g.Accept(Snapshot{RunID: run, Seq: 3}); err == nil {
		t.Fatalf("seq=3 不该通过：seq=5 已成功落库，回滚不该把它变回可写（恢复点静默回退）")
	}

	// 同序号重试仍应放行——Release 的本职不能被前值保留破坏。
	if err := g.Accept(Snapshot{RunID: run, Seq: 7}); err != nil {
		t.Fatalf("seq=7 重试应放行: %v", err)
	}
}

// TestReleaseWithoutHistoryClearsGate 覆盖「本就没有前值」时退化成删除的分支。
func TestReleaseWithoutHistoryClearsGate(t *testing.T) {
	g := NewSeqGate()
	const run = "fresh"

	if err := g.Accept(Snapshot{RunID: run, Seq: 1}); err != nil {
		t.Fatalf("首个快照应通过: %v", err)
	}
	if !g.Release(run, 1) {
		t.Fatalf("Release 应认领 seq=1")
	}
	if got := g.Last(run); got != 0 {
		t.Fatalf("无前值时回滚应退回「无记录」（0），实得 %d", got)
	}
	// 无前值场景下同序号本就可重试——确认闸确实松开。
	if err := g.Accept(Snapshot{RunID: run, Seq: 1}); err != nil {
		t.Fatalf("无前值时同序号应可重试: %v", err)
	}
}

// TestReleaseIgnoresSupersededSeq 确认并发场景下不误退：期间已有更新的写入落地时，
// Release 必须不动任何状态（否则会把新写的闸退回去，给陈旧写开口子）。
func TestReleaseIgnoresSupersededSeq(t *testing.T) {
	g := NewSeqGate()
	const run = "r1"

	if err := g.Accept(Snapshot{RunID: run, Seq: 3}); err != nil {
		t.Fatalf("seq=3 应通过: %v", err)
	}
	if err := g.Accept(Snapshot{RunID: run, Seq: 9}); err != nil {
		t.Fatalf("seq=9 应通过: %v", err)
	}
	// seq=3 的慢写此刻落库失败，但它早已被 9 取代。
	if g.Release(run, 3) {
		t.Fatalf("last=9 ≠ 3，Release 不该认领")
	}
	if got := g.Last(run); got != 9 {
		t.Fatalf("Release 未认领时状态不应变，实得 %d", got)
	}
}

// TestSeedInvalidatesStaleRollbackTarget 确认 Seed 抬高基线后作废旧的前值：
// 宿主重启场景下内存里的历史本就不可信，退回它等于退回一个陈旧地板。
func TestSeedInvalidatesStaleRollbackTarget(t *testing.T) {
	g := NewSeqGate()
	const run = "r1"

	if err := g.Accept(Snapshot{RunID: run, Seq: 2}); err != nil { // prev=0, last=2
		t.Fatalf("seq=2 应通过: %v", err)
	}
	if err := g.Accept(Snapshot{RunID: run, Seq: 4}); err != nil { // prev=2, last=4
		t.Fatalf("seq=4 应通过: %v", err)
	}
	g.Seed(run, 10) // 基线抬到 10，旧前值 2 作废

	// last 已是 10，seq=4 不再是当前最大值，Release 不该认领。
	if g.Release(run, 4) {
		t.Fatalf("Seed 抬高基线后 last=10，Release(4) 不该认领")
	}
	if got := g.Last(run); got != 10 {
		t.Fatalf("Seed 抬高基线后地板应为 10，实得 %d", got)
	}
	if err := g.Accept(Snapshot{RunID: run, Seq: 3}); err == nil {
		t.Fatalf("Seed 基线 10 之下，seq=3 必须被拒")
	}
}
