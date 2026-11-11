package sidetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「结果未知」一族的契约场景（v0.32.0 新增）。
//
// # 这一族判的是什么
//
// 同一族的四条场景判的是同一件事：**结果未知的 Key 不得再被执行**。
//
// 它是副作用闸存在的理由（「同一个逻辑动作只施加一次」）在**失败路径**上的
// 兑现。此前失败路径只有一条出口：释放闸 → 允许重试。于是
// 「远端已生效但响应丢了」这一形态，会被下一次重试变成**重复施加**——
// 而调用方全程只拿到一个普通错误，没有任何信号说「这个动作可能已经做过了」。
//
// 契约要求存储实现做到三件事：
//
//  1. MarkUncertain 之后，同一 Key 的 Acquire 必须返回 OutcomeUncertain；
//  2. ResolveUncertain(applied=false) 之后，该 Key 重新可被执行；
//  3. ResolveUncertain(applied=true) 之后，该 Key 变成「已完成」，
//     Acquire 返回 OutcomeReplayed 并带回那次的结果。
//
// # 为什么每条场景都写成「返回 error」而不是直接 t.Fatalf
//
// 因为套件需要**自证有分辨力**：同一批场景还要拿去跑一个**故意写坏**的
// 实现，确认它被判红（见 uncertain_internal_test.go）。
// 若场景只调 t.Fatalf，这个负对照就没法在同一个进程里做——
// 「坏实现被判红」与「好实现被判绿」必须能分别断言。

// checkUncertainBlocksExecution 钉住：标记未知后，同一 Key 不得再被授予执行权。
func checkUncertainBlocksExecution(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-uncertain", "res-uncertain")

	outcome, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("首次 Acquire 应为 applied，实得 %v", outcome)
	}
	if err := st.MarkUncertain(ctx, a, lease, "响应丢了"); err != nil {
		return fmt.Errorf("MarkUncertain 失败: %w", err)
	}

	outcome, _, _, err = st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("未知态下的 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeUncertain {
		return fmt.Errorf("结果未知的 Key 不得再被授予执行权（否则就是重复施加），实得 %v", outcome)
	}
	return nil
}

// checkResolveNotAppliedAllowsRetry 钉住：裁决「未生效」后，该 Key 重新可执行。
func checkResolveNotAppliedAllowsRetry(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-resolve-no", "res-resolve-no")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.MarkUncertain(ctx, a, lease, "响应丢了"); err != nil {
		return fmt.Errorf("MarkUncertain 失败: %w", err)
	}
	// 释放资源持有：编排器的顺序是「标记未知 → 释放闸」（闸是资源级的，
	// 未知是 Key 级的——让别的动作用这个资源，但这个 Key 在裁决前不得再被执行）。
	if err := st.Release(ctx, a, lease); err != nil {
		return fmt.Errorf("释放持有失败: %w", err)
	}
	if _, _, err := st.ResolveUncertain(ctx, a, sideeffect.Resolution{
		Applied: false,
		Reason:  "查下游：未生效",
	}); err != nil {
		return fmt.Errorf("裁决「未生效」失败: %w", err)
	}

	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("裁决后的 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("裁决「未生效」后该 Key 应重新可执行，实得 %v", outcome)
	}
	return nil
}

// checkResolveAppliedRecordsResult 钉住：裁决「已生效」后，该 Key 变成已完成，
// 且取回那次的结果（重放而不是重做）。
func checkResolveAppliedRecordsResult(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-resolve-yes", "res-resolve-yes")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.MarkUncertain(ctx, a, lease, "响应丢了"); err != nil {
		return fmt.Errorf("MarkUncertain 失败: %w", err)
	}
	if err := st.Release(ctx, a, lease); err != nil {
		return fmt.Errorf("释放持有失败: %w", err)
	}
	want := sideeffect.Result{Payload: []byte("v1")}
	if _, _, err := st.ResolveUncertain(ctx, a, sideeffect.Resolution{
		Applied: true,
		Result:  want,
		Reason:  "查下游：已生效",
	}); err != nil {
		return fmt.Errorf("裁决「已生效」失败: %w", err)
	}

	outcome, _, got, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("裁决后的 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		return fmt.Errorf("裁决「已生效」后该 Key 应为已完成（replayed），实得 %v", outcome)
	}
	if string(got.Payload) != string(want.Payload) {
		return fmt.Errorf("应取回裁决时记录的结果 %q，实得 %q", want.Payload, got.Payload)
	}
	return nil
}

// checkResolveFirstWins 钉住：同一 Key 的多次裁决**先到者为准**。
//
// 失效形态：后到的裁决覆盖先到的 ⇒ 两个对账者结论相反时，
// 最终状态取决于谁慢，不是取决于事实——那不是幂等，是竞态。
func checkResolveFirstWins(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-first-wins", "res-first-wins")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if err := st.MarkUncertain(ctx, a, lease, "响应丢了"); err != nil {
		return fmt.Errorf("MarkUncertain 失败: %w", err)
	}
	if err := st.Release(ctx, a, lease); err != nil {
		return fmt.Errorf("释放持有失败: %w", err)
	}
	if _, _, err := st.ResolveUncertain(ctx, a, sideeffect.Resolution{
		Applied: true,
		Result:  sideeffect.Result{Payload: []byte("first")},
		Reason:  "第一次裁决",
	}); err != nil {
		return fmt.Errorf("首次裁决失败: %w", err)
	}
	// 第二次裁决给相反的结论（未生效）：**不得**推翻先到者。
	if _, _, err := st.ResolveUncertain(ctx, a, sideeffect.Resolution{
		Applied: false,
		Reason:  "第二次裁决（相反结论）",
	}); err != nil {
		return fmt.Errorf("第二次裁决不该报错: %w", err)
	}

	outcome, _, got, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("裁决后的 Acquire 失败: %w", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		return fmt.Errorf("先到者为准：后到的相反裁决不得推翻已完成，实得 %v", outcome)
	}
	if string(got.Payload) != "first" {
		return fmt.Errorf("应保留先到者的结果 first，实得 %q", got.Payload)
	}
	return nil
}

// checkStaleFenceCannotMarkUncertain 钉住：过期持有者不得标记未知态。
//
// 失效形态：过期持有者把新持有者刚做完的动作标成「未知」——
// 而新持有者那边的结果是确定的。事实被写反了，后续对账会裁决错。
func checkStaleFenceCannotMarkUncertain(st sideeffect.Store, clock *FakeClock) error {
	ctx := context.Background()
	a := act("k-stale-mark", "res-stale-mark")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	clock.Advance(2 * time.Minute)
	_, _, _, err = st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("接管 Acquire 失败: %w", err)
	}

	err = st.MarkUncertain(ctx, a, lease1, "过期持有者")
	if err == nil {
		return fmt.Errorf("过期持有者不得标记未知态（会把新持有者的确定结果写反）")
	}
	return nil
}

// ── 场景入口（供 Run 调用）────────────────────────────────────────────

func runUncertainBlocksExecution(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkUncertainBlocksExecution(st); err != nil {
		t.Fatalf("违反「结果未知不得再被执行」契约: %v", err)
	}
}

func runResolveNotAppliedAllowsRetry(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkResolveNotAppliedAllowsRetry(st); err != nil {
		t.Fatalf("违反「裁决未生效后可重试」契约: %v", err)
	}
}

func runResolveAppliedRecordsResult(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkResolveAppliedRecordsResult(st); err != nil {
		t.Fatalf("违反「裁决已生效后重放结果」契约: %v", err)
	}
}

func runResolveFirstWins(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkResolveFirstWins(st); err != nil {
		t.Fatalf("违反「裁决先到者为准」契约: %v", err)
	}
}

func runStaleFenceCannotMarkUncertain(t *testing.T, factory Factory) {
	t.Helper()
	st, clock := newStore(t, factory)
	if err := checkStaleFenceCannotMarkUncertain(st, clock); err != nil {
		t.Fatalf("违反「过期持有者不得标记未知」契约: %v", err)
	}
}
