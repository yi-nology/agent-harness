package sidetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「动作身份不可变」一族的契约场景（评审第三轮 P0）。
//
// # 这一族判的是什么
//
// Key 声明「这是同一个逻辑动作」，但 Resource / Payload / RetrySafety
// 才是这个动作做什么、失败了怎么办的定义。三者若可被后续调用改写：
//
//   - RetrySafety：首次 Unsafe（明确不幂等）的尝试过期后，重试方换传
//     Safe 就拿到执行权——「不可重试」被后到的声明推翻，重复施加的入口
//     重新打开；
//   - Resource：同 Key 换资源不是重试，是两个动作；
//   - Payload：同 Key 换参数重放的是另一个请求，旧结果答非所问。
//
// 契约（Store.Acquire「动作身份不可变」）：身份不一致 → ErrActionConflict，
// 不授予、不重放、不转未知态，且判定优先于「已完成 → 重放」。

// identityAct 构造带显式身份三元组的动作（act() 的身份可变版）。
func identityAct(key, resource, payload string, safety sideeffect.RetrySafety) sideeffect.Action {
	return sideeffect.Action{
		Key:         sideeffect.Key(key),
		Resource:    resource,
		Payload:     []byte(payload),
		RetrySafety: safety,
	}
}

// checkActionIdentityImmutable 四段：改 Safety / 改 Resource / 改 Payload /
// 已完成后改 Payload，全部必须被 ErrActionConflict 拒绝。
func checkActionIdentityImmutable(st sideeffect.Store, clock *FakeClock) error {
	ctx := context.Background()

	// ── 段 1（评审验收句）：首试 Unsafe 过期后改传 Safe 不得放行 ──
	a := identityAct("k-id-safe", "res-id", "p-id", sideeffect.RetrySafetyUnsafe)
	if outcome, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("首试应授予执行权（身份此刻登记），实得 outcome=%v err=%v", outcome, err)
	}
	clock.Advance(2 * time.Minute) // 尝试过期（未裁决）

	unsafeSwap := identityAct("k-id-safe", "res-id", "p-id", sideeffect.RetrySafetySafe)
	if _, _, _, err := st.Acquire(ctx, unsafeSwap, time.Minute); !errors.Is(err, sideeffect.ErrActionConflict) {
		return fmt.Errorf("首试 Unsafe、过期后改传 Safe 必须被拒为 ErrActionConflict（否则不可重试被后到的声明推翻），实得 %v", err)
	}

	// ── 段 2：同 Key 换 Resource 被拒 ──
	if _, _, _, err := st.Acquire(ctx, identityAct("k-id-safe", "res-id-2", "p-id", sideeffect.RetrySafetyUnsafe), time.Minute); !errors.Is(err, sideeffect.ErrActionConflict) {
		return fmt.Errorf("同 Key 换 Resource 必须被拒为 ErrActionConflict（租约与意图会张冠李戴），实得 %v", err)
	}

	// ── 段 3：同 Key 换 Payload 被拒 ──
	if _, _, _, err := st.Acquire(ctx, identityAct("k-id-safe", "res-id", "p-id-2", sideeffect.RetrySafetyUnsafe), time.Minute); !errors.Is(err, sideeffect.ErrActionConflict) {
		return fmt.Errorf("同 Key 换 Payload 必须被拒为 ErrActionConflict（同 Key 不同请求不是重试），实得 %v", err)
	}

	// ── 段 4：已完成也不得把旧结果重放给新身份（判定优先于重放）──
	b := identityAct("k-id-done", "res-id-done", "p-done", sideeffect.RetrySafetyUnknown)
	_, lease, _, err := st.Acquire(ctx, b, time.Minute)
	if err != nil {
		return fmt.Errorf("段 4 首次 Acquire 失败: %v", err)
	}
	if _, err := st.Complete(ctx, b, lease, sideeffect.Result{Payload: []byte("first-answer")}); err != nil {
		return fmt.Errorf("段 4 Complete 失败: %v", err)
	}
	changed := identityAct("k-id-done", "res-id-done", "p-done-OTHER", sideeffect.RetrySafetyUnknown)
	if _, _, _, err := st.Acquire(ctx, changed, time.Minute); !errors.Is(err, sideeffect.ErrActionConflict) {
		return fmt.Errorf("已完成的 Key 换身份再 Acquire 必须被拒为 ErrActionConflict（重放旧结果给新身份是答非所问），实得 %v", err)
	}
	return nil
}

// ── 场景入口（供 Run 调用）────────────────────────────────────────────

func runActionIdentityImmutable(t *testing.T, factory Factory) {
	t.Helper()
	st, clock := newStore(t, factory)
	if err := checkActionIdentityImmutable(st, clock); err != nil {
		t.Fatalf("违反「同 Key 动作身份不可变」契约: %v", err)
	}
}
