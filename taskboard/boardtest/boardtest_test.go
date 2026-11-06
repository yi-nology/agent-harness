// boardtest_test.go —— 用一份内存实现把套件跑绿，再用一份**故意不原子**的实现
// 证明套件真的会红。
//
// 第二半不可省：只跑「好实现通过」等于没钉住判据——把 ConcurrentClaim 改成
// 永远返回 1 也能全绿。
package boardtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/agent-harness/taskboard"
)

// memBoard 一份最小的内存实现（仅供套件自证；生产实现归宿主）。
type memBoard struct {
	mu    sync.Mutex
	tasks map[taskboard.TaskID]*taskboard.Task
}

func newMemBoard(t *testing.T) taskboard.Board {
	t.Helper()
	return &memBoard{tasks: map[taskboard.TaskID]*taskboard.Task{}}
}

// Seed 摆初始任务（套件约定：创建语义是宿主资产，故走这个可选接口）。
func (m *memBoard) Seed(tasks ...taskboard.Task) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, tk := range tasks {
		cp := tk
		cp.Deps = append([]taskboard.TaskID(nil), tk.Deps...)
		m.tasks[tk.ID] = &cp
	}
}

func (m *memBoard) List(ctx context.Context) ([]taskboard.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]taskboard.Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		cp := *t
		cp.Deps = append([]taskboard.TaskID(nil), t.Deps...)
		out = append(out, cp)
	}
	return out, nil
}

func (m *memBoard) Claim(ctx context.Context, id taskboard.TaskID, agent taskboard.AgentID, lease taskboard.Lease) (taskboard.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrNotFound, string(id))
	}
	if t.State.Terminal() {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrTerminal, string(id))
	}
	if t.State == taskboard.StateClaimed && !leaseExpired(t.Lease, time.Now()) {
		return taskboard.Task{}, errors.Wrap(taskboard.ErrTaken, string(id))
	}
	for _, d := range t.Deps {
		dep, ok := m.tasks[d]
		if !ok || !dep.State.Terminal() {
			return taskboard.Task{}, errors.Wrap(taskboard.ErrBlocked, string(d))
		}
	}
	t.State = taskboard.StateClaimed
	t.Owner = agent
	t.Lease = lease
	t.Seq++
	return *t, nil
}

func (m *memBoard) Release(ctx context.Context, id taskboard.TaskID, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return taskboard.ErrNotFound
	}
	if t.State != taskboard.StateClaimed || t.Lease.Token != token {
		return taskboard.ErrLeaseLost
	}
	t.State = taskboard.StateOpen
	t.Owner = ""
	t.Lease = taskboard.Lease{}
	t.Seq++
	return nil
}

func (m *memBoard) Complete(ctx context.Context, id taskboard.TaskID, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return taskboard.ErrNotFound
	}
	if t.State != taskboard.StateClaimed || t.Lease.Token != token {
		return taskboard.ErrLeaseLost
	}
	t.State = taskboard.StateDone
	t.Owner = ""
	t.Lease = taskboard.Lease{}
	t.Seq++
	return nil
}

func leaseExpired(l taskboard.Lease, now time.Time) bool {
	return !l.Until.IsZero() && !now.Before(l.Until)
}

// TestConformanceOnMemoryBoard 好实现必须全绿。
func TestConformanceOnMemoryBoard(t *testing.T) {
	Run(t, newMemBoard)
}

// brokenBoard 故意把认领做成**读-判-写三段分离**：所有读者先读到 open，
// 再一起写入。这正是「没做到原子」的真实形态（先查后写，中间没有事务）。
type brokenBoard struct {
	*memBoard
	barrier *sync.WaitGroup
}

func (b *brokenBoard) Claim(ctx context.Context, id taskboard.TaskID, agent taskboard.AgentID, lease taskboard.Lease) (taskboard.Task, error) {
	// 第一段：读（拿完锁就放开）。
	b.mu.Lock()
	t, ok := b.tasks[id]
	state := taskboard.StateOpen
	if ok {
		state = t.State
	}
	b.mu.Unlock()
	if !ok {
		return taskboard.Task{}, taskboard.ErrNotFound
	}
	// 所有并发读者在这里对齐：保证「判」都发生在「写」之前。
	b.barrier.Done()
	b.barrier.Wait()
	// 第二段：判 + 第三段：写（各自凭自己读到的旧值决定）。
	if state.Terminal() {
		return taskboard.Task{}, taskboard.ErrTerminal
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.tasks[id]
	cur.State = taskboard.StateClaimed
	cur.Owner = agent
	cur.Lease = lease
	cur.Seq++
	return *cur, nil
}

// TestJudgeCatchesNonAtomicBoard 判据的**反向验证**：不原子的实现必须被抓住。
//
// 没有这一条，ConcurrentClaim 可能无论实现对错都返回 1，而套件照样全绿。
func TestJudgeCatchesNonAtomicBoard(t *testing.T) {
	const n = 4
	base := &memBoard{tasks: map[taskboard.TaskID]*taskboard.Task{
		"t1": {ID: "t1", State: taskboard.StateOpen},
	}}
	b := &brokenBoard{memBoard: base, barrier: &sync.WaitGroup{}}
	b.barrier.Add(n)

	agents := []taskboard.AgentID{"a1", "a2", "a3", "a4"}
	winners, err := taskboard.ConcurrentClaim(context.Background(), b, "t1", agents,
		func(a taskboard.AgentID) taskboard.Lease { return taskboard.Lease{Token: "tok-" + string(a)} })
	if err != nil {
		t.Fatalf("判据本身不该报错: %v", err)
	}
	if winners == 1 {
		t.Fatalf("不原子的实现也判成 1 个赢家：判据没有测到争用（winners=%d）", winners)
	}
}
