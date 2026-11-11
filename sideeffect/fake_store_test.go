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
	// uncertain 结果未知的 Key → 原因（标记后该 Key 不得再被执行）。
	uncertain map[Key]string
	// holders Resource → 当前持有。
	holders map[string]fakeHold
	// intents Key → 未裁决的执行意图（Acquire 原子写入）。
	intents map[Key]fakeIntent
	// maxFence Resource → 已见过的最大令牌。
	maxFence map[string]Fence

	// now 当前时刻（测试手动推进，用于 TTL 过期）。
	now time.Time
	// nowFn 时钟注入点（nil 时用 now 字段）。
	nowFn func() time.Time

	// 故障注入。
	acquireErr       error
	completeErr      error
	releaseErr       error
	abandonErr       error
	markUncertainErr error
	resolveErr       error

	// 计数。
	acquireCalls       int
	completeCalls      int
	releaseCalls       int
	abandonCalls       int
	markUncertainCalls int
	resolveCalls       int
}

// fakeHold 一次持有（含到期时刻与持有者）。
type fakeHold struct {
	lease   Lease
	holder  Key
	expires time.Time
}

// fakeIntent 一次未裁决的执行意图。
//
// 它回答的不是「做过没有」，而是「这个 Key 上有一次尝试还没结论」——
// 进程死在施加成功之后、记账之前时，存储侧留下的就是这一条。
type fakeIntent struct {
	resource string
	fence    Fence
	expires  time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		done:      map[Key]Result{},
		uncertain: map[Key]string{},
		holders:   map[string]fakeHold{},
		intents:   map[Key]fakeIntent{},
		maxFence:  map[string]Fence{},
		now:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
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

// Acquire 一次原子判定：已完成 > 结果未知 > 执行中 > 资源被占 > 授予。
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

	// 判据二：结果未知 → 拒绝执行，要求先对账（优先于占用）。
	if _, ok := s.uncertain[act.Key]; ok {
		return OutcomeUncertain, Lease{}, Result{}, nil
	}

	now := s.clockLocked()

	// 判据三：本 Key 上有一次未裁决的尝试。
	if it, ok := s.intents[act.Key]; ok {
		if now.Before(it.expires) {
			// 尝试还在进行中：同一个动作不得被施加第二次。
			return OutcomeContended, Lease{}, Result{}, nil
		}
		// 租约过期 ⇒ 本次尝试的结果未知（可能已施加、账没记上）。
		s.dropIntentLocked(act, it)
		if act.RetrySafety != RetrySafetySafe {
			s.uncertain[act.Key] = "上一次尝试的租约已过期，结果未知"
			return OutcomeUncertain, Lease{}, Result{}, nil
		}
		// 声明了「下游按幂等键去重」⇒ 重做不会重复施加 ⇒ 可重新授予。
	}

	// 判据四：资源被他人持有且未过期 → 拒绝（不发放令牌）。
	if h, ok := s.holders[act.Resource]; ok && now.Before(h.expires) {
		return OutcomeContended, Lease{}, Result{}, nil
	}

	// 判据五：授予持有，令牌 = 已见最大值 + 1（不回退），
	// 并**原子地登记执行意图**（少了它，「进程死在记账之前」就查无实据）。
	next := s.maxFence[act.Resource] + 1
	s.maxFence[act.Resource] = next
	lease := Lease{Resource: act.Resource, Fence: next, TTL: ttl}
	s.holders[act.Resource] = fakeHold{lease: lease, holder: act.Key, expires: now.Add(ttl)}
	s.intents[act.Key] = fakeIntent{resource: act.Resource, fence: next, expires: now.Add(ttl)}
	return OutcomeApplied, lease, Result{}, nil
}

// dropIntentLocked 撤销一条执行意图，并把**它自己的**资源闸一并作废。
//
// 作废资源闸的理由：意图与资源持有是同一次尝试的两个投影，同刻到期。
// 意图过期时那份持有也过期了，但**记录还在**——若不清掉，原持有者拿着
// 同一枚令牌仍能 Release/Complete/Renew（栅栏只在接管时递增，没人接管时
// 令牌没变），于是「这次尝试已经转入未知态」这件事在资源侧看不出来。
// 清掉它之后，原持有者的任何迟到操作都被拒（ErrFenceStale）。
//
// 只清**自己**那份：闸若已易主（别人接管了同一个资源），那是别人的闸，
// 不能碰。
func (s *fakeStore) dropIntentLocked(act Action, it fakeIntent) {
	delete(s.intents, act.Key)
	if it.resource == "" {
		return
	}
	if h, ok := s.holders[it.resource]; ok && h.holder == act.Key && h.lease.Fence == it.fence {
		delete(s.holders, it.resource)
	}
}

// intentOwned 本次尝试是否仍归该令牌所有。
func (s *fakeStore) intentOwned(act Action, lease Lease) bool {
	it, ok := s.intents[act.Key]
	return ok && it.fence == lease.Fence
}

// holdOwned 资源闸是否仍由本次尝试持有。
//
// 只比令牌不够：释放闸之后令牌**没有**变（栅栏只在接管时递增），
// 于是「释放后的租约」能拿同一个令牌继续续期/记账——闸已经易主，
// 而持有者以为还拿着。这就是「释放后的租约仍能续期」那道洞。
func (s *fakeStore) holdOwned(act Action, lease Lease) bool {
	h, ok := s.holders[act.Resource]
	return ok && h.lease.Fence == lease.Fence && h.holder == act.Key
}

func (s *fakeStore) Complete(_ context.Context, act Action, lease Lease, res Result) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	if s.completeErr != nil {
		return Result{}, s.completeErr
	}
	// 幂等：先到者的结果为准，不被后到者覆盖。
	if prior, ok := s.done[act.Key]; ok {
		return prior, nil
	}
	if !s.holdOwned(act, lease) {
		return Result{}, errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代或资源已易主", "resource", act.Resource)
	}
	if !s.intentOwned(act, lease) {
		return Result{}, errors.Wrap(ErrFenceStale, "本次尝试已不属于该令牌（已转入未知态或已被放弃）", "resource", act.Resource)
	}
	s.done[act.Key] = res
	delete(s.intents, act.Key)
	// 动作已完成 ⇒ 资源闸释放（否则后来者会被「资源正忙」挡住）。
	delete(s.holders, act.Resource)
	return res, nil
}

// Release 只释放**资源持有**，不动该 Key 的执行意图。
func (s *fakeStore) Release(_ context.Context, act Action, lease Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseCalls++
	if s.releaseErr != nil {
		return s.releaseErr
	}
	if !s.holdOwned(act, lease) {
		return errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代或资源已易主", "resource", act.Resource)
	}
	delete(s.holders, act.Resource)
	// 释放**不**撤销执行意图：意图没被裁决之前，这个 Key 不得再被执行。
	return nil
}

// Abandon 显式放弃本次尝试：撤销执行意图 + 释放资源持有。
func (s *fakeStore) Abandon(_ context.Context, act Action, lease Lease, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.abandonCalls++
	if s.abandonErr != nil {
		return s.abandonErr
	}
	if !s.intentOwned(act, lease) {
		return errors.Wrap(ErrFenceStale, "本次尝试已不属于该令牌（已转入未知态或已被放弃）", "resource", act.Resource)
	}
	delete(s.intents, act.Key)
	// 资源闸只在仍是自己的时才放——易主了就不碰（那是新持有者的闸）。
	if s.holdOwned(act, lease) {
		delete(s.holders, act.Resource)
	}
	return nil
}

func (s *fakeStore) MarkUncertain(_ context.Context, act Action, lease Lease, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markUncertainCalls++
	if s.markUncertainErr != nil {
		return s.markUncertainErr
	}
	// 令牌校验 + 意图归属：过期持有者不得改写新持有者的状态，
	// 也不得把一段「已被放弃」的尝试改写成未知。
	if !s.holdOwned(act, lease) {
		return errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代或资源已易主", "resource", act.Resource)
	}
	if !s.intentOwned(act, lease) {
		return errors.Wrap(ErrFenceStale, "本次尝试已不属于该令牌（已转入未知态或已被放弃）", "resource", act.Resource)
	}
	s.uncertain[act.Key] = reason
	delete(s.intents, act.Key)
	return nil
}

func (s *fakeStore) ResolveUncertain(_ context.Context, act Action, res Resolution) (Outcome, Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveCalls++
	if s.resolveErr != nil {
		return OutcomeContended, Result{}, s.resolveErr
	}
	// 已完成 → 幂等重放。
	if prior, ok := s.done[act.Key]; ok {
		return OutcomeReplayed, prior, nil
	}
	if _, ok := s.uncertain[act.Key]; !ok {
		return OutcomeContended, Result{}, ErrNotUncertain
	}
	// 裁决即结论：执行意图一并撤销（这次尝试已有结论）。
	delete(s.intents, act.Key)
	if res.Applied {
		s.done[act.Key] = res.Result
		delete(s.uncertain, act.Key)
		return OutcomeApplied, res.Result, nil
	}
	delete(s.uncertain, act.Key)
	return OutcomeContended, Result{}, nil
}

func (s *fakeStore) Renew(_ context.Context, act Action, lease Lease, ttl time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 资格校验与续期在同一次原子操作里：校验不过就不续期。
	if !s.holdOwned(act, lease) {
		return Lease{}, errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代或资源已易主", "resource", act.Resource)
	}
	if !s.intentOwned(act, lease) {
		return Lease{}, errors.Wrap(ErrFenceStale, "本次尝试已不属于该令牌（已转入未知态或已被放弃）", "resource", act.Resource)
	}
	now := s.clockLocked()
	cur := s.holders[act.Resource]
	cur.expires = now.Add(ttl)
	cur.lease.TTL = ttl
	s.holders[act.Resource] = cur
	// 意图的到期时刻与资源持有同步推后：两者是同一次尝试的两个投影。
	it := s.intents[act.Key]
	it.expires = now.Add(ttl)
	s.intents[act.Key] = it
	return cur.lease, nil
}
