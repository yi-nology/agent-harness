package checkpoint

// 回滚路径：闸跑到存储前面会把 run 卡死。合成夹具，不带领域味。

import "testing"

// TestReleaseAllowsSameSeqRetry 落库失败后回滚，同序号必须能重试成功。
// 不回滚的真实后果不是「多花一个序号」，而是该 run 永远写不进这条快照。
func TestReleaseAllowsSameSeqRetry(t *testing.T) {
	g := NewSeqGate()
	s := snap("r", 5, "running")

	if err := g.Accept(s); err != nil {
		t.Fatalf("首次应通过: %v", err)
	}
	// 模拟 Store.Save 失败 → 回滚
	if !g.Release("r", 5) {
		t.Fatal("seq 与当前记录一致时应回滚成功")
	}
	if g.Last("r") != 0 {
		t.Fatalf("回滚后应无记录，实得 %d", g.Last("r"))
	}
	// 同序号必须能重试
	if err := g.Accept(s); err != nil {
		t.Fatalf("回滚后同序号重试应通过: %v", err)
	}
}

// TestReleaseRefusesWhenSuperseded 期间已有更新的写入落地时不得回滚——
// 把闸退回去等于让陈旧写重新获得通行权。
func TestReleaseRefusesWhenSuperseded(t *testing.T) {
	g := NewSeqGate()
	if err := g.Accept(snap("r", 5, "running")); err != nil {
		t.Fatal(err)
	}
	if err := g.Accept(snap("r", 9, "running")); err != nil {
		t.Fatal(err)
	}
	if g.Release("r", 5) {
		t.Fatal("已有更新的写入落地时不得回滚旧的 seq")
	}
	if g.Last("r") != 9 {
		t.Fatalf("回滚失败时状态不得变动，实得 %d", g.Last("r"))
	}
}

// TestReleaseUnknownRunAndZeroSeq 未知 run 与 seq=0 都不得改状态。
func TestReleaseUnknownRunAndZeroSeq(t *testing.T) {
	g := NewSeqGate()
	if g.Release("never", 3) {
		t.Fatal("未知 run 无从回滚")
	}
	if err := g.Accept(snap("r", 4, "running")); err != nil {
		t.Fatal(err)
	}
	if g.Release("r", 0) {
		t.Fatal("seq=0 不参与单调判定，也无从回滚")
	}
	if g.Last("r") != 4 {
		t.Fatalf("seq=0 的回滚请求不得影响状态，实得 %d", g.Last("r"))
	}
}

// TestReleaseThenStaleStillRejected 回滚不削弱单调性：更高序号写下后，
// 旧序号依然被拒。
func TestReleaseThenStaleStillRejected(t *testing.T) {
	g := NewSeqGate()
	if err := g.Accept(snap("r", 5, "running")); err != nil {
		t.Fatal(err)
	}
	g.Release("r", 5)
	if err := g.Accept(snap("r", 7, "running")); err != nil {
		t.Fatalf("回滚后更高序号应通过: %v", err)
	}
	if err := g.Accept(snap("r", 5, "running")); err == nil {
		t.Fatal("5 < 7，仍应被判为陈旧")
	}
}
