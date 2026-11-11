// effect_acquire.go —— Acquire：三判合一的一个事务。
//
// 判定顺序是契约里最要紧的一行：**已完成 > 结果未知 > 执行中 > 资源被占 > 授予**。
// 顺序写反的代价是重复施加：把「结果未知」排到「资源被占」之后，调用方会拿到
// contended（以为「晚点重试就行」），于是重试发生、重复施加随之发生。
//
// 为什么整段在一个事务里：三判合一的每一步都读同一份状态，行锁保证第二个
// 进程要么读到判定前的旧状态（然后在锁上等），要么读到判定后的新状态——
// 不存在「两个进程同时读到没做过、资源空闲」的窗口。
package pgstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// Acquire 一次原子判定（契约语义逐条对照 sideeffect.Store 的文档）。
//
// 授予时在同一事务里做三件事：令牌 = 资源已见最大值 + 1；持有者身份由
// sideeffect.MintHolder() 在**同一事务**里铸造；执行意图（本次令牌 + 到期
// 时刻）随授予一并落库。三者分开写就等于没写：中间死掉 ⇒ 意图悬空或没写，
// 重复施加的入口重新打开。
func (s *EffectStore) Acquire(ctx context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	now := s.clock()
	return withinTx3(ctx, s.pool, func(tx pgx.Tx) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
		if err := ensureResourceRow(ctx, tx, act.Resource); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
		}
		if err := ensureActionRow(ctx, tx, act.Key); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
		}
		rr, err := lockResource(ctx, tx, act.Resource)
		if err != nil {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
		}
		a, err := lockAction(ctx, tx, act.Key)
		if err != nil {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
		}

		// 1. 已完成 → 重放既有结果（不再需要令牌）。
		if a.done {
			var res sideeffect.Result
			if a.result != nil {
				res.Payload = append([]byte(nil), *a.result...)
			}
			return sideeffect.OutcomeReplayed, sideeffect.Lease{}, res, nil
		}

		// 2. 结果未知 → 硬阻塞（先对账，不是等一等）。
		if a.uncertain {
			return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
		}

		// 2b. 对账占用未过期 ⇒ 对账中：仍属未知态一族。过期的占用在这里清掉
		// （可重入：对账者死亡不永久占住这个 Key）。
		if a.hasRecon() {
			if now.Before(*a.reconExpires) {
				return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
			}
			a.reconHolder, a.reconExpires = nil, nil
			if err := writeAction(ctx, tx, act.Key, a); err != nil {
				return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
			}
		}

		// 3. 执行中：这次尝试还没裁决 ⇒ 不得重新授予执行权。
		if a.hasIntent() {
			if now.Before(*a.intentExpires) {
				return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
			}
			// 租约过期 ⇒ 这次尝试的结果未知（可能已施加、账没记上）。
			// 意图与资源持有是同一次尝试的两个投影，同刻到期：作废意图时，
			// 若资源闸仍是这次尝试的那份，一并作废（没人接管时令牌没变，
			// 不清的话「尝试已转入未知态」在资源侧看不出来）。
			irResource, irHolder, irFence := *a.intentResource, "", *a.intentFence
			if a.intentHolder != nil {
				irHolder = *a.intentHolder
			}
			a.intentResource, a.intentFence, a.intentHolder, a.intentExpires = nil, nil, nil, nil
			if irResource == act.Resource && rr.holder != nil && *rr.holder == irHolder && rr.fence == irFence {
				rr.holder = nil
				if err := writeResource(ctx, tx, act.Resource, rr); err != nil {
					return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
				}
			}
			if act.RetrySafety != sideeffect.RetrySafetySafe {
				a.uncertain = true
				a.reason = "上一次尝试的租约已过期，结果未知"
				if err := writeAction(ctx, tx, act.Key, a); err != nil {
					return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
				}
				return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
			}
		}

		// 4. 资源被他人持有且租约未过期 → 可重试的拒绝。
		if rr.held(now) {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
		}

		// 5. 授予：令牌单调递增 + 铸造持有者身份 + 登记执行意图，同一事务。
		rr.fence++
		holder := sideeffect.MintHolder()
		rr.holder = &holder
		until := now.Add(ttl)
		rr.until = &until
		if err := writeResource(ctx, tx, act.Resource, rr); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
		}
		intentResource := act.Resource
		intentFence := rr.fence
		a.intentResource = &intentResource
		a.intentFence = &intentFence
		a.intentHolder = &holder
		a.intentExpires = &until
		if err := writeAction(ctx, tx, act.Key, a); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
		}
		return sideeffect.OutcomeApplied,
			sideeffect.Lease{Resource: act.Resource, Fence: sideeffect.Fence(rr.fence), Holder: holder, TTL: ttl},
			sideeffect.Result{}, nil
	})
}

// writeResource 把资源级状态写回（调用方持行锁，无条件写即可）。
func writeResource(ctx context.Context, tx pgx.Tx, resource string, rr *resourceRow) error {
	_, err := tx.Exec(ctx,
		`UPDATE pgref_effect_resource SET fence = $2, holder = $3, until = $4 WHERE resource = $1`,
		resource, rr.fence, rr.holder, rr.until)
	if err != nil {
		return errors.Wrap(err, "pgstore: 写资源行失败", "resource", resource)
	}
	return nil
}

// writeAction 把 Key 级状态写回（调用方持行锁，无条件写即可）。
func writeAction(ctx context.Context, tx pgx.Tx, key sideeffect.Key, a *actionRow) error {
	_, err := tx.Exec(ctx,
		`UPDATE pgref_effect_action
		    SET done = $2, result = $3, uncertain = $4, reason = $5,
		        intent_resource = $6, intent_fence = $7, intent_holder = $8, intent_expires = $9,
		        recon_holder = $10, recon_expires = $11
		  WHERE key = $1`,
		string(key), a.done, a.result, a.uncertain, a.reason,
		a.intentResource, a.intentFence, a.intentHolder, a.intentExpires,
		a.reconHolder, a.reconExpires)
	if err != nil {
		return errors.Wrap(err, "pgstore: 写动作行失败", "key", string(key))
	}
	return nil
}
