package sideeffect

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/oekit/pkg/errors"
)

// TestSinkAcceptsFreshFence 钉住：令牌更大（更新一代）→ 接受写入。
func TestSinkAcceptsFreshFence(t *testing.T) {
	sink := newMemSink()
	ctx := context.Background()
	called := false

	err := sink.WriteFenced(ctx, "res-a", 5, func(context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("更新的令牌应被接受: %v", err)
	}
	if !called {
		t.Fatal("写入体应被执行")
	}
	if sink.writesOf("res-a") != 1 {
		t.Fatalf("应写入 1 次，实得 %d", sink.writesOf("res-a"))
	}
}

// TestSinkAcceptsEqualFence 钉住：**等于**已见最大令牌 → **接受**。
//
// 这是比较口径的关键一条：只有**更小**才是陈旧。
// 误用形态：把「小于等于」当成陈旧判据 → 同一代持有者的多次写入
// （如分批提交）被拒，动作卡在中途。
func TestSinkAcceptsEqualFence(t *testing.T) {
	sink := newMemSink()
	ctx := context.Background()

	if err := sink.WriteFenced(ctx, "res-a", 5, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	// 同一代再写一次 → 必须被接受。
	called := false
	if err := sink.WriteFenced(ctx, "res-a", 5, func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("同代令牌应被接受: %v", err)
	}
	if !called {
		t.Fatal("同代令牌的写入体应被执行")
	}
	if sink.writesOf("res-a") != 2 {
		t.Fatalf("应写入 2 次，实得 %d", sink.writesOf("res-a"))
	}
}

// TestSinkRejectsStaleFence 钉住：令牌更小 → 拒收为 ErrFenceStale，
// 且**没有发生任何写入**。
//
// 误用形态：拒收但仍执行了写入体 → 陈旧写入照样落库，栅栏形同虚设。
func TestSinkRejectsStaleFence(t *testing.T) {
	sink := newMemSink()
	ctx := context.Background()

	// 先让资源见过令牌 5。
	if err := sink.WriteFenced(ctx, "res-a", 5, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	before := sink.writesOf("res-a")

	// 令牌 3（过期持有者）→ 必须被拒。
	called := false
	err := sink.WriteFenced(ctx, "res-a", 3, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrFenceStale) {
		t.Fatalf("陈旧令牌应被拒为 ErrFenceStale，实得 %v", err)
	}
	if called {
		t.Fatal("陈旧写入的写入体不该被执行")
	}
	if sink.writesOf("res-a") != before {
		t.Fatalf("陈旧写入不该改变写入计数：before=%d after=%d", before, sink.writesOf("res-a"))
	}
}

// TestSinkRejectsStaleFenceAfterFreshWrite 钉住「顺序无关」的陈旧拒收：
// **新令牌先写、旧令牌后写**时，旧那次必须被拒，且已见最大令牌仍是新值。
//
// 这条与上面的并发用例互补：并发用例里两个 goroutine 的先后**不确定**，
// 因此它可能只覆盖到「旧先新后」或「新先旧后」其中一种。
// 本条**确定性地**覆盖「新先旧后」——
// 正是这个顺序能暴露「已见最大令牌不取最大（旧的覆盖新的）」那种实现缺陷：
// 若实现无条件 `seen = presented`，旧那次虽被拒，但若它仍更新了 seen，
// 已见最大令牌就会从 9 掉回 3，下一次持令牌 5 的有效写入会被误判成陈旧。
func TestSinkRejectsStaleFenceAfterFreshWrite(t *testing.T) {
	sink := newMemSink()
	ctx := context.Background()

	// 先用令牌 9 写。
	if err := sink.WriteFenced(ctx, "res-a", 9, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("令牌 9 的写入失败: %v", err)
	}
	// 再用令牌 3 写 → 必须被拒。
	err := sink.WriteFenced(ctx, "res-a", 3, func(context.Context) error { return nil })
	if !errors.Is(err, ErrFenceStale) {
		t.Fatalf("令牌 3 应被拒为 ErrFenceStale，实得 %v", err)
	}
	// 关键：已见最大令牌必须仍是 9（不能掉回 3）。
	if got := sink.seenOf("res-a"); got != 9 {
		t.Fatalf("已见最大令牌应仍为 9，实得 %d（掉回去了 = 陈旧写入污染了它）", got)
	}
	// 且此后持令牌 9 的写入仍应被接受（没被误判成陈旧）。
	if err := sink.WriteFenced(ctx, "res-a", 9, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("被拒的陈旧写入不该影响后续合法写入: %v", err)
	}
}

// TestNonAtomicSinkIsCaught 钉住「原子性」这条契约要求**不是空话**。
//
// 它用一个三步实现（读已见最大 → 判断 → 再写入）的反面教材替身，
// **确定性地**证明：同样的并发场景下，非原子实现会让更旧的那次写入后落。
//
// 确定性从哪来：替身里 `beforeWrite` 钩子在「读已见最大」之后、
// 「判断」之前卡住——两个持有者因此**必然**都读到同一个旧值 seen=0，
// 双双通过检查。然后令牌 5 那次后落，把令牌 9 的结果覆盖成旧的。
//
// 这条用例的价值：它把「为什么必须原子」从散文变成**可执行的事实**。
// 没有它，「原子性」只是一句要求；有了它，任何人想把实现改成三步
// （为了性能或为了简单）都会看到具体后果——旧数据覆盖新数据。
func TestNonAtomicSinkIsCaught(t *testing.T) {
	sink := newRacySink()
	ctx := context.Background()

	// 第一次写入：令牌 9。钩子在此卡住，让第二次写入插进来读到 seen=0。
	entered := make(chan struct{})
	release := make(chan struct{})
	// 用原子标志而非 sync.Once：**Once.Do 会阻塞后续调用者**，
	// 而第二次写入必须能穿过钩子（否则与主流程互等 → 死锁）。
	var blocked atomic.Bool
	sink.beforeRead = func() {
		if blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}

	var wg sync.WaitGroup
	var errOld error
	wg.Add(1)
	go func() {
		defer wg.Done()
		// 令牌 5（更旧的那次）先开始，卡在读之后。
		errOld = sink.WriteFenced(ctx, "res-a", 5, func(context.Context) error { return nil })
	}()

	<-entered // 等令牌 5 读完 seen（=0）并卡住

	// 此时令牌 9 那次执行：它也读到 seen=0（因为令牌 5 还没写回），
	// 于是**也通过检查**——三步实现的破绽。
	errNew := sink.WriteFenced(ctx, "res-a", 9, func(context.Context) error { return nil })
	close(release)
	wg.Wait()

	// 关键断言：更旧的令牌 5 竟然通过了检查（没有被拒）——
	// 这正是非原子实现的破绽：两次都读到 seen=0，双双通过。
	if errOld != nil {
		t.Fatalf("三步实现下旧的令牌那次竟被拒了（竞态未复现，替身构造有问题）: %v", errOld)
	}
	if errNew != nil {
		t.Fatalf("三步实现下新令牌那次失败: %v", errNew)
	}
	// 且更旧的那次**后落** —— 旧数据覆盖新数据。
	sink.mu.Lock()
	order := append([]Fence(nil), sink.order...)
	sink.mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("应落两次，实得 %d", len(order))
	}
	if order[1] != 5 {
		t.Fatalf("后落的应是更旧的令牌 5（覆盖新数据），实得 %d（顺序=%v）", order[1], order)
	}
}

// TestSinkRejectsConcurrentStaleWrite 钉住**原子性**这条要求：
// 两次写入并发时，更旧的那次**不能后落覆盖**更新的那次。
//
// 契约要求「比较与写入在同一次原子操作里」，故正确实现下
// 旧令牌那次必然被拒。
//
// 与 TestNonAtomicSinkIsCaught 互补：那条证明三步实现**确实会坏**，
// 这条证明符合契约的实现**不会坏**。两者合起来才是完整证据。
func TestSinkRejectsConcurrentStaleWrite(t *testing.T) {
	sink := newMemSink()
	ctx := context.Background()

	// 先见过令牌 5。
	if err := sink.WriteFenced(ctx, "res-a", 5, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}

	// 并发：令牌 9（新）与令牌 3（旧）同时写。
	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0] = sink.WriteFenced(ctx, "res-a", 9, func(context.Context) error { return nil })
	}()
	go func() {
		defer wg.Done()
		results[1] = sink.WriteFenced(ctx, "res-a", 3, func(context.Context) error { return nil })
	}()
	wg.Wait()

	// 令牌 3 那次必须被拒（无论谁先谁后）。
	if !errors.Is(results[1], ErrFenceStale) {
		t.Fatalf("并发下旧令牌那次应被拒为 ErrFenceStale，实得 %v", results[1])
	}
	// 令牌 9 那次应成功。
	if results[0] != nil {
		t.Fatalf("并发下新令牌那次应成功，实得 %v", results[0])
	}
	// 最终已见最大令牌是 9（不是 3）。
	if got := sink.seenOf("res-a"); got != 9 {
		t.Fatalf("已见最大令牌应为 9，实得 %d（旧的覆盖了新的 = 原子性破了）", got)
	}
}
