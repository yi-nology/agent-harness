package sidetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「对账占用」一族的契约场景（v0.33.0 新增）。
//
// # 这一族判的是什么
//
// 对账本身是个竞争窗口：没有对账占用时，两个对账者可以同时对同一个 Key
// 查下游、同时提交裁决——ResolveUncertain 的「先到者为准」只是把冲突
// 结果掩盖掉，不是判据；落败那次裁决的下游查询成本与副作用没人认领。
// 对账中途进程死亡时，Key 停在未知态，与「从未开始对账」不可区分——
// 运维看不出这条 Key 是没人管，还是管的人死了。
//
// 契约要求（Store.BeginReconcile）：
//
//  1. 认领后到裁决前，第二个对账者被拒（ErrReconcileTaken）；
//  2. 认领过期后可重新认领（可重入）——对账者死亡不永久占住这个 Key；
//  3. 认领期间该 Key 仍是未知态（对账中 ≠ 已裁决，更不等于可以重做）；
//  4. 裁决清除占用（先到者为准兜底不变）。

// reconcileFixture 构造一个处于「未知态」的 Key，返回动作与凭据。
func reconcileFixture(st sideeffect.Store, key, resource string) (sideeffect.Action, error) {
	ctx := context.Background()
	a := act(key, resource)
	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return a, fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.MarkUncertain(ctx, a, lease, "施加失败：结果未知"); err != nil {
		return a, fmt.Errorf("标记未知失败: %w", err)
	}
	// 标记成功后只放资源闸：未知标记与资源闸无关（闸不放的话，后面的
	// 「裁决后重新可执行」断言会被「资源正忙」挡住）。
	if err := st.Release(ctx, a, lease); err != nil {
		return a, fmt.Errorf("释放资源闸失败: %w", err)
	}
	return a, nil
}

// checkReconcileTakenWhileActive 钉住：未过期的对账占用挡住第二个对账者。
func checkReconcileTakenWhileActive(st sideeffect.Store) error {
	ctx := context.Background()
	a, err := reconcileFixture(st, "k-recon-taken", "res-recon-taken")
	if err != nil {
		return err
	}

	first, err := st.BeginReconcile(ctx, a, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("首次认领对账权失败: %w", err)
	}
	if first.Holder == "" {
		return fmt.Errorf("认领必须铸造认领者身份（占用记录要能回答「谁在对账」）")
	}
	if _, err := st.BeginReconcile(ctx, a, 10*time.Minute); !errors.Is(err, sideeffect.ErrReconcileTaken) {
		return fmt.Errorf("占用未过期时第二个对账者应被拒为 ErrReconcileTaken（否则对账仍是竞争窗口），实得 %v", err)
	}

	// 占用期间该 Key 仍是未知态：不得授予执行权。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("占用期间 Acquire 失败: %w", err)
	}
	if outcome == sideeffect.OutcomeApplied {
		return fmt.Errorf("对账占用期间该 Key 仍处于未知态，不得授予执行权，实得 applied")
	}

	// 裁决清除占用（ResolveUncertain 在对账中同样可用；先到者为准兜底）。
	if _, _, err := st.ResolveUncertain(ctx, a, sideeffect.Resolution{Applied: false, Reason: "对账完成"}); err != nil {
		return fmt.Errorf("占用期间裁决应可用: %w", err)
	}
	if outcome, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("裁决后该 Key 应重新可执行，实得 outcome=%v err=%v", outcome, err)
	}
	return nil
}

// checkReconcileReentrantAfterExpiry 钉住：认领过期后可重新认领，且该 Key
// 全程保持阻塞。
//
// 失效形态：对账者死亡后占用被当作「对账完成」处理——Key 被放行重试，
// 而那次对账什么结论都没留下。
func checkReconcileReentrantAfterExpiry(st sideeffect.Store, clock *FakeClock) error {
	ctx := context.Background()
	a, err := reconcileFixture(st, "k-recon-reenter", "res-recon-reenter")
	if err != nil {
		return err
	}

	if _, err := st.BeginReconcile(ctx, a, 10*time.Minute); err != nil {
		return fmt.Errorf("首次认领失败: %w", err)
	}
	clock.Advance(11 * time.Minute) // 对账者死了，占用过期

	// 过期不等于完成：占用失效前，Key 仍必须被挡着。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("占用过期后 Acquire 失败: %w", err)
	}
	if outcome == sideeffect.OutcomeApplied {
		return fmt.Errorf("对账占用过期 ≠ 对账完成：Key 不得被放行重试，实得 applied")
	}

	// 可重入：新的对账者可以重新认领。
	second, err := st.BeginReconcile(ctx, a, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("过期后重新认领应成功（对账者死亡不永久占住这个 Key）: %w", err)
	}
	if second.Holder == "" {
		return fmt.Errorf("重新认领也必须铸造认领者身份")
	}
	return nil
}

// ── 场景入口（供 Run 调用）────────────────────────────────────────────

func runReconcileTakenWhileActive(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkReconcileTakenWhileActive(st); err != nil {
		t.Fatalf("违反「对账占用同一时刻至多一个」契约: %v", err)
	}
}

func runReconcileReentrantAfterExpiry(t *testing.T, factory Factory) {
	t.Helper()
	st, clock := newStore(t, factory)
	if err := checkReconcileReentrantAfterExpiry(st, clock); err != nil {
		t.Fatalf("违反「对账占用过期可重入且不放行」契约: %v", err)
	}
}
