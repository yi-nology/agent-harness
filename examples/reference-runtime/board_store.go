// board_store.go —— taskboard.Board 的参考实现（内存版）。
//
// 生命周期: Experimental（v0.33.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 令牌由存储铸造（v0.33.0 的要害）
//
// 认领请求里**没有令牌**：调用方只说「要多长的租约」，令牌由本存储铸出来
// 并随 Task 返回。调用方自带令牌的话，「释放/完成只认 token」这条契约
// 名存实亡——它能造出任意 token，于是「输家的迟到释放放掉赢家的闸」
// 又回来了，而报告里一切正常。
//
// 到期时刻也由**存储自己的时钟**算：调用方的时钟与存储的时钟可能对不上，
// 跨进程时更不能假设一致。
package main

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// boardStore 任务认领面：认领是读-判-写，必须原子。
type boardStore struct {
	mu    sync.Mutex
	tasks map[taskboard.TaskID]taskboard.Task

	// seq 令牌计数器：每次认领铸一枚新令牌（不复用 ⇒ 旧令牌无法重用）。
	seq uint64

	// now 可注入时钟（过期判定必须读它，否则过期场景恒不触发）。
	now func() time.Time
}

func newBoardStore() *boardStore {
	return &boardStore{
		tasks: map[taskboard.TaskID]taskboard.Task{},
		now:   time.Now,
	}
}

// UseClock 注入时钟（boardtest 契约套件用）。
func (s *boardStore) UseClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

// clock 当前时刻。**只在持锁时调用**。
func (s *boardStore) clock() time.Time { return s.now() }

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
//
// 令牌由本存储铸造（见文件头注释）；到期时刻按存储自己的时钟算。
func (s *boardStore) Claim(_ context.Context, id taskboard.TaskID, agent taskboard.AgentID, req taskboard.ClaimRequest) (taskboard.Task, error) {
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
	if leaseHeld(t, s.clock()) {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrTaken, "任务已被他人认领", "task", string(id))
	}

	// 铸一枚新令牌：每次认领都换新 ⇒ 上一轮的令牌无法重用（ABA）。
	s.seq++
	lease := taskboard.Lease{Token: fmt.Sprintf("tok-%d", s.seq)}
	if req.TTL > 0 {
		lease.Until = s.clock().Add(req.TTL)
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
func leaseHeld(t taskboard.Task, now time.Time) bool {
	if t.State != taskboard.StateClaimed {
		return false
	}
	if t.Lease.Until.IsZero() {
		return true
	}
	return t.Lease.Until.After(now)
}
