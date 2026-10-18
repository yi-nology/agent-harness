package checkpoint

// 进程重启后的基线重建：SeqGate 的记录只在内存里，不 Seed 就等于闸被重置。
// 合成夹具，不带领域味。

import "testing"

// TestSeedRestoresBaselineAcrossRestart 重启后用已持久化的快照灌基线，
// 上一进程的陈旧快照必须重新被拒——这正是本包要防的「恢复点静默回退」。
//
// 头两步刻意断言**失效**行为：空闸会畅通写入陈旧快照。
// 这是本包已声明的局限（见 SeqGate 文档「局限」小节），必须钉住，
// 否则有人日后把闸改成持久化时不会知道这里曾经是靠 Seed 补上的。
func TestSeedRestoresBaselineAcrossRestart(t *testing.T) {
	// 上一进程写到 seq=9
	g := NewSeqGate()
	if err := g.Accept(snap("r", 9, "running")); err != nil {
		t.Fatal(err)
	}

	// 重启：新进程，闸是空的 —— 陈旧快照畅通写入（已声明的局限）
	g2 := NewSeqGate()
	if err := g2.Accept(snap("r", 5, "running")); err != nil {
		t.Fatalf("空闸本就该接受（无基线可拒）；若将来闸改为持久化，这条会红，"+
			"说明 Seed 已不再必需: %v", err)
	}

	// 用 Store.Load 拿回的快照灌基线
	g2.Seed("r", 9)
	if err := g2.Accept(snap("r", 4, "running")); err == nil {
		t.Fatal("Seed 之后陈旧快照必须被拒")
	}
	if err := g2.Accept(snap("r", 10, "running")); err != nil {
		t.Fatalf("Seed 之后更新的快照应放行: %v", err)
	}
}

// TestSeedIgnoresDegenerateInput 空 runID 与零序号不得污染基线。
func TestSeedIgnoresDegenerateInput(t *testing.T) {
	g := NewSeqGate()
	g.Seed("", 9)
	g.Seed("r", 0)
	if g.Last("") != 0 || g.Last("r") != 0 {
		t.Fatalf("退化输入不得写入基线: %q=%d r=%d", "", g.Last(""), g.Last("r"))
	}
	// 退化 Seed 之后仍可正常接受首个快照
	if err := g.Accept(snap("r", 1, "running")); err != nil {
		t.Fatalf("应正常接受首个快照: %v", err)
	}
}

// TestSeedDoesNotLowerBaseline Seed 不得把基线退回去——重复 Seed 同一个快照
// 应幂等，用更小的 seq 也不该削弱已建立的基线。
func TestSeedDoesNotLowerBaseline(t *testing.T) {
	g := NewSeqGate()
	g.Seed("r", 9)
	g.Seed("r", 3) // 陈旧或错误的 Load 结果
	g.Seed("r", 9) // 重复灌同一份，幂等
	if g.Last("r") != 9 {
		t.Fatalf("基线不应被降低，实得 %d", g.Last("r"))
	}
	if err := g.Accept(snap("r", 8, "running")); err == nil {
		t.Fatal("基线 9 之下仍应被拒")
	}
}

// TestSeedAcrossRunsIndependent 不同 run 各自独立基线。
func TestSeedAcrossRunsIndependent(t *testing.T) {
	g := NewSeqGate()
	g.Seed("r-a", 20)
	if g.Last("r-b") != 0 {
		t.Fatal("未 Seed 的 run 不应有基线")
	}
	if err := g.Accept(snap("r-b", 1, "running")); err != nil {
		t.Fatalf("r-b 应独立接受首个快照: %v", err)
	}
}
