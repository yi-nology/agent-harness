package sideeffect_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/sidetest"
)

// TestSinkContract 内存替身接**共享栅栏契约套件**。
//
// 判据本体在 sidetest/sink.go（fresh / equal / stale 不写 / 新先旧后保最大 /
// 并发陈旧被拒 / 写入体失败不部分写，共 6 条）——判据只有一份，内存替身与
// examples/pgstore 的 PostgreSQL 下游跑的是同一段断言。
func TestSinkContract(t *testing.T) {
	sidetest.RunSinkContract(t, func(*testing.T) sidetest.SinkProbe {
		return newMemSink()
	})
}

// TestNonAtomicSinkIsCaught 钉住「原子性」这条契约要求**不是空话**。
//
// 它用一个三步实现（读已见最大 → 判断 → 再写入）的反面教材替身，
// **确定性地**证明：同样的并发场景下，非原子实现会让更旧的那次写入后落。
//
// 确定性从哪来：替身里 `beforeRead` 钩子在「读已见最大」之后、
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
	order := append([]sideeffect.Fence(nil), sink.order...)
	sink.mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("应落两次，实得 %d", len(order))
	}
	if order[1] != 5 {
		t.Fatalf("后落的应是更旧的令牌 5（覆盖新数据），实得 %d（顺序=%v）", order[1], order)
	}
}
