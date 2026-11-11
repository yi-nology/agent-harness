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
	maxFence  map[string]sideeffect.Fence

	nowFn func() time.Time
}

type memHold struct {
	lease   sideeffect.Lease
	holder  sideeffect.Key
	expires time.Time
}

// memIntent 一次未裁决的执行意图。
type memIntent struct {
	resource string
	fence    sideeffect.Fence
	expires  time.Time
}

func newMemStore() *memStore {
	return &memStore{
		done:      map[sideeffect.Key]sideeffect.Result{},
		uncertain: map[sideeffect.Key]string{},
		holders:   map[string]memHold{},
		intents:   map[sideeffect.Key]memIntent{},
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

	if res, ok := m.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, sideeffect.Lease{}, res, nil
	}

	// 结果未知 → 拒绝执行（优先于占用）：这个 Key 得先对账。
	if _, ok := m.uncertain[act.Key]; ok {
		return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
	}

	now := m.nowLocked()

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
	lease := sideeffect.Lease{Resource: act.Resource, Fence: next, TTL: ttl}
	m.holders[act.Resource] = memHold{lease: lease, holder: act.Key, expires: now.Add(ttl)}
	m.intents[act.Key] = memIntent{resource: act.Resource, fence: next, expires: now.Add(ttl)}
	return sideeffect.OutcomeApplied, lease, sideeffect.Result{}, nil
}

// dropIntent 撤销一条执行意图，并把它自己的资源闸一并作废。
func (m *memStore) dropIntent(act sideeffect.Action, it memIntent) {
	delete(m.intents, act.Key)
	if it.resource == "" {
		return
	}
	if h, ok := m.holders[it.resource]; ok && h.holder == act.Key && h.lease.Fence == it.fence {
		delete(m.holders, it.resource)
	}
}

func (m *memStore) nowLocked() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

// holdOwned 资源闸是否仍由本次尝试持有（只比令牌不够：释放后令牌没变）。
func (m *memStore) holdOwned(act sideeffect.Action, lease sideeffect.Lease) bool {
	h, ok := m.holders[act.Resource]
	return ok && h.lease.Fence == lease.Fence && h.holder == act.Key
}

// intentOwned 本次尝试是否仍归该令牌所有。
func (m *memStore) intentOwned(act sideeffect.Action, lease sideeffect.Lease) bool {
	it, ok := m.intents[act.Key]
	return ok && it.fence == lease.Fence
}

func (m *memStore) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if prior, ok := m.done[act.Key]; ok {
		return prior, nil
	}
	if !m.holdOwned(act, lease) || !m.intentOwned(act, lease) {
		return sideeffect.Result{}, errors.Wrap(sideeffect.ErrFenceStale, "本次持有已失效", "resource", act.Resource)
	}
	m.done[act.Key] = res
	delete(m.intents, act.Key)
	delete(m.holders, act.Resource)
	return res, nil
}

func (m *memStore) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.holdOwned(act, lease) {
		return errors.Wrap(sideeffect.ErrFenceStale, "本次持有已失效", "resource", act.Resource)
	}
	delete(m.holders, act.Resource)
	// 释放只放闸，不撤销执行意图。
	return nil
}

func (m *memStore) Abandon(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.intentOwned(act, lease) {
		return errors.Wrap(sideeffect.ErrFenceStale, "本次尝试已不属于该令牌", "resource", act.Resource)
	}
	delete(m.intents, act.Key)
	if m.holdOwned(act, lease) {
		delete(m.holders, act.Resource)
	}
	return nil
}

func (m *memStore) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 资格校验与续期在同一次原子操作里：校验不过就不续期。
	if !m.holdOwned(act, lease) || !m.intentOwned(act, lease) {
		return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale, "本次持有已失效", "resource", act.Resource)
	}
	now := m.nowLocked()
	cur := m.holders[act.Resource]
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

	if !m.holdOwned(act, lease) || !m.intentOwned(act, lease) {
		return errors.Wrap(sideeffect.ErrFenceStale, "本次持有已失效", "resource", act.Resource)
	}
	m.uncertain[act.Key] = reason
	delete(m.intents, act.Key)
	return nil
}

func (m *memStore) ResolveUncertain(_ context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if prior, ok := m.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, prior, nil
	}
	if _, ok := m.uncertain[act.Key]; !ok {
		return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrNotUncertain
	}
	delete(m.intents, act.Key)
	if res.Applied {
		m.done[act.Key] = res.Result
		delete(m.uncertain, act.Key)
		return sideeffect.OutcomeApplied, res.Result, nil
	}
	delete(m.uncertain, act.Key)
	return sideeffect.OutcomeContended, sideeffect.Result{}, nil
}

// TestContract 跑共享契约套件：这份实现按契约写，应当全绿。
//
// 宿主接套件的方式就是把这一行写进自己的测试里。
func TestContract(t *testing.T) {
	sidetest.Run(t, func(t *testing.T, clock *sidetest.FakeClock) sidetest.ClockedStore {
		return newMemStore()
	})
}
