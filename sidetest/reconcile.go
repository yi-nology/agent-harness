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

// asOwned 取带凭据裁决能力。缺失即硬失败（与 RenewingBoard 同一处置：
// 跳过 = 把「没测过」伪装成「通过了」）。
func asOwned(st sideeffect.Store) (sideeffect.OwnedResolveStore, error) {
	owned, ok := st.(sideeffect.OwnedResolveStore)
	if !ok {
		return nil, errors.New("sidetest: 存储未实现 OwnedResolveStore——带占用凭据的裁决是契约要求（跨进程对账的迟到提交必须能被拒）")
	}
	return owned, nil
}

// checkOwnedResolveRequiresValidHolder 钉住：迟到的对账者不得提交裁决
// （凭据必须是**当前有效**的那份），且凭据校验不改任何状态。
//
// 主案（评审场景）：A 认领 → A 卡顿超时、占用过期 → B 接管 → A 恢复，
// 先于 B 提交旧查询结果 → 必须被拒（ErrReconcileLost）且 B 的占用原样；
// B 提交成功后该 Key 按 B 的裁决收场。
//
// 第二段钉契约的另一半顺序：该 Key **已完成**时凭据不作数——读既有
// 结果是无害重放，不问资格（ResolveUncertainOwned 契约第 3 条）。
func checkOwnedResolveRequiresValidHolder(st sideeffect.Store, clock *FakeClock) error {
	ctx := context.Background()
	owned, err := asOwned(st)
	if err != nil {
		return err
	}

	// ── 主案：过期对账者的迟到裁决被拒，状态不动 ──
	a, err := reconcileFixture(st, "k-owned-recon", "res-owned-recon")
	if err != nil {
		return err
	}
	first, err := st.BeginReconcile(ctx, a, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("首个对账者的认领失败: %w", err)
	}
	clock.Advance(11 * time.Minute) // A 卡死了，占用过期
	second, err := st.BeginReconcile(ctx, a, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("接管对账权的新对账者认领失败: %w", err)
	}
	if second.Holder == first.Holder {
		return errors.New("接管必须铸造新身份（新旧凭据同值则凭据校验形同虚设）")
	}

	// A 恢复，持旧凭据先提交 → 必须被拒。
	if _, _, err := owned.ResolveUncertainOwned(ctx, a, sideeffect.Resolution{
		Applied: false,
		Reason:  "A 的迟到查询结果",
	}, first.Holder); !errors.Is(err, sideeffect.ErrReconcileLost) {
		return fmt.Errorf("过期对账者的迟到裁决必须被拒为 ErrReconcileLost（先到者为准不校验资格），实得 %v", err)
	}

	// 拒绝不改状态：B 的占用原样（第三个对账者仍被挡住）。
	if _, err := st.BeginReconcile(ctx, a, 10*time.Minute); !errors.Is(err, sideeffect.ErrReconcileTaken) {
		return fmt.Errorf("迟到裁决被拒不得波及现任对账者的占用（第三个对账者应被拒为 ErrReconcileTaken），实得 %v", err)
	}
	// Key 仍在未知态（A 的裁决没落地，也没把门打开）。
	if outcome, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil || outcome == sideeffect.OutcomeApplied {
		return fmt.Errorf("迟到裁决被拒后该 Key 应仍在未知态（不得授予执行权），实得 outcome=%v err=%v", outcome, err)
	}

	// B 持有效凭据裁决成功，该 Key 按 B 的结论收场（未生效 → 重新可执行）。
	out, _, err := owned.ResolveUncertainOwned(ctx, a, sideeffect.Resolution{
		Applied: false,
		Reason:  "B 查过下游：未生效",
	}, second.Holder)
	if err != nil {
		return fmt.Errorf("当前持有者的裁决应成功: %w", err)
	}
	if out != sideeffect.OutcomeApplied {
		return fmt.Errorf("裁决「未生效」后该 Key 应重新可执行（OutcomeApplied），实得 %v", out)
	}

	// ── 第二段：已完成 → 重放无害，不问凭据 ──
	b, err := reconcileFixture(st, "k-owned-done", "res-owned-done")
	if err != nil {
		return err
	}
	third, err := st.BeginReconcile(ctx, b, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("认领失败: %w", err)
	}
	clock.Advance(11 * time.Minute)
	fourth, err := st.BeginReconcile(ctx, b, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("接管失败: %w", err)
	}
	if _, _, err := owned.ResolveUncertainOwned(ctx, b, sideeffect.Resolution{
		Applied: true,
		Result:  sideeffect.Result{Payload: []byte("done")},
		Reason:  "查实已生效",
	}, fourth.Holder); err != nil {
		return fmt.Errorf("有效凭据的裁决应成功: %w", err)
	}
	// 已完成后，旧凭据来读也是重放（读既有事实不问资格）。
	out, res, err := owned.ResolveUncertainOwned(ctx, b, sideeffect.Resolution{
		Applied: true,
		Reason:  "A 的迟到提交",
	}, third.Holder)
	if err != nil {
		return fmt.Errorf("已完成的重放不应被凭据拒（读既有结果无害）: %w", err)
	}
	if out != sideeffect.OutcomeReplayed || string(res.Payload) != "done" {
		return fmt.Errorf("应返回 OutcomeReplayed + 既有结果（实得 %v / %q）", out, res.Payload)
	}

	// 空凭据（从未认领）必须被拒。
	c, err := reconcileFixture(st, "k-owned-empty", "res-owned-empty")
	if err != nil {
		return err
	}
	if _, _, err := owned.ResolveUncertainOwned(ctx, c, sideeffect.Resolution{Applied: false}, ""); !errors.Is(err, sideeffect.ErrReconcileLost) {
		return fmt.Errorf("空凭据必须被拒为 ErrReconcileLost，实得 %v", err)
	}
	return nil
}

// checkReconcileTTLPositive 钉住：零/负 TTL 的对账认领必须被拒。
func checkReconcileTTLPositive(st sideeffect.Store) error {
	ctx := context.Background()
	a, err := reconcileFixture(st, "k-recon-ttl", "res-recon-ttl")
	if err != nil {
		return err
	}
	if _, err := st.BeginReconcile(ctx, a, 0); !errors.Is(err, sideeffect.ErrInvalidTTL) {
		return fmt.Errorf("零 TTL 认领必须被拒为 ErrInvalidTTL（出生即过期的认领等于没有占用），实得 %v", err)
	}
	if _, err := st.BeginReconcile(ctx, a, -time.Minute); !errors.Is(err, sideeffect.ErrInvalidTTL) {
		return fmt.Errorf("负 TTL 认领必须被拒为 ErrInvalidTTL，实得 %v", err)
	}
	// 拒绝不留坏状态：正常认领照常成功。
	if _, err := st.BeginReconcile(ctx, a, 10*time.Minute); err != nil {
		return fmt.Errorf("拒绝非法 TTL 后正常认领应成功: %w", err)
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

// runOwnedResolveRequiresValidHolder 迟到的对账者不得提交裁决。
func runOwnedResolveRequiresValidHolder(t *testing.T, factory Factory) {
	t.Helper()
	st, clock := newStore(t, factory)
	if err := checkOwnedResolveRequiresValidHolder(st, clock); err != nil {
		t.Fatalf("违反「带凭据的裁决只认当前有效占用」契约: %v", err)
	}
}

// runReconcileTTLPositive 零/负 TTL 的对账认领必须被拒。
func runReconcileTTLPositive(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkReconcileTTLPositive(st); err != nil {
		t.Fatalf("违反「对账认领 TTL 必须为正」契约: %v", err)
	}
}
