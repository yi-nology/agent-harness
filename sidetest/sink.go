// sink.go —— 下游栅栏（sideeffect.FencedSink）的**共享契约测试套件**。
//
// # 判据从哪来
//
// 本套件的六条场景就是把 sideeffect 包内 `sink_contract_test.go` 对内存替身
// 的判据收编成可复跑的共享形态（与 Store 一族的 Run 同一范式）：判据**只有
// 一份**，内存替身与 PostgreSQL 参考实现（examples/pgstore）跑的是同一段
// 断言——宿主的 FencedSink 实现也接它，不另造一套。
//
// # 覆盖了什么
//
//  1. fresh_accepted             令牌更大（更新一代）→ 接受写入
//  2. equal_accepted             **等于**已见最大 → 接受（只有更小才是陈旧；
//     「小于等于当陈旧」会把分批提交卡死在中途）
//  3. stale_rejected_no_write    令牌更小 → 拒收 ErrFenceStale，且**没有
//     发生任何写入**（写入体不执行、无部分写）
//  4. stale_after_fresh_keeps_max 新令牌先写、旧令牌后写 → 旧的那次被拒，
//     且已见最大令牌**仍是新值**（陈旧写不得污染它）
//  5. concurrent_stale_rejected  并发下更旧的那次**不能后落覆盖**更新的那次
//  6. write_error_no_partial_write 写入体报错 → 整体不落地（预占与标记一起回滚）
//
// 第 3 与第 6 条合起来才是完整的「不得部分写」：陈旧拒收不得写，
// 写入体失败也不得写——栅栏预占与业务数据要么都落地、要么都不落地。
//
// # 原子性是判据，不是注释
//
// 场景 5 用并发撞「比较与写入不在同一次原子操作」的窗；上游
// sideeffect 包内的 TestNonAtomicSinkIsCaught 用钩子替身**确定性**证明
// 三步实现会坏（替身专属，收不进通用套件）——两者互补，缺一不可。
package sidetest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// SinkProbe 套件要求的能力：FencedSink 契约 + 两个观测面。
//
// 观测面只服务判据（读已见最大 / 制造并读取一次参考业务写入），
// 不要求实现方暴露任何内部结构。
type SinkProbe interface {
	sideeffect.FencedSink
	// SeenFence 返回该资源已见过的最大令牌（无记录返回 0）。
	SeenFence(ctx context.Context, resource string) (sideeffect.Fence, error)
	// WriteMarker 在**写入体里**执行一次参考业务写入（与栅栏预占同一原子面）。
	// 供「不得部分写」场景使用：先写标记再报错，断言标记与预占一起消失。
	WriteMarker(ctx context.Context, resource string) error
	// Marker 读取已持久化的参考业务写入（无记录返回空串）。
	Marker(ctx context.Context, resource string) (string, error)
}

// SinkFactory 构造被测下游实现；每个子测试调一次，构造全新实现。
type SinkFactory func(t *testing.T) SinkProbe

// RunSinkContract 跑完整栅栏契约场景（6 个子测试，任一失败即实现违反契约）。
func RunSinkContract(t *testing.T, factory SinkFactory) {
	t.Helper()

	t.Run("fresh_accepted", func(t *testing.T) { runSinkFreshAccepted(t, factory) })
	t.Run("equal_accepted", func(t *testing.T) { runSinkEqualAccepted(t, factory) })
	t.Run("stale_rejected_no_write", func(t *testing.T) { runSinkStaleRejectedNoWrite(t, factory) })
	t.Run("stale_after_fresh_keeps_max", func(t *testing.T) { runSinkStaleAfterFreshKeepsMax(t, factory) })
	t.Run("concurrent_stale_rejected", func(t *testing.T) { runSinkConcurrentStaleRejected(t, factory) })
	t.Run("write_error_no_partial_write", func(t *testing.T) { runSinkWriteErrorNoPartialWrite(t, factory) })
}

// newSink 构造被测实现。
func newSink(t *testing.T, factory SinkFactory) SinkProbe {
	t.Helper()
	p := factory(t)
	if p == nil {
		t.Fatalf("factory 返回了 nil 实现")
	}
	return p
}

// sinkExec 套件的执行体计数器（并发场景安全）。
type sinkExec struct {
	n atomic.Int32
}

// callback 返回一个计数写入体。
func (e *sinkExec) callback() func(context.Context) error {
	return func(context.Context) error { e.n.Add(1); return nil }
}

// checkSinkFreshAccepted 钉住：令牌更大（更新一代）→ 接受写入。
func checkSinkFreshAccepted(p SinkProbe) error {
	ctx := context.Background()
	var e sinkExec
	if err := p.WriteFenced(ctx, "res-a", 5, e.callback()); err != nil {
		return errors.Wrap(err, "更新的令牌应被接受")
	}
	if e.n.Load() != 1 {
		return fmt.Errorf("写入体应被执行 1 次，实得 %d", e.n.Load())
	}
	seen, err := p.SeenFence(ctx, "res-a")
	if err != nil {
		return err
	}
	if seen != 5 {
		return fmt.Errorf("已见最大令牌应为 5，实得 %d", seen)
	}
	return nil
}

// checkSinkEqualAccepted 钉住：**等于**已见最大令牌 → **接受**。
//
// 比较口径的关键一条：只有**更小**才是陈旧。误用「小于等于当陈旧」，
// 同一代持有者的多次写入（如分批提交）会被拒，动作卡在中途。
func checkSinkEqualAccepted(p SinkProbe) error {
	ctx := context.Background()
	var e sinkExec
	if err := p.WriteFenced(ctx, "res-a", 5, e.callback()); err != nil {
		return errors.Wrap(err, "首次写入失败")
	}
	if err := p.WriteFenced(ctx, "res-a", 5, e.callback()); err != nil {
		return errors.Wrap(err, "同代令牌应被接受")
	}
	if e.n.Load() != 2 {
		return fmt.Errorf("同代令牌的写入体应再执行一次（共 2 次），实得 %d", e.n.Load())
	}
	return nil
}

// checkSinkStaleRejectedNoWrite 钉住：令牌更小 → 拒收为 ErrFenceStale，
// 且**没有发生任何写入**（写入体不执行、标记不落、已见最大不动）。
//
// 误用形态：拒收但仍执行了写入体 → 陈旧写入照样落库，栅栏形同虚设。
func checkSinkStaleRejectedNoWrite(p SinkProbe) error {
	ctx := context.Background()
	if err := p.WriteFenced(ctx, "res-a", 5, func(context.Context) error { return nil }); err != nil {
		return errors.Wrap(err, "首次写入失败")
	}
	executed := false
	err := p.WriteFenced(ctx, "res-a", 3, func(context.Context) error {
		executed = true
		return p.WriteMarker(ctx, "res-a")
	})
	if !errors.Is(err, sideeffect.ErrFenceStale) {
		return fmt.Errorf("陈旧令牌应被拒为 ErrFenceStale，实得 %v", err)
	}
	if executed {
		return errors.New("陈旧写入的写入体不该被执行")
	}
	marker, err := p.Marker(ctx, "res-a")
	if err != nil {
		return err
	}
	if marker != "" {
		return fmt.Errorf("陈旧写入不得部分写：标记竟已落盘 %q", marker)
	}
	seen, err := p.SeenFence(ctx, "res-a")
	if err != nil {
		return err
	}
	if seen != 5 {
		return fmt.Errorf("已见最大令牌应仍为 5，实得 %d", seen)
	}
	return nil
}

// checkSinkStaleAfterFreshKeepsMax 钉住「顺序无关」的陈旧拒收：
// **新令牌先写、旧令牌后写**时，旧那次必须被拒，且已见最大令牌仍是新值。
//
// 这条确定性地覆盖「新先旧后」——正是它暴露「已见最大不取最大（旧的
// 覆盖新的）」的实现缺陷：若实现无条件 seen = presented，已见最大会从 9
// 掉回 3，下一次持令牌 9 的有效写入会被误判成陈旧。
func checkSinkStaleAfterFreshKeepsMax(p SinkProbe) error {
	ctx := context.Background()
	if err := p.WriteFenced(ctx, "res-a", 9, func(context.Context) error { return nil }); err != nil {
		return errors.Wrap(err, "令牌 9 的写入失败")
	}
	if err := p.WriteFenced(ctx, "res-a", 3, func(context.Context) error { return nil }); !errors.Is(err, sideeffect.ErrFenceStale) {
		return fmt.Errorf("令牌 3 应被拒为 ErrFenceStale，实得 %v", err)
	}
	seen, err := p.SeenFence(ctx, "res-a")
	if err != nil {
		return err
	}
	if seen != 9 {
		return fmt.Errorf("已见最大令牌应仍为 9，实得 %d（掉回去了 = 陈旧写入污染了它）", seen)
	}
	if err := p.WriteFenced(ctx, "res-a", 9, func(context.Context) error { return nil }); err != nil {
		return errors.Wrap(err, "被拒的陈旧写入不该影响后续合法写入")
	}
	return nil
}

// checkSinkConcurrentStaleRejected 钉住**原子性**：
// 两次写入并发时，更旧的那次**不能后落覆盖**更新的那次。
//
// 契约要求「比较与写入在同一次原子操作里」，故正确实现下旧令牌那次必然被拒。
// 与 sideeffect 包内的 TestNonAtomicSinkIsCaught（三步反面教材）互补：
// 那条证明三步实现确实会坏，这条证明符合契约的实现不会坏。
func checkSinkConcurrentStaleRejected(p SinkProbe) error {
	ctx := context.Background()
	if err := p.WriteFenced(ctx, "res-a", 5, func(context.Context) error { return nil }); err != nil {
		return errors.Wrap(err, "首次写入失败")
	}
	var wg sync.WaitGroup
	var errFresh, errStale error
	wg.Add(2)
	// 走 async.GoSafe 而非裸 go func：本仓门禁禁止裸 goroutine
	// （panic 无法被外层 recover 捕获，会直接杀掉宿主进程）。
	async.GoSafe(func() {
		defer wg.Done()
		errFresh = p.WriteFenced(ctx, "res-a", 9, func(context.Context) error { return nil })
	})
	async.GoSafe(func() {
		defer wg.Done()
		errStale = p.WriteFenced(ctx, "res-a", 3, func(context.Context) error { return nil })
	})
	wg.Wait()
	if !errors.Is(errStale, sideeffect.ErrFenceStale) {
		return fmt.Errorf("并发下旧令牌那次应被拒为 ErrFenceStale，实得 %v", errStale)
	}
	if errFresh != nil {
		return errors.Wrap(errFresh, "并发下新令牌那次应成功")
	}
	seen, err := p.SeenFence(ctx, "res-a")
	if err != nil {
		return err
	}
	if seen != 9 {
		return fmt.Errorf("已见最大令牌应为 9，实得 %d（旧的覆盖了新的 = 原子性破了）", seen)
	}
	return nil
}

// errSinkBoom 「不得部分写」场景用的写入体故障。
var errSinkBoom = errors.New("写入体注入的故障")

// checkSinkWriteErrorNoPartialWrite 钉住：写入体报错 → 整体不落地。
//
// 失效形态：栅栏预占成功、写入体半途报错，预占却留在库里——已见最大令牌
// 被抬高而业务数据没写，后续这一代的合法写入反而被判「等于，合法」，
// 实际上什么都没写进去。预占与业务数据要么都在，要么都不在。
func checkSinkWriteErrorNoPartialWrite(p SinkProbe) error {
	ctx := context.Background()
	if err := p.WriteFenced(ctx, "res-a", 5, func(context.Context) error { return nil }); err != nil {
		return errors.Wrap(err, "首次写入失败")
	}
	err := p.WriteFenced(ctx, "res-a", 7, func(ctx context.Context) error {
		if mErr := p.WriteMarker(ctx, "res-a"); mErr != nil {
			return mErr
		}
		return errSinkBoom
	})
	if !errors.Is(err, errSinkBoom) {
		return fmt.Errorf("写入体的故障应原样透出，实得 %v", err)
	}
	marker, mErr := p.Marker(ctx, "res-a")
	if mErr != nil {
		return mErr
	}
	if marker != "" {
		return fmt.Errorf("写入体失败后不得部分写：标记竟已落盘 %q", marker)
	}
	seen, err := p.SeenFence(ctx, "res-a")
	if err != nil {
		return err
	}
	if seen != 5 {
		return fmt.Errorf("写入体失败后预占必须一起回滚：已见最大应仍为 5，实得 %d", seen)
	}
	return nil
}

// ── run 包装：把判据 error 变成子测试红（与结果判据一族同一形态）─────────

func runSinkFreshAccepted(t *testing.T, factory SinkFactory) {
	t.Helper()
	if err := checkSinkFreshAccepted(newSink(t, factory)); err != nil {
		t.Fatalf("%v", err)
	}
}

func runSinkEqualAccepted(t *testing.T, factory SinkFactory) {
	t.Helper()
	if err := checkSinkEqualAccepted(newSink(t, factory)); err != nil {
		t.Fatalf("%v", err)
	}
}

func runSinkStaleRejectedNoWrite(t *testing.T, factory SinkFactory) {
	t.Helper()
	if err := checkSinkStaleRejectedNoWrite(newSink(t, factory)); err != nil {
		t.Fatalf("%v", err)
	}
}

func runSinkStaleAfterFreshKeepsMax(t *testing.T, factory SinkFactory) {
	t.Helper()
	if err := checkSinkStaleAfterFreshKeepsMax(newSink(t, factory)); err != nil {
		t.Fatalf("%v", err)
	}
}

func runSinkConcurrentStaleRejected(t *testing.T, factory SinkFactory) {
	t.Helper()
	if err := checkSinkConcurrentStaleRejected(newSink(t, factory)); err != nil {
		t.Fatalf("%v", err)
	}
}

func runSinkWriteErrorNoPartialWrite(t *testing.T, factory SinkFactory) {
	t.Helper()
	if err := checkSinkWriteErrorNoPartialWrite(newSink(t, factory)); err != nil {
		t.Fatalf("%v", err)
	}
}
