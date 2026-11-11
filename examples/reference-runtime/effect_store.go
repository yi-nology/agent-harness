// effect_store.go —— sideeffect.Store 的参考实现（内存版）。
//
// # 这份实现钉住的三条判据
//
//  1. **三项联查**：Renew / Complete / MarkUncertain / Abandon / Release
//     都必须校验「令牌仍是最新代 + 持有者身份与授予时铸造的一致 + 租约与
//     动作的资源一致」。只比栅栏令牌是不够的：释放闸之后令牌**没有变**
//     （栅栏只在接管时递增），于是「释放后的租约」能拿同一枚令牌继续续期、
//     继续记账；令牌还会经下游流转（FencedSink），见过的组件拿着令牌就能
//     冒充持有者——持有者身份是授予时**存储侧铸造**的第二份凭据
//     (sideeffect.MintHolder)，不经任何下游接口流转。
//  2. **执行意图**：Acquire 授予执行权时原子登记「本次尝试还在」；租约过期
//     且未裁决时，同一 Key 转入需对账态，而不是重新授予执行权。
//  3. **对账占用**：BeginReconcile 同一时刻至多一个对账者，占用过期可重入；
//     RetrySafetyUnsafe 的动作裁决「未生效」不解除阻塞（显式决策才放行）。
//
// 三者都是「同一份状态的多个投影」在存储侧的落法：Key 级（这个动作做过没有）
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

	// recon 对账占用（BeginReconcile 写入；nil = 无人认领对账）。
	recon *reconRecord
}

// intentRecord 一次未裁决的执行意图。
type intentRecord struct {
	resource string
	fence    sideeffect.Fence
	holder   string
	expires  time.Time
}

// resRecord 一个资源上的独占持有与栅栏令牌。
type resRecord struct {
	fence sideeffect.Fence
	// holder 授予时铸造的持有者身份（不再用 act.Key：持有者是凭据，
	// 必须由存储铸造且不可预测——见 sideeffect.MintHolder）。
	holder string
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
	// 对账占用未过期 ⇒ 对账中：该 Key 仍处于未知态，不得授予执行权。
	// 过期的占用在这里清掉（可重入：对账者死亡不永久占住这个 Key）。
	if rec.recon != nil {
		if now.Before(rec.recon.expires) {
			return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
		rec.recon = nil
	}
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
	holder := sideeffect.MintHolder()
	rr.holder = holder
	rr.until = now.Add(ttl)
	// 意图、令牌、持有者身份在同一次原子操作里登记：分两次写，中间死掉就等于没写。
	rec.intent = &intentRecord{resource: act.Resource, fence: rr.fence, holder: holder, expires: now.Add(ttl)}
	return sideeffect.OutcomeApplied,
		sideeffect.Lease{Resource: act.Resource, Fence: rr.fence, Holder: holder, TTL: ttl},
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
	if rr, ok := s.ress[it.resource]; ok && rr.holder == it.holder && rr.fence == it.fence {
		rr.holder = ""
	}
}

// leaseResourceErr 三项联查的第一项：租约的资源必须与动作的资源一致。
//
// A 资源的租约被拿去做 B 资源的操作时，两个 Resource 各说各话——
// 不拦下的话，后面按 act.Resource 去查记录、按 lease.Fence 去比令牌，
// 两份真相拼出一个错误的「合规」。
func leaseResourceErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if lease.Resource != act.Resource {
		return errors.Wrap(sideeffect.ErrResourceMismatch,
			"凭据被拒：租约与动作的资源不一致",
			"act_resource", act.Resource, "lease_resource", lease.Resource)
	}
	return nil
}

// holdOwnerErr 三项联查：资源闸是否仍由**这份凭据**持有。
//
// 判定顺序有意如此：令牌不匹配或已释放 → ErrFenceStale（这一代已经过去）；
// 令牌还在这一代但持有者不是出示者 → ErrNotHolder（令牌会经下游流转，
// 见过的组件拿着令牌来冒充持有者，被这道防线拦下）。
func (s *effectStore) holdOwnerErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	rr := s.resLocked(act.Resource)
	if lease.Fence == 0 || rr.holder == "" || rr.fence != lease.Fence {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次持有已失效（令牌不是最新代或资源已释放）",
			"key", string(act.Key), "resource", act.Resource)
	}
	if rr.holder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder,
			"凭据被拒：持有者身份不匹配（令牌不是本次授予铸造的那份）",
			"key", string(act.Key), "resource", act.Resource)
	}
	return nil
}

// intentOwnerErr 三项联查：本次尝试是否仍归**这份凭据**所有。
func (s *effectStore) intentOwnerErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	rec := s.actLocked(act.Key)
	if rec.intent == nil {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次尝试已无未裁决的执行意图",
			"key", string(act.Key))
	}
	if rec.intent.fence != lease.Fence {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"凭据被拒：本次尝试已不属于该令牌",
			"key", string(act.Key), "resource", act.Resource)
	}
	if rec.intent.holder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder,
			"凭据被拒：持有者身份不匹配（意图不是本次授予铸造的那份）",
			"key", string(act.Key))
	}
	return nil
}

// Complete 标记该键的动作已完成并留存结果。**先到者为准**。
func (s *effectStore) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.actLocked(act.Key)
	if rec.done {
		return rec.result, nil // 幂等：先到者的结果不被后到者覆盖
	}
	if err := s.holdOwnerErr(act, lease); err != nil {
		return sideeffect.Result{}, err
	}
	if err := s.intentOwnerErr(act, lease); err != nil {
		return sideeffect.Result{}, err
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

	if err := s.holdOwnerErr(act, lease); err != nil {
		return err
	}
	rr := s.resLocked(act.Resource)
	rr.holder = ""
	return nil
}

// Abandon 显式放弃本次尝试：撤销执行意图，并释放资源持有。
func (s *effectStore) Abandon(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.intentOwnerErr(act, lease); err != nil {
		return err
	}
	rec := s.actLocked(act.Key)
	rec.intent = nil
	// 资源闸只在仍是自己的时才放——易主了就不碰（那是新持有者的闸）。
	if err := s.holdOwnerErr(act, lease); err == nil {
		rr := s.resLocked(act.Resource)
		rr.holder = ""
	}
	return nil
}

// Renew 续期并**同时确认持有资格**：闸已易主则拒绝续期（假续期是重复施加的入口）。
func (s *effectStore) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.holdOwnerErr(act, lease); err != nil {
		return sideeffect.Lease{}, err
	}
	if err := s.intentOwnerErr(act, lease); err != nil {
		return sideeffect.Lease{}, err
	}
	now := s.clock()
	rr := s.resLocked(act.Resource)
	rr.until = now.Add(ttl)
	rec := s.actLocked(act.Key)
	rec.intent.expires = now.Add(ttl)
	return sideeffect.Lease{Resource: act.Resource, Fence: lease.Fence, Holder: lease.Holder, TTL: ttl}, nil
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
	rec := s.actLocked(act.Key)
	rec.uncertain = true
	rec.reason = reason
	rec.intent = nil
	return nil
}

// ResolveUncertain 裁决「结果未知」的键。**先到者为准**。
//
// RetrySafetyUnsafe 的动作：未生效的裁决不解除阻塞（ErrUnsafeNotRetryable），
// 必须 Resolution.ForceRetryable 的显式决策才放行——「不可重试」是判据，
// 不是注释。
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
