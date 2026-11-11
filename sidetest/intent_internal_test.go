package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是套件的**负对照**（执行意图一族）：证明这些场景真的有分辨力。
//
// # 为什么必须钉这条
//
// 套件判的是**宿主实现**。若场景本身写松了——比如只判「Acquire 没报错」
// 而不判它返回什么——那么一个把「执行意图」整个摘掉的实现照样全绿，
// 宿主就会拿到一句「你的实现符合契约」，而实际上重复施加的门是敞开的。
//
// 这正是本仓反复吃过的亏：**「从没见过它触发」的规则等于没有规则**。
// 套件也一样——「从没红过」的套件等于没在判。

// brokenIntentStore 一个**在 Acquire 里不登记、不查执行意图**的存储实现。
//
// 它模拟的是「意图这条判据整个不存在」的真实形态：抢闸就是放行，
// 既不问「这个 Key 上有没有未裁决的尝试」，也不登记「本次尝试还在」。
type brokenIntentStore struct {
	sideeffect.Store

	fence sideeffect.Fence
}

func (b *brokenIntentStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	b.fence++
	return sideeffect.OutcomeApplied,
		sideeffect.Lease{Resource: act.Resource, Fence: b.fence, TTL: ttl},
		sideeffect.Result{}, nil
}

// Release / Abandon 走空实现：这个坏实现坏在**Acquire 不认意图**，
// 它的收尾操作一律「成功」——正是「顺手就写出来」的那种坏法。
func (b *brokenIntentStore) Release(context.Context, sideeffect.Action, sideeffect.Lease) error {
	return nil
}

func (b *brokenIntentStore) Abandon(context.Context, sideeffect.Action, sideeffect.Lease, string) error {
	return nil
}

// TestIntentScenarioRejectsBrokenStore 负对照：摘掉执行意图判据的实现，
// 必须被「执行意图」一族场景判红。
func TestIntentScenarioRejectsBrokenStore(t *testing.T) {
	broken := &brokenIntentStore{}

	if err := checkIntentBlocksReacquire(broken); err == nil {
		t.Fatal("坏实现（Acquire 无视执行意图）应被场景判红，套件却判它合规")
	}
	if err := checkExpiredIntentRequiresReconcile(broken, NewFakeClock()); err == nil {
		t.Fatal("坏实现（Acquire 无视执行意图）应被「过期⇒需对账」场景判红，套件却判它合规")
	}
	if err := checkReleaseKeepsIntent(broken); err == nil {
		t.Fatal("坏实现（释放即撤销意图）应被场景判红，套件却判它合规")
	}
}
