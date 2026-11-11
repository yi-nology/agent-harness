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
	// recons Key → 对账占用（BeginReconcile 写入）。
	recons map[Key]fakeRecon
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

// fakeHold 一次持有（含到期时刻与持有者身份）。
type fakeHold struct {
	lease   Lease
	holder  string
	expires time.Time
}

// fakeIntent 一次未裁决的执行意图。
//
// 它回答的不是「做过没有」，而是「这个 Key 上有一次尝试还没结论」——
// 进程死在施加成功之后、记账之前时，存储侧留下的就是这一条。
type fakeIntent struct {
	resource string
	fence    Fence
	holder   string
	expires  time.Time
}

// fakeRecon 一次对账认领。
type fakeRecon struct {
	holder  string
	expires time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		done:      map[Key]Result{},
		uncertain: map[Key]string{},
		holders:   map[string]fakeHold{},
		intents:   map[Key]fakeIntent{},
		recons:    map[Key]fakeRecon{},
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

	// 判据二点五：对账占用未过期 ⇒ 对账中（仍是未知态，不得授予执行权）。
	// 过期的占用在这里清掉（可重入：对账者死亡不永久占住这个 Key）。
	if r, ok := s.recons[act.Key]; ok {
		if now.Before(r.expires) {
			return OutcomeUncertain, Lease{}, Result{}, nil
		}
		delete(s.recons, act.Key)
	}

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
	holder := MintHolder()
	lease := Lease{Resource: act.Resource, Fence: next, Holder: holder, TTL: ttl}
	s.holders[act.Resource] = fakeHold{lease: lease, holder: holder, expires: now.Add(ttl)}
	s.intents[act.Key] = fakeIntent{resource: act.Resource, fence: next, holder: holder, expires: now.Add(ttl)}
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
	if h, ok := s.holders[it.resource]; ok && h.holder == it.holder && h.lease.Fence == it.fence {
		delete(s.holders, it.resource)
	}
}

// leaseResourceErr 三项联查第一项：租约的资源必须与动作的资源一致。
func leaseResourceErr(act Action, lease Lease) error {
	if lease.Resource != act.Resource {
		return errors.Wrap(ErrResourceMismatch, "凭据被拒：租约与动作的资源不一致",
			"act_resource", act.Resource, "lease_resource", lease.Resource)
	}
	return nil
}

// intentOwnerErr 三项联查：本次尝试是否仍归**这份凭据**所有。
func (s *fakeStore) intentOwnerErr(act Action, lease Lease) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	it, ok := s.intents[act.Key]
	if !ok {
		return errors.Wrap(ErrFenceStale, "本次尝试已无未裁决的执行意图", "key", string(act.Key))
	}
	if it.fence != lease.Fence {
		return errors.Wrap(ErrFenceStale, "本次尝试已不属于该令牌（已转入未知态或已被放弃）", "resource", act.Resource)
	}
	if it.holder != lease.Holder {
		return errors.Wrap(ErrNotHolder, "凭据被拒：持有者身份不匹配（意图不是本次授予铸造的那份）", "key", string(act.Key))
	}
	return nil
}

// holdOwnerErr 三项联查：资源闸是否仍由**这份凭据**持有。
//
// 判定顺序有意如此：令牌不匹配或已释放 → ErrFenceStale（这一代已经过去）；
// 令牌还在这一代但持有者不是出示者 → ErrNotHolder（令牌会经下游流转，
// 见过的组件拿着令牌来冒充持有者，被这道防线拦下）。
func (s *fakeStore) holdOwnerErr(act Action, lease Lease) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	h, ok := s.holders[act.Resource]
	if !ok || lease.Fence == 0 || h.lease.Fence != lease.Fence {
		return errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代或资源已释放", "resource", act.Resource)
	}
	if h.holder != lease.Holder {
		return errors.Wrap(ErrNotHolder, "凭据被拒：持有者身份不匹配（持有不是本次授予铸造的那份）", "resource", act.Resource)
	}
	return nil
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
	if err := s.holdOwnerErr(act, lease); err != nil {
		return Result{}, err
	}
	if err := s.intentOwnerErr(act, lease); err != nil {
		return Result{}, err
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
	if err := s.holdOwnerErr(act, lease); err != nil {
		return err
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
	if err := s.intentOwnerErr(act, lease); err != nil {
		return err
	}
	delete(s.intents, act.Key)
	// 资源闸只在仍是自己的时才放——易主了就不碰（那是新持有者的闸）。
	if err := s.holdOwnerErr(act, lease); err == nil {
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
	// 三项联查：过期持有者不得改写新持有者的状态，也不得把一段
	// 「已被放弃」的尝试改写成未知。校验全部发生在**任何状态被改之前**——
	// 写失败（含 markUncertainErr 注入）时意图原样留在存储里（契约硬要求：
	// 标记失败后它是唯一还在挡着重试的东西）。
	if err := s.holdOwnerErr(act, lease); err != nil {
		return err
	}
	if err := s.intentOwnerErr(act, lease); err != nil {
		return err
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
	// 裁决即结论：执行意图与对账占用一并撤销（这次尝试已有结论）。
	delete(s.intents, act.Key)
	delete(s.recons, act.Key)
	if res.Applied {
		s.done[act.Key] = res.Result
		delete(s.uncertain, act.Key)
		return OutcomeReplayed, res.Result, nil
	}
	if act.RetrySafety == RetrySafetyUnsafe && !res.ForceRetryable {
		// 「不可重试」的动作，一句来自对账的「未生效」不足以重新开门。
		// 未知标记原样保留（仍被硬阻塞），放行必须显式签字。
		return OutcomeUncertain, Result{}, ErrUnsafeNotRetryable
	}
	delete(s.uncertain, act.Key)
	return OutcomeContended, Result{}, nil
}

func (s *fakeStore) Renew(_ context.Context, act Action, lease Lease, ttl time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// 资格校验（三项联查）与续期在同一次原子操作里：校验不过就不续期。
	if err := s.holdOwnerErr(act, lease); err != nil {
		return Lease{}, err
	}
	if err := s.intentOwnerErr(act, lease); err != nil {
		return Lease{}, err
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

// BeginReconcile 认领对账权：同一 Key 同一时刻至多一个对账者，过期可重入。
func (s *fakeStore) BeginReconcile(_ context.Context, act Action, ttl time.Duration) (Reconcile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.done[act.Key]; ok {
		return Reconcile{}, ErrNotUncertain
	}
	if _, ok := s.uncertain[act.Key]; !ok {
		return Reconcile{}, ErrNotUncertain
	}
	now := s.clockLocked()
	if r, ok := s.recons[act.Key]; ok && now.Before(r.expires) {
		return Reconcile{}, ErrReconcileTaken
	}
	holder := MintHolder()
	s.recons[act.Key] = fakeRecon{holder: holder, expires: now.Add(ttl)}
	return Reconcile{Holder: holder, ExpiresAt: now.Add(ttl)}, nil
}
