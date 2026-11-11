// 本文件是参考运行时的**内存存储**：三份存储合起来满足三套存储中立契约——
// checkpoint.StoreCAS（进度快照的单调存取）、sideeffect.Store（副作用闸）
// 与 taskboard.Board（任务认领面）。
// 之所以是三个类型：三套契约里有两个方法同名而不同义（sideeffect.Store.Complete
// 与 taskboard.Board.Complete 签名不同），塞进一个类型必然二选一；分开还顺带把
// 「哪段判据归哪把锁」写清楚了——每套契约各自原子，彼此不共享状态。
// ⚠️ 原子性来自**进程内的互斥锁**，只在单进程内成立；真实宿主必须把同样的判据
// 落到数据库的原子操作上（条件更新 / CAS 循环 / 事务），见 README。
package main

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// memStore 三份存储的集合。
type memStore struct {
	snaps   *snapStore
	effects *effectStore
	tasks   *boardStore
}

func newMemStore() *memStore {
	return &memStore{
		snaps:   newSnapStore(),
		effects: newEffectStore(),
		tasks:   newBoardStore(),
	}
}

// ── checkpoint.Store / checkpoint.StoreCAS ──────────────────────────────────

// snapStore 进度快照的存取（序号单调）。
type snapStore struct {
	mu    sync.Mutex
	snaps map[string]checkpoint.Snapshot
}

func newSnapStore() *snapStore {
	return &snapStore{snaps: map[string]checkpoint.Snapshot{}}
}

// SaveIfNewer 仅当已持久化的序号**严格小于**本次序号时才写入。
func (s *snapStore) SaveIfNewer(_ context.Context, snap checkpoint.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.snaps[snap.RunID]
	if ok && snap.Seq != 0 && cur.Seq >= snap.Seq {
		return errors.Wrap(checkpoint.ErrStaleSeq,
			"快照序号未严格递增", "run_id", snap.RunID, "seq", snap.Seq, "persisted", cur.Seq)
	}
	s.snaps[snap.RunID] = snap.Clone()
	return nil
}

// Save 满足 checkpoint.Store：同一 (RunID, Seq) 幂等。
func (s *snapStore) Save(_ context.Context, snap checkpoint.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, ok := s.snaps[snap.RunID]
	if ok && cur.Seq == snap.Seq {
		return nil
	}
	if ok && cur.Seq > snap.Seq {
		return errors.Wrap(checkpoint.ErrStaleSeq, "快照序号回退", "run_id", snap.RunID)
	}
	s.snaps[snap.RunID] = snap.Clone()
	return nil
}

// Load 读取快照。无记录时返回 checkpoint.ErrNotFound（**不得**返回零值 + nil）。
func (s *snapStore) Load(_ context.Context, runID string) (checkpoint.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, ok := s.snaps[runID]
	if !ok {
		return checkpoint.Snapshot{}, errors.Wrap(checkpoint.ErrNotFound, "该运行无进度快照", "run_id", runID)
	}
	return snap.Clone(), nil
}

// ── sideeffect.Store ────────────────────────────────────────────────────────

// effectStore 副作用闸：幂等键 + 资源独占 + 栅栏令牌，三者共享一份真相。
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
// 判定顺序是契约里最要紧的一行：**已完成 > 结果未知 > 资源被占 > 授予**。
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
	rr := s.resLocked(act.Resource)
	if rr.holder != "" && rr.until.After(s.clock()) {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, nil
	}
	rr.fence++
	rr.holder = act.Key
	rr.until = s.clock().Add(ttl)
	return sideeffect.OutcomeApplied,
		sideeffect.Lease{Resource: act.Resource, Fence: rr.fence, TTL: ttl},
		sideeffect.Result{}, nil
}

// Complete 标记该键的动作已完成并留存结果。**先到者为准**。
func (s *effectStore) Complete(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := s.actLocked(act.Key)
	if rec.done {
		return rec.result, nil // 幂等：先到者的结果不被后到者覆盖
	}
	rr := s.resLocked(act.Resource)
	if lease.Fence == 0 || lease.Fence != rr.fence {
		return sideeffect.Result{}, errors.Wrap(sideeffect.ErrFenceStale,
			"完成被拒：本次持有令牌已不是最新代", "key", string(act.Key), "resource", act.Resource)
	}
	rec.done = true
	rec.result = res
	rr.holder = "" // 动作已完成 ⇒ 资源闸释放（否则后来者会被「资源正忙」挡住）
	return rec.result, nil
}

// Release 释放持有（凭令牌）。**释放不把键标记为已完成**。
func (s *effectStore) Release(_ context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rr := s.resLocked(act.Resource)
	if lease.Fence == 0 || lease.Fence != rr.fence {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"释放被拒：本次持有令牌已不是最新代", "key", string(act.Key), "resource", act.Resource)
	}
	if rr.holder == act.Key {
		rr.holder = ""
	}
	return nil
}

// Renew 续期并**同时确认持有资格**：闸已易主则拒绝续期（假续期是重复施加的入口）。
func (s *effectStore) Renew(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rr := s.resLocked(act.Resource)
	if lease.Fence == 0 || lease.Fence != rr.fence {
		return sideeffect.Lease{}, errors.Wrap(sideeffect.ErrFenceStale,
			"续期被拒：闸已易主", "key", string(act.Key), "resource", act.Resource)
	}
	rr.until = s.clock().Add(ttl)
	return sideeffect.Lease{Resource: act.Resource, Fence: lease.Fence, TTL: ttl}, nil
}

// MarkUncertain 把该键标记为「结果未知」（按键，与资源租约无关）。
func (s *effectStore) MarkUncertain(_ context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rr := s.resLocked(act.Resource)
	if lease.Fence == 0 || lease.Fence != rr.fence {
		return errors.Wrap(sideeffect.ErrFenceStale,
			"标记未知被拒：本次持有令牌已不是最新代", "key", string(act.Key), "resource", act.Resource)
	}
	rec := s.actLocked(act.Key)
	rec.uncertain = true
	rec.reason = reason
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
	if res.Applied {
		rec.done = true
		rec.uncertain = false
		rec.result = res.Result
		return sideeffect.OutcomeReplayed, rec.result, nil
	}
	rec.uncertain = false
	return sideeffect.OutcomeApplied, sideeffect.Result{}, nil
}

// ── taskboard.Board ─────────────────────────────────────────────────────────

// boardStore 任务认领面：认领是读-判-写，必须原子。
type boardStore struct {
	mu    sync.Mutex
	tasks map[taskboard.TaskID]taskboard.Task
}

func newBoardStore() *boardStore {
	return &boardStore{tasks: map[taskboard.TaskID]taskboard.Task{}}
}

// Seed 摆初始任务（契约里没有「创建」入口：任务拓扑是宿主的资产）。
func (s *boardStore) Seed(tasks ...taskboard.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range tasks {
		if t.ID == "" {
			continue
		}
		t.Seq++
		s.tasks[t.ID] = t
	}
}

// List 返回一致快照（不是边改边读的流）。
func (s *boardStore) List(_ context.Context) ([]taskboard.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]taskboard.Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		t.Deps = append([]taskboard.TaskID(nil), t.Deps...)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Claim 认领一个任务。**读-判-写不可分割**：这里靠同一把互斥锁保证只有一个赢家。
func (s *boardStore) Claim(_ context.Context, id taskboard.TaskID, agent taskboard.AgentID, lease taskboard.Lease) (taskboard.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
	}
	if t.State.Terminal() {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrTerminal, "任务已是终态", "task", string(id))
	}
	for _, dep := range t.Deps {
		d, ok := s.tasks[dep]
		if !ok || !d.State.Terminal() {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrBlocked,
				"依赖未完成", "task", string(id), "dep", string(dep))
		}
	}
	if leaseHeld(t) {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrTaken, "任务已被他人认领", "task", string(id))
	}
	if lease.Token == "" {
		return taskboard.Task{}, errors.New("认领必须带租约令牌：释放与完成都凭令牌认人")
	}
	t.State = taskboard.StateClaimed
	t.Owner = agent
	t.Lease = lease
	t.Seq++
	s.tasks[id] = t
	return t, nil
}

// Release 归还认领（凭令牌）：非持有者释放一律被拒，不许静默成功。
func (s *boardStore) Release(_ context.Context, id taskboard.TaskID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok {
		return errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
	}
	if t.State != taskboard.StateClaimed || t.Lease.Token != token {
		return errors.Wrap(taskboard.ErrLeaseLost, "租约令牌不匹配", "task", string(id))
	}
	t.State = taskboard.StateOpen
	t.Owner = ""
	t.Lease = taskboard.Lease{}
	t.Seq++
	s.tasks[id] = t
	return nil
}

// Complete 标记完成（凭令牌）：终态不带 owner，避免「谁做完的」与「谁还占着」混在一起。
func (s *boardStore) Complete(_ context.Context, id taskboard.TaskID, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.tasks[id]
	if !ok {
		return errors.Wrap(taskboard.ErrNotFound, "任务不存在", "task", string(id))
	}
	if t.State != taskboard.StateClaimed || t.Lease.Token != token {
		return errors.Wrap(taskboard.ErrLeaseLost, "租约令牌不匹配", "task", string(id))
	}
	t.State = taskboard.StateDone
	t.Owner = ""
	t.Lease = taskboard.Lease{}
	t.Seq++
	s.tasks[id] = t
	return nil
}

// leaseHeld 该任务当前是否仍被持有。
// 零值到期时刻表示「不定期」——当成已过期会让「两个执行者同时认领」变成「两个赢家」。
func leaseHeld(t taskboard.Task) bool {
	if t.State != taskboard.StateClaimed {
		return false
	}
	if t.Lease.Until.IsZero() {
		return true
	}
	return t.Lease.Until.After(time.Now())
}
