// Package boardtest @@Board@@ 的**共享契约测试套件**。
//
// 宿主写完存储实现后跑一遍：违反哪条契约就红在哪一条，免得「各判一套、
// 漂移到没人发现为止」（与 @sidetest@ 同一范式）。
//
// # 硬失败不静默跳过
//
// 显式接入套件 = 声明要满足契约。跳过等于把「没测过」伪装成「通过了」。
package boardtest

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// Run 跑整套契约判据。newBoard 每次返回一个**干净的**存储（每条例句独立）。
func Run(t *testing.T, newBoard func(t *testing.T) taskboard.Board) {
	t.Helper()
	t.Run("并发认领只有一个赢家", func(t *testing.T) {
		b := newBoard(t)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		agents := []taskboard.AgentID{"a1", "a2", "a3", "a4"}
		n, err := taskboard.ConcurrentClaim(context.Background(), b, "t1", agents, func(a taskboard.AgentID) taskboard.Lease {
			return taskboard.Lease{Token: "tok-" + string(a)}
		})
		if err != nil {
			t.Fatalf("判据无法给出结论: %v", err)
		}
		if n != 1 {
			t.Fatalf("并发认领先成功者 = %d，契约要求恰好 1 个", n)
		}
	})

	t.Run("非持有者释放被拒", func(t *testing.T) {
		b := newBoard(t)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.Lease{Token: "tok-1"}); err != nil {
			t.Fatalf("首次认领应当成功: %v", err)
		}
		// **同一个 agent 名**也能被误认：这正是「凭 token 不凭内容」的理由。
		err := b.Release(context.Background(), "t1", "tok-2")
		if !errors.Is(err, taskboard.ErrLeaseLost) {
			t.Fatalf("非持有者释放必须报 ErrLeaseLost（实得 %v）——静默成功会让输家以为自己的释放生效了", err)
		}
	})

	t.Run("依赖未完成不得认领", func(t *testing.T) {
		b := newBoard(t)
		seed(t, b,
			taskboard.Task{ID: "t1", State: taskboard.StateOpen},
			taskboard.Task{ID: "t2", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"t1"}},
		)
		if _, err := b.Claim(context.Background(), "t2", "a1", taskboard.Lease{Token: "tok-1"}); !errors.Is(err, taskboard.ErrBlocked) {
			t.Fatalf("依赖未完成时认领必须报 ErrBlocked（实得 %v）——否则两段工作会并行施加", err)
		}
		if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.Lease{Token: "tok-1"}); err != nil {
			t.Fatalf("无依赖的任务应当可认领: %v", err)
		}
		if err := b.Complete(context.Background(), "t1", "tok-1"); err != nil {
			t.Fatalf("持有者完成应当成功: %v", err)
		}
		if _, err := b.Claim(context.Background(), "t2", "a2", taskboard.Lease{Token: "tok-2"}); err != nil {
			t.Fatalf("依赖完成后应当可认领: %v", err)
		}
	})

	t.Run("完成后不得重开", func(t *testing.T) {
		b := newBoard(t)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.Lease{Token: "tok-1"}); err != nil {
			t.Fatal(err)
		}
		if err := b.Complete(context.Background(), "t1", "tok-1"); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Claim(context.Background(), "t1", "a2", taskboard.Lease{Token: "tok-2"}); !errors.Is(err, taskboard.ErrTerminal) {
			t.Fatalf("终态认领必须报 ErrTerminal（实得 %v）", err)
		}
	})

	t.Run("失败的任务可重新认领", func(t *testing.T) {
		b := newBoard(t)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateFailed})
		// 失败不是终态：一次失败不能把子任务永久钉死，否则补救路径静默失效。
		if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.Lease{Token: "tok-1"}); err != nil {
			t.Fatalf("失败的任务必须能重做（实得 %v）", err)
		}
	})

	t.Run("租约过期可被接管", func(t *testing.T) {
		b := newBoard(t)
		seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
		// 已经过期的租约：持有着崩了，不接管会留下一把永久占用。
		expired := taskboard.Lease{Token: "tok-old", Until: time.Now().Add(-time.Minute)}
		if _, err := b.Claim(context.Background(), "t1", "a1", expired); err != nil {
			t.Fatalf("首次认领应当成功: %v", err)
		}
		if _, err := b.Claim(context.Background(), "t1", "a2", taskboard.Lease{Token: "tok-new"}); err != nil {
			t.Fatalf("过期租约必须可被接管（实得 %v）", err)
		}
	})
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
