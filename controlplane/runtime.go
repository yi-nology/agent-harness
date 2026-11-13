// runtime.go —— MemPlane 的**最小运行时面**（建场景与认领/提交/归还）。
//
// 控制面的介入判据必须对着运行时的租约与代次演示（强释之后旧令牌提交被拒、
// 下一次认领代次严格递增），没有运行时面就没有可演示的「同一套判据」。
// 这些方法不是 Plane 契约的一部分——它们是「运行时那一侧」的参考实现：
// 宿主的真实运行时是自己的 taskboard.Board / sideeffect Runner / 状态存储。
//
// Seed* 先整单校验再落账：部分落账的 run 是半成品状态，视图会把
// 「建了一半」当成「建成了」。
package controlplane

import (
	"context"
	"fmt"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/contract"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// SeedRun 运行时侧：建一个运行及其任务（taskID 全局唯一）。
// status 为空取 contract.StatusCreated。重复 run / 重复任务 → ErrAlreadyExists。
// 先整单校验再落账：部分落账的 run 是半成品状态，视图会把「建了一半」
// 当成「建成了」。
func (m *MemPlane) SeedRun(id RunID, status string, tasks []taskboard.Task) error {
	if id == "" {
		return errors.Wrap(ErrInvalidRequest, "SeedRun: run id 为空")
	}
	if status == "" {
		status = contract.StatusCreated
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[id]; ok {
		return errors.Wrap(ErrAlreadyExists, "SeedRun: run 已存在")
	}
	seen := make(map[taskboard.TaskID]bool, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			return errors.Wrap(ErrInvalidRequest, "SeedRun: 任务缺 id")
		}
		if seen[t.ID] {
			return errors.Wrap(ErrInvalidRequest, "SeedRun: 同一 run 内任务 id 重复")
		}
		seen[t.ID] = true
		if _, exists := m.tasks[t.ID]; exists {
			return errors.Wrap(ErrAlreadyExists, "SeedRun: 任务已存在")
		}
	}
	run := &memRun{status: status, gen: 1}
	for _, t := range tasks {
		m.tasks[t.ID] = &memTask{task: cloneTask(t), gen: t.Lease.Fence, run: id}
		run.taskIDs = append(run.taskIDs, t.ID)
	}
	m.runs[id] = run
	return nil
}

// SeedEffect 运行时侧：登记一个处于「结果未知」态的副作用键。
// unsafe 对应 sideeffect 的 RetrySafetyUnsafe（未生效裁决不自动开门）。
func (m *MemPlane) SeedEffect(key string, unsafe bool) error {
	if key == "" {
		return errors.Wrap(ErrInvalidRequest, "SeedEffect: 效果键为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.effects[key]; ok {
		return errors.Wrap(ErrAlreadyExists, "SeedEffect: 效果键已存在")
	}
	m.effects[key] = &memEffect{uncertain: true, unsafe: unsafe}
	return nil
}

// Claim 运行时侧：认领（铸造令牌与接管代次，语义对齐 taskboard.Board.Claim
// ——被占用 ErrTaken / 终态 ErrTerminal / 依赖未完成 ErrBlocked）。
func (m *MemPlane) Claim(_ context.Context, id taskboard.TaskID, agent taskboard.AgentID, ttl time.Duration) (taskboard.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tasks[id]
	if !ok {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrNotFound, "任务不存在: "+string(id))
	}
	switch mt.task.State {
	case taskboard.StateClaimed:
		return taskboard.Task{}, errors.Wrap(taskboard.ErrTaken, "任务已被他人认领")
	case taskboard.StateDone:
		return taskboard.Task{}, errors.Wrap(taskboard.ErrTerminal, "任务已是终态")
	}
	for _, d := range mt.task.Deps {
		dep, ok := m.tasks[d]
		if !ok || !dep.task.State.Terminal() {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrBlocked, "依赖未完成: "+string(d))
		}
	}
	m.tokens++
	mt.gen++
	mt.task.State = taskboard.StateClaimed
	mt.task.Owner = agent
	mt.task.Lease = taskboard.Lease{
		Token: fmt.Sprintf("t-%d", m.tokens),
		Fence: mt.gen,
	}
	if ttl > 0 {
		mt.task.Lease.Until = time.Now().Add(ttl)
	}
	mt.task.Seq++
	return cloneTask(mt.task), nil
}

// Complete 运行时侧：凭 token 完成（旧令牌 / 非认领态 → taskboard.ErrLeaseLost）。
func (m *MemPlane) Complete(_ context.Context, id taskboard.TaskID, token string) error {
	return m.finish(id, token, taskboard.StateDone)
}

// Release 运行时侧：凭 token 归还认领（旧令牌 → taskboard.ErrLeaseLost）。
func (m *MemPlane) Release(_ context.Context, id taskboard.TaskID, token string) error {
	return m.finish(id, token, taskboard.StateOpen)
}

// finish 凭令牌的租约变更（Complete / Release 共用判据）。
func (m *MemPlane) finish(id taskboard.TaskID, token string, to taskboard.State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tasks[id]
	if !ok {
		return errors.Wrap(taskboard.ErrNotFound, "任务不存在: "+string(id))
	}
	if mt.task.State != taskboard.StateClaimed || token == "" || token != mt.task.Lease.Token {
		// 令牌不匹配 / 任务已不在认领态：一律 ErrLeaseLost——
		// 迟到持有者不被告知「任务其实已经易主到什么状态」。
		return errors.Wrap(taskboard.ErrLeaseLost, "令牌不匹配或租约已不在")
	}
	mt.task.State = to
	mt.task.Owner = ""
	mt.task.Lease = taskboard.Lease{}
	mt.task.Seq++
	return nil
}
