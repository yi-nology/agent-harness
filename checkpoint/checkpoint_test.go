package checkpoint

// 夹具一律用合成步骤名（alpha/beta/gamma…），不带任何领域味——
// 夹具是契约的示例，示例带味等于把契约钉死在某个领域上。

import (
	"context"
	"testing"

	"github.com/oekit/pkg/errors"
)

func snap(runID string, seq uint64, status string, done ...string) Snapshot {
	return Snapshot{RunID: runID, Seq: seq, Status: status, Done: done}
}

// fakeStore 契约自检用的最小宿主替身：证明 Store 可在零领域依赖下实现。
type fakeStore struct {
	rows   map[string]Snapshot
	saves  int
	loaded []string
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]Snapshot{}} }

func (f *fakeStore) Save(_ context.Context, s Snapshot) error {
	if _, dup := f.rows[s.RunID]; dup && f.rows[s.RunID].Seq == s.Seq {
		return nil // 同 (RunID, Seq) 幂等：不产生第二份记录
	}
	f.saves++
	f.rows[s.RunID] = s.Clone()
	return nil
}

func (f *fakeStore) Load(_ context.Context, runID string) (Snapshot, error) {
	f.loaded = append(f.loaded, runID)
	s, ok := f.rows[runID]
	if !ok {
		return Snapshot{}, ErrNotFound // 关键：无记录必须返回哨兵，不得返回零值+nil
	}
	return s.Clone(), nil
}

var _ Store = (*fakeStore)(nil)

// TestMinimalHostSatisfiesStore 最小替身即可满足 Store——
// 零领域依赖是本包的硬指标（同 contract 的契约自检）。
func TestMinimalHostSatisfiesStore(t *testing.T) {
	st := newFakeStore()
	if err := st.Save(context.Background(), snap("run-1", 1, "running")); err != nil {
		t.Fatalf("首次落库应成功: %v", err)
	}
	got, err := st.Load(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("读取应成功: %v", err)
	}
	if got.Seq != 1 || got.RunID != "run-1" {
		t.Fatalf("读回内容不符: %+v", got)
	}
}

// TestLoadMissingReturnsErrNotFound 无记录必须是哨兵错误。
// 若实现返回 (零值, nil)，上游会误判成「从没跑过」从而全量重跑，
// 重复副作用正是续跑要避免的事——这条要用测试钉死。
func TestLoadMissingReturnsErrNotFound(t *testing.T) {
	st := newFakeStore()
	got, err := st.Load(context.Background(), "run-absent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("无记录应返回 ErrNotFound，实得 err=%v", err)
	}
	if got.Seq != 0 || got.Done != nil {
		t.Fatalf("出错时返回值应为零值快照，实得 %+v", got)
	}
}

// TestSaveIdempotent 同一 (RunID, Seq) 重复落库不得产生第二份记录。
func TestSaveIdempotent(t *testing.T) {
	st := newFakeStore()
	s := snap("run-2", 7, "running")
	for i := 0; i < 3; i++ {
		if err := st.Save(context.Background(), s); err != nil {
			t.Fatalf("第 %d 次落库失败: %v", i+1, err)
		}
	}
	if st.saves != 1 {
		t.Fatalf("幂等落库应只写一次，实写 %d 次", st.saves)
	}
}

// TestCloneDeepCopies Clone 必须深拷贝 Done 与 Fields——
// 落库后调用方继续改切片/字典会让恢复判定的依据失真。
func TestCloneDeepCopies(t *testing.T) {
	src := Snapshot{
		RunID:  "run-3",
		Done:   []string{"step-alpha"},
		Fields: map[string]string{"k": "v1"},
	}
	cp := src.Clone()
	cp.Done[0] = "step-beta"
	cp.Fields["k"] = "v2"
	cp.Fields["added"] = "x"

	if src.Done[0] != "step-alpha" {
		t.Fatalf("Done 被别名污染: %v", src.Done)
	}
	if src.Fields["k"] != "v1" || len(src.Fields) != 1 {
		t.Fatalf("Fields 被别名污染: %v", src.Fields)
	}
}

// TestCloneNilSlices Clone 对 nil 切片/字典保持 nil（不得凭空造空容器）。
func TestCloneNilSlices(t *testing.T) {
	cp := Snapshot{RunID: "run-4"}.Clone()
	if cp.Done != nil || cp.Fields != nil {
		t.Fatalf("nil 应保持 nil: Done=%v Fields=%v", cp.Done, cp.Fields)
	}
}

// TestSeqGateStrictlyMonotonic 序号必须严格递增：相等也拒（不是「不小于」）。
func TestSeqGateStrictlyMonotonic(t *testing.T) {
	g := NewSeqGate()
	if err := g.Accept(snap("r", 1, "running")); err != nil {
		t.Fatalf("首个快照应通过: %v", err)
	}
	if err := g.Accept(snap("r", 2, "running")); err != nil {
		t.Fatalf("递增应通过: %v", err)
	}
	if err := g.Accept(snap("r", 2, "running")); !errors.Is(err, ErrStaleSeq) {
		t.Fatalf("相同序号应被拒，实得 %v", err)
	}
	if err := g.Accept(snap("r", 1, "running")); !errors.Is(err, ErrStaleSeq) {
		t.Fatalf("回退序号应被拒，实得 %v", err)
	}
	if g.Last("r") != 2 {
		t.Fatalf("被拒的快照不应改变已接受最大值，实得 %d", g.Last("r"))
	}
}

// TestSeqGatePerRunIsolation 不同 RunID 互不影响。
func TestSeqGatePerRunIsolation(t *testing.T) {
	g := NewSeqGate()
	if err := g.Accept(snap("r-a", 9, "running")); err != nil {
		t.Fatalf("run-a 应通过: %v", err)
	}
	if err := g.Accept(snap("r-b", 1, "running")); err != nil {
		t.Fatalf("run-b 序号从 1 起，应独立通过: %v", err)
	}
	if g.Last("r-a") != 9 || g.Last("r-b") != 1 {
		t.Fatalf("两个 run 的序号被串扰: %d / %d", g.Last("r-a"), g.Last("r-b"))
	}
	if g.Last("never-seen") != 0 {
		t.Fatalf("未见过的 run 应返回 0，实得 %d", g.Last("never-seen"))
	}
}

// TestSeqGateZeroSeqPassthrough 序号 0 表示「未初始化」，不参与单调判定。
func TestSeqGateZeroSeqPassthrough(t *testing.T) {
	g := NewSeqGate()
	if err := g.Accept(snap("r", 0, "created")); err != nil {
		t.Fatalf("seq=0 应放行: %v", err)
	}
	if g.Last("r") != 0 {
		t.Fatalf("seq=0 不应被记录为有效序号，实得 %d", g.Last("r"))
	}
	if err := g.Accept(snap("r", 5, "running")); err != nil {
		t.Fatalf("seq=5 应通过: %v", err)
	}
}
