package sidetest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/agent-harness/sideeffect"
	"github.com/yi-nology/agent-harness/sidetest"
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

	done     map[sideeffect.Key]sideeffect.Result
	holders  map[string]memHold
	maxFence map[string]sideeffect.Fence

	nowFn func() time.Time
}

type memHold struct {
	lease   sideeffect.Lease
	expires time.Time
}

func newMemStore() *memStore {
	return &memStore{
		done:     map[sideeffect.Key]sideeffect.Result{},
		holders:  map[string]memHold{},
		maxFence: map[string]sideeffect.Fence{},
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

	now := m.nowLocked()
	if h, ok := m.holders[act.Resource]; ok && now.Before(h.expires) {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}

	next := m.maxFence[act.Resource] + 1
	m.maxFence[act.Resource] = next
	lease := sideeffect.Lease{Resource: act.Resource, Fence: next, TTL: ttl}
	m.holders[act.Resource] = memHold{lease: lease, expires: now.Add(ttl)}
	return sideeffect.OutcomeApplied, lease, sideeffect.Result{}, nil
}

func (m *memStore) nowLocked() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

func (m *memStore) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, ok := m.holders[act.Resource]
	if !ok || cur.lease.Fence != lease.Fence {
		return sideeffect.Result{}, errors.Wrap(sideeffect.ErrFenceStale, "栅栏令牌已不是最新代", "resource", act.Resource)
	}
	if prior, ok := m.done[act.Key]; ok {
		return prior, nil
	}
	m.done[act.Key] = res
	return res, nil
}

func (m *memStore) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, ok := m.holders[act.Resource]
	if !ok || cur.lease.Fence != lease.Fence {
		return errors.Wrap(sideeffect.ErrFenceStale, "栅栏令牌已不是最新代", "resource", act.Resource)
	}
	delete(m.holders, act.Resource)
	return nil
}

func (m *memStore) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cur, ok := m.holders[act.Resource]
	if !ok || cur.lease.Fence != lease.Fence {
		// 资格校验与续期在同一次原子操作里：校验不过就不续期。
		return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale, "栅栏令牌已不是最新代", "resource", act.Resource)
	}
	cur.expires = m.nowLocked().Add(ttl)
	cur.lease.TTL = ttl
	m.holders[act.Resource] = cur
	return cur.lease, nil
}

// TestContract 跑共享契约套件：这份实现按契约写，应当全绿。
//
// 宿主接套件的方式就是把这一行写进自己的测试里。
func TestContract(t *testing.T) {
	sidetest.Run(t, func(t *testing.T, clock *sidetest.FakeClock) sidetest.ClockedStore {
		return newMemStore()
	})
}
