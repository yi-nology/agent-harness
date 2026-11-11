package sidetest

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「对账占用」一族的**负对照**：证明这两个场景真的有分辨力。
//
// brokenReconcileStore 模拟「对账占用形同虚设」的实现形态：BeginReconcile
// 永远成功（没有「同一时刻至多一个对账者」的判据），而且顺手把未知标记
// 清掉——占用被它当成了「对账完成」，对账者死亡后这个 Key 直接放行重试。
type brokenReconcileStore struct {
	sideeffect.Store

	fence     sideeffect.Fence
	uncertain map[sideeffect.Key]bool
}

func newBrokenReconcileStore() *brokenReconcileStore {
	return &brokenReconcileStore{uncertain: map[sideeffect.Key]bool{}}
}

func (b *brokenReconcileStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	if b.uncertain[act.Key] {
		return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	b.fence++
	return sideeffect.OutcomeApplied,
		sideeffect.Lease{Resource: act.Resource, Fence: b.fence, Holder: "broken", TTL: ttl},
		sideeffect.Result{}, nil
}

func (b *brokenReconcileStore) MarkUncertain(_ context.Context, act sideeffect.Action, _ sideeffect.Lease, _ string) error {
	b.uncertain[act.Key] = true
	return nil
}

func (b *brokenReconcileStore) Release(context.Context, sideeffect.Action, sideeffect.Lease) error {
	return nil
}

func (b *brokenReconcileStore) BeginReconcile(_ context.Context, act sideeffect.Action, _ time.Duration) (sideeffect.Reconcile, error) {
	// 坏就坏在这里：认领即「完成」——未知标记被清掉，Key 放行重试。
	delete(b.uncertain, act.Key)
	return sideeffect.Reconcile{Holder: "broken", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// TestReconcileScenariosRejectBrokenStore 负对照：对账占用无判据的实现，
// 必须被这两个场景判红。
func TestReconcileScenariosRejectBrokenStore(t *testing.T) {
	broken := newBrokenReconcileStore()

	if err := checkReconcileTakenWhileActive(broken); err == nil {
		t.Fatal("坏实现（无占用判据）应被「占用挡住第二个对账者」场景判红，套件却判它合规")
	}
	if err := checkReconcileReentrantAfterExpiry(broken, NewFakeClock()); err == nil {
		t.Fatal("坏实现（占用被当成对账完成）应被「过期可重入且不放行」场景判红，套件却判它合规")
	}
}
