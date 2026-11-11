// mutation_test.go —— 变异实现 + 判据的牙齿验证。
//
// 套件的红必须有证据：每条新增场景（issue #87 / #97）配一个精确命中它的
// 变异实现，摘掉判据必红；「好实现绿」的那一半由 Run 在 memBoard 上闭合
// （同一批 check 核心）——成对验证缺一边等于没验证。
//
//	splitCheckBoard  → 重复令牌（校验与写入分离，两个并发同令牌提交都成功）
//	tokenBlindBoard  → 过期接管后旧持有者提交 / 跨任务令牌（不校验令牌）
//	fakeRenewBoard   → 旧令牌续期（不校验令牌的假续期）
//	fencelessBoard   → 接管代次递增（代次恒不递增）
package boardtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// splitCheckBoard 把 Complete 的**校验**与**写入**拆成两段（不原子）：
// 两个并发同令牌的提交都先通过校验，再先后写入——两个都成功。
// 这是重复令牌场景（issue #97）要抓的真实缺陷形态。
// barrier 按恰好 2 个并发提交者对齐（判据 DuplicateTokenSubmit 的 workers=2）。
type splitCheckBoard struct {
	*memBoard
	barrier *sync.WaitGroup
}

func newSplitCheckBoard(_ *testing.T, _ *FakeClock) ClockedBoard {
	b := &splitCheckBoard{memBoard: rawMemBoard(), barrier: &sync.WaitGroup{}}
	b.barrier.Add(2)
	return b
}

func (b *splitCheckBoard) Complete(_ context.Context, id taskboard.TaskID, token string) error {
	b.mu.Lock()
	t, ok := b.tasks[id]
	valid := ok && t.State == taskboard.StateClaimed && t.Lease.Token == token
	b.mu.Unlock()
	if !ok {
		return taskboard.ErrNotFound
	}
	if !valid {
		return taskboard.ErrLeaseLost
	}
	// 两个并发提交者在这里对齐：校验都发生在任何写入之前。
	b.barrier.Done()
	b.barrier.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := b.tasks[id]
	cur.State = taskboard.StateDone
	cur.Owner = ""
	cur.Lease = taskboard.Lease{}
	cur.Seq++
	return nil
}

// tokenBlindBoard **不校验令牌**：任何令牌都能释放/完成（只看任务是否被认领）。
// 这是「只认 token」名存实亡的最直接形态——过期持有者的迟到提交、别的任务上
// 铸造的令牌，全都放行。
type tokenBlindBoard struct {
	*memBoard
}

func newTokenBlindBoard(_ *testing.T, _ *FakeClock) ClockedBoard {
	return &tokenBlindBoard{memBoard: rawMemBoard()}
}

func (b *tokenBlindBoard) Release(_ context.Context, id taskboard.TaskID, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return taskboard.ErrNotFound
	}
	if t.State != taskboard.StateClaimed {
		return taskboard.ErrLeaseLost
	}
	t.State = taskboard.StateOpen
	t.Owner = ""
	t.Lease = taskboard.Lease{}
	t.Seq++
	return nil
}

func (b *tokenBlindBoard) Complete(_ context.Context, id taskboard.TaskID, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return taskboard.ErrNotFound
	}
	if t.State != taskboard.StateClaimed {
		return taskboard.ErrLeaseLost
	}
	t.State = taskboard.StateDone
	t.Owner = ""
	t.Lease = taskboard.Lease{}
	t.Seq++
	return nil
}

// fakeRenewBoard 续期**不校验令牌**（假续期）：谁的令牌来都给续，
// 租约已易主时延长的其实是别人的闸。
type fakeRenewBoard struct {
	*memBoard
}

func newFakeRenewBoard(_ *testing.T, _ *FakeClock) ClockedBoard {
	return &fakeRenewBoard{memBoard: rawMemBoard()}
}

func (b *fakeRenewBoard) Renew(_ context.Context, id taskboard.TaskID, _ string, req taskboard.ClaimRequest) (taskboard.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tasks[id]
	if !ok {
		return taskboard.Task{}, taskboard.ErrNotFound
	}
	if t.State != taskboard.StateClaimed {
		return taskboard.Task{}, taskboard.ErrLeaseLost
	}
	if leaseExpired(t.Lease, b.clockLocked()) {
		return taskboard.Task{}, taskboard.ErrLeaseLost
	}
	if req.TTL > 0 {
		t.Lease.Until = b.clockLocked().Add(req.TTL)
	} else {
		t.Lease.Until = time.Time{}
	}
	t.Seq++
	return *t, nil
}

// fencelessBoard 接管代次**恒不递增**：认领照常、令牌照常换新，
// 但返回给调用方的 Lease.Fence 永远是 0——下游凭代次拒收迟到写入的栅栏失效。
type fencelessBoard struct {
	*memBoard
}

func newFencelessBoard(_ *testing.T, _ *FakeClock) ClockedBoard {
	return &fencelessBoard{memBoard: rawMemBoard()}
}

func (b *fencelessBoard) Claim(ctx context.Context, id taskboard.TaskID, agent taskboard.AgentID, req taskboard.ClaimRequest) (taskboard.Task, error) {
	tk, err := b.memBoard.Claim(ctx, id, agent, req)
	if err != nil {
		return tk, err
	}
	tk.Lease.Fence = 0
	return tk, nil
}

// ── 牙齿验证：每条新增判据，摘掉它必红 ─────────────────────────────────

// TestSuiteCatchesSplitCompleteCheck 重复令牌判据的牙齿：
// 校验与写入分离的实现，两个并发同令牌提交都成功 ⇒ 判据必须看见 won != 1。
func TestSuiteCatchesSplitCompleteCheck(t *testing.T) {
	b, _ := newBoard(t, newSplitCheckBoard)
	if err := seedBoard(b, taskboard.Task{ID: "t1", State: taskboard.StateOpen}); err != nil {
		t.Fatal(err)
	}
	won, err := taskboard.DuplicateTokenSubmit(context.Background(), b, "t1", 2, time.Minute)
	if err != nil {
		t.Fatalf("判据本身不该报错: %v", err)
	}
	if won == 1 {
		t.Fatal("校验与写入分离的实现也判成恰好一人成功：判据没有牙齿")
	}
}

// TestSuiteCatchesBlindTokenAcceptance 「旧持有者提交被拒」「跨任务令牌」
// 两条判据的牙齿：不校验令牌的实现必须红。
func TestSuiteCatchesBlindTokenAcceptance(t *testing.T) {
	b, clock := newBoard(t, newTokenBlindBoard)
	if err := checkTakeoverRejectsStaleSubmit(context.Background(), b, clock); err == nil {
		t.Fatal("不校验令牌的实现也通过了「过期接管后旧持有者提交被拒」：判据没有牙齿")
	}
	b2, _ := newBoard(t, newTokenBlindBoard)
	if err := checkCrossTaskTokenRejected(context.Background(), b2); err == nil {
		t.Fatal("不校验令牌的实现也通过了「跨任务令牌复用被拒」：判据没有牙齿")
	}
}

// TestSuiteCatchesFakeRenew 续期判据的牙齿：不校验令牌的假续期必须红。
func TestSuiteCatchesFakeRenew(t *testing.T) {
	b, clock := newBoard(t, newFakeRenewBoard)
	if err := checkRenewRejectsStaleToken(context.Background(), b, clock); err == nil {
		t.Fatal("不校验令牌的假续期也通过了「旧令牌续期被拒」：判据没有牙齿")
	}
}

// TestSuiteCatchesStalledFence 代次判据的牙齿：代次不递增的实现必须红。
//
// 「好实现绿」的那一半不用单独写：Run 的每条场景（同一批 check 核心）在
// memBoard 上全绿，变异红的另一半在这里——成对验证已闭合。
func TestSuiteCatchesStalledFence(t *testing.T) {
	b, clock := newBoard(t, newFencelessBoard)
	if err := checkTakeoverFenceIncrements(context.Background(), b, clock); err == nil {
		t.Fatal("代次恒不递增的实现也通过了「接管代次严格递增」：判据没有牙齿")
	}
}
