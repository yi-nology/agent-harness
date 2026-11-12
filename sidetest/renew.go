package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
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

// runRenewAfterReleaseRejected 钉住：闸被**释放**之后，原租约不得再续期。
//
// 失效形态：只比栅栏令牌、不验持有者 ⇒ 释放后的租约仍能续期。
// 栅栏只在**接管**时递增——没人接管时令牌没有变，于是「已经放手」这件事
// 在令牌上完全看不出来，原持有者以为还拿着闸（其实闸是空的，
// 随时可能被别人抢走）。
//
// 这条与「过期持有者续期被拒」同族但不同因：那条是令牌变了，这条是
// **令牌没变而持有关系没了**——只比令牌的实现会漏掉它。
// runRenewAfterExpiryRejected 钉住：租约**已过期且无人接管**时，原持有者的
// 续期必须被拒（过期即资格终止）。
//
// 与其他两条续期拒绝场景的区别：那两条是「闸已易主 / 已释放」（有人动过），
// 这条是**什么都没发生**——只有时间走完了。只校验「令牌是最新代 + 持有者
// 身份」的实现对它无感：令牌没变、身份没变，于是 GC 停顿超过 TTL 的持有者
// 回来一续就复活了租约——而它上一次施加的结果可能正是「已生效但账没记上」，
// 复活的租约让那次尝试继续推进，绕开 Acquire 的「过期 → 结果未知先对账」。
//
// 断言分两层：续期被拒（ErrFenceStale），且**没有被续命**——同 Key 再 Acquire
// 必须走过期路径转未知态（若 Renew 偷偷把意图续上，这里会拿到 contended）。
func runRenewAfterExpiryRejected(t *testing.T, factory Factory) {
	t.Helper()
	st, clock := newStore(t, factory)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// 时间走完，且**不发生任何新的 Acquire**（没有接管者动过这道闸）。
	clock.Advance(2 * time.Minute)

	if _, err := st.Renew(ctx, a, lease, time.Minute); !errors.Is(err, sideeffect.ErrFenceStale) {
		t.Fatalf("过期且无人接管时续期必须被拒为 ErrFenceStale，实得 %v", err)
	}

	// 没有被续命：意图仍是过期未裁决 ⇒ 同 Key 再 Acquire 转入需对账态。
	// （若上面的 Renew 成功把意图续上，这里会是 contended——这层断言
	// 抓的就是「续期写成功了」这种坏实现。）
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("过期后的 Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("过期且未裁决的尝试应转入需对账态（续期不得把它续活），实得 %v", outcome)
	}
}

func runRenewAfterReleaseRejected(t *testing.T, factory Factory) {
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

	if _, err := st.Renew(ctx, a, lease, time.Minute); !errors.Is(err, sideeffect.ErrFenceStale) {
		t.Fatalf("释放后的租约不得再续期（应被拒为 ErrFenceStale），实得 %v", err)
	}
}
