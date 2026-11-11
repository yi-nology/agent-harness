package sidetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「执行意图」一族的契约场景（v0.33.0 新增）。
//
// # 这一族判的是什么
//
// 判的是**同一条**红线：**同一个 Key 上有一次尚未裁决的尝试时，不得再被执行**。
//
// 它封的是本包最贵的那个窗口：进程在抢到闸之后、施加成功之后、记账之前
// 直接死掉。此时存储侧既没有「完成」也没有「未知」——若实现不额外记下
// 「这次尝试还在」，下一次同一 Key 的 Acquire 就会重新授予执行权 ⇒
// 同一动作被施加两次，而调用方全程只拿到一个普通错误。
//
// 契约要求存储实现做到四件事：
//
//  1. Acquire 授予执行权时**原子地**登记执行意图；
//  2. 意图未裁决时，同一 Key 的 Acquire 不得授予执行权；
//  3. 意图的租约过期且未裁决时，同一 Key 的 Acquire **转入需对账态**
//     （除非动作显式声明了「下游按幂等键去重」，那种情况下可重新授予）；
//  4. 「显式放弃」（Abandon）撤销意图 ⇒ 该 Key 重新可执行；
//     而「只放资源闸」（Release）**不**撤销意图。
//
// 第 4 条里那对「放弃 / 放闸」的分工是 v0.33.0 的要害：结果未知的那条路
// 必须只放闸。若它顺手把意图也撤了，这个 Key 就重新变成「可被执行」，
// 重复施加的入口重新打开——且失效是**静默**的。
//
// # 为什么每条场景都写成「返回 error」而不是直接 t.Fatalf
//
// 因为套件需要**自证有分辨力**：同一批场景还要拿去跑一个**故意写坏**的
// 实现，确认它被判红（见 intent_internal_test.go）。
// 若场景只调 t.Fatalf，这个负对照就没法在同一个进程里做——
// 「坏实现被判红」与「好实现被判绿」必须能分别断言。

// checkIntentBlocksReacquire 钉住：意图未裁决时，同一 Key 不得被重新
// 授予执行权。
func checkIntentBlocksReacquire(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-intent", "res-intent")

	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	outcome, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("二次 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeContended {
		return fmt.Errorf("未裁决的尝试不得被重新授予执行权（否则就是重复施加），实得 %v", outcome)
	}
	if lease.Fence != 0 {
		return fmt.Errorf("被拒时不该发放令牌（发了等于承认它），实得 Fence=%d", lease.Fence)
	}
	return nil
}

// checkExpiredIntentRequiresReconcile 钉住：意图的租约过期且未裁决时，
// 同一 Key 转入**需对账态**（不是重新授予执行权）。
//
// 这是「进程死在施加成功之后、记账之前」那条形态的封口。
func checkExpiredIntentRequiresReconcile(st sideeffect.Store, clock *FakeClock) error {
	ctx := context.Background()
	a := act("k-expired", "res-expired")

	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	// 进程死在收场之前：既没 Complete，也没 MarkUncertain，也没 Release。
	clock.Advance(2 * time.Minute)

	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("过期后 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeUncertain {
		return fmt.Errorf("租约过期且未裁决 ⇒ 该 Key 应转入需对账态（不得重新授予执行权），实得 %v", outcome)
	}
	return nil
}

// checkExpiredIntentSafeMayRetry 钉住：动作显式声明了「下游按幂等键去重」
// 时，租约过期后同一 Key 仍可重新执行。
//
// 这是宿主对**下游事实**的显式声明（Action.RetrySafety），不是内核的乐观
// 假设——未声明（零值）时按最弱处理，走「转入需对账态」那条。
func checkExpiredIntentSafeMayRetry(st sideeffect.Store, clock *FakeClock) error {
	ctx := context.Background()
	a := act("k-safe", "res-safe")
	a.RetrySafety = sideeffect.RetrySafetySafe

	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	clock.Advance(2 * time.Minute)

	outcome, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("过期后 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("声明了可安全重试时，过期后应重新授予执行权，实得 %v", outcome)
	}
	if lease.Fence == 0 {
		return fmt.Errorf("重新授予时必须发放令牌（下游据此拒收陈旧写入）")
	}
	return nil
}

// checkAbandonAllowsRetry 钉住：显式放弃后该 Key 重新可执行。
func checkAbandonAllowsRetry(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-abandon", "res-abandon")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.Abandon(ctx, a, lease, "准备阶段失败：未施加任何副作用"); err != nil {
		return fmt.Errorf("显式放弃失败: %w", err)
	}

	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("放弃后 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("显式放弃后该 Key 应重新可执行，实得 %v", outcome)
	}
	return nil
}

// checkReleaseKeepsIntent 钉住：只放资源闸**不**撤销执行意图。
//
// 失效形态：Release 顺手把意图也撤了 ⇒ 结果未知的那条路上，存储侧什么都不剩
// ⇒ 下一次 Acquire 重新授予执行权 ⇒ 重复施加。
func checkReleaseKeepsIntent(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-release", "res-release")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.Release(ctx, a, lease); err != nil {
		return fmt.Errorf("释放失败: %w", err)
	}

	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("释放后 Acquire 失败: %w", err)
	}
	if outcome == sideeffect.OutcomeReplayed {
		return fmt.Errorf("释放不该把动作标记为已完成（replayed）")
	}
	if outcome == sideeffect.OutcomeApplied {
		return fmt.Errorf("释放只放闸、不撤销执行意图：同 Key 再来不得授予执行权，实得 applied")
	}
	return nil
}

// checkAbandonRequiresOwner 钉住：放弃只认**本次尝试**的令牌。
//
// 失效形态：拿一枚不属于本次尝试的令牌撤销意图 ⇒ 本次尝试被凭空「放弃」
// ⇒ 那个 Key 重新变成「可被执行」⇒ 重复施加。
func checkAbandonRequiresOwner(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-owner", "res-owner")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	forged := lease
	forged.Fence = lease.Fence + 1
	if err := st.Abandon(ctx, a, forged, "伪造令牌"); !errors.Is(err, sideeffect.ErrFenceStale) {
		return fmt.Errorf("放弃只认本次尝试的令牌，伪造令牌应被拒为 ErrFenceStale，实得 %v", err)
	}
	// 意图仍在 ⇒ 这个 Key 仍被挡着（没被凭空「放弃」）。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("Acquire 失败: %w", err)
	}
	if outcome == sideeffect.OutcomeApplied {
		return fmt.Errorf("被拒的放弃不该撤销意图：同 Key 再来不得授予执行权，实得 applied")
	}
	return nil
}

// ── 场景入口（供 Run 调用）────────────────────────────────────────────

func runIntentBlocksReacquire(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkIntentBlocksReacquire(st); err != nil {
		t.Fatalf("违反「未裁决的尝试不得重新授予执行权」契约: %v", err)
	}
}

func runExpiredIntentRequiresReconcile(t *testing.T, factory Factory) {
	t.Helper()
	st, clock := newStore(t, factory)
	if err := checkExpiredIntentRequiresReconcile(st, clock); err != nil {
		t.Fatalf("违反「租约过期未裁决 ⇒ 转入需对账态」契约: %v", err)
	}
}

func runExpiredIntentSafeMayRetry(t *testing.T, factory Factory) {
	t.Helper()
	st, clock := newStore(t, factory)
	if err := checkExpiredIntentSafeMayRetry(st, clock); err != nil {
		t.Fatalf("违反「声明可安全重试 ⇒ 过期后可重新授予」契约: %v", err)
	}
}

func runAbandonAllowsRetry(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkAbandonAllowsRetry(st); err != nil {
		t.Fatalf("违反「显式放弃后该 Key 重新可执行」契约: %v", err)
	}
}

func runReleaseKeepsIntent(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkReleaseKeepsIntent(st); err != nil {
		t.Fatalf("违反「释放只放闸、不撤销执行意图」契约: %v", err)
	}
}

func runAbandonRequiresOwner(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkAbandonRequiresOwner(st); err != nil {
		t.Fatalf("违反「放弃只认本次尝试的令牌」契约: %v", err)
	}
}
