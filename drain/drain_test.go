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
	go func() { Drain(context.Background(), f, Budget(5*time.Second)); close(done) }()

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

	Drain(dctx, f, Budget(time.Hour))

	if err := f.lastCause(t); !errors.Is(err, ErrDeadline) {
		t.Fatalf("取消原因应为 ErrDeadline, got: %v", err)
	}
}

// TestDrainCancelNotMisreportedAsDeadline 关停链二次取消 → cause 必须是真实原因
// context.Canceled，不得记成 ErrDeadline（issue #55）。
//
// 此前 ctx.Done 分支恒记 ErrDeadline：运维在审计里看到「排水超时」会去调宽限期，
// 而真实原因是关停链提前二次取消——两者修法完全不同。留痕 cause 与 cancel(cause)
// 同源（同一个变量），故这里钉住 cancel 面即同时钉住留痕面。
func TestDrainCancelNotMisreportedAsDeadline(t *testing.T) {
	f := &fakeTurns{active: 2}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Drain(ctx, f, Budget(time.Hour)) // 预算远未到点，等的是二次取消
		close(done)
	}()

	// 让 Drain 先进入收敛等待再取消：覆盖的是循环内的 Done 分支（与入口即 Done
	// 同一分支，但这是关停链二次取消的真实时序——排水已跑了一段时间）。
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Drain 应返回")
	}
	cause := f.lastCause(t)
	if !errors.Is(cause, context.Canceled) {
		t.Fatalf("二次取消的 cause 应为 context.Canceled, got: %v", cause)
	}
	if errors.Is(cause, ErrDeadline) {
		t.Fatal("二次取消不得被误标为排水超时（ErrDeadline）")
	}
}

// TestDrainNoForceWhenConverged 已收敛的轮不该被强杀（否则等于没排水）。
func TestDrainNoForceWhenConverged(t *testing.T) {
	f := &fakeTurns{} // 空 registry
	Drain(context.Background(), f, Budget(time.Hour))
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
	go func() { Drain(context.Background(), f, Budget(-time.Second)); close(done) }()
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
	detached := DetachCtx(root, Budget(time.Minute))
	cancelRoot()
	if detached.Err() != nil {
		t.Fatal("脱钩后根 ctx 取消不应传播")
	}

	root2, cancelRoot2 := context.WithCancel(context.Background())
	kept := DetachCtx(root2, Budget(-time.Second))
	cancelRoot2()
	if kept.Err() == nil {
		t.Fatal("脱钩关闭态应保留取消传播")
	}
}

// TestDetachCtxKeepsValues 值必须保留（脱钩只切取消传播，不切数据面）。
func TestDetachCtxKeepsValues(t *testing.T) {
	type k struct{}
	ctx := context.WithValue(context.Background(), k{}, "v")
	if got := DetachCtx(ctx, Budget(time.Minute)).Value(k{}); got != "v" {
		t.Fatalf("脱钩应保留 ctx 值, got: %v", got)
	}
}

// TestBudget 预算解析：0=缺省，负=原样（关闭语义由调用方判）。
func TestBudget(t *testing.T) {
	if got := time.Duration(Budget(0)); got != DefaultTimeout {
		t.Fatalf("0 应回落缺省 %s, got %s", DefaultTimeout, got)
	}
	if got := time.Duration(Budget(3 * time.Second)); got != 3*time.Second {
		t.Fatalf("非零预算应原样返回, got %s", got)
	}
	if got := time.Duration(Budget(-1)); got != -1 {
		t.Fatalf("负预算应原样返回（关闭语义）, got %s", got)
	}
}

// TestBudgetIsNotATimeDuration 钉住**编译期**护栏本身：Budget 返回命名类型
// DrainBudget，故 `context.WithTimeout(ctx, Budget(x))` 编译不过——误用从
// 「运行期静默硬切」变成「构建失败」。
//
// 改名/改签名时唯一危险的方向是「把 DrainBudget 换回 time.Duration 别名」：
// 那样所有既有调用点照样编译、全部用例照样绿，护栏静默消失。故这里断言的是
// 动态类型**不是** time.Duration，这条在换回别名时必红。
func TestBudgetIsNotATimeDuration(t *testing.T) {
	var v any = Budget(0)
	if _, ok := v.(time.Duration); ok {
		t.Fatal("Budget 返回了 time.Duration：WithTimeout(ctx, Budget(x)) 重新变成合法的静默硬切")
	}
	if _, ok := v.(DrainBudget); !ok {
		t.Fatalf("Budget 应返回 DrainBudget, got %T", v)
	}
}

// TestShutdownBudget 停机收尾预算：0 与负值都归缺省——配置侧「负=关闭排水」
// 不等于收尾路径不要余量；正值原样。
func TestShutdownBudget(t *testing.T) {
	if ShutdownBudget(0) != DefaultTimeout {
		t.Fatalf("0 应回落缺省 %s, got %s", DefaultTimeout, ShutdownBudget(0))
	}
	if ShutdownBudget(-5*time.Second) != DefaultTimeout {
		t.Fatalf("负值应归缺省 %s（负配置直喂 WithTimeout 会立即过期）, got %s",
			DefaultTimeout, ShutdownBudget(-5*time.Second))
	}
	if ShutdownBudget(30*time.Second) != 30*time.Second {
		t.Fatal("正值应原样返回")
	}
}

// TestBudgetAndShutdownBudgetAreNotInterchangeable 把「两个入口不可互换」这条
// 契约钉成机器判定——它是本包最贵的一次踩坑（harness#24）：负配置走 Budget 后
// 被喂进 context.WithTimeout，ctx 立即过期，优雅停机静默变硬切且不报错。
//
// 两条方向**必须相反**，缺一条这个陷阱就还在：
//   - Budget 对负值必须**继续**返回负值（<0 是 Drain/DetachCtx 的关闭语义）；
//   - ShutdownBudget 对同样的输入必须返回**恒正**值（喂 WithTimeout 才安全）。
//
// 反向验证：把 Budget 的 `d == 0` 改成 `d <= 0`（即「顺手归一负值」），本用例红。
// 那条改动看起来是修 bug，实际是把「关闭排水」静默翻成「排水 60 秒」——
// 方向由「少做」变成「多做」，且配置意图被吞掉。
func TestBudgetAndShutdownBudgetAreNotInterchangeable(t *testing.T) {
	for _, d := range []time.Duration{-1, -time.Second, -time.Hour} {
		if got := time.Duration(Budget(d)); got >= 0 {
			t.Fatalf("Budget(%v) = %v：负值必须原样透传，否则「关闭排水」被静默翻成「排水 %v」",
				d, got, got)
		}
		if got := ShutdownBudget(d); got <= 0 {
			t.Fatalf("ShutdownBudget(%v) = %v：喂 WithTimeout 的预算必须恒正", d, got)
		}
	}
}
