// Package sidetest 副作用闸（sideeffect.Store）的**共享契约测试套件**。
//
// # 为什么需要它
//
// sideeffect.Store 的正确性无法靠「看起来对」保证：它要跨进程、靠一次原子
// 操作维持三个判据的联合，而**每一家宿主都会自己写一遍存储实现**。
// 各写一遍必然各判一套，语义漂移开到没人发现为止——这正是本仓
// 「跨包契约的规则只能有一份，且要有端到端用例」那条纪律要防的事。
//
// 套件把「宿主实现必须满足什么」固化成**一组可复跑的子测试**：
// 宿主写完存储实现后，把本套件接进自己的测试里跑一遍，
// 违反哪条契约就红在哪一条，不必等人肉 review 发现。
//
// 范式沿用上游 `oekit/pkg/cache/cachetest` 与 `pkg/singleflighttest`
// （同一家族的做法）：共享套件 + **硬失败，不静默跳过**。
//
// # 为什么是「硬失败」
//
// 套件入口一律 `t.Fatalf` 而不是 `t.Skip`。理由：调用方**显式接入**套件
// 就是在声明「我这个实现要满足副作用闸的契约」——此时跳过一个场景，
// 等于把「我没测过」伪装成「我通过了」。这与本仓反复出现的教训同型：
// **静默跳过是最贵的失败形态**，因为它让未验证的东西看起来像已验证。
//
// # 覆盖了什么
//
// 套件覆盖的是**跨实现必须一致**的语义（每条子测试对应一个真实失效形态）：
//
//  1. apply_once            同一 Key 第二次 Acquire → replayed，且取回同一结果
//  2. completed_wins        已完成优先于「资源正忙」（不因占用而看不到已完成）
//  3. contended_while_held  租约未过期时，第二个持有者拿到 contended
//  4. takeover_after_expiry 租约过期后可被接管，且新令牌更大
//  5. fence_monotonic       同一 Resource 上令牌单调递增，不回退
//  6. stale_complete_rejected 过期持有者的 Complete 被拒（ErrFenceStale）
//  7. stale_release_rejected  过期持有者的 Release 被拒（不能放掉新持有者的闸）
//  8. release_does_not_done  Release 不把 Key 标记为已完成
//
// 另有两场景在 **renew.go**（续期一族，与「结果判据」是不同语义）：
//
//  9. renew_extends_lease             Renew 延长有效期且不换令牌
//  10. renew_after_takeover_rejected   闸已易主后 Renew 被拒（假续期的封口）
//
// # 时钟
//
// 租约过期需要可控时钟，否则只能靠 sleep（慢且 flaky）。
// 套件提供 FakeClock；实现须通过 ClockedStore 接口暴露「用哪个时钟」，
// 且**过期判定必须读这个时钟**（不是 time.Now）——
// 否则套件的过期场景测的是真实时间，恒不触发，套件就成了一组空断言。
package sidetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/agent-harness/sideeffect"
)

// FakeClock 套件提供的假时钟（并发安全；测试内手动推进）。
//
// 取非零真实量级时刻，避开零值 time.Time 的「无过期」歧义。
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock 创建起点为 2026-01-01T00:00:00Z 的假时钟。
func NewFakeClock() *FakeClock {
	return &FakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now 返回当前假时刻。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance 把假时钟前进 d。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ClockedStore 实现须提供的能力：把时钟注入到存储实现里。
//
// **为什么单独一个接口**：过期判定是存储实现内部的事（它自己知道
// 「租约还在不在」）。套件不能替它判，只能要求它**用可注入的时钟**。
// 没实现这个接口 → 套件硬失败（而不是跳过过期场景——跳过等于假绿）。
type ClockedStore interface {
	sideeffect.Store
	// UseClock 指定存储实现读时钟的方式（通常是注入一个 func() time.Time）。
	UseClock(now func() time.Time)
}

// Factory 构造被测存储实现。
//
// factory 每个子测试调用一次，构造一份全新实现，并把套件的假时钟注入它。
// 实现的后台资源由 factory 经 t.Cleanup 清理。
type Factory func(t *testing.T, clock *FakeClock) ClockedStore

// Run 跑完整契约场景（8 个子测试，任何一个失败即实现违反契约）。
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("apply_once", func(t *testing.T) { runApplyOnce(t, factory) })
	t.Run("completed_wins", func(t *testing.T) { runCompletedWins(t, factory) })
	t.Run("contended_while_held", func(t *testing.T) { runContendedWhileHeld(t, factory) })
	t.Run("takeover_after_expiry", func(t *testing.T) { runTakeoverAfterExpiry(t, factory) })
	t.Run("fence_monotonic", func(t *testing.T) { runFenceMonotonic(t, factory) })
	t.Run("stale_complete_rejected", func(t *testing.T) { runStaleCompleteRejected(t, factory) })
	t.Run("stale_release_rejected", func(t *testing.T) { runStaleReleaseRejected(t, factory) })
	t.Run("release_does_not_done", func(t *testing.T) { runReleaseDoesNotDone(t, factory) })
	t.Run("renew_extends_lease", func(t *testing.T) { runRenewExtendsLease(t, factory) })
	t.Run("renew_after_takeover_rejected", func(t *testing.T) { runRenewAfterTakeoverRejected(t, factory) })
}

// newStore 构造被测实现并注入假时钟。
func newStore(t *testing.T, factory Factory) (sideeffect.Store, *FakeClock) {
	t.Helper()
	clock := NewFakeClock()
	st := factory(t, clock)
	if st == nil {
		t.Fatalf("factory 返回了 nil 实现")
	}
	st.UseClock(clock.Now)
	return st, clock
}

// act 构造一个测试动作（合成数据，不含任何领域词面）。
func act(key, resource string) sideeffect.Action {
	return sideeffect.Action{Key: sideeffect.Key(key), Resource: resource, Payload: []byte("p")}
}

// runApplyOnce 钉住：同一 Key 第二次 Acquire → replayed，且取回**同一**结果。
//
// 失效形态：实现把「已完成」当成「没做过」→ 同一动作被施加两次，
// 而这正是本包存在的全部理由。
func runApplyOnce(t *testing.T, factory Factory) {
	st, _ := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	outcome, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		t.Fatalf("首次 Acquire 应为 applied，实得 %v", outcome)
	}
	want := sideeffect.Result{Payload: []byte("v1")}
	if _, err := st.Complete(ctx, a, lease, want); err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}

	outcome2, _, got, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("二次 Acquire 失败: %v", err)
	}
	if outcome2 != sideeffect.OutcomeReplayed {
		t.Fatalf("同一 Key 二次 Acquire 应为 replayed，实得 %v", outcome2)
	}
	if string(got.Payload) != "v1" {
		t.Fatalf("应取回既有结果 v1，实得 %q", got.Payload)
	}
}

// runCompletedWins 钉住：已完成**优先于**「资源正忙」。
//
// 失效形态：优先级写反 → 调用方把「早就做完了」误判成「没轮上我」。
func runCompletedWins(t *testing.T, factory Factory) {
	st, _ := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if _, err := st.Complete(ctx, a, lease, sideeffect.Result{Payload: []byte("v")}); err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	// 同一资源上另开一个不同 Key 的持有（模拟资源正忙）。
	other := act("k2", "res-a")
	if _, _, _, err := st.Acquire(ctx, other, time.Minute); err != nil {
		t.Fatalf("另一 Key 的 Acquire 失败: %v", err)
	}

	outcome, _, got, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		t.Fatalf("已完成应优先返回 replayed（哪怕资源正忙），实得 %v", outcome)
	}
	if string(got.Payload) != "v" {
		t.Fatalf("应取回既有结果 v，实得 %q", got.Payload)
	}
}

// runContendedWhileHeld 钉住：租约未过期时，第二个持有者拿到 contended
// 且**不发放令牌**（发了令牌就等于承认了它）。
func runContendedWhileHeld(t *testing.T, factory Factory) {
	st, _ := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	outcome, lease, _, err := st.Acquire(ctx, act("k2", "res-a"), time.Minute)
	if err != nil {
		t.Fatalf("二次 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeContended {
		t.Fatalf("租约未过期时第二个持有者应为 contended，实得 %v", outcome)
	}
	if lease.Fence != 0 {
		t.Fatalf("被拒时不该发放令牌（发了等于承认它），实得 Fence=%d", lease.Fence)
	}
}

// runTakeoverAfterExpiry 钉住：租约过期后可被接管，且**新令牌更大**。
//
// 失效形态：过期后接管但令牌不递增 → 过期持有者的迟到写入认不出是过期的。
func runTakeoverAfterExpiry(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	clock.Advance(2 * time.Minute)

	outcome, lease2, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("过期后 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		t.Fatalf("租约过期后应可接管（applied），实得 %v", outcome)
	}
	if lease2.Fence <= lease1.Fence {
		t.Fatalf("接管令牌应更大：old=%d new=%d", lease1.Fence, lease2.Fence)
	}
}

// runFenceMonotonic 钉住：同一 Resource 上令牌单调递增，不回退。
func runFenceMonotonic(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()

	var prev sideeffect.Fence
	for i := range 3 {
		outcome, lease, _, err := st.Acquire(ctx, act("k", "res-a"), time.Minute)
		if err != nil {
			t.Fatalf("第 %d 轮 Acquire 失败: %v", i, err)
		}
		if outcome != sideeffect.OutcomeApplied {
			t.Fatalf("第 %d 轮应 granted，实得 %v", i, outcome)
		}
		if lease.Fence <= prev {
			t.Fatalf("第 %d 轮令牌应严格递增：prev=%d cur=%d", i, prev, lease.Fence)
		}
		prev = lease.Fence
		clock.Advance(2 * time.Minute)
	}
}

// runStaleCompleteRejected 钉住：过期持有者的 Complete 被拒。
//
// 这是「更新的状态被旧状态覆盖」那道洞的直接封口。
func runStaleCompleteRejected(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	clock.Advance(2 * time.Minute)
	_, lease2, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("接管 Acquire 失败: %v", err)
	}

	_, err = st.Complete(ctx, a, lease1, sideeffect.Result{Payload: []byte("stale")})
	if !errors.Is(err, sideeffect.ErrFenceStale) {
		t.Fatalf("过期持有者的 Complete 应被拒为 ErrFenceStale，实得 %v", err)
	}
	if _, err := st.Complete(ctx, a, lease2, sideeffect.Result{Payload: []byte("fresh")}); err != nil {
		t.Fatalf("当前持有者的 Complete 应被接受: %v", err)
	}
}

// runStaleReleaseRejected 钉住：过期持有者的 Release 被拒
// （不能放掉新持有者的闸）。
func runStaleReleaseRejected(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	clock.Advance(2 * time.Minute)
	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		t.Fatalf("接管 Acquire 失败: %v", err)
	}

	if err := st.Release(ctx, a, lease1); !errors.Is(err, sideeffect.ErrFenceStale) {
		t.Fatalf("过期持有者的 Release 应被拒为 ErrFenceStale，实得 %v", err)
	}
	// 新持有者的闸必须仍在：再抢应仍是 contended。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeContended {
		t.Fatalf("新持有者的闸应仍在（未被过期持有者放掉），实得 %v", outcome)
	}
}

// runReleaseDoesNotDone 钉住：Release 不把 Key 标记为已完成。
//
// 失效形态：Release 时顺手标完成 → 动作被记成做过，实际没做，
// 后续谁都不会再做它。
func runReleaseDoesNotDone(t *testing.T, factory Factory) {
	st, _ := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if err := st.Release(ctx, a, lease); err != nil {
		t.Fatalf("Release 失败: %v", err)
	}

	// 动作仍可被施加（未被标成做过）。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		t.Fatalf("释放后应仍能获得执行权（未被标成已完成），实得 %v", outcome)
	}
}
