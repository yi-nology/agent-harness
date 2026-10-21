package sideeffect

import (
	"context"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"
)

// fakeStore 测试替身：一个内存版的 Store。
//
// # 它必须忠实模拟原子语义，否则测试验证的是它自己
//
// 真实的 Store 靠「一次原子操作」保证三判合一。本替身用一把 mutex
// 模拟那次原子性：Acquire 的读与写在同一把锁内完成，不存在中间态。
//
// 刻意**不**模拟的部分：跨进程（单进程内不可能真跨进程）。
// 跨进程的正确性由「Acquire 是原子操作」这一契约保证，本包测不了——
// 能测的是「内核不依赖它是否原子」的编排层语义。
type fakeStore struct {
	mu sync.Mutex

	// done 已完成的 Key → 结果。
	done map[Key]Result
	// holders Resource → 当前持有。
	holders map[string]fakeHold
	// maxFence Resource → 已见过的最大令牌。
	maxFence map[string]Fence

	// now 当前时刻（测试手动推进，用于 TTL 过期）。
	now time.Time
	// nowFn 时钟注入点（nil 时用 now 字段）。
	nowFn func() time.Time

	// 故障注入。
	acquireErr  error
	completeErr error
	releaseErr  error

	// 计数。
	acquireCalls  int
	completeCalls int
	releaseCalls  int
}

// fakeHold 一次持有（含到期时刻）。
type fakeHold struct {
	lease   Lease
	expires time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		done:     map[Key]Result{},
		holders:  map[string]fakeHold{},
		maxFence: map[string]Fence{},
		now:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (s *fakeStore) clockLocked() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return s.now
}

// advance 推进时钟。
//
// 注意：注入的时钟（nowFn）与 now 字段是**两套刻度**，推进时必须同步——
// 否则过期判定读的是 nowFn（未推进）而 advance 只动了 now，
// 租约就永远不过期，相关的过期用例会静默失效（测了个寂寞）。
func (s *fakeStore) advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = s.now.Add(d)
	if s.nowFn != nil {
		// 注入的时钟是套件的 FakeClock：它有自己的推进方法，
		// 这里无法直接推进，只能靠「当前时刻 + d」重建。
		s.nowFn = func() time.Time { return s.now }
	}
}

func (s *fakeStore) Acquire(_ context.Context, act Action, ttl time.Duration) (Outcome, Lease, Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquireCalls++
	if s.acquireErr != nil {
		return OutcomeApplied, Lease{}, Result{}, s.acquireErr
	}

	// 判据一：已完成 → 取回既有结果（优先于占用）。
	if res, ok := s.done[act.Key]; ok {
		return OutcomeReplayed, Lease{}, res, nil
	}

	now := s.clockLocked()

	// 判据二：资源被他人持有且未过期 → 拒绝（不发放令牌）。
	if h, ok := s.holders[act.Resource]; ok && now.Before(h.expires) {
		return OutcomeContended, Lease{}, Result{}, nil
	}

	// 判据三：授予持有，令牌 = 已见最大值 + 1（不回退）。
	next := s.maxFence[act.Resource] + 1
	s.maxFence[act.Resource] = next
	lease := Lease{Resource: act.Resource, Fence: next, TTL: ttl}
	s.holders[act.Resource] = fakeHold{lease: lease, expires: now.Add(ttl)}
	return OutcomeApplied, lease, Result{}, nil
}

func (s *fakeStore) Complete(_ context.Context, act Action, lease Lease, res Result) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	if s.completeErr != nil {
		return Result{}, s.completeErr
	}
	// 令牌校验：只有当前最新代才接受这次完成。
	cur, ok := s.holders[act.Resource]
	if !ok || cur.lease.Fence != lease.Fence {
		return Result{}, errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代", "resource", act.Resource)
	}
	// 幂等：先到者的结果为准，不被后到者覆盖。
	if prior, ok := s.done[act.Key]; ok {
		return prior, nil
	}
	s.done[act.Key] = res
	return res, nil
}

func (s *fakeStore) Release(_ context.Context, act Action, lease Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCalls++
	if s.releaseErr != nil {
		return s.releaseErr
	}
	cur, ok := s.holders[act.Resource]
	if !ok || cur.lease.Fence != lease.Fence {
		// 不是自己那份持有：拒绝释放（否则会放掉别人的闸）。
		return errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代", "resource", act.Resource)
	}
	delete(s.holders, act.Resource)
	return nil
}
