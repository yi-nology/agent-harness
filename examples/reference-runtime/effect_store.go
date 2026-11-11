// effect_store.go —— sideeffect.Store 的参考实现（内存版）。
//
// # 这份实现钉住的两条判据
//
//  1. **持有者校验**：Renew / Complete / MarkUncertain / Abandon 都必须校验
//     「资源闸仍归本次持有」——只比栅栏令牌是不够的。释放闸之后令牌**没有变**
//     （栅栏只在接管时递增），于是「释放后的租约」能拿同一枚令牌继续续期、
//     继续记账：闸已经易主，持有者却以为还拿着。
//  2. **执行意图**：Acquire 授予执行权时原子登记「本次尝试还在」；租约过期
//     且未裁决时，同一 Key 转入需对账态，而不是重新授予执行权。
//
// 两者都是「同一份状态的两个投影」在存储侧的落法：Key 级（这个动作做过没有）
// 与资源级（这个资源谁占着）各自原子，彼此不共享状态。
package main

import (
	"context"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// effectStore 副作用闸：幂等键 + 资源独占 + 栅栏令牌 + 执行意图。
type effectStore struct {
	mu sync.Mutex

	// now 是可注入时钟：过期判定必须读它，否则过期场景恒不触发（sidetest 的硬要求）。
	now func() time.Time

	acts map[sideeffect.Key]*actRecord
	ress map[string]*resRecord
}

// actRecord 一个幂等键的状态。
type actRecord struct {
	done      bool
	result    sideeffect.Result
	uncertain bool
	reason    string

	// intent 未裁决的执行意图（Acquire 原子写入）。
	intent *intentRecord
}

// intentRecord 一次未裁决的执行意图。
type intentRecord struct {
	resource string
	fence    sideeffect.Fence
	expires  time.Time
}

// resRecord 一个资源上的独占持有与栅栏令牌。
type resRecord struct {
	fence  sideeffect.Fence
	holder sideeffect.Key
	until  time.Time
}

func newEffectStore() *effectStore {
	return &effectStore{
		now:  time.Now,
		acts: map[sideeffect.Key]*actRecord{},
		ress: map[string]*resRecord{},
	}
}

// UseClock 注入时钟（sidetest 契约套件用）。
func (s *effectStore) UseClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

// clock 当前时刻。**只在持锁时调用**。
func (s *effectStore) clock() time.Time { return s.now() }

func (s *effectStore) actLocked(key sideeffect.Key) *actRecord {
	rec, ok := s.acts[key]
	if !ok {
		rec = &actRecord{}
		s.acts[key] = rec
	}
	return rec
}

func (s *effectStore) resLocked(resource string) *resRecord {
	rec, ok := s.ress[resource]
	if !ok {
		rec = &resRecord{}
		s.ress[resource] = rec
	}
	return rec
}

// Acquire 一次原子判定。
//
// 判定顺序是契约里最要紧的一行：**已完成 > 结果未知 > 执行中 > 资源被占 > 授予**。
// 顺序写反的代价是重复施加：把「结果未知」排到「资源被占」之后，调用方会拿到
// contended（以为「晚点重试就行」），重试于是发生。
func (s *effectStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.actLocked(act.Key)
	if rec.done {
		return sideeffect.OutcomeReplayed, sideeffect.Lease{}, rec.result, nil
	}
	if rec.uncertain {
		return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
	}

	now := s.clock()
	// 执行中：这次尝试还没裁决 ⇒ 不得重新授予执行权。
	if rec.intent != nil {
		if now.Before(rec.intent.expires) {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
		// 租约过期 ⇒ 这次尝试的结果未知（可能已施加、账没记上）。
		s.dropIntentLocked(act, rec)
		if act.RetrySafety != sideeffect.RetrySafetySafe {
			rec.uncertain = true
			rec.reason = "上一次尝试的租约已过期，结果未知"
			return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
	}

	rr := s.resLocked(act.Resource)
	if rr.holder != "" && rr.until.After(now) {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	rr.fence++
	rr.holder = act.Key
	rr.until = now.Add(ttl)
	// 意图与令牌在同一次原子操作里登记：分两次写，中间死掉就等于没写。
	rec.intent = &intentRecord{resource: act.Resource, fence: rr.fence, expires: now.Add(ttl)}
	return sideeffect.OutcomeApplied,
		sideeffect.Lease{Resource: act.Resource, Fence: rr.fence, TTL: ttl},
		sideeffect.Result{}, nil
}

// dropIntentLocked 撤销一条执行意图，并把它自己的资源闸一并作废。
//
// 作废资源闸的理由：意图与资源持有是同一次尝试的两个投影，同刻到期。
// 意图过期时那份持有也过期了，但**记录还在**——不清掉的话，原持有者拿着
// 同一枚令牌仍能 Renew/Complete（栅栏只在接管时递增，没人接管时令牌没变），
// 于是「这次尝试已转入未知态」在资源侧看不出来。
func (s *effectStore) dropIntentLocked(act sideeffect.Action, rec *actRecord) {
	it := rec.intent
	rec.intent = nil
	if it == nil || it.resource == "" {
		return
	}
	if rr, ok := s.ress[it.resource]; ok && rr.holder == act.Key && rr.fence == it.fence {
		rr.holder = ""
	}
}

// holdOwned 资源闸是否仍由本次尝试持有。
//
// 只比令牌不够：释放闸之后令牌没有变，于是「释放后的租约」仍能续期。
func (s *effectStore) holdOwned(act sideeffect.Action, lease sideeffect.Lease) bool {
	rr := s.resLocked(act.Resource)
	return rr.holder == act.Key && rr.fence == lease.Fence && lease.Fence != 0
}

// intentOwned 本次尝试是否仍归该令牌所有。
func (s *effectStore) intentOwned(act sideeffect.Action, lease sideeffect.Lease) bool {
	rec := s.actLocked(act.Key)
	return rec.intent != nil && rec.intent.fence == lease.Fence
}

// Complete 标记该键的动作已完成并留存结果。**先到者为准**。
func (s *effectStore) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.actLocked(act.Key)
	if rec.done {
		return rec.result, nil // 幂等：先到者的结果不被后到者覆盖
	}
	if !s.holdOwned(act, lease) {
		return sideeffect.Result{}, errors.Wrap(sideeffect.ErrFenceStale,
			"完成被拒：本次持有已失效（令牌不是最新代或资源已易主）",
			"key", string(act.Key), "resource", act.Resource)
	}
	if !s.intentOwned(act, lease) {
		return sideeffect.Result{}, errors.Wrap(sideeffect.ErrFenceStale,
			"完成被拒：本次尝试已不属于该令牌",
			"key", string(act.Key), "resource", act.Resource)
	}
	rec.done = true
	rec.result = res
	rec.intent = nil
	rr := s.resLocked(act.Resource)
	rr.holder = "" // 动作已完成 ⇒ 资源闸释放（否则后来者会被「资源正忙」挡住）
	return rec.result, nil
}

// Release 释放**资源持有**，**不动**该键的执行意图。
//
// 意图没被裁决之前，这个键不得再被执行——否则「施加成功但进程死在记账
// 之前」会被下一次 Acquire 变成重复施加。
func (s *effectStore) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.holdOwned(act, lease) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"释放被拒：本次持有已失效（令牌不是最新代或资源已易主）",
			"key", string(act.Key), "resource", act.Resource)
	}
	rr := s.resLocked(act.Resource)
	rr.holder = ""
	return nil
}

// Abandon 显式放弃本次尝试：撤销执行意图，并释放资源持有。
func (s *effectStore) Abandon(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.actLocked(act.Key)
	if !s.intentOwned(act, lease) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"放弃被拒：本次尝试已不属于该令牌",
			"key", string(act.Key), "resource", act.Resource)
	}
	rec.intent = nil
	// 资源闸只在仍是自己的时才放——易主了就不碰（那是新持有者的闸）。
	if s.holdOwned(act, lease) {
		rr := s.resLocked(act.Resource)
		rr.holder = ""
	}
	return nil
}

// Renew 续期并**同时确认持有资格**：闸已易主则拒绝续期（假续期是重复施加的入口）。
func (s *effectStore) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.holdOwned(act, lease) {
		return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale,
			"续期被拒：本次持有已失效（令牌不是最新代或资源已易主）",
			"key", string(act.Key), "resource", act.Resource)
	}
	if !s.intentOwned(act, lease) {
		return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale,
			"续期被拒：本次尝试已不属于该令牌",
			"key", string(act.Key), "resource", act.Resource)
	}
	now := s.clock()
	rr := s.resLocked(act.Resource)
	rr.until = now.Add(ttl)
	rec := s.actLocked(act.Key)
	rec.intent.expires = now.Add(ttl)
	return sideeffect.Lease{Resource: act.Resource, Fence: lease.Fence, TTL: ttl}, nil
}

// MarkUncertain 把该键标记为「结果未知」（按键，与资源租约无关）。
func (s *effectStore) MarkUncertain(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.holdOwned(act, lease) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"标记未知被拒：本次持有已失效（令牌不是最新代或资源已易主）",
			"key", string(act.Key), "resource", act.Resource)
	}
	rec := s.actLocked(act.Key)
	if !s.intentOwned(act, lease) {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"标记未知被拒：本次尝试已不属于该令牌",
			"key", string(act.Key), "resource", act.Resource)
	}
	rec.uncertain = true
	rec.reason = reason
	rec.intent = nil
	return nil
}

// ResolveUncertain 裁决「结果未知」的键。**先到者为准**。
func (s *effectStore) ResolveUncertain(_ context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.actLocked(act.Key)
	if rec.done {
		return sideeffect.OutcomeReplayed, rec.result, nil
	}
	if !rec.uncertain {
		return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrNotUncertain
	}
	// 裁决即结论：执行意图一并撤销（这次尝试已有结论）。
	rec.intent = nil
	if res.Applied {
		rec.done = true
		rec.uncertain = false
		rec.result = res.Result
		return sideeffect.OutcomeReplayed, rec.result, nil
	}
	rec.uncertain = false
	return sideeffect.OutcomeApplied, sideeffect.Result{}, nil
}
