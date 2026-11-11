// contention_test.go —— 并发判据的**在包内**测试。
//
// 判据是导出 API（宿主不接套件也能用），故它们自己的行为要在本包内测：
// 恰好一个赢家的正例、agent 数为空 / 依赖未完成 / 基础设施故障这几条
// 「判据无法给出结论」的出口。跨实现的契约语义（好实现全绿 + 变异必红）
// 归 boardtest 套件。
package taskboard

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// fakeBoard 判据测试用的最小内存实现（跨实现契约语义归 boardtest）。
type fakeBoard struct {
	mu    sync.Mutex
	tasks map[TaskID]Task
	seq   uint64
}

func newFakeBoard(tasks ...Task) *fakeBoard {
	b := &fakeBoard{tasks: map[TaskID]Task{}}
	b.Seed(tasks...)
	return b
}

func (b *fakeBoard) Seed(tasks ...Task) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range tasks {
		t.Seq++
		b.tasks[t.ID] = t
	}
}

func (b *fakeBoard) List(_ context.Context) ([]Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Task, 0, len(b.tasks))
	for _, t := range b.tasks {
		out = append(out, t)
	}
	return out, nil
}

func (b *fakeBoard) Claim(_ context.Context, id TaskID, agent AgentID, req ClaimRequest) (Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return Task{}, errors.Wrap(ErrNotFound, string(id))
	}
	if t.State.Terminal() {
		return Task{}, errors.Wrap(ErrTerminal, string(id))
	}
	if t.State == StateClaimed {
		return Task{}, errors.Wrap(ErrTaken, string(id))
	}
	for _, d := range t.Deps {
		if dep, ok := b.tasks[d]; !ok || !dep.State.Terminal() {
			return Task{}, errors.Wrap(ErrBlocked, string(d))
		}
	}
	b.seq++
	lease := Lease{Token: fmt.Sprintf("tok-%d", b.seq), Fence: Fence(b.seq)}
	if req.TTL > 0 {
		lease.Until = time.Now().Add(req.TTL)
	}
	t.State = StateClaimed
	t.Owner = agent
	t.Lease = lease
	t.Seq++
	b.tasks[id] = t
	return t, nil
}

func (b *fakeBoard) Release(_ context.Context, id TaskID, token string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok || t.State != StateClaimed || t.Lease.Token != token {
		return ErrLeaseLost
	}
	t.State = StateOpen
	t.Owner = ""
	t.Lease = Lease{}
	t.Seq++
	b.tasks[id] = t
	return nil
}

func (b *fakeBoard) Complete(_ context.Context, id TaskID, token string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok || t.State != StateClaimed || t.Lease.Token != token {
		return ErrLeaseLost
	}
	t.State = StateDone
	t.Owner = ""
	t.Lease = Lease{}
	t.Seq++
	b.tasks[id] = t
	return nil
}

// failingBoard 故障注入：Claim / Complete 返回非哨兵错误，
// 验判据把基础设施故障与「正常的输家」分开（混在一起判据会在存储坏掉时变绿）。
type failingBoard struct {
	Board
	claimErr    error
	completeErr error
}

func (f *failingBoard) Claim(ctx context.Context, id TaskID, agent AgentID, req ClaimRequest) (Task, error) {
	if f.claimErr != nil {
		return Task{}, f.claimErr
	}
	return f.Board.Claim(ctx, id, agent, req)
}

func (f *failingBoard) Complete(ctx context.Context, id TaskID, token string) error {
	if f.completeErr != nil {
		return f.completeErr
	}
	return f.Board.Complete(ctx, id, token)
}

func TestConcurrentClaimExactlyOneWinner(t *testing.T) {
	b := newFakeBoard(Task{ID: "t1", State: StateOpen})
	agents := make([]AgentID, 0, 8)
	for i := range 8 {
		agents = append(agents, AgentID(fmt.Sprintf("a%d", i+1)))
	}
	n, err := ConcurrentClaim(context.Background(), b, "t1", agents, time.Minute)
	if err != nil {
		t.Fatalf("判据无法给出结论: %v", err)
	}
	if n != 1 {
		t.Fatalf("成功者 = %d，要求恰好 1", n)
	}
}

func TestConcurrentClaimRequiresAgents(t *testing.T) {
	if _, err := ConcurrentClaim(context.Background(), newFakeBoard(Task{ID: "t1"}), "t1", nil, time.Minute); err == nil {
		t.Fatal("空 agent 列表必须报错，不能静默返回 0 个赢家")
	}
}

func TestConcurrentClaimBlockedIsNotLoss(t *testing.T) {
	// 依赖未完成 ⇒ agent 们压根没走到争用那一步，判据必须拒绝给结论。
	b := newFakeBoard(
		Task{ID: "t1", State: StateOpen},
		Task{ID: "t2", State: StateOpen, Deps: []TaskID{"t1"}},
	)
	if _, err := ConcurrentClaim(context.Background(), b, "t2", []AgentID{"a1", "a2"}, time.Minute); err == nil {
		t.Fatal("依赖未完成的争用必须报错——它测到的不是争用")
	}
}

func TestConcurrentClaimFatalErrorIsSurfaced(t *testing.T) {
	base := newFakeBoard(Task{ID: "t1", State: StateOpen})
	b := &failingBoard{Board: base, claimErr: errors.New("存储故障")}
	if _, err := ConcurrentClaim(context.Background(), b, "t1", []AgentID{"a1", "a2"}, time.Minute); err == nil {
		t.Fatal("非争用类错误必须显式报出，不能被算成输家")
	}
}

func TestDuplicateTokenSubmitExactlyOneWinner(t *testing.T) {
	b := newFakeBoard(Task{ID: "t1", State: StateOpen})
	won, err := DuplicateTokenSubmit(context.Background(), b, "t1", 2, time.Minute)
	if err != nil {
		t.Fatalf("判据无法给出结论: %v", err)
	}
	if won != 1 {
		t.Fatalf("同一令牌 2 个并发提交，成功者 = %d，要求恰好 1", won)
	}
}

func TestDuplicateTokenSubmitRequiresTwoWorkers(t *testing.T) {
	b := newFakeBoard(Task{ID: "t1", State: StateOpen})
	if _, err := DuplicateTokenSubmit(context.Background(), b, "t1", 1, time.Minute); err == nil {
		t.Fatal("少于两个提交者必须报错——单个提交者测不出令牌复用")
	}
}

func TestDuplicateTokenSubmitClaimFailureIsSurfaced(t *testing.T) {
	base := newFakeBoard(Task{ID: "t1", State: StateOpen})
	b := &failingBoard{Board: base, claimErr: errors.New("存储故障")}
	if _, err := DuplicateTokenSubmit(context.Background(), b, "t1", 2, time.Minute); err == nil {
		t.Fatal("判据自己的认领失败必须显式报出")
	}
}

func TestDuplicateTokenSubmitFatalErrorIsSurfaced(t *testing.T) {
	base := newFakeBoard(Task{ID: "t1", State: StateOpen})
	b := &failingBoard{Board: base, completeErr: errors.New("存储故障")}
	if _, err := DuplicateTokenSubmit(context.Background(), b, "t1", 2, time.Minute); err == nil {
		t.Fatal("非争用类错误必须显式报出，不能被算成输家")
	}
}
