package drain

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeSessionTurns 最小宿主替身：按会话分别记在途轮，并记录取消动作。
type fakeSessionTurns struct {
	mu        sync.Mutex
	active    map[string]int
	cancelAll []string
	cancelled map[string]error
}

func newFakeSessionTurns() *fakeSessionTurns {
	return &fakeSessionTurns{active: map[string]int{}, cancelled: map[string]error{}}
}

func (f *fakeSessionTurns) Active() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.active {
		n += v
	}
	return n
}

func (f *fakeSessionTurns) CancelAll(cause error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.active {
		f.cancelAll = append(f.cancelAll, k)
		f.cancelled[k] = cause
		delete(f.active, k)
	}
}

func (f *fakeSessionTurns) ActiveOf(sessionID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[sessionID]
}

func (f *fakeSessionTurns) CancelOf(sessionID string, cause error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.active[sessionID]; !ok {
		return // 无在途轮时必须无操作
	}
	f.cancelled[sessionID] = cause
	delete(f.active, sessionID)
}

func (f *fakeSessionTurns) seed(sessionID string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[sessionID] = n
}

// TestDrainSessionNoActive 无在途轮时立即返回，不触发任何取消。
func TestDrainSessionNoActive(t *testing.T) {
	f := newFakeSessionTurns()
	DrainSession(context.Background(), f, "s-1", time.Second)
	if len(f.cancelAll) != 0 || len(f.cancelled) != 0 {
		t.Fatalf("无在途轮不应触发取消: all=%v one=%v", f.cancelAll, f.cancelled)
	}
}

// TestDrainSessionWaitsForConvergence 在途轮自然收敛后返回，且不误伤别的会话。
func TestDrainSessionWaitsForConvergence(t *testing.T) {
	f := newFakeSessionTurns()
	f.seed("s-1", 1)
	f.seed("s-2", 1)

	go func() {
		time.Sleep(30 * time.Millisecond)
		f.mu.Lock()
		delete(f.active, "s-1")
		f.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	DrainSession(ctx, f, "s-1", 2*time.Second)

	if _, killed := f.cancelled["s-1"]; killed {
		t.Fatal("自然收敛不应触发强制取消")
	}
	if f.ActiveOf("s-2") != 1 {
		t.Fatal("排一个会话不得动到另一个会话的在途轮")
	}
}

// TestDrainSessionForceCancelOnlyThatSession **爆炸半径**的核心断言：
// 预算耗尽时只强杀目标会话，其余会话的在途轮必须原样保留。
func TestDrainSessionForceCancelOnlyThatSession(t *testing.T) {
	f := newFakeSessionTurns()
	f.seed("s-stuck", 1)
	f.seed("s-healthy", 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已耗尽的预算

	DrainSession(ctx, f, "s-stuck", time.Second)

	if got := f.cancelled["s-stuck"]; got != ErrDeadline {
		t.Fatalf("目标会话应被强杀且 cause 为 ErrDeadline，实得 %v", got)
	}
	if _, killed := f.cancelled["s-healthy"]; killed {
		t.Fatal("健康会话不得被陪葬——这正是本文件要消除的爆炸半径")
	}
	if f.ActiveOf("s-healthy") != 1 {
		t.Fatal("健康会话的在途轮应原样保留")
	}
	if len(f.cancelAll) != 0 {
		t.Fatalf("会话级排水不得退化成全局取消: %v", f.cancelAll)
	}
}

// TestDrainSessionEmptyIDDoesNotFallBackToGlobal 空会话标识无法寻址，
// 必须拒绝而不是退化成 CancelAll——那正是要消除的误伤。
func TestDrainSessionEmptyIDDoesNotFallBackToGlobal(t *testing.T) {
	f := newFakeSessionTurns()
	f.seed("s-1", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	DrainSession(ctx, f, "", time.Second)
	if len(f.cancelAll) != 0 || len(f.cancelled) != 0 {
		t.Fatalf("空会话标识不得触发任何取消: all=%v one=%v", f.cancelAll, f.cancelled)
	}
	if f.ActiveOf("s-1") != 1 {
		t.Fatal("空会话标识不得影响任何在途轮")
	}
}

// TestDrainSessionBudgetNegativeNoOp budget<0 = 脱钩关闭态，无排水动作。
func TestDrainSessionBudgetNegativeNoOp(t *testing.T) {
	f := newFakeSessionTurns()
	f.seed("s-1", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	DrainSession(ctx, f, "s-1", -1)
	if len(f.cancelAll) != 0 || f.ActiveOf("s-1") != 1 {
		t.Fatalf("budget<0 应完全无操作: all=%v active=%d", f.cancelAll, f.ActiveOf("s-1"))
	}
}

// TestSessionTurnsIsSupersetOfTurns SessionTurns 必须是 Turns 的超集，
// 宿主已实现的 Turns 能力不被破坏（纯加法承诺）。
func TestSessionTurnsIsSupersetOfTurns(t *testing.T) {
	var (
		_ SessionTurns = (*fakeSessionTurns)(nil)
		_ Turns        = (*fakeSessionTurns)(nil)
	)
	f := newFakeSessionTurns()
	f.seed("s-1", 2)
	f.seed("s-2", 3)
	if f.Active() != 5 {
		t.Fatalf("全局在途数应含所有会话，实得 %d", f.Active())
	}
	if f.ActiveOf("s-1") != 2 || f.ActiveOf("s-2") != 3 {
		t.Fatal("按会话计数不正确")
	}
}

// TestGlobalDrainStillWorks 原全局语义未受影响（回归护栏）。
func TestGlobalDrainStillWorks(t *testing.T) {
	f := newFakeSessionTurns()
	f.seed("s-1", 1)
	f.seed("s-2", 1)
	// Drain 的强制收刀由 ctx 驱动（budget 只进日志，不参与超时判定，见 Drain 契约），
	// 故这里必须给一个会到点的 ctx——传 context.Background() 会永久阻塞在 select 上。
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	Drain(ctx, f, time.Second)
	if f.Active() != 0 {
		t.Fatalf("全局 Drain 应排空所有在途轮，实余 %d", f.Active())
	}
}
