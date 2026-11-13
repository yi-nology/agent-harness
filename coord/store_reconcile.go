// store_reconcile.go —— Store 适配器的裁决侧（ResolveUncertain / BeginReconcile）。
// 从 store.go 拆出：裁决与对账占用是「结果未知」一族的方法面，与施加侧
// （Acquire/Complete/...）的判定性质不同——裁决**不校验栅栏**（依据是下游
// 事实，不是持有权），单独成文件把这个差别写在眼前。
package coord

import (
	"context"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// resolveOut ResolveUncertain 的复合结论：业务错误随事务一起落地——
// Unsafe 拒放行那条路径**先提交**「意图/占用撤销」，再返回错误
// （与参考实现的内存版语义逐字对齐：撤销发生在裁决被受理的那一刻）。
type resolveOut struct {
	outcome sideeffect.Outcome
	res     sideeffect.Result
	bizErr  error
}

// ResolveUncertain 裁决「结果未知」的 Key（先到者为准；**不校验栅栏**——
// 它的依据是下游事实，不是持有权，对账恰恰要在持有者死亡后进行）。
// 简化入口不校验对账凭据——短对账无须认领，跨进程长对账走下面的
// ResolveUncertainOwned。
func (s *StoreAdapter) ResolveUncertain(ctx context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	return s.resolveWithin(ctx, act, res, "")
}

// ResolveUncertainOwned 带对账占用凭据的裁决（OwnedResolveStore）：
// holder 不是**当前有效**对账认领的持有者 → ErrReconcileLost，不改任何状态。
//
// 校验与裁决同一事务：CAS 断言保证判定读到的占用就是裁决要落的那份——
// 「校验时还有效、裁决前已被接管」的窗口由断言失败重判兜住。
func (s *StoreAdapter) ResolveUncertainOwned(ctx context.Context, act sideeffect.Action, res sideeffect.Resolution, holder string) (sideeffect.Outcome, sideeffect.Result, error) {
	if holder == "" {
		return sideeffect.OutcomeContended, sideeffect.Result{}, errors.Wrap(sideeffect.ErrReconcileLost,
			"空凭据不是任何一次对账认领", "key", string(act.Key))
	}
	return s.resolveWithin(ctx, act, res, holder)
}

// resolveWithin 裁决主体。requiredHolder 非空时先校验对账占用凭据
// （已完成的重放无害，先于凭据校验——读既有事实不问资格）。
func (s *StoreAdapter) resolveWithin(ctx context.Context, act sideeffect.Action, res sideeffect.Resolution, requiredHolder string) (sideeffect.Outcome, sideeffect.Result, error) {
	ak := actKeyPrefix + string(act.Key)
	out, err := casLoop(ctx, s.b, []string{ak},
		func(snap map[string]Entry) (resolveOut, []Assert, []Mutation, error) {
			a, err := decodeAct(snap[ak])
			if err != nil {
				return resolveOut{}, nil, nil, err
			}
			if a.Done {
				return resolveOut{outcome: sideeffect.OutcomeReplayed,
					res: sideeffect.Result{Payload: a.Result}}, nil, nil, nil
			}
			if requiredHolder != "" {
				// 带凭据的裁决：占用必须存在、未过期、身份匹配——否则
				// 出示的是迟到对账者的旧凭据，拒之且不改任何状态。
				now := s.clock()
				if a.Recon == nil || !now.Before(a.Recon.Expires) || a.Recon.Holder != requiredHolder {
					return resolveOut{}, nil, nil, errors.Wrap(sideeffect.ErrReconcileLost,
						"出示的对账认领不是当前有效的一份（已过期或已被接管）",
						"key", string(act.Key))
				}
			}
			if !a.Uncertain {
				return resolveOut{outcome: sideeffect.OutcomeContended,
					bizErr: sideeffect.ErrNotUncertain}, nil, nil, nil
			}
			// 裁决即结论：执行意图与对账占用一并撤销（这次尝试已有结论）。
			a.Intent = nil
			a.Recon = nil
			switch {
			case res.Applied:
				a.Done = true
				a.Uncertain = false
				a.Result = res.Result.Payload
			case act.RetrySafety == sideeffect.RetrySafetyUnsafe && !res.ForceRetryable:
				// 「不可重试」的动作，一句来自对账的「未生效」不足以重新开门：
				// 未知标记**原样保留**（这个 Key 仍被硬阻塞），放行必须显式签字。
			default:
				a.Uncertain = false
			}
			am, merr := actMutation(act.Key, a)
			if merr != nil {
				return resolveOut{}, nil, nil, merr
			}
			biz := error(nil)
			out := sideeffect.OutcomeApplied
			switch {
			case res.Applied:
				out = sideeffect.OutcomeReplayed
			case a.Uncertain:
				out = sideeffect.OutcomeUncertain
				biz = sideeffect.ErrUnsafeNotRetryable
			}
			return resolveOut{outcome: out, res: sideeffect.Result{Payload: a.Result}, bizErr: biz},
				assertsFrom(snap), []Mutation{am}, nil
		})
	if err != nil {
		return sideeffect.OutcomeUncertain, sideeffect.Result{}, err
	}
	return out.outcome, out.res, out.bizErr
}

// BeginReconcile 认领对账权（同一 Key 同一时刻至多一个对账者；
// 占用过期可重入——对账者死亡不永久占住这个 Key）。
func (s *StoreAdapter) BeginReconcile(ctx context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Reconcile, error) {
	// 入参判据先行（与状态无关，拒了就什么都没发生）：零/负 TTL 造出的是
	// 出生即过期的认领——承诺从未生效，等于没有占用。
	if ttl <= 0 {
		return sideeffect.Reconcile{}, errors.Wrap(sideeffect.ErrInvalidTTL,
			"对账认领时长必须为正（零/负 TTL 造出出生即过期的认领）",
			"key", string(act.Key))
	}
	ak := actKeyPrefix + string(act.Key)
	type beginOut struct{ recon sideeffect.Reconcile }
	out, err := casLoop(ctx, s.b, []string{ak},
		func(snap map[string]Entry) (beginOut, []Assert, []Mutation, error) {
			a, err := decodeAct(snap[ak])
			if err != nil {
				return beginOut{}, nil, nil, err
			}
			if a.Done || !a.Uncertain {
				return beginOut{}, nil, nil, sideeffect.ErrNotUncertain
			}
			now := s.clock()
			if a.Recon != nil && now.Before(a.Recon.Expires) {
				return beginOut{}, nil, nil, sideeffect.ErrReconcileTaken
			}
			holder := sideeffect.MintHolder()
			a.Recon = &reconRec{Holder: holder, Expires: now.Add(ttl)}
			am, merr := actMutation(act.Key, a)
			if merr != nil {
				return beginOut{}, nil, nil, merr
			}
			return beginOut{recon: sideeffect.Reconcile{Holder: holder, ExpiresAt: now.Add(ttl)}},
				assertsFrom(snap), []Mutation{am}, nil
		})
	if err != nil {
		return sideeffect.Reconcile{}, err
	}
	return out.recon, nil
}

// 编译期契约断言：带凭据的裁决入口是 sidetest 硬要求（缺失即套件硬失败）。
var _ sideeffect.OwnedResolveStore = (*StoreAdapter)(nil)
