package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是**结果判据**一族的契约场景（幂等键 / 栅栏 / 资源独占）。
//
// 这一族判的是「这个动作做过没有、该不该做」——与 renew.go（资格）、
// uncertain.go（结果未知）、intent.go（未裁决的尝试）是四族不同的语义。
//
// 为什么要拆出来单放一文件：本仓的结构阈值是单文件 400 行，
// 四族场景都堆在一个文件里必然超限，而超限的债「上调阈值」比「拆分」贵——
// 拆开之后每一族各自可读，且行数天然分散。

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

// runTakeoverAfterExpiry 钉住：**资源**上的租约过期后可被接管，
// 且**新令牌更大**。
//
// 失效形态：过期后接管但令牌不递增 → 过期持有者的迟到写入认不出是过期的。
//
// ⚠️ 接管者用**另一个 Key**：同一个 Key 在租约过期后走的是「执行意图」
// 那条判据（转入需对账态，而不是重新授予执行权）——见 intent.go。
// 这里判的是**资源级**的接管。
func runTakeoverAfterExpiry(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	clock.Advance(2 * time.Minute)

	outcome, lease2, _, err := st.Acquire(ctx, act("k2", "res-a"), time.Minute)
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
//
// ⚠️ 每轮用**不同的 Key**：同一个 Key 的执行意图在租约过期后会把它转入
// 需对账态（v0.33.0 新判据），那不是资源级的令牌单调性。
func runFenceMonotonic(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()

	var prev sideeffect.Fence
	for i := range 3 {
		outcome, lease, _, err := st.Acquire(ctx, act("k"+string(rune('a'+i)), "res-a"), time.Minute)
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
	// 接管者用另一个 Key（资源级接管 ⇒ 栅栏令牌前进一位）。
	second := act("k2", "res-a")
	_, lease2, _, err := st.Acquire(ctx, second, time.Minute)
	if err != nil {
		t.Fatalf("接管 Acquire 失败: %v", err)
	}

	_, err = st.Complete(ctx, a, lease1, sideeffect.Result{Payload: []byte("stale")})
	if !errors.Is(err, sideeffect.ErrFenceStale) {
		t.Fatalf("过期持有者的 Complete 应被拒为 ErrFenceStale，实得 %v", err)
	}
	if _, err := st.Complete(ctx, second, lease2, sideeffect.Result{Payload: []byte("fresh")}); err != nil {
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
	if _, _, _, err := st.Acquire(ctx, act("k2", "res-a"), time.Minute); err != nil {
		t.Fatalf("接管 Acquire 失败: %v", err)
	}

	if err := st.Release(ctx, a, lease1); !errors.Is(err, sideeffect.ErrFenceStale) {
		t.Fatalf("过期持有者的 Release 应被拒为 ErrFenceStale，实得 %v", err)
	}
	// 新持有者的闸必须仍在：再抢应仍是 contended。
	outcome, _, _, err := st.Acquire(ctx, act("k3", "res-a"), time.Minute)
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
//
// v0.33.0 起这条判据还多钉一层：Release 只放**资源闸**，**不撤销执行意图**
// ⇒ 同 Key 再来不是 replayed（没被标成做过），但也不是 applied
// （这次尝试尚未裁决，不得重新施加）。要让该 Key 重新可执行，
// 得显式放弃（Abandon）——见 intent.go 的 abandon_allows_retry。
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

	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome == sideeffect.OutcomeReplayed {
		t.Fatalf("释放不该把动作标记为已完成（replayed），实得 %v", outcome)
	}
	if outcome != sideeffect.OutcomeContended {
		t.Fatalf("释放只放闸、不撤销执行意图：同 Key 再来应 contended，实得 %v", outcome)
	}
}
