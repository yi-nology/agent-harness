// effect_reconcile.go —— 副作用闸参考实现的对账占用部分。
//
// BeginReconcile 与裁决的关系：对账是跨进程查下游的慢动作，查询期间
// 别人不得重复查。占用过期可重入（对账者死亡不永久占住这个 Key）；
// 裁决（ResolveUncertain）清除占用。「对账中」在 Acquire 的判定里归入
// 未知态一族：对账中 ≠ 已裁决，更不等于可以重做。
package main

import (
	"context"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// reconRecord 一次对账认领。
type reconRecord struct {
	holder  string
	expires time.Time
}

// BeginReconcile 认领对账权：同一 Key 同一时刻至多一个对账者。
//
// 占用过期可重新认领（可重入）——对账者死亡不永久占住这个 Key。
// 认领期间该 Key 仍是未知态（Acquire 继续拒绝），裁决走 ResolveUncertain。
func (s *effectStore) BeginReconcile(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Reconcile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.actLocked(act.Key)
	if rec.done {
		return sideeffect.Reconcile{}, sideeffect.ErrNotUncertain
	}
	if !rec.uncertain {
		return sideeffect.Reconcile{}, sideeffect.ErrNotUncertain
	}
	now := s.clock()
	if rec.recon != nil && now.Before(rec.recon.expires) {
		return sideeffect.Reconcile{}, sideeffect.ErrReconcileTaken
	}
	holder := sideeffect.MintHolder()
	rec.recon = &reconRecord{holder: holder, expires: now.Add(ttl)}
	return sideeffect.Reconcile{Holder: holder, ExpiresAt: now.Add(ttl)}, nil
}
