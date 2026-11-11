// durable_store_test.go —— 上面那条链路用的「持久化存储」最小实现。
//
// 为什么单放一文件：本仓的结构阈值是单文件 400 行，而这份存储要同时满足
// checkpoint.StoreCAS、sideeffect.Store、commitgate 三套契约——
// 它的体量已经占了整条链路的一半，与用例混在一个文件里必然超限。
//
// 它的语义**按契约写**（Acquire 一次原子判定、Complete 校验令牌与持有者、
// SaveIfNewer 严格递增且拒收序号为 0 的快照、执行意图随 Acquire 原子登记）——
// 把契约抄成宽松版就等于把被测对象换掉，用例会绿得毫无意义。
package checkpoint_test

import (
	"context"
	"sync"
	"time"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/commitgate"
	"github.com/yi-nology/goagent-kernel/sideeffect"
)

type durable struct {
	mu sync.Mutex

	snaps map[string]checkpoint.Snapshot

	done      map[sideeffect.Key]sideeffect.Result
	uncertain map[sideeffect.Key]string
	held      map[string]sideeffect.Lease
	expiry    map[string]time.Time
	holders   map[string]sideeffect.Key
	intents   map[sideeffect.Key]durableIntent
	maxFence  map[string]sideeffect.Fence
	terminals map[commitgate.Ref]commitgate.Commit

	// applied 真实世界的副作用计数：**跨重启累计**，是「有没有重放」的唯一证据。
	applied map[string]int
}

func newDurable() *durable {
	return &durable{
		snaps:     map[string]checkpoint.Snapshot{},
		done:      map[sideeffect.Key]sideeffect.Result{},
		uncertain: map[sideeffect.Key]string{},
		held:      map[string]sideeffect.Lease{},
		expiry:    map[string]time.Time{},
		holders:   map[string]sideeffect.Key{},
		intents:   map[sideeffect.Key]durableIntent{},
		maxFence:  map[string]sideeffect.Fence{},
		terminals: map[commitgate.Ref]commitgate.Commit{},
		applied:   map[string]int{},
	}
}

// durableIntent 一次未裁决的执行意图（Acquire 原子登记）。
type durableIntent struct {
	resource string
	fence    sideeffect.Fence
	expires  time.Time
}

// ── checkpoint.StoreCAS ──────────────────────────────────────────────

func (d *durable) SaveIfNewer(_ context.Context, s checkpoint.Snapshot) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cur, ok := d.snaps[s.RunID]; ok && cur.Seq >= s.Seq {
		return checkpoint.ErrStaleSeq
	}
	d.snaps[s.RunID] = s.Clone()
	return nil
}

func (d *durable) Load(_ context.Context, runID string) (checkpoint.Snapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.snaps[runID]
	if !ok {
		return checkpoint.Snapshot{}, checkpoint.ErrNotFound
	}
	return s.Clone(), nil
}

// ── sideeffect.Store ────────────────────────────────────────────────

func (d *durable) Acquire(_ context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 已完成优先于占用：后来者应当拿回结果，而不是被「资源正忙」挡住。
	if res, ok := d.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, sideeffect.Lease{}, res, nil
	}
	// 结果未知 → 不得再执行（否则就是重复施加）。
	if _, ok := d.uncertain[act.Key]; ok {
		return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	now := time.Now()
	// 执行意图未裁决 → 不得重新授予执行权（进程死在记账之前的那条窗口）。
	if it, ok := d.intents[act.Key]; ok {
		if now.Before(it.expires) {
			return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
		// 租约过期 ⇒ 这次尝试的结果未知。
		d.dropIntent(act, it)
		if act.RetrySafety != sideeffect.RetrySafetySafe {
			d.uncertain[act.Key] = "上一次尝试的租约已过期，结果未知"
			return sideeffect.OutcomeUncertain, sideeffect.Lease{}, sideeffect.Result{}, nil
		}
	}
	if _, ok := d.held[act.Resource]; ok && now.Before(d.expiry[act.Resource]) {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	d.maxFence[act.Resource]++
	lease := sideeffect.Lease{Resource: act.Resource, Fence: d.maxFence[act.Resource], TTL: ttl}
	d.held[act.Resource] = lease
	d.expiry[act.Resource] = now.Add(ttl)
	d.holders[act.Resource] = act.Key
	// 意图与令牌在同一次原子操作里登记。
	d.intents[act.Key] = durableIntent{resource: act.Resource, fence: lease.Fence, expires: now.Add(ttl)}
	return sideeffect.OutcomeApplied, lease, sideeffect.Result{}, nil
}

// dropIntent 撤销一条执行意图，并把它自己的资源闸一并作废。
func (d *durable) dropIntent(act sideeffect.Action, it durableIntent) {
	delete(d.intents, act.Key)
	if it.resource == "" {
		return
	}
	if h, ok := d.holders[it.resource]; ok && h == act.Key {
		delete(d.held, it.resource)
		delete(d.expiry, it.resource)
		delete(d.holders, it.resource)
	}
}

// holdOwned 资源闸是否仍由本次尝试持有（只比令牌不够：释放后令牌没变）。
func (d *durable) holdOwned(act sideeffect.Action, lease sideeffect.Lease) bool {
	cur, ok := d.held[act.Resource]
	if !ok || cur.Fence != lease.Fence {
		return false
	}
	return d.holders[act.Resource] == act.Key
}

// intentOwned 本次尝试是否仍归该令牌所有。
func (d *durable) intentOwned(act sideeffect.Action, lease sideeffect.Lease) bool {
	it, ok := d.intents[act.Key]
	return ok && it.fence == lease.Fence
}

func (d *durable) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if prior, ok := d.done[act.Key]; ok {
		return prior, nil
	}
	if !d.holdOwned(act, lease) || !d.intentOwned(act, lease) {
		return sideeffect.Result{}, sideeffect.ErrFenceStale
	}
	d.done[act.Key] = res
	delete(d.intents, act.Key)
	delete(d.held, act.Resource)
	delete(d.expiry, act.Resource)
	delete(d.holders, act.Resource)
	return res, nil
}

// Release 只释放资源持有，不动该键的执行意图。
func (d *durable) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.holdOwned(act, lease) {
		return sideeffect.ErrFenceStale
	}
	delete(d.held, act.Resource)
	delete(d.expiry, act.Resource)
	delete(d.holders, act.Resource)
	return nil
}

// Abandon 显式放弃本次尝试：撤销执行意图，并释放资源持有。
func (d *durable) Abandon(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.intentOwned(act, lease) {
		return sideeffect.ErrFenceStale
	}
	delete(d.intents, act.Key)
	if d.holdOwned(act, lease) {
		delete(d.held, act.Resource)
		delete(d.expiry, act.Resource)
		delete(d.holders, act.Resource)
	}
	return nil
}

func (d *durable) MarkUncertain(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.holdOwned(act, lease) || !d.intentOwned(act, lease) {
		return sideeffect.ErrFenceStale
	}
	d.uncertain[act.Key] = reason
	delete(d.intents, act.Key)
	return nil
}

func (d *durable) ResolveUncertain(_ context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if prior, ok := d.done[act.Key]; ok {
		return sideeffect.OutcomeReplayed, prior, nil
	}
	if _, ok := d.uncertain[act.Key]; !ok {
		return sideeffect.OutcomeContended, sideeffect.Result{}, sideeffect.ErrNotUncertain
	}
	delete(d.intents, act.Key)
	if res.Applied {
		d.done[act.Key] = res.Result
		delete(d.uncertain, act.Key)
		return sideeffect.OutcomeApplied, res.Result, nil
	}
	delete(d.uncertain, act.Key)
	return sideeffect.OutcomeContended, sideeffect.Result{}, nil
}

func (d *durable) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.holdOwned(act, lease) || !d.intentOwned(act, lease) {
		return sideeffect.Lease{}, sideeffect.ErrFenceStale
	}
	now := time.Now()
	d.expiry[act.Resource] = now.Add(ttl)
	it := d.intents[act.Key]
	it.expires = now.Add(ttl)
	d.intents[act.Key] = it
	lease.TTL = ttl
	return lease, nil
}
