package checkpoint

import (
	"context"
	"sync"
	"testing"

	"github.com/oekit/pkg/errors"
)

// memCAS 是一份**按契约实现**的内存 CAS 存储，用来检验 Committer 的语义。
//
// 它刻意实现「条件更新」而非「无条件覆盖」：只有 seq 严格大于已持久化值才写，
// 否则返回 ErrStaleSeq 且不写入。这正是宿主在 SQL 里要做的事。
type memCAS struct {
	mu   sync.Mutex
	data map[string]Snapshot
	// saves 统计写入尝试次数（含被拒的），用于断言「陈旧写根本没碰到存储」。
	saves int
}

func newMemCAS() *memCAS { return &memCAS{data: map[string]Snapshot{}} }

func (m *memCAS) SaveIfNewer(_ context.Context, s Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	if s.Seq == 0 {
		return nil // Seq==0 不参与单调判定，不是真实提交
	}
	if cur, ok := m.data[s.RunID]; ok && s.Seq <= cur.Seq {
		return ErrStaleSeq
	}
	m.data[s.RunID] = s.Clone()
	return nil
}

func (m *memCAS) Load(_ context.Context, runID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.data[runID]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return s.Clone(), nil
}

func mkSnap(runID string, seq uint64, status string) Snapshot {
	return Snapshot{RunID: runID, Seq: seq, Status: status}
}

// TestCommitAdvancesGateAndStores 正常提交：闸前进 + 落库。
func TestCommitAdvancesGateAndStores(t *testing.T) {
	store := newMemCAS()
	c := NewCommitter(NewSeqGate(), store)

	if err := c.Commit(context.Background(), mkSnap("r1", 1, "running")); err != nil {
		t.Fatalf("首次提交应成功，实得 %v", err)
	}
	if got := c.Last("r1"); got != 1 {
		t.Fatalf("闸应前进到 1，实得 %d", got)
	}
	if got, _ := store.Load(context.Background(), "r1"); got.Seq != 1 {
		t.Fatalf("存储应记下 seq=1，实得 %d", got.Seq)
	}
}

// TestGateRejectsBeforeTouchingStore 进程内已知陈旧的写不该惊动存储。
func TestGateRejectsBeforeTouchingStore(t *testing.T) {
	store := newMemCAS()
	gate := NewSeqGate()
	c := NewCommitter(gate, store)

	_ = c.Commit(context.Background(), mkSnap("r1", 5, "running"))
	before := store.saves

	err := c.Commit(context.Background(), mkSnap("r1", 3, "running"))
	if !errors.Is(err, ErrStaleSeq) {
		t.Fatalf("陈旧写应被进程内闸拒绝，实得 %v", err)
	}
	if store.saves != before {
		t.Fatalf("进程内闸已拒，不该再打到存储（多一次往返，且掩盖真实原因）")
	}
}

// TestStaleWriteRejectedAndGateRolledBack 存储侧拒绝时，闸**必须回滚**。
//
// 不回滚的后果不是「多花一个序号」，是这个 run 从此卡死：同序号重试会被判陈旧，
// 跳过它又留下一条永远补不回来的空洞。
func TestStaleWriteRejectedAndGateRolledBack(t *testing.T) {
	store := newMemCAS()
	// 先让存储里有一个比闸更「新」的事实。
	_ = store.SaveIfNewer(context.Background(), mkSnap("r1", 9, "running"))
	gate := NewSeqGate()
	c := NewCommitter(gate, store)

	err := c.Commit(context.Background(), mkSnap("r1", 4, "running"))
	if !errors.Is(err, ErrStaleSeq) {
		t.Fatalf("跨进程陈旧写应被拒，实得 %v", err)
	}
	if got := c.Last("r1"); got != 0 {
		t.Fatalf("失败后闸必须回滚（实得 %d）；不回滚会让该 run 卡死", got)
	}
}

// TestSuccessfulCommitKeepsGateAdvanced 成功后**不得**回滚闸。
//
// 闸的当前值是「本进程已见过的最大序号」。落库成功后把它退回去，一次陈旧的
// seq=3 慢写就会畅通无阻——正是本机制要防的恢复点静默回退。
func TestSuccessfulCommitKeepsGateAdvanced(t *testing.T) {
	store := newMemCAS()
	gate := NewSeqGate()
	c := NewCommitter(gate, store)

	_ = c.Commit(context.Background(), mkSnap("r1", 5, "running"))
	if got := c.Last("r1"); got != 5 {
		t.Fatalf("成功后闸应停在 5，实得 %d", got)
	}
	// 后续陈旧慢写仍应被挡下（这正是「不回滚」的意义）。
	if err := c.Commit(context.Background(), mkSnap("r1", 3, "running")); err == nil {
		t.Fatal("成功后陈旧慢写仍应被拒")
	}
}

// TestTwoProcessesOneStoreWinners 核心场景：两个进程（各自独立的闸）抢同一个 run。
// 慢的那次必须被存储侧挡下，且它的闸回到原状而不是卡死。
func TestTwoProcessesOneStoreWinners(t *testing.T) {
	store := newMemCAS()
	fast := NewCommitter(NewSeqGate(), store)
	slow := NewCommitter(NewSeqGate(), store) // 模拟另一个进程：闸是空的

	if err := fast.Commit(context.Background(), mkSnap("r1", 5, "running")); err != nil {
		t.Fatal(err)
	}
	// 慢进程拿到一个陈旧视图（它没见过 seq=5），写 seq=3。
	if err := slow.Commit(context.Background(), mkSnap("r1", 3, "running")); err == nil {
		t.Fatal("慢进程的陈旧写必须被跨进程判据挡下")
	}
	if got, _ := store.Load(context.Background(), "r1"); got.Seq != 5 {
		t.Fatalf("存储应仍是 seq=5（慢写不得覆盖），实得 %d", got.Seq)
	}
	if got := slow.Last("r1"); got != 0 {
		t.Fatalf("慢进程闸应回滚，实得 %d", got)
	}
	// 且慢进程未被卡死：回灌后可以正常继续。
	if err := slow.SeedFromStore(context.Background(), "r1"); err != nil {
		t.Fatal(err)
	}
	if got := slow.Last("r1"); got != 5 {
		t.Fatalf("回灌后闸应见 seq=5，实得 %d", got)
	}
	if err := slow.Commit(context.Background(), mkSnap("r1", 6, "running")); err != nil {
		t.Fatalf("回灌后应能继续推进，实得 %v", err)
	}
}

// TestSeedFromStoreClosesRestartGap 重启后回灌，上一进程的慢写被挡下。
// 不回灌的后果是「闸被重置后没人告知」——不是「闸没生效」。
func TestSeedFromStoreClosesRestartGap(t *testing.T) {
	store := newMemCAS()
	before := NewCommitter(NewSeqGate(), store)
	_ = before.Commit(context.Background(), mkSnap("r1", 7, "running"))

	// 进程重启：新闸是空的。
	after := NewCommitter(NewSeqGate(), store)
	if got := after.Last("r1"); got != 0 {
		t.Fatalf("重启后闸应为空，实得 %d", got)
	}
	if err := after.SeedFromStore(context.Background(), "r1"); err != nil {
		t.Fatalf("回灌应成功，实得 %v", err)
	}
	if got := after.Last("r1"); got != 7 {
		t.Fatalf("回灌后闸应见 seq=7，实得 %d", got)
	}
	if err := after.Commit(context.Background(), mkSnap("r1", 3, "running")); err == nil {
		t.Fatal("重启后的陈旧慢写必须被挡下")
	}
}

// TestSeedFromStoreFirstRunIsNotAnError 首次执行没有记录可回灌，不算错误。
func TestSeedFromStoreFirstRunIsNotAnError(t *testing.T) {
	c := NewCommitter(NewSeqGate(), newMemCAS())
	if err := c.SeedFromStore(context.Background(), "never-ran"); err != nil {
		t.Fatalf("首次执行回灌应返回 nil，实得 %v", err)
	}
}

// TestMissingStoreFailsLoudly 未装配存储必须显式失败，不能静默成功。
// 「没接存储也返回 nil」会让调用方以为进度已落库，恢复时从错误的位置续跑。
func TestMissingStoreFailsLoudly(t *testing.T) {
	c := NewCommitter(NewSeqGate(), nil)
	if err := c.Commit(context.Background(), mkSnap("r1", 1, "running")); !errors.Is(err, ErrStoreMissing) {
		t.Fatalf("未装配存储应显式失败，实得 %v", err)
	}
}

// TestNilGateStillSafeWithoutGate 无进程内闸时仍安全——判据在存储侧。
func TestNilGateStillSafeWithoutGate(t *testing.T) {
	store := newMemCAS()
	c := NewCommitter(nil, store)
	if err := c.Commit(context.Background(), mkSnap("r1", 1, "running")); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(context.Background(), mkSnap("r1", 2, "running")); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(context.Background(), mkSnap("r1", 1, "running")); err == nil {
		t.Fatal("无进程内闸时，跨进程判据仍须生效")
	}
	if got := c.Last("r1"); got != 0 {
		t.Fatalf("无闸形态下 Last 应为 0，实得 %d", got)
	}
}

// TestConcurrentCommitsSerialize 多个提交并发时，存储里不得出现序号倒退。
func TestConcurrentCommitsSerialize(t *testing.T) {
	store := newMemCAS()
	c := NewCommitter(NewSeqGate(), store)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(n uint64) {
			defer wg.Done()
			if err := c.Commit(context.Background(), mkSnap("r1", n, "running")); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(uint64(i))
	}
	wg.Wait()

	final, err := store.Load(context.Background(), "r1")
	if err != nil {
		t.Fatal(err)
	}
	if final.Seq < 32 {
		t.Fatalf("并发提交后存储应至少记到某个较大序号，实得 %d（成功 %d 次）", final.Seq, ok)
	}
	if ok == 0 {
		t.Fatal("并发下不应一次都提交不成功")
	}
}
