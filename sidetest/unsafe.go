package sidetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「重试安全三档」的契约场景（v0.33.0 新增）。
//
// # 这一族判的是什么
//
// RetrySafety 三档在**判据上**分开，而不是只在注释里分开：
//
//   - Safe：下游按幂等键去重 → 失败后可安全重试（intent 一族已覆盖）；
//   - Unknown（未声明）：失败 → 未知态 → 对账裁决「未生效」后重新可执行
//     （uncertain 一族已覆盖，resolve_not_applied_allows_retry）；
//   - **Unsafe**：失败 → 未知态 → 对账裁决「未生效」**仍阻塞**——
//     必须Resolution.ForceRetryable 的显式决策才放行。
//
// 失效形态：把 Unsafe 写回与 Unknown 同一条路径——「不可重试」退化成
// 注释里的一句意图，改一行分支不会有任何用例变红。而那句注释管的正是
// 「重试会重复施加」的动作：对账的「未生效」结论来自宿主的一次查询，
// 查询可能出错，查错的代价就是重复施加。

// checkUnsafeResolveStaysBlocked 钉住：Unsafe 动作裁决「未生效」后仍阻塞，
// 且裁决被拒为 ErrUnsafeNotRetryable（未知标记原样保留）。
func checkUnsafeResolveStaysBlocked(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-unsafe", "res-unsafe")
	a.RetrySafety = sideeffect.RetrySafetyUnsafe

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.MarkUncertain(ctx, a, lease, "施加失败：结果未知"); err != nil {
		return fmt.Errorf("标记未知失败: %w", err)
	}
	// 标记成功后只放资源闸（Runner 的失败出口同样如此）：未知标记与资源
	// 闸无关，闸不放的话别的动作会被「资源正忙」挡住。
	if err := st.Release(ctx, a, lease); err != nil {
		return fmt.Errorf("释放资源闸失败: %w", err)
	}

	outcome, _, err := st.ResolveUncertain(ctx, a, sideeffect.Resolution{Applied: false, Reason: "对账说没做"})
	if !errors.Is(err, sideeffect.ErrUnsafeNotRetryable) {
		return fmt.Errorf("不可重试（Unsafe）动作的「未生效」裁决应被拒为 ErrUnsafeNotRetryable（不得自动放行），实得 outcome=%v err=%v", outcome, err)
	}

	// 被拒的裁决不得解除阻塞：同 Key 再来不得授予执行权。
	outcome, _, _, err = st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("裁决被拒后的 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeUncertain {
		return fmt.Errorf("不可重试动作裁决「未生效」后必须仍阻塞（实得 %v）——否则「不可重试」只是注释", outcome)
	}
	return nil
}

// checkUnsafeForceRetryableExplicit 钉住：显式决策（ForceRetryable）放行
// Unsafe 动作，且「确认已生效」的裁决对 Unsafe 一直有效。
func checkUnsafeForceRetryableExplicit(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-unsafe-force", "res-unsafe-force")
	a.RetrySafety = sideeffect.RetrySafetyUnsafe

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.MarkUncertain(ctx, a, lease, "施加失败：结果未知"); err != nil {
		return fmt.Errorf("标记未知失败: %w", err)
	}
	if err := st.Release(ctx, a, lease); err != nil {
		return fmt.Errorf("释放资源闸失败: %w", err)
	}

	// 显式签字：宿主确认「对账结论可靠、重试风险已评估」。
	if _, _, err := st.ResolveUncertain(ctx, a, sideeffect.Resolution{
		Applied:        false,
		Reason:         "对账说没做，宿主显式接受重试风险",
		ForceRetryable: true,
	}); err != nil {
		return fmt.Errorf("显式决策后的裁决应放行: %w", err)
	}
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("放行后 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("显式决策后该 Key 应重新可执行，实得 %v", outcome)
	}

	// 对照：Unsafe 动作「确认已生效」的裁决不涉及重试，一直有效。
	b := act("k-unsafe-done", "res-unsafe-done")
	b.RetrySafety = sideeffect.RetrySafetyUnsafe
	_, lease2, _, err := st.Acquire(ctx, b, time.Minute)
	if err != nil {
		return fmt.Errorf("对照动作 Acquire 失败: %w", err)
	}
	if err := st.MarkUncertain(ctx, b, lease2, "施加失败：结果未知"); err != nil {
		return fmt.Errorf("对照动作标记未知失败: %w", err)
	}
	if err := st.Release(ctx, b, lease2); err != nil {
		return fmt.Errorf("对照动作释放资源闸失败: %w", err)
	}
	outcome2, res2, err := st.ResolveUncertain(ctx, b, sideeffect.Resolution{
		Applied: true, Result: sideeffect.Result{Payload: []byte("done")}, Reason: "对账确认已生效",
	})
	if err != nil {
		return fmt.Errorf("「确认已生效」的裁决对 Unsafe 动作应有效（不涉及重试）: %w", err)
	}
	if outcome2 != sideeffect.OutcomeReplayed || string(res2.Payload) != "done" {
		return fmt.Errorf("「确认已生效」应记为已完成并重放结果，实得 outcome=%v res=%v", outcome2, res2)
	}
	outcome3, _, got, err := st.Acquire(ctx, b, time.Minute)
	if err != nil || outcome3 != sideeffect.OutcomeReplayed || string(got.Payload) != "done" {
		return fmt.Errorf("裁决后的 Acquire 应重放既有结果，实得 outcome=%v res=%v err=%v", outcome3, got, err)
	}
	return nil
}

// ── 场景入口（供 Run 调用）────────────────────────────────────────────

func runUnsafeResolveStaysBlocked(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkUnsafeResolveStaysBlocked(st); err != nil {
		t.Fatalf("违反「Unsafe 裁决未生效仍阻塞」契约: %v", err)
	}
}

func runUnsafeForceRetryableExplicit(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkUnsafeForceRetryableExplicit(st); err != nil {
		t.Fatalf("违反「Unsafe 显式决策才放行」契约: %v", err)
	}
}
