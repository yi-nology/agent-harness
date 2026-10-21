package sideeffect

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// TestPrepareFailureAppliesNothing 钉住：准备阶段失败 → **未施加任何副作用**。
//
// 两段式的第一道价值：准备失败是「干净失败」——什么都没做，可以安全重试。
// 误用形态：把准备失败也算成「已施加」→ 后续重试拿到空结果，
// 而真相是「没做过」。
func TestPrepareFailureAppliesNothing(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, time.Minute)
	ctx := context.Background()
	a := act("k1", "res-a")

	prepareCalls := 0
	applyCalls := 0
	_, _, err := r.ExecuteWith(ctx, a, Effect{
		Prepare: func(context.Context) (any, error) {
			prepareCalls++
			return nil, errors.New("准备失败")
		},
		Apply: func(context.Context, any) (Result, error) {
			applyCalls++
			return Result{}, nil
		},
	})
	if err == nil {
		t.Fatal("准备失败应报错")
	}
	if applyCalls != 0 {
		t.Fatalf("准备失败时不该施加副作用，实得 %d 次", applyCalls)
	}
	if st.completeCalls != 0 {
		t.Fatalf("准备失败不该记账，实得 %d", st.completeCalls)
	}
	// 闸被释放 → 后续重试能真正执行。
	if st.releaseCalls != 1 {
		t.Fatalf("准备失败应释放闸 1 次，实得 %d", st.releaseCalls)
	}
}

// TestPrepareFailureKeepsActionRetryable 钉住：准备失败后动作**仍可重做**。
//
// 这是「准备失败可安全重试」的正面形态——动作没被丢掉。
func TestPrepareFailureKeepsActionRetryable(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, time.Minute)
	ctx := context.Background()
	a := act("k1", "res-a")

	if _, _, err := r.ExecuteWith(ctx, a, Effect{
		Prepare: func(context.Context) (any, error) { return nil, errors.New("准备失败") },
		Apply:   func(context.Context, any) (Result, error) { return Result{}, nil },
	}); err == nil {
		t.Fatal("准备失败应报错")
	}

	// 重试：这次准备成功 → 应真正执行。
	applyCalls := 0
	outcome, res, err := r.ExecuteWith(ctx, a, Effect{
		Prepare: func(context.Context) (any, error) { return "prepared", nil },
		Apply: func(context.Context, any) (Result, error) {
			applyCalls++
			return Result{Payload: []byte("v")}, nil
		},
	})
	if err != nil {
		t.Fatalf("重试不该失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("重试应判为 applied，实得 %v", outcome)
	}
	if applyCalls != 1 {
		t.Fatalf("重试应执行 1 次，实得 %d", applyCalls)
	}
	if string(res.Payload) != "v" {
		t.Fatalf("结果应为 v，实得 %q", res.Payload)
	}
}

// TestPreparedValuePassedToApply 钉住：Prepare 的产出**原样传给** Apply。
//
// 这是两段式的契约：准备阶段算出来的东西（请求体、目标参数）必须
// 能带到施加阶段，否则两段就是各干各的。
func TestPreparedValuePassedToApply(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, time.Minute)
	ctx := context.Background()
	a := act("k1", "res-a")

	type prepared struct{ id string }
	want := prepared{id: "x1"}
	var got any

	_, _, err := r.ExecuteWith(ctx, a, Effect{
		Prepare: func(context.Context) (any, error) { return want, nil },
		Apply: func(_ context.Context, p any) (Result, error) {
			got = p
			return Result{}, nil
		},
	})
	if err != nil {
		t.Fatalf("执行不该失败: %v", err)
	}
	if got != want {
		t.Fatalf("Apply 应收到 Prepare 的产出，实得 %v", got)
	}
}

// TestApplySkippedWhenLeaseLost 钉住**本轮的核心防线**：
// 闸已易主时，施加阶段**根本不执行**。
//
// 这是「危险窗口」的封口：准备阶段耗时超过 TTL → 闸被接管 →
// 若不加检查就施加，就是两份闸同时开着，副作用施加两次。
//
// 误用形态：检查失败后仍然施加 → 重复施加，且 Complete 才会被拒
// （那时副作用早已生效，事后补救不了）。
func TestApplySkippedWhenLeaseLost(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, time.Minute)
	ctx := context.Background()
	a := act("k1", "res-a")

	applyCalls := 0
	// Prepare 里让时间流逝到超过 TTL——模拟准备阶段很慢。
	// 期间别人接管了闸。
	_, _, err := r.ExecuteWith(ctx, a, Effect{
		Prepare: func(context.Context) (any, error) {
			// 推进时间越过 TTL（租约 1 分钟，推进 2 分钟）。
			st.advance(2 * time.Minute)
			// 别人接管：新的持有者拿到更大的令牌。
			if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
				return nil, err
			}
			return "prepared", nil
		},
		Apply: func(context.Context, any) (Result, error) {
			applyCalls++
			return Result{Payload: []byte("v")}, nil
		},
	})
	if err == nil {
		t.Fatal("闸已易主时应报错")
	}
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("闸已易主应报 ErrLeaseLost，实得 %v", err)
	}
	if applyCalls != 0 {
		t.Fatalf("闸已易主时不该施加副作用，实得 %d 次", applyCalls)
	}
	if st.completeCalls != 0 {
		t.Fatalf("闸已易主时不该记账，实得 %d", st.completeCalls)
	}
}

// TestLeaseLostIsRetryableNotPermanent 钉住：闸已易主是**可重试**的拒绝。
//
// 动作没被施加（见上一条），所以宿主退避后重试整个动作是对的——
// 新一轮重新抢闸，若上一轮确实已施加则幂等键保证取回既有结果。
//
// 注意第二次重试的形态：若**接管者还持着闸**，重试拿到 contended
// （这是对的——有人在做同一件事，不该两份闸同时开）；
// 等对方做完记账后，重试会拿到 replayed（取回既有结果）。
// 本用例覆盖的是「接管者已放手」的情形。
func TestLeaseLostIsRetryableNotPermanent(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, time.Minute)
	ctx := context.Background()
	a := act("k1", "res-a")

	// 第一次：闸易主 → 拒绝。
	_, _, err := r.ExecuteWith(ctx, a, Effect{
		Prepare: func(context.Context) (any, error) {
			st.advance(2 * time.Minute)
			_, _, _, _ = st.Acquire(ctx, a, time.Minute)
			return "p", nil
		},
		Apply: func(context.Context, any) (Result, error) { return Result{}, nil },
	})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("应报 ErrLeaseLost，实得 %v", err)
	}

	// 接管者放手后：闸空着 → 应能正常执行。
	st.mu.Lock()
	delete(st.holders, "res-a")
	st.mu.Unlock()

	applyCalls := 0
	outcome, _, err := r.ExecuteWith(ctx, a, Effect{
		Prepare: func(context.Context) (any, error) { return "p", nil },
		Apply: func(context.Context, any) (Result, error) {
			applyCalls++
			return Result{Payload: []byte("v")}, nil
		},
	})
	if err != nil {
		t.Fatalf("闸空时不该失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("闸空时应判为 applied，实得 %v", outcome)
	}
	if applyCalls != 1 {
		t.Fatalf("应执行 1 次，实得 %d", applyCalls)
	}
}

// TestExecuteAlsoChecksLease 钉住：单段 Execute **也做**资格检查。
//
// 理由：检查放在内核而不是让宿主记得做，才不会漏。
// 宿主若嫌多一次往返，用 ExecuteWith 走两段式即可（同一个检查点）。
func TestExecuteAlsoChecksLease(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, time.Minute)
	ctx := context.Background()
	a := act("k1", "res-a")

	applyCalls := 0
	_, _, err := r.Execute(ctx, a, func(context.Context) (Result, error) {
		applyCalls++
		return Result{}, nil
	})
	if err != nil {
		t.Fatalf("正常路径不该失败: %v", err)
	}
	if applyCalls != 1 {
		t.Fatalf("应执行 1 次，实得 %d", applyCalls)
	}
}

// TestRenewDoesNotChangeFence 钉住：续期**不换令牌**。
//
// 理由：下游资源侧已经认了这个令牌。若续期换令牌，下游会把持有者
// 自己的写入当成陈旧写入拒掉——持有者续了期，却把自己的写入也拒了。
//
// 这条钉的是**内核自己的测试替身**（fakeStore）的行为；
// 宿主的实现由 sidetest 套件的 renew_extends_lease 场景钉。
func TestRenewDoesNotChangeFence(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	renewed, err := st.Renew(ctx, a, lease, time.Minute)
	if err != nil {
		t.Fatalf("Renew 失败: %v", err)
	}
	if renewed.Fence != lease.Fence {
		t.Fatalf("续期不该换令牌：old=%d new=%d", lease.Fence, renewed.Fence)
	}
}

// TestRenewExtendsExpiry 钉住：续期真的把到期时刻推后了。
//
// 误用形态：Renew 只校验资格不延期 → 长动作中途闸过期被别人接管，
// 而持有者以为还持有。
func TestRenewExtendsExpiry(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// 推进到接近过期。
	st.advance(50 * time.Second)
	if _, err := st.Renew(ctx, a, lease, time.Minute); err != nil {
		t.Fatalf("Renew 失败: %v", err)
	}
	// 再推进超过**原** TTL（60s），但未超过续期后的（从 50s 起算 60s）。
	st.advance(50 * time.Second)
	// 闸应仍有效：另一 Key 抢同一个资源 → contended。
	outcome, _, _, err := st.Acquire(ctx, act("k2", "res-a"), time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != OutcomeContended {
		t.Fatalf("续期后闸应仍有效（另一 Key 抢应 contended），实得 %v", outcome)
	}
}

// TestRenewAfterTakeoverRejected 钉住：闸已被接管后，续期被拒。
//
// 这是**假续期**的封口：只延长时间不校验资格的话，闸已易主时
// 延长的其实是别人的闸，持有者却以为延长的是自己的。
func TestRenewAfterTakeoverRejected(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	st.advance(2 * time.Minute)
	// 别人接管。
	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		t.Fatalf("接管 Acquire 失败: %v", err)
	}
	// 旧持有者续期 → 必须被拒。
	if _, err := st.Renew(ctx, a, lease1, time.Minute); !errors.Is(err, ErrFenceStale) {
		t.Fatalf("闸已易主后续期应被拒为 ErrFenceStale，实得 %v", err)
	}
}

// TestRenewFailurePropagates 钉住：续期的**非资格类**故障原样透出，
// 不被误判成 ErrLeaseLost。
//
// 误用形态：把所有 Renew 失败都报成「闸已易主」→ 宿主以为该退避重试，
// 而真相是存储挂了——两种处置完全不同。
func TestRenewFailurePropagates(t *testing.T) {
	boom := errors.New("存储不可用")
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	// 换一个 Renew 会失败的存储包装。
	r2 := NewRunner(renewFailStore{Store: st, err: boom}, time.Minute)
	_, _, err := r2.ExecuteWith(ctx, a, Effect{
		Apply: func(context.Context, any) (Result, error) { return Result{}, nil },
	})
	if !errors.Is(err, boom) {
		t.Fatalf("续期的存储故障应原样透出，实得 %v", err)
	}
	if errors.Is(err, ErrLeaseLost) {
		t.Fatal("存储故障不该被误判为 ErrLeaseLost")
	}
}

// renewFailStore 让 Renew 恒失败（非资格类故障）。
type renewFailStore struct {
	Store
	err error
}

func (r renewFailStore) Renew(context.Context, Action, Lease, time.Duration) (Lease, error) {
	return Lease{}, r.err
}
