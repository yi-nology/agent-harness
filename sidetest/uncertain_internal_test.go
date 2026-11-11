package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是套件的**负对照**：证明「结果未知」一族的场景真的有分辨力。
//
// # 为什么必须钉这条
//
// 套件判的是**宿主实现**。若场景本身写松了——比如只判「Acquire 没报错」
// 而不判它返回什么——那么一个把未知态当空气的实现照样全绿，
// 宿主就会拿到一句「你的实现符合契约」，而实际上重复施加的门是敞开的。
//
// 这正是本仓反复吃过的亏：**「从没见过它触发」的规则等于没有规则**。
// 套件也一样——「从没红过」的套件等于没在判。
//
// 故这里构造一个**故意写坏**的实现，确认场景会把它判红。

// brokenUncertainStore 一个**在 Acquire 里不查未知标记**的存储实现。
//
// 它把「结果未知」这条判据整个摘掉了：标记照写，但下次抢闸照样放行。
type brokenUncertainStore struct {
	sideeffect.Store

	marked map[sideeffect.Key]bool
	fence  sideeffect.Fence
}

func (b *brokenUncertainStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	b.fence++
	return sideeffect.OutcomeApplied, sideeffect.Lease{Resource: act.Resource, Fence: b.fence, TTL: ttl}, sideeffect.Result{}, nil
}

func (b *brokenUncertainStore) MarkUncertain(_ context.Context, act sideeffect.Action, _ sideeffect.Lease, _ string) error {
	b.marked[act.Key] = true
	return nil
}

// TestUncertainScenarioRejectsBrokenStore 负对照：摘掉未知标记判据的实现，
// 必须被「结果未知」一族场景判红。
func TestUncertainScenarioRejectsBrokenStore(t *testing.T) {
	broken := &brokenUncertainStore{marked: map[sideeffect.Key]bool{}}
	if err := checkUncertainBlocksExecution(broken); err == nil {
		t.Fatal("坏实现（Acquire 无视未知标记）应被场景判红，套件却判它合规")
	}
	if !broken.marked["k-uncertain"] {
		t.Fatal("场景没有走到 MarkUncertain，说明负对照根本没触发判据")
	}
}
