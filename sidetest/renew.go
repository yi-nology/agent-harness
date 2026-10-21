package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/agent-harness/sideeffect"
)

// 本文件钉住**租约续期（Renew）**的语义。
//
// 续期与「结果判据」（同包 sidetest.go）是两族不同的语义：
//   - 结果判据管的是「这个动作做过没有、该不该做」；
//   - 续期管的是「我还持有这道闸吗」——它回答的是**资格**，不是**结果**。
//
// 续期的关键性质是**它同时做两件事**：延长有效期 + 校验令牌仍是最新代。
// 只做前者的续期是「假续期」：闸已易主时，延长的其实是别人的闸，
// 持有者却以为延长的是自己的——于是两份闸同时开着，副作用施加两次。

// runRenewExtendsLease 钉住：Renew 延长有效期，且**不换令牌**。
//
// 不换令牌是关键：下游资源侧已经认了这个令牌，续期若换令牌，
// 下游会把持有者自己的写入当成陈旧写入拒掉。
func runRenewExtendsLease(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// 推进到接近过期（但未过期）。
	clock.Advance(50 * time.Second)

	renewed, err := st.Renew(ctx, a, lease1, time.Minute)
	if err != nil {
		t.Fatalf("Renew 失败: %v", err)
	}
	if renewed.Fence != lease1.Fence {
		t.Fatalf("续期不该换令牌：old=%d new=%d", lease1.Fence, renewed.Fence)
	}

	// 续期后再推进超过原 TTL（但未超过续期后的）→ 闸仍有效。
	clock.Advance(50 * time.Second)
	outcome, _, _, err := st.Acquire(ctx, act("k2", "res-a"), time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeContended {
		t.Fatalf("续期后闸应仍有效（另一 Key 抢应 contended），实得 %v", outcome)
	}
}

// runRenewAfterTakeoverRejected 钉住：闸已被接管后，续期被拒。
//
// 这是**假续期**的封口：只延长时间不校验资格的话，闸已易主时
// 延长的其实是别人的闸，持有者却以为延长的是自己的。
func runRenewAfterTakeoverRejected(t *testing.T, factory Factory) {
	st, clock := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// 过期 → 别人接管。
	clock.Advance(2 * time.Minute)
	if _, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil {
		t.Fatalf("接管 Acquire 失败: %v", err)
	}

	// 旧持有者续期 → 必须被拒。
	if _, err := st.Renew(ctx, a, lease1, time.Minute); !errors.Is(err, sideeffect.ErrFenceStale) {
		t.Fatalf("闸已易主后续期应被拒为 ErrFenceStale，实得 %v", err)
	}
}
