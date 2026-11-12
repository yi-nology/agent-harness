package sidetest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/sidetest"
)

// memStore 是一份**按契约实现**的内存存储，用来检验 sidetest 套件本身。
//
// # 它扮演的是「宿主」的角色
//
// 本仓不提供存储实现（存储归宿主）。但套件若没有实现跑过，它自己就可能
// 是错的——「套件全绿」和「套件本身写错了」在结果上无法区分。
// 故本文件提供一份最小实现：它按契约写，跑套件应当全绿；
// 若它红了，那是套件或契约错了，不是实现错了。
//
// 这份实现同时是**宿主怎么接套件**的活样例：实现 ClockedStore 接口，
// 把时钟接上，然后把自己的测试指向 sidetest.Run。
type memStore struct {
	mu sync.Mutex

	done      map[sideeffect.Key]sideeffect.Result
	uncertain map[sideeffect.Key]string
	holders   map[string]memHold
	intents   map[sideeffect.Key]memIntent
	recons    map[sideeffect.Key]memRecon
	maxFence  map[string]sideeffect.Fence
	identity  map[sideeffect.Key]memIdentity

	nowFn func() time.Time
}

type memHold struct {
	lease   sideeffect.Lease
	holder  string
	expires time.Time
}

// memIntent 一次未裁决的执行意图。
type memIntent struct {
	resource string
	fence    sideeffect.Fence
	holder   string
	expires  time.Time
}

// memRecon 一次对账认领。
type memRecon struct {
	holder  string
	expires time.Time
}

// memIdentity 已登记的动作身份（首次 Acquire 时钉死）。
type memIdentity struct {
	resource string
	safety   sideeffect.RetrySafety
	hash     string
}

func newMemStore() *memStore {
	return &memStore{
		done:      map[sideeffect.Key]sideeffect.Result{},
		uncertain: map[sideeffect.Key]string{},
		holders:   map[string]memHold{},
		intents:   map[sideeffect.Key]memIntent{},
		recons:    map[sideeffect.Key]memRecon{},
		identity:  map[sideeffect.Key]memIdentity{},
		maxFence:  map[string]sideeffect.Fence{},
	}
}

func (m *memStore) UseClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nowFn = now
}

func (m *memStore) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 动作身份不可变（判据在最前，先于「已完成 → 重放」）。
	if id, ok := m.identity[act.Key]; ok {
		if id.resource != act.Resource || id.safety != act.RetrySafety ||
			id.hash != sideeffect.HashPayload(act.Payload) {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, sideeffect.ErrActionConflict
		}
	} else {
		m.identity[act.Key] = memIdentity{
			resource: act.Resource,
			safety:   act.RetrySafety,
			hash:     sideeffect.HashPayload(act.Payload),
		}
	}

	if res, ok := m.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, sideeffect.Lease{}, res, nil
	}

	// 结果未知 → 拒绝执行（优先于占用）：这个 Key 得先对账。
	if _, ok := m.uncertain[act.Key]; ok {
		return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
	}

	now := m.nowLocked()

	// 对账占用未过期 ⇒ 对账中（仍是未知态）。过期占用清掉（可重入）。
	if r, ok := m.recons[act.Key]; ok {
		if now.Before(r.expires) {
			return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
		delete(m.recons, act.Key)
	}

	// 执行意图未裁决 → 不得重新授予执行权。
	if it, ok := m.intents[act.Key]; ok {
		if now.Before(it.expires) {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
		// 租约过期 ⇒ 这次尝试的结果未知。
		m.dropIntent(act, it)
		if act.RetrySafety != sideeffect.RetrySafetySafe {
			m.uncertain[act.Key] = "上一次尝试的租约已过期，结果未知"
			return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
	}

	if h, ok := m.holders[act.Resource]; ok && now.Before(h.expires) {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}

	next := m.maxFence[act.Resource] + 1
	m.maxFence[act.Resource] = next
	holder := sideeffect.MintHolder()
	lease := sideeffect.Lease{Resource: act.Resource, Fence: next, Holder: holder, TTL: ttl}
	m.holders[act.Resource] = memHold{lease: lease, holder: holder, expires: now.Add(ttl)}
	m.intents[act.Key] = memIntent{resource: act.Resource, fence: next, holder: holder, expires: now.Add(ttl)}
	return sideeffect.OutcomeApplied, lease, sideeffect.Result{}, nil
}

// dropIntent 撤销一条执行意图，并把它自己的资源闸一并作废。
func (m *memStore) dropIntent(act sideeffect.Action, it memIntent) {
	delete(m.intents, act.Key)
	if it.resource == "" {
		return
	}
	if h, ok := m.holders[it.resource]; ok && h.holder == it.holder && h.lease.Fence == it.fence {
		delete(m.holders, it.resource)
	}
}

func (m *memStore) nowLocked() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

// leaseResourceErr 三项联查第一项：租约的资源必须与动作的资源一致。
func leaseResourceErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if lease.Resource != act.Resource {
		return errors.Wrap(sideeffect.ErrResourceMismatch, "凭据被拒：租约与动作的资源不一致",
			"act_resource", act.Resource, "lease_resource", lease.Resource)
	}
	return nil
}

// holdOwnerErr 三项联查：资源闸是否仍由**这份凭据**持有。
func (m *memStore) holdOwnerErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	h, ok := m.holders[act.Resource]
	if !ok || lease.Fence == 0 || h.lease.Fence != lease.Fence {
		return errors.Wrap(sideeffect.ErrFenceStale, "本次持有已失效（令牌不是最新代或资源已释放）", "resource", act.Resource)
	}
	if h.holder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder, "凭据被拒：持有者身份不匹配", "resource", act.Resource)
	}
	return nil
}

// intentOwnerErr 三项联查：本次尝试是否仍归**这份凭据**所有。
func (m *memStore) intentOwnerErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	it, ok := m.intents[act.Key]
	if !ok {
		return errors.Wrap(sideeffect.ErrFenceStale, "本次尝试已无未裁决的执行意图", "key", string(act.Key))
	}
	if it.fence != lease.Fence {
		return errors.Wrap(sideeffect.ErrFenceStale, "本次尝试已不属于该令牌", "resource", act.Resource)
	}
	if it.holder != lease.Holder {
		return errors.Wrap(sideeffect.ErrNotHolder, "凭据被拒：持有者身份不匹配", "key", string(act.Key))
	}
	return nil
}

func (m *memStore) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if prior, ok := m.done[act.Key]; ok {
		return prior, nil
	}
	if err := m.holdOwnerErr(act, lease); err != nil {
		return sideeffect.Result{}, err
	}
	if err := m.intentOwnerErr(act, lease); err != nil {
		return sideeffect.Result{}, err
	}
	m.done[act.Key] = res
	delete(m.intents, act.Key)
	delete(m.holders, act.Resource)
	return res, nil
}

func (m *memStore) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.holdOwnerErr(act, lease); err != nil {
		return err
	}
	delete(m.holders, act.Resource)
	// 释放只放闸，不撤销执行意图。
	return nil
}

func (m *memStore) Abandon(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.intentOwnerErr(act, lease); err != nil {
		return err
	}
	delete(m.intents, act.Key)
	if err := m.holdOwnerErr(act, lease); err == nil {
		delete(m.holders, act.Resource)
	}
	return nil
}

func (m *memStore) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 资格校验（三项联查）与续期在同一次原子操作里：校验不过就不续期。
	if err := m.holdOwnerErr(act, lease); err != nil {
		return sideeffect.Lease{}, err
	}
	if err := m.intentOwnerErr(act, lease); err != nil {
		return sideeffect.Lease{}, err
	}
	now := m.nowLocked()
	cur := m.holders[act.Resource]
	// 过期即资格终止（Store.Renew 契约）：租约已过期而无人接管时不得续命。
	if cur.expires.IsZero() || !cur.expires.After(now) {
		return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale,
			"租约已过期且无人接管——到期即资格终止（要继续做请重新 Acquire）",
			"key", string(act.Key), "resource", act.Resource)
	}
	cur.expires = now.Add(ttl)
	cur.lease.TTL = ttl
	m.holders[act.Resource] = cur
	it := m.intents[act.Key]
	it.expires = now.Add(ttl)
	m.intents[act.Key] = it
	return cur.lease, nil
}

func (m *memStore) MarkUncertain(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 三项联查全部发生在任何状态被改之前：写失败时意图原样留在存储里
	// （契约硬要求：标记失败后它是唯一还在挡着重试的东西）。
	if err := m.holdOwnerErr(act, lease); err != nil {
		return err
	}
	if err := m.intentOwnerErr(act, lease); err != nil {
		return err
	}
	m.uncertain[act.Key] = reason
	delete(m.intents, act.Key)
	return nil
}

func (m *memStore) ResolveUncertain(_ context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.resolveWithin(act, res, "")
}

// ResolveUncertainOwned 带对账占用凭据的裁决（OwnedResolveStore）。
func (m *memStore) ResolveUncertainOwned(_ context.Context, act sideeffect.Action, res sideeffect.Resolution, holder string) (sideeffect.Outcome, sideeffect.Result, error) {
	if holder == "" {
		return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrReconcileLost
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.resolveWithin(act, res, holder)
}

// resolveWithin 裁决主体：已完成重放先于凭据校验（读既有事实不问资格）。
func (m *memStore) resolveWithin(act sideeffect.Action, res sideeffect.Resolution, requiredHolder string) (sideeffect.Outcome, sideeffect.Result, error) {
	if prior, ok := m.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, prior, nil
	}
	if requiredHolder != "" {
		now := m.nowLocked()
		r, ok := m.recons[act.Key]
		if !ok || !now.Before(r.expires) || r.holder != requiredHolder {
			return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrReconcileLost
		}
	}
	if _, ok := m.uncertain[act.Key]; !ok {
		return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrNotUncertain
	}
	delete(m.intents, act.Key)
	delete(m.recons, act.Key)
	if res.Applied {
		m.done[act.Key] = res.Result
		delete(m.uncertain, act.Key)
		return sideeffect.OutcomeReplayed, res.Result, nil
	}
	if act.RetrySafety == sideeffect.RetrySafetyUnsafe && !res.ForceRetryable {
		// 「不可重试」的动作，裁决「未生效」不解除阻塞（显式决策才放行）。
		return sideeffect.OutcomeUncertain, sideeffect.Result{}, sideeffect.ErrUnsafeNotRetryable
	}
	delete(m.uncertain, act.Key)
	return sideeffect.OutcomeApplied, sideeffect.Result{}, nil
}

func (m *memStore) BeginReconcile(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Reconcile, error) {
	if ttl <= 0 {
		return sideeffect.Reconcile{}, sideeffect.ErrInvalidTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.done[act.Key]; ok {
		return sideeffect.Reconcile{}, sideeffect.ErrNotUncertain
	}
	if _, ok := m.uncertain[act.Key]; !ok {
		return sideeffect.Reconcile{}, sideeffect.ErrNotUncertain
	}
	now := m.nowLocked()
	if r, ok := m.recons[act.Key]; ok && now.Before(r.expires) {
		return sideeffect.Reconcile{}, sideeffect.ErrReconcileTaken
	}
	holder := sideeffect.MintHolder()
	m.recons[act.Key] = memRecon{holder: holder, expires: now.Add(ttl)}
	return sideeffect.Reconcile{Holder: holder, ExpiresAt: now.Add(ttl)}, nil
}

// TestContract 跑共享契约套件：这份实现按契约写，应当全绿。
//
// 宿主接套件的方式就是把这一行写进自己的测试里。
func TestContract(t *testing.T) {
	sidetest.Run(t, func(t *testing.T, clock *sidetest.FakeClock) sidetest.ClockedStore {
		return newMemStore()
	})
}
