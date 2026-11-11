// Package boardtest @@Board@@ 的**共享契约测试套件**。
//
// 宿主写完存储实现后跑一遍：违反哪条契约就红在哪一条，免得「各判一套、
// 漂移到没人发现为止」（与 @sidetest@ 同一范式）。
//
// # 硬失败不静默跳过
//
// 显式接入套件 = 声明要满足契约。跳过等于把「没测过」伪装成「通过了」。
//
// # 时钟
//
// 「租约过期可被接管」这条契约需要可控时钟，否则只能靠 sleep（慢且 flaky）。
// 套件提供 FakeClock；实现须通过 ClockedBoard 接口暴露「用哪个时钟」，
// 且**过期判定必须读这个时钟**（不是 time.Now）——
// 否则套件的过期场景测的是真实时间，恒不触发，套件就成了一组空断言。
// （与 @sidetest@ 的 ClockedStore 同一范式。）
package boardtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// FakeClock 套件提供的假时钟（并发安全；测试内手动推进）。
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock 创建起点为 2026-01-01T00:00:00Z 的假时钟。
func NewFakeClock() *FakeClock {
	return &FakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now 返回当前假时刻。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance 把假时钟前进 d。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ClockedBoard 实现须提供的能力：把时钟注入到存储实现里。
//
// **为什么单独一个接口**：过期判定是存储实现内部的事（它自己知道
// 「租约还在不在」）。套件不能替它判，只能要求它**用可注入的时钟**。
// 没实现这个接口 → 套件硬失败（而不是跳过过期场景——跳过等于假绿）。
type ClockedBoard interface {
	taskboard.Board
	// UseClock 指定存储实现读时钟的方式（通常是注入一个 func() time.Time）。
	UseClock(now func() time.Time)
}

// Factory 构造被测存储实现。
//
// factory 每个子测试调用一次，构造一份全新实现，并把套件的假时钟注入它。
type Factory func(t *testing.T, clock *FakeClock) ClockedBoard

// Run 跑整套契约判据。factory 每个子测试调用一次（各条例句独立）。
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("并发认领只有一个赢家", func(t *testing.T) {
		b, _ := newBoard(t, factory)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		agents := []taskboard.AgentID{"a1", "a2", "a3", "a4"}
		n, err := taskboard.ConcurrentClaim(context.Background(), b, "t1", agents, time.Minute)
		if err != nil {
			t.Fatalf("判据无法给出结论: %v", err)
		}
		if n != 1 {
			t.Fatalf("并发认领先成功者 = %d，契约要求恰好 1 个", n)
		}
	})

	t.Run("令牌由存储铸造", func(t *testing.T) {
		b, _ := newBoard(t, factory)
		seed(t, b,
			taskboard.Task{ID: "t1", State: taskboard.StateOpen},
			taskboard.Task{ID: "t2", State: taskboard.StateOpen},
		)
		// 认领请求里**没有令牌**——令牌只能是存储铸出来的。
		t1, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
		if err != nil {
			t.Fatalf("首次认领应当成功: %v", err)
		}
		if t1.Lease.Token == "" {
			t.Fatal("认领必须返回存储铸造的令牌（否则释放与完成无从验证持有者）")
		}
		// 不同次认领的令牌必须不同：否则「上一轮的迟到释放」会认成这一轮的。
		t2, err := b.Claim(context.Background(), "t2", "a2", taskboard.ClaimRequest{TTL: time.Minute})
		if err != nil {
			t.Fatalf("另一任务的认领应当成功: %v", err)
		}
		if t1.Lease.Token == t2.Lease.Token {
			t.Fatalf("不同次认领的令牌必须不同，实得同一个 %q", t1.Lease.Token)
		}
	})

	t.Run("非持有者释放被拒", func(t *testing.T) {
		b, _ := newBoard(t, factory)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		task, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
		if err != nil {
			t.Fatalf("首次认领应当成功: %v", err)
		}
		// **同一个 agent 名**也能被误认：这正是「凭 token 不凭内容」的理由。
		err = b.Release(context.Background(), "t1", "不是这一次的令牌")
		if !errors.Is(err, taskboard.ErrLeaseLost) {
			t.Fatalf("非持有者释放必须报 ErrLeaseLost（实得 %v）——静默成功会让输家以为自己的释放生效了", err)
		}
		_ = task
	})

	t.Run("旧令牌重用被拒", func(t *testing.T) {
		b, _ := newBoard(t, factory)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		first, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
		if err != nil {
			t.Fatalf("首次认领应当成功: %v", err)
		}
		if err := b.Release(context.Background(), "t1", first.Lease.Token); err != nil {
			t.Fatalf("持有者释放应当成功: %v", err)
		}
		// 重新认领：令牌必须换新（否则旧令牌还能用 ⇒ ABA）。
		second, err := b.Claim(context.Background(), "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute})
		if err != nil {
			t.Fatalf("释放后应当可再认领: %v", err)
		}
		if second.Lease.Token == first.Lease.Token {
			t.Fatalf("重新认领必须换新令牌，实得仍是 %q（否则上一轮的迟到释放会放掉这一轮的闸）", first.Lease.Token)
		}
		// 上一轮的令牌：**释放**与**完成**都必须被拒。
		if err := b.Release(context.Background(), "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
			t.Fatalf("旧令牌释放必须报 ErrLeaseLost（实得 %v）", err)
		}
		if err := b.Complete(context.Background(), "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
			t.Fatalf("旧令牌完成必须报 ErrLeaseLost（实得 %v）", err)
		}
		// 当前持有者的令牌仍然有效。
		if err := b.Complete(context.Background(), "t1", second.Lease.Token); err != nil {
			t.Fatalf("当前持有者的完成应当成功: %v", err)
		}
	})

	t.Run("依赖未完成不得认领", func(t *testing.T) {
		b, _ := newBoard(t, factory)
		seed(t, b,
			taskboard.Task{ID: "t1", State: taskboard.StateOpen},
			taskboard.Task{ID: "t2", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"t1"}},
		)
		if _, err := b.Claim(context.Background(), "t2", "a1", taskboard.ClaimRequest{TTL: time.Minute}); !errors.Is(err, taskboard.ErrBlocked) {
			t.Fatalf("依赖未完成时认领必须报 ErrBlocked（实得 %v）——否则两段工作会并行施加", err)
		}
		t1, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
		if err != nil {
			t.Fatalf("无依赖的任务应当可认领: %v", err)
		}
		if err := b.Complete(context.Background(), "t1", t1.Lease.Token); err != nil {
			t.Fatalf("持有者完成应当成功: %v", err)
		}
		if _, err := b.Claim(context.Background(), "t2", "a2", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
			t.Fatalf("依赖完成后应当可认领: %v", err)
		}
	})

	t.Run("完成后不得重开", func(t *testing.T) {
		b, _ := newBoard(t, factory)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		task, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Complete(context.Background(), "t1", task.Lease.Token); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Claim(context.Background(), "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute}); !errors.Is(err, taskboard.ErrTerminal) {
			t.Fatalf("终态认领必须报 ErrTerminal（实得 %v）", err)
		}
	})

	t.Run("失败的任务可重新认领", func(t *testing.T) {
		b, _ := newBoard(t, factory)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateFailed})
		// 失败不是终态：一次失败不能把子任务永久钉死，否则补救路径静默失效。
		if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
			t.Fatalf("失败的任务必须能重做（实得 %v）", err)
		}
	})

	t.Run("租约过期可被接管", func(t *testing.T) {
		b, clock := newBoard(t, factory)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
			t.Fatalf("首次认领应当成功: %v", err)
		}
		// 租约到期：持有着崩了，不接管会留下一把永久占用。
		clock.Advance(2 * time.Minute)
		if _, err := b.Claim(context.Background(), "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
			t.Fatalf("过期租约必须可被接管（实得 %v）", err)
		}
	})
}

// newBoard 构造被测实现并注入假时钟。
func newBoard(t *testing.T, factory Factory) (taskboard.Board, *FakeClock) {
	t.Helper()
	clock := NewFakeClock()
	b := factory(t, clock)
	if b == nil {
		t.Fatalf("factory 返回了 nil 实现")
	}
	b.UseClock(clock.Now)
	return b, clock
}

// seed 把初始任务放进存储。
//
// 用 Claim + Complete 之外的第三个入口：契约里没有「创建」方法（任务拓扑是
// 宿主资产，从哪来是它的事），故套件要求 newBoard 自己在调用前就把 seed 的
// 任务摆进去——这里通过一个可选接口 Seed 表达，没实现就直接失败（不跳过）。
func seed(t *testing.T, b taskboard.Board, tasks ...taskboard.Task) {
	t.Helper()
	s, ok := b.(interface{ Seed(tasks ...taskboard.Task) })
	if !ok {
		t.Fatal("存储实现必须提供 Seed(tasks ...Task) 供套件摆初始任务（本仓不定义创建语义，但套件需要一个入口）")
	}
	s.Seed(tasks...)
}
