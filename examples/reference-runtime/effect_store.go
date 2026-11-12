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

	// 动作身份（首次登记，永久事实；idSet=false = 未登记）。
	// 与 sideeffect.ErrActionConflict 对应，登记/比对在 actLocked 内完成。
	idSet         bool
	idResource    string
	idSafety      sideeffect.RetrySafety
	idPayloadHash string

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

// actLocked 取（或创建）Key 的状态记录，**取到后立刻登记/比对动作身份**
// ——所有入口共用这一处，身份判定天然先于任何状态判定（Store.Acquire
// 契约「身份不可变」：不授予、不重放、不转未知态，优先于已完成重放）。
func (s *effectStore) actLocked(act sideeffect.Action) (*actRecord, error) {
	rec, ok := s.acts[act.Key]
	if !ok {
		rec = &actRecord{}
		s.acts[act.Key] = rec
	}
	if !rec.idSet {
		// 首次见到这个 Key：登记身份（即便本次 Acquire 将返回 contended
		// ——身份不等授予）。
		rec.idSet = true
		rec.idResource = act.Resource
		rec.idSafety = act.RetrySafety
		rec.idPayloadHash = sideeffect.HashPayload(act.Payload)
		return rec, nil
	}
	if rec.idResource != act.Resource || rec.idSafety != act.RetrySafety ||
		rec.idPayloadHash != sideeffect.HashPayload(act.Payload) {
		return nil, errors.Wrap(sideeffect.ErrActionConflict,
			"同 Key 的动作身份与首次登记不一致（这不是重试，是另一个动作）",
			"key", string(act.Key),
			"登记 resource", rec.idResource, "本次 resource", act.Resource,
			"登记 safety", rec.idSafety, "本次 safety", act.RetrySafety)
	}
	return rec, nil
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

	rec, err := s.actLocked(act)
	if err != nil {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, err
	}
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
	rec, err := s.actLocked(act)
	if err != nil {
		return err
	}
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

	rec, err := s.actLocked(act)
	if err != nil {
		return sideeffect.Result{}, err
	}
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
	rec, err := s.actLocked(act)
	if err != nil {
		return err
	}
	rec.intent = nil
	// 资源闸只在仍是自己的时才放——易主了就不碰（那是新持有者的闸）。
	if err := s.holdOwnerErr(act, lease); err == nil {
		rr := s.resLocked(act.Resource)
		rr.holder = ""
	}
	return nil
}

// Renew 续期并**同时确认持有资格**：闸已易主、尝试已失效**或租约已过期**
// 则拒绝续期（假续期是重复施加的入口）。
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
	// 过期即资格终止（Store.Renew 契约）：租约已过期而无人接管时，
	// 旧持有者不得复活它——否则过期那次尝试绕开「先对账」直接继续推进。
	if !rr.until.After(now) {
		return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale,
			"租约已过期且无人接管——到期即资格终止（要继续做请重新 Acquire）",
			"key", string(act.Key), "resource", act.Resource)
	}
	rr.until = now.Add(ttl)
	rec, err := s.actLocked(act)
	if err != nil {
		return sideeffect.Lease{}, err
	}
	rec.intent.expires = now.Add(ttl)
	return sideeffect.Lease{Resource: act.Resource, Fence: lease.Fence, Holder: lease.Holder, TTL: ttl}, nil
}
