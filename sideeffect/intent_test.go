package sideeffect

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// 本文件钉住 v0.33.0 新增的**执行意图**语义。
//
// # 这组用例在钉什么
//
// 钉的是一条**不该被绕过的红线**：同一个 Key 上有一次**尚未裁决**的尝试时，
// 不得再被授予执行权。
//
// 失效形态（此前真实存在，且**静默**）：进程在抢到闸之后、记账之前死掉——
// 存储侧既没有「完成」也没有「未知」，只有一把还在的（或刚过期的）闸。
// 下一次同一 Key 的 Acquire 把它当成「没做过」⇒ 重新授予执行权 ⇒
// 同一动作被施加两次，而调用方全程只拿到一个普通错误。
//
// 每条判据都成对写：**好实现能过** + **坏实现会红**。只有前者的话，
// 一个把判据整个写坏的实现照样全绿——而这条判据正是本轮改动的全部意义。

// TestExpiredIntentRequiresReconcile 钉住核心红线：
// 上一次尝试的租约过期且**未被裁决**时，同一 Key 不得再被执行。
//
// 这是「进程死在施加成功之后、记账之前」那条形态的封口。
func TestExpiredIntentRequiresReconcile(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	// 第一次：抢到闸，然后「进程死掉」——既没 Complete，也没 MarkUncertain，
	// 更没 Release（进程死了，什么都做不了）。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("首次 Acquire 应为 applied，实得 %v", outcome)
	}

	// 租约过期。
	st.advance(2 * time.Minute)

	// 第二次：同一 Key ⇒ 结果未知（不是 applied），且动作体不得被执行。
	calls := 0
	r := NewRunner(st, time.Minute)
	outcome, _, err = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("second")}, nil
	})
	if outcome != OutcomeUncertain {
		t.Fatalf("租约过期且未裁决时，同一 Key 应判 uncertain（须先对账），实得 %v", outcome)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("应可判结果未知，实得 %v", err)
	}
	if calls != 0 {
		t.Fatalf("未裁决的尝试不得被重新施加（否则就是重复施加），实得执行 %d 次", calls)
	}
}

// TestInFlightBlocksSameKey 钉住：租约**尚未过期**时，同一个 Key 不得被
// 第二次授予执行权。
//
// 这是「两个 worker 抢同一个逻辑动作」在 Key 级的那道门——
// 资源级那道门（闸）管不住「同一 Key、不同资源」的情形。
func TestInFlightBlocksSameKey(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	outcome, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("二次 Acquire 失败: %v", err)
	}
	if outcome != OutcomeContended {
		t.Fatalf("同一 Key 上一次尝试未裁决时，二次 Acquire 应为 contended，实得 %v", outcome)
	}
	if lease.Fence != 0 {
		t.Fatalf("被拒时不该发放令牌（发了等于承认它），实得 Fence=%d", lease.Fence)
	}
}

// TestAbandonAllowsRetry 钉住「显式放弃」的正面形态：
// 放弃之后该 Key 重新可被执行。
//
// 这是「未知态可解」的对称面：放弃是「本次确定没做」的另一种说法，
// 与对账裁决「未生效」等价，但它不需要下游事实——调用方自己知道。
func TestAbandonAllowsRetry(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if err := st.Abandon(ctx, a, lease, "准备阶段失败，什么都没做"); err != nil {
		t.Fatalf("放弃失败: %v", err)
	}

	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("放弃后 Acquire 失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("显式放弃后该 Key 应重新可执行，实得 %v", outcome)
	}
}

// TestSafeActionMayRetryAfterExpiry 钉住「声明了可安全重试」的那条出路：
// 租约过期后同一个 Key 仍可重新执行。
//
// 因为 Action.RetrySafety == RetrySafetySafe 断言的是「下游按幂等键去重，
// 重做不会重复施加」——这是宿主对**下游事实**的显式声明，不是内核的乐观假设。
func TestSafeActionMayRetryAfterExpiry(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")
	a.RetrySafety = RetrySafetySafe

	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	st.advance(2 * time.Minute)

	outcome, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("过期后 Acquire 失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("声明了可安全重试时，过期后应重新授予执行权，实得 %v", outcome)
	}
	if lease.Fence == 0 {
		t.Fatal("重新授予时必须发放令牌（下游据此拒收陈旧写入）")
	}
}

// TestAbandonRejectsForeignToken 钉住：放弃只认**本次尝试**的令牌。
//
// 失效形态：拿一枚不属于本次尝试的令牌撤销意图 ⇒ 本次尝试被凭空「放弃」
// ⇒ 那个 Key 重新变成「可被执行」⇒ 重复施加。
func TestAbandonRejectsForeignToken(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// 用一枚**不属于本次尝试**的令牌放弃 → 必须被拒。
	forged := lease
	forged.Fence = lease.Fence + 1
	if err := st.Abandon(ctx, a, forged, "伪造令牌"); !errors.Is(err, ErrFenceStale) {
		t.Fatalf("放弃只认本次尝试的令牌，伪造令牌应被拒为 ErrFenceStale，实得 %v", err)
	}
	// 意图仍在 ⇒ 这个 Key 仍被挡着（没被凭空「放弃」）。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != OutcomeContended {
		t.Fatalf("被拒的放弃不该撤销意图：同 Key 再来应 contended，实得 %v", outcome)
	}
}

// TestIntentJudgeIsSensitiveToBrokenStore 反向验证（负对照）：
// 把「Acquire 不查执行意图」这条判据摘掉的存储实现，**必须**被放行重试。
//
// 这条用例证明的是**判据有分辨力**：如果坏实现也被拦住，说明拦住它的
// 是别的东西（或者压根没拦住，只是用例写得太松），那么好实现那条用例
// 就成了空断言。
//
// 为什么值得单独钉：这条红线的失效形态是「静默地多施加一次」——
// 没有症状，只有后果。
func TestIntentJudgeIsSensitiveToBrokenStore(t *testing.T) {
	// 正常实现：过期后转入未知态，动作体不再执行。
	calls, outcome := retryAfterExpiry(t, newFakeStore())
	if calls != 0 || outcome != OutcomeUncertain {
		t.Fatalf("正常实现应拦住重试（判结果未知），实得 outcome=%v calls=%d", outcome, calls)
	}

	// 坏实现：Acquire 无视执行意图 ⇒ 重试被放行 ⇒ 副作用会被施加两遍。
	broken := &ignoresIntentStore{fakeStore: newFakeStore()}
	calls, outcome = retryAfterExpiry(t, broken)
	if calls == 0 || outcome == OutcomeUncertain {
		t.Fatalf("坏实现（Acquire 无视执行意图）应放行重试，说明判据确实被摘掉了；"+
			"实得 outcome=%v calls=%d", outcome, calls)
	}
}

// retryAfterExpiry 制造一次「抢闸后什么都没做」的中断，等租约过期后再重试，
// 返回重试时的判据与动作体被调用次数。
func retryAfterExpiry(t *testing.T, st Store) (calls int, outcome Outcome) {
	t.Helper()
	ctx := context.Background()
	a := act("k1", "res-a")

	// 第一次：只抢闸，不做任何收场（模拟进程死在记账之前）。
	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	advanceStore(t, st)

	r := NewRunner(st, time.Minute)
	outcome, _, _ = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("second")}, nil
	})
	return calls, outcome
}

// advancer 可推进时钟的存储实现（假存储与它的包装都满足）。
type advancer interface {
	advance(d time.Duration)
}

// advanceStore 把存储实现的时钟往前推。
func advanceStore(t *testing.T, st Store) {
	t.Helper()
	a, ok := st.(advancer)
	if !ok {
		t.Fatalf("advanceStore 需要可推进时钟的存储实现，实得 %T", st)
	}
	a.advance(2 * time.Minute)
}

// ignoresIntentStore 一个**在 Acquire 里不查执行意图**的存储实现：
// 它把「意图的存在本身就是一条判据」整个摘掉了——负对照专用。
type ignoresIntentStore struct {
	*fakeStore
}

func (s *ignoresIntentStore) Acquire(ctx context.Context, act Action, ttl time.Duration) (Outcome, Lease, Result, error) {
	s.mu.Lock()
	delete(s.intents, act.Key)
	s.mu.Unlock()
	return s.fakeStore.Acquire(ctx, act, ttl)
}
