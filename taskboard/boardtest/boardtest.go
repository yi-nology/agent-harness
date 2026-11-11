// Package boardtest @@Board@@ 的**共享契约测试套件**。
//
// 宿主写完存储实现后跑一遍：违反哪条契约就红在哪一条，免得「各判一套、
// 漂移到没人发现为止」（与 @sidetest@ 同一范式）。
//
// # 硬失败不静默跳过
//
// 显式接入套件 = 声明要满足契约。跳过等于把「没测过」伪装成「通过了」。
// 续期（@@taskboard.RenewingBoard@@）与种子（Seed）都是可选接口，但
// **接入套件就必须实现**——套件对缺失硬失败，不降级、不跳过。
//
// # 时钟
//
// 「租约过期可被接管」「续期把到期时刻推后」这些契约需要可控时钟，否则只能
// 靠 sleep（慢且 flaky）。套件提供 FakeClock；实现须通过 ClockedBoard 接口
// 暴露「用哪个时钟」，且**过期判定必须读这个时钟**（不是 time.Now）——
// 否则套件的过期场景测的是真实时间，恒不触发，套件就成了一组空断言。
// （与 @sidetest@ 的 ClockedStore 同一范式。）
//
// # 并发规模（issue #97）
//
// 「读-判-写不可分割」这条要求在 4 并发下几乎撞不出来。套件把并发认领的
// 规模做成可配置（Config.ClaimWorkers），缺省跑 **10 与 100** 两档——
// 存储实现过 4 并发不算过套件。宿主按自己的生产量级调那一档。
package boardtest

import (
	"context"
	"fmt"
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

// DefaultClaimWorkers 大规模并发认领档位的缺省 agent 数。
const DefaultClaimWorkers = 100

// Config 套件参数（零值 = 缺省档位）。
type Config struct {
	// ClaimWorkers 大规模并发认领判据的 agent 数（零值 = DefaultClaimWorkers）。
	//
	// 套件固定另跑一档 10；本值是「生产量级」那一档，宿主按自己的并发量调
	// ——存储只要过小规模并发就能过套件的时代结束了（issue #97）。
	// 负值在套件启动时即报错：写错的配置要让人看到，不静默回落缺省。
	ClaimWorkers int
}

// Run 跑整套契约判据（缺省规模档位：10 与 100 个 agent）。
func Run(t *testing.T, factory Factory) {
	t.Helper()
	RunConfig(t, factory, Config{})
}

// RunConfig 跑整套契约判据，并发规模可配置（issue #97）。
//
// 每个子测试独立调用 factory（各条例句互不污染）。
func RunConfig(t *testing.T, factory Factory, cfg Config) {
	t.Helper()
	big := cfg.ClaimWorkers
	if big == 0 {
		big = DefaultClaimWorkers
	}
	if big < 0 {
		t.Fatalf("Config.ClaimWorkers 不能为负（实得 %d）", big)
	}

	t.Run("并发认领只有一个赢家", func(t *testing.T) { runConcurrentClaimSingleWinner(t, factory) })
	t.Run("并发认领 10 个 agent 恰好一个赢家", func(t *testing.T) { runConcurrentClaimAtScale(t, factory, 10) })
	if big != 10 {
		t.Run(fmt.Sprintf("并发认领 %d 个 agent 恰好一个赢家", big),
			func(t *testing.T) { runConcurrentClaimAtScale(t, factory, big) })
	}
	t.Run("令牌由存储铸造", func(t *testing.T) { runTokenMintedByStorage(t, factory) })
	t.Run("非持有者释放被拒", func(t *testing.T) { runNonHolderReleaseRejected(t, factory) })
	t.Run("旧令牌重用被拒", func(t *testing.T) { runStaleTokenReuseRejected(t, factory) })
	t.Run("跨任务令牌复用被拒", func(t *testing.T) { runCrossTaskTokenRejected(t, factory) })
	t.Run("重复令牌并发提交恰好一人成功", func(t *testing.T) { runDuplicateTokenOneWinner(t, factory) })
	t.Run("依赖未完成不得认领", func(t *testing.T) { runBlockedClaimRejected(t, factory) })
	t.Run("完成后不得重开", func(t *testing.T) { runTerminalNoReopen(t, factory) })
	t.Run("失败的任务可重新认领", func(t *testing.T) { runFailedTaskReclaimable(t, factory) })
	t.Run("租约过期可被接管", func(t *testing.T) { runExpiredTakeover(t, factory) })
	t.Run("过期接管后旧持有者提交被拒", func(t *testing.T) { runTakeoverRejectsStaleSubmit(t, factory) })
	t.Run("接管代次严格递增", func(t *testing.T) { runTakeoverFenceIncrements(t, factory) })
	t.Run("长任务续期后可继续持有", func(t *testing.T) { runRenewKeepsLongTask(t, factory) })
	t.Run("旧令牌续期被拒", func(t *testing.T) { runRenewRejectsStaleToken(t, factory) })
}

// ── 并发认领（判据 taskboard.ConcurrentClaim 的套件接线）────────────────

// runConcurrentClaimSingleWinner 小规模冒烟档（4 个 agent）。
//
// 大规模档在 RunConfig 里按 Config 跑——4 并发撞不出「读-判-写」窗口，
// 这一条只是最便宜的那层保险，不是判据的全部量纲。
func runConcurrentClaimSingleWinner(t *testing.T, factory Factory) {
	t.Helper()
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
}

// runConcurrentClaimAtScale 规模档：N 个 agent 同时认领同一任务（issue #97）。
//
// 判据强度不变——仍是「恰好一个赢家」：0 个说明谁都认领不了，>1 个说明
// 存储没做到原子，非 ErrTaken 的失败说明这次根本没测到争用。
// N 由套件配置（缺省 10 与 100 两档），宿主可按自己的生产量级抬。
func runConcurrentClaimAtScale(t *testing.T, factory Factory, n int) {
	t.Helper()
	if n < 1 {
		t.Fatalf("规模档的 agent 数必须为正（实得 %d）", n)
	}
	b, _ := newBoard(t, factory)
	seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
	agents := make([]taskboard.AgentID, 0, n)
	for i := range n {
		agents = append(agents, taskboard.AgentID(fmt.Sprintf("w%03d", i)))
	}
	got, err := taskboard.ConcurrentClaim(context.Background(), b, "t1", agents, time.Minute)
	if err != nil {
		t.Fatalf("判据无法给出结论: %v", err)
	}
	if got != 1 {
		t.Fatalf("%d 个 agent 并发认领，成功者 = %d，契约要求恰好 1 个", n, got)
	}
}

// ── 状态机场景（依赖 / 终态 / 重做）──────────────────────────────────────

// runBlockedClaimRejected 依赖未完成不得认领。
func runBlockedClaimRejected(t *testing.T, factory Factory) {
	t.Helper()
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
}

// runTerminalNoReopen 完成后不得重开。
func runTerminalNoReopen(t *testing.T, factory Factory) {
	t.Helper()
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
}

// runFailedTaskReclaimable 失败的任务可重新认领。
func runFailedTaskReclaimable(t *testing.T, factory Factory) {
	t.Helper()
	b, _ := newBoard(t, factory)
	seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateFailed})
	// 失败不是终态：一次失败不能把子任务永久钉死，否则补救路径静默失效。
	if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		t.Fatalf("失败的任务必须能重做（实得 %v）", err)
	}
}

// ── 套件基础设施 ────────────────────────────────────────────────────────

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

// seed 把初始任务放进存储（t 形态的薄包装，Fatal 在这里、判据核心不碰 testing）。
func seed(t *testing.T, b taskboard.Board, tasks ...taskboard.Task) {
	t.Helper()
	if err := seedBoard(b, tasks...); err != nil {
		t.Fatal(err)
	}
}

// seedBoard 摆初始任务。
//
// 用 Claim + Complete 之外的第三个入口：契约里没有「创建」方法（任务拓扑是
// 宿主资产，从哪来是它的事），故套件要求实现自己在调用前把 seed 的任务摆进去
// ——这里通过一个可选接口 Seed 表达，没实现就报错（不跳过）。
func seedBoard(b taskboard.Board, tasks ...taskboard.Task) error {
	s, ok := b.(interface{ Seed(tasks ...taskboard.Task) })
	if !ok {
		return errors.New("boardtest: 存储实现必须提供 Seed(tasks ...Task) 供套件摆初始任务" +
			"（本仓不定义创建语义，但套件需要一个入口）")
	}
	s.Seed(tasks...)
	return nil
}

// asRenewing 取续期能力；没有就报错（不跳过——跳过等于假绿）。
func asRenewing(b taskboard.Board) (taskboard.RenewingBoard, error) {
	r, ok := b.(taskboard.RenewingBoard)
	if !ok {
		return nil, errors.New("boardtest: 存储实现必须实现 taskboard.RenewingBoard（租约续期）——" +
			"长任务没有续期手段就必然被接管，旧持有者的迟到提交也没有资格判据可拒")
	}
	return r, nil
}

// leaseExpired 报告租约在 now 是否已过期（零值 Until = 不限期）。
func leaseExpired(l taskboard.Lease, now time.Time) bool {
	return !l.Until.IsZero() && !now.Before(l.Until)
}
