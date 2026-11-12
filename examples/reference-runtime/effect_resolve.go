// effect_resolve.go —— 副作用闸参考实现的裁决部分（ResolveUncertain 一族）。
//
// 裁决「结果未知」的 Key：简化入口（先到者为准，短对账无须占用）与
// 带对账占用凭据的入口（OwnedResolveStore——跨进程长对账的迟到提交被拒）。
// 判据契约见 sideeffect.Store.ResolveUncertain 与 OwnedResolveStore。
package main

import (
	"context"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// ResolveUncertain 裁决「结果未知」的键。**先到者为准**（简化入口，
// 不校验对账凭据——短对账无须认领，见 OwnedResolveStore）。
//
// RetrySafetyUnsafe 的动作：未生效的裁决不解除阻塞（ErrUnsafeNotRetryable），
// 必须 Resolution.ForceRetryable 的显式决策才放行——「不可重试」是判据，
// 不是注释。
func (s *effectStore) ResolveUncertain(_ context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.resolveWithin(act, res, "")
}

// ResolveUncertainOwned 带对账占用凭据的裁决（OwnedResolveStore）：
// holder 不是**当前有效**对账认领的持有者 → ErrReconcileLost，不改状态。
func (s *effectStore) ResolveUncertainOwned(_ context.Context, act sideeffect.Action, res sideeffect.Resolution, holder string) (sideeffect.Outcome, sideeffect.Result, error) {
	if holder == "" {
		return sideeffect.OutcomeContended, sideeffect.Result{}, errors.Wrap(sideeffect.ErrReconcileLost,
			"空凭据不是任何一次对账认领", "key", string(act.Key))
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.resolveWithin(act, res, holder)
}

// resolveWithin 裁决主体。requiredHolder 非空时先校验对账占用凭据
// （已完成的重放无害，先于凭据校验——读既有事实不问资格）。
func (s *effectStore) resolveWithin(act sideeffect.Action, res sideeffect.Resolution, requiredHolder string) (sideeffect.Outcome, sideeffect.Result, error) {
	rec, err := s.actLocked(act)
	if err != nil {
		return sideeffect.OutcomeContended, sideeffect.Result{}, err
	}
	if rec.done {
		return sideeffect.OutcomeReplayed, rec.result, nil
	}
	if requiredHolder != "" {
		now := s.clock()
		if rec.recon == nil || !now.Before(rec.recon.expires) || rec.recon.holder != requiredHolder {
			return sideeffect.OutcomeContended, sideeffect.Result{}, errors.Wrap(sideeffect.ErrReconcileLost,
				"出示的对账认领不是当前有效的一份（已过期或已被接管）",
				"key", string(act.Key))
		}
	}
	if !rec.uncertain {
		return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrNotUncertain
	}
	// 裁决即结论：执行意图与对账占用一并撤销（这次尝试已有结论）。
	rec.intent = nil
	rec.recon = nil
	if res.Applied {
		rec.done = true
		rec.uncertain = false
		rec.result = res.Result
		return sideeffect.OutcomeReplayed, rec.result, nil
	}
	if act.RetrySafety == sideeffect.RetrySafetyUnsafe && !res.ForceRetryable {
		// 「不可重试」的动作，一句来自对账的「未生效」不足以重新开门：
		// 那句结论来自宿主的一次查询，查询可能出错，查错的代价是重复施加。
		// 未知标记**原样保留**（这个 Key 仍被硬阻塞），放行必须显式签字。
		return sideeffect.OutcomeUncertain, sideeffect.Result{}, sideeffect.ErrUnsafeNotRetryable
	}
	rec.uncertain = false
	return sideeffect.OutcomeApplied, sideeffect.Result{}, nil
}
