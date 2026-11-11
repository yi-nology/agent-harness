// effect_reconcile.go —— 对账侧：ResolveUncertain 与 BeginReconcile。
//
// 裁决**不校验栅栏**（契约硬要求）：它的依据是**下游事实**，不是持有权——
// 原持有者可能已经死亡、租约早已过期，而对账恰恰要在这种情况下进行。
// 占用与裁决的关系：占用过期可重入（对账者死亡不永久占住）；裁决成功即
// 清除占用（这次尝试已有结论）。
package pgstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// ResolveUncertain 裁决「结果未知」的键。**先到者为准**。
//
// RetrySafetyUnsafe 的动作：未生效的裁决不解除阻塞（ErrUnsafeNotRetryable），
// 必须 Resolution.ForceRetryable 的显式决策才放行——「不可重试」是判据，
// 不是注释。
func (s *EffectStore) ResolveUncertain(ctx context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	return withinTx2(ctx, s.pool, func(tx pgx.Tx) (sideeffect.Outcome, sideeffect.Result, error) {
		if err := ensureActionRow(ctx, tx, act.Key); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Result{}, err
		}
		a, err := lockAction(ctx, tx, act.Key)
		if err != nil {
			return sideeffect.OutcomeContended, sideeffect.Result{}, err
		}
		if a.done {
			var prior sideeffect.Result
			if a.result != nil {
				prior.Payload = append([]byte(nil), *a.result...)
			}
			return sideeffect.OutcomeReplayed, prior, nil
		}
		if !a.uncertain {
			return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrNotUncertain
		}
		// 裁决即结论：执行意图与对账占用一并撤销（这次尝试已有结论）。
		a.intentResource, a.intentFence, a.intentHolder, a.intentExpires = nil, nil, nil, nil
		a.reconHolder, a.reconExpires = nil, nil
		if res.Applied {
			payload := append([]byte(nil), res.Result.Payload...)
			a.done = true
			a.uncertain = false
			a.result = &payload
			if err := writeAction(ctx, tx, act.Key, a); err != nil {
				return sideeffect.OutcomeContended, sideeffect.Result{}, err
			}
			return sideeffect.OutcomeReplayed, res.Result, nil
		}
		if act.RetrySafety == sideeffect.RetrySafetyUnsafe && !res.ForceRetryable {
			// 「不可重试」的动作，一句来自对账的「未生效」不足以重新开门：
			// 那句结论来自宿主的一次查询，查询可能出错，查错的代价是重复施加。
			// 未知标记**原样保留**（这个 Key 仍被硬阻塞），放行必须显式签字。
			if err := writeAction(ctx, tx, act.Key, a); err != nil {
				return sideeffect.OutcomeContended, sideeffect.Result{}, err
			}
			return sideeffect.OutcomeUncertain, sideeffect.Result{}, sideeffect.ErrUnsafeNotRetryable
		}
		a.uncertain = false
		if err := writeAction(ctx, tx, act.Key, a); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Result{}, err
		}
		return sideeffect.OutcomeApplied, sideeffect.Result{}, nil
	})
}

// BeginReconcile 认领对账权：同一 Key 同一时刻至多一个对账者。
//
// 仅当该 Key 处于未知态时接受认领；占用未过期时拒绝（ErrReconcileTaken）；
// 占用过期可重新认领（可重入）——对账者死亡不永久占住这个 Key。
func (s *EffectStore) BeginReconcile(ctx context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Reconcile, error) {
	now := s.clock()
	return withinTx(ctx, s.pool, func(tx pgx.Tx) (sideeffect.Reconcile, error) {
		if err := ensureActionRow(ctx, tx, act.Key); err != nil {
			return sideeffect.Reconcile{}, err
		}
		a, err := lockAction(ctx, tx, act.Key)
		if err != nil {
			return sideeffect.Reconcile{}, err
		}
		if a.done || !a.uncertain {
			return sideeffect.Reconcile{}, sideeffect.ErrNotUncertain
		}
		if a.hasRecon() && now.Before(*a.reconExpires) {
			return sideeffect.Reconcile{}, sideeffect.ErrReconcileTaken
		}
		holder := sideeffect.MintHolder()
		expires := now.Add(ttl)
		a.reconHolder = &holder
		a.reconExpires = &expires
		if err := writeAction(ctx, tx, act.Key, a); err != nil {
			return sideeffect.Reconcile{}, err
		}
		return sideeffect.Reconcile{Holder: holder, ExpiresAt: expires}, nil
	})
}
