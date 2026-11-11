package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「重试安全三档」的**负对照**：证明这个场景真的有分辨力。
//
// brokenUnsafeStore 模拟「把 Unsafe 写回与 Unknown 同一条路径」的实现形态：
// ResolveUncertain 对 Applied=false 一律清除未知标记放行——不管动作声明了
// 什么。「不可重试」在它的世界观里只是注释。这正是 #89 点名的那种坏法：
// 改一行分支不会有任何用例变红——本负对照就是那条用例。
type brokenUnsafeStore struct {
	sideeffect.Store

	fence     sideeffect.Fence
	uncertain map[sideeffect.Key]bool
}

func newBrokenUnsafeStore() *brokenUnsafeStore {
	return &brokenUnsafeStore{uncertain: map[sideeffect.Key]bool{}}
}

func (b *brokenUnsafeStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	if b.uncertain[act.Key] {
		return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	b.fence++
	return sideeffect.OutcomeApplied,
		sideeffect.Lease{Resource: act.Resource, Fence: b.fence, Holder: "broken", TTL: ttl},
		sideeffect.Result{}, nil
}

func (b *brokenUnsafeStore) MarkUncertain(_ context.Context, act sideeffect.Action, _ sideeffect.Lease, _ string) error {
	b.uncertain[act.Key] = true
	return nil
}

func (b *brokenUnsafeStore) Release(context.Context, sideeffect.Action, sideeffect.Lease) error {
	return nil
}

func (b *brokenUnsafeStore) ResolveUncertain(_ context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	if res.Applied {
		delete(b.uncertain, act.Key)
		return sideeffect.OutcomeReplayed, res.Result, nil
	}
	// 坏就坏在这里：不看 RetrySafety，未生效一律放行。
	delete(b.uncertain, act.Key)
	return sideeffect.OutcomeApplied, sideeffect.Result{}, nil
}

// TestUnsafeScenarioRejectsBrokenStore 负对照：把 Unsafe 与 Unknown 同路的
// 实现，必须被场景判红。
func TestUnsafeScenarioRejectsBrokenStore(t *testing.T) {
	broken := newBrokenUnsafeStore()

	if err := checkUnsafeResolveStaysBlocked(broken); err == nil {
		t.Fatal("坏实现（Unsafe 与 Unknown 同路放行）应被场景判红，套件却判它合规")
	}
}
