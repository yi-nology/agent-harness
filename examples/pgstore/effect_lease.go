// effect_lease.go —— 凭据一族：Complete / Release / Abandon / Renew / MarkUncertain。
//
// 五个方法共用同一套纪律：
//   - 校验与写入在**同一个事务**里 ⇒ 校验失败即整体回滚，存储侧不剩半截状态；
//   - 资源行 → 动作行的固定锁序（见 effect.go）；
//   - 三项联查不过即拒（ErrFenceStale / ErrNotHolder / ErrResourceMismatch），
//     拒绝时**不落任何一行**。
package pgstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// lockPair 一次事务的行读取：资源行在前、动作行在后（锁序恒定）。
func lockPair(ctx context.Context, tx pgx.Tx, act sideeffect.Action) (*resourceRow, *actionRow, error) {
	if err := ensureResourceRow(ctx, tx, act.Resource); err != nil {
		return nil, nil, err
	}
	if err := ensureActionRow(ctx, tx, act); err != nil {
		return nil, nil, err
	}
	rr, err := lockResource(ctx, tx, act.Resource)
	if err != nil {
		return nil, nil, err
	}
	a, err := lockAction(ctx, tx, act)
	if err != nil {
		return nil, nil, err
	}
	return rr, a, nil
}

// Complete 标记该键的动作已完成并留存结果。**先到者为准**。
//
// 幂等：同一 Key 重复 Complete 不报错，返回首次记录的结果（后到者不覆盖）。
func (s *EffectStore) Complete(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	return withinTx(ctx, s.pool, func(tx pgx.Tx) (sideeffect.Result, error) {
		rr, a, err := lockPair(ctx, tx, act)
		if err != nil {
			return sideeffect.Result{}, err
		}
		if a.done {
			// 幂等：先到者的结果不被后到者覆盖（此检查在凭据校验之前，
			// 与内存版一致——已完成的事实不因出示者过期而不可见）。
			var prior sideeffect.Result
			if a.result != nil {
				prior.Payload = append([]byte(nil), *a.result...)
			}
			return prior, nil
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return sideeffect.Result{}, err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return sideeffect.Result{}, err
		}
		payload := append([]byte(nil), res.Payload...)
		a.done = true
		a.result = &payload
		a.intentResource, a.intentFence, a.intentHolder, a.intentExpires = nil, nil, nil, nil
		if err := writeAction(ctx, tx, act.Key, a); err != nil {
			return sideeffect.Result{}, err
		}
		// 动作已完成 ⇒ 资源闸释放（否则后来者会被「资源正忙」挡住）。
		rr.holder = nil
		if err := writeResource(ctx, tx, act.Resource, rr); err != nil {
			return sideeffect.Result{}, err
		}
		return res, nil
	})
}

// Release 释放**资源持有**，**不动**该键的执行意图。
//
// 意图没被裁决之前，这个键不得再被执行——否则「施加成功但进程死在记账
// 之前」会被下一次 Acquire 变成重复施加。两条出口（Abandon / Release）
// 不可互相替代，见 Store.Release 的契约注释。
func (s *EffectStore) Release(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	_, err := withinTx(ctx, s.pool, func(tx pgx.Tx) (bool, error) {
		rr, _, err := lockPair(ctx, tx, act)
		if err != nil {
			return false, err
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return false, err
		}
		rr.holder = nil
		return false, writeResource(ctx, tx, act.Resource, rr)
	})
	return err
}

// Abandon 显式放弃本次尝试：撤销执行意图，并释放（仍是自己的）资源持有。
//
// 资源闸只在仍是自己的时才放——易主了就不碰（那是新持有者的闸）。
func (s *EffectStore) Abandon(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	_, err := withinTx(ctx, s.pool, func(tx pgx.Tx) (bool, error) {
		rr, a, err := lockPair(ctx, tx, act)
		if err != nil {
			return false, err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return false, err
		}
		a.intentResource, a.intentFence, a.intentHolder, a.intentExpires = nil, nil, nil, nil
		if err := writeAction(ctx, tx, act.Key, a); err != nil {
			return false, err
		}
		if holdOwnerErr(act, lease, rr) == nil {
			rr.holder = nil
			if err := writeResource(ctx, tx, act.Resource, rr); err != nil {
				return false, err
			}
		}
		return false, nil
	})
	return err
}

// Renew 续期并**同时确认持有资格**：闸已易主、尝试已失效**或租约已过期**
// 则拒绝续期（假续期是重复施加的入口）。
func (s *EffectStore) Renew(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	return withinTx(ctx, s.pool, func(tx pgx.Tx) (sideeffect.Lease, error) {
		rr, a, err := lockPair(ctx, tx, act)
		if err != nil {
			return sideeffect.Lease{}, err
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return sideeffect.Lease{}, err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return sideeffect.Lease{}, err
		}
		// 过期即资格终止：租约已过期而无人接管时，旧持有者不得复活它。
		// 判定读**行锁之后**的当前时刻（nowIn），拿锁期间的时间流逝不被
		// 计入租约寿命；过期的尝试本该由 Acquire 转入未知态先对账，
		// 续期放行就绕开了那条判据（Store.Renew 契约「过期即资格终止」）。
		now, err := s.nowIn(ctx, tx)
		if err != nil {
			return sideeffect.Lease{}, err
		}
		if rr.until == nil || !rr.until.After(now) {
			return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale,
				"租约已过期且无人接管——到期即资格终止（要继续做请重新 Acquire）",
				"key", string(act.Key), "resource", act.Resource)
		}
		until := now.Add(ttl)
		rr.until = &until
		if err := writeResource(ctx, tx, act.Resource, rr); err != nil {
			return sideeffect.Lease{}, err
		}
		a.intentExpires = &until
		if err := writeAction(ctx, tx, act.Key, a); err != nil {
			return sideeffect.Lease{}, err
		}
		return sideeffect.Lease{Resource: act.Resource, Fence: lease.Fence, Holder: lease.Holder, TTL: ttl}, nil
	})
}

// MarkUncertain 把该键标记为「结果未知」（按键，与资源租约无关）。
//
// # 标记写失败必须保住执行意图（契约硬要求第 5 条）
//
// 本实现「校验 → 改状态」在一个事务里完成：任何一步失败都发生在**任何状态
// 被改之前**，整体回滚 ⇒ 意图原样留在存储里，后续 Acquire 被意图挡住。
// 这不是实现自由：落到数据库上时，标记与撤销意图**不得**「先撤意图、再标
// 未知」分两步提交——第二步失败，存储侧就什么都不剩，另一个 worker 直接重放。
//
// 标记成功后意图一并撤销（这次尝试已有结论：未知），但**闸不自动释放**
// ——调用方还要显式调 Release 只放资源。
func (s *EffectStore) MarkUncertain(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	_, err := withinTx(ctx, s.pool, func(tx pgx.Tx) (bool, error) {
		rr, a, err := lockPair(ctx, tx, act)
		if err != nil {
			return false, err
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return false, err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return false, err
		}
		a.uncertain = true
		a.reason = reason
		a.intentResource, a.intentFence, a.intentHolder, a.intentExpires = nil, nil, nil, nil
		return false, writeAction(ctx, tx, act.Key, a)
	})
	return err
}
