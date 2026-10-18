package drain

// 排水机制单测：用假 Turns 钉住「等待/强制收刀/关闭态」三态语义，
// 与宿主（谁在跑轮、轮怎么注册）无关——这是把机制下沉内核后能拿到的测试面。

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// fakeTurns 可控在途轮集合：Active 计数、CancelAll 记录取消原因。
type fakeTurns struct {
	active int32
	mu     sync.Mutex
	causes []error
}

func (f *fakeTurns) Active() int { return int(atomic.LoadInt32(&f.active)) }

func (f *fakeTurns) CancelAll(cause error) {
	f.mu.Lock()
	f.causes = append(f.causes, cause)
	f.mu.Unlock()
	atomic.StoreInt32(&f.active, 0)
}

func (f *fakeTurns) lastCause(t *testing.T) error {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.causes) == 0 {
		t.Fatal("CancelAll 未被调用")
	}
	return f.causes[len(f.causes)-1]
}

// TestDrainWaitsForActiveTurns 在途轮存在时排水不得立即返回；轮收敛后应返回。
func TestDrainWaitsForActiveTurns(t *testing.T) {
	f := &fakeTurns{active: 1}
	done := make(chan struct{})
	go func() { Drain(context.Background(), f, 5*time.Second); close(done) }()

	select {
	case <-done:
		t.Fatal("在途轮存在时 Drain 不应立即返回")
	case <-time.After(400 * time.Millisecond):
	}

	atomic.StoreInt32(&f.active, 0) // 轮自然收敛
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("在途轮收敛后 Drain 应返回")
	}
}

// TestDrainDeadlineCancelsLeftovers 预算耗尽 → 强制收刀，取消原因须为 ErrDeadline。
func TestDrainDeadlineCancelsLeftovers(t *testing.T) {
	f := &fakeTurns{active: 2}
	dctx, dcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer dcancel()

	Drain(dctx, f, time.Hour)

	if err := f.lastCause(t); !errors.Is(err, ErrDeadline) {
		t.Fatalf("取消原因应为 ErrDeadline, got: %v", err)
	}
}

// TestDrainNoForceWhenConverged 已收敛的轮不该被强杀（否则等于没排水）。
func TestDrainNoForceWhenConverged(t *testing.T) {
	f := &fakeTurns{} // 空 registry
	Drain(context.Background(), f, time.Hour)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.causes) != 0 {
		t.Fatal("无在途轮时不应触发 CancelAll")
	}
}

// TestDrainDisabled 负预算 = 整体关闭：不等待、不动轮。
func TestDrainDisabled(t *testing.T) {
	f := &fakeTurns{active: 3}
	done := make(chan struct{})
	go func() { Drain(context.Background(), f, -time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("负预算（关闭态）应立即返回")
	}
	if f.Active() != 3 {
		t.Fatal("关闭态不应触碰在途轮")
	}
}

// TestDetachCtxGate 脱钩门控：正预算切断取消传播，负预算保留传播。
func TestDetachCtxGate(t *testing.T) {
	root, cancelRoot := context.WithCancel(context.Background())
	detached := DetachCtx(root, time.Minute)
	cancelRoot()
	if detached.Err() != nil {
		t.Fatal("脱钩后根 ctx 取消不应传播")
	}

	root2, cancelRoot2 := context.WithCancel(context.Background())
	kept := DetachCtx(root2, -time.Second)
	cancelRoot2()
	if kept.Err() == nil {
		t.Fatal("脱钩关闭态应保留取消传播")
	}
}

// TestDetachCtxKeepsValues 值必须保留（脱钩只切取消传播，不切数据面）。
func TestDetachCtxKeepsValues(t *testing.T) {
	type k struct{}
	ctx := context.WithValue(context.Background(), k{}, "v")
	if got := DetachCtx(ctx, time.Minute).Value(k{}); got != "v" {
		t.Fatalf("脱钩应保留 ctx 值, got: %v", got)
	}
}

// TestBudget 预算解析：0=缺省，负=原样（关闭语义由调用方判）。
func TestBudget(t *testing.T) {
	if Budget(0) != DefaultTimeout {
		t.Fatalf("0 应回落缺省 %s, got %s", DefaultTimeout, Budget(0))
	}
	if Budget(3*time.Second) != 3*time.Second {
		t.Fatal("非零预算应原样返回")
	}
	if Budget(-1) != -1 {
		t.Fatal("负预算应原样返回（关闭语义）")
	}
}
