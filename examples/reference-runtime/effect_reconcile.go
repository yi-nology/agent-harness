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

	"github.com/oekit/pkg/errors"

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
	if ttl <= 0 {
		return sideeffect.Reconcile{}, errors.Wrap(sideeffect.ErrInvalidTTL,
			"对账认领时长必须为正（零/负 TTL 造出出生即过期的认领）",
			"key", string(act.Key))
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.actLocked(act)
	if err != nil {
		return sideeffect.Reconcile{}, err
	}
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

// MarkUncertain 把该键标记为「结果未知」（按键，与资源租约无关）。
//
// # 标记写失败必须保住执行意图
//
// 本实现里「校验 → 改状态」在一个临界区内完成：任何一步失败都发生在
// **任何状态被改之前**——意图原样留在存储里，后续 Acquire 会被意图挡住。
// 这不是实现自由，是契约硬要求（Store.MarkUncertain 第 5 条）：真实宿主
// 落到数据库上时，标记与撤销意图**不得**「先撤意图、再标未知」分两步写
// ——第二步失败，存储侧就什么都不剩，另一个 worker 直接重放。
func (s *effectStore) MarkUncertain(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.holdOwnerErr(act, lease); err != nil {
		return err
	}
	if err := s.intentOwnerErr(act, lease); err != nil {
		return err
	}
	rec, err := s.actLocked(act)
	if err != nil {
		return err
	}
	rec.uncertain = true
	rec.reason = reason
	rec.intent = nil
	return nil
}
