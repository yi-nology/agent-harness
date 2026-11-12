// loop.go —— 六段生命周期的驱动：注册 → 领取 → 执行 → 心跳/续期 → 排水 → 注销。
//
// # 驱动不吞失败
//
// 每一段的失败都有**明确的处置**，不静默吞：
//
//   - 注册失败 → 直接返回（没身份就别开工）；
//   - 心跳失败（@@ErrNotRegistered@@）→ 身份没了：停止领取，收刀退出；
//   - 任务租约续期失败（@@taskboard.ErrLeaseLost@@）→ 资格没了：**取消在途
//     执行的 ctx**，本次任务按失败收场——资格检查失败后的继续执行就是
//     与新持有者重复施加（危险窗口在施加之前封口）；
//   - handler 失败 → 任务 Release 回任务板（failed/open 都可被重做，
//     一次失败不把子任务钉死——taskboard 的状态语义）。
//
// # 优雅退出 = 停止领取 + 在途收敛 + 注销
//
// 外层 ctx 取消后：不再领取新任务，**当前在途任务收场**（收场动作走
// WithoutCancel——排水期间的 Complete/Release 不能因 ctx 已取消而失败），
// 然后注销注册。在途何时收敛由 handler 对 ctx 的响应决定：handler 无视
// 取消就等于无限期排水——ctx 已把「该收手了」送到它手里，剩下的不是
// 本驱动能替它决定的。
package worker

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// DefaultPollInterval 领取轮询的缺省间隔。
const DefaultPollInterval = 200 * time.Millisecond

// Handler 一个任务的执行体（宿主实现；本包不认识任务内容）。
//
// Execute 收到的 ctx 在「租约易主」「外层停机」时被取消：**施加副作用之前**
// 应检查它。返回 nil ⇒ 驱动 Complete 任务；返回 err ⇒ 驱动 Release 任务
// （任务可被重做）。
type Handler interface {
	Execute(ctx context.Context, task taskboard.Task) error
}

// HandlerFunc 函数适配器。
type HandlerFunc func(ctx context.Context, task taskboard.Task) error

// Execute 实现 Handler。
func (f HandlerFunc) Execute(ctx context.Context, task taskboard.Task) error { return f(ctx, task) }

// Config 驱动配置（零值不可用；NewLoop 做装配期校验）。
type Config struct {
	// Agent 任务认领者标识（taskboard.AgentID；注册 Spec.ID 也取它）。
	Agent taskboard.AgentID
	// TTL 认领租约与注册共用的有效期（>0）。共用一个刻度只是本驱动的
	// 缺省口径——要分开调的宿主自己组装即可（机制面不焊死策略）。
	TTL time.Duration
	// PollInterval 领取轮询间隔（0 = DefaultPollInterval）。
	PollInterval time.Duration
	// HeartbeatInterval 心跳/续期周期（0 = TTL/3）。必须短于 TTL，
	// 否则注册周期性过期（装配期拦下，不静默运行）。
	HeartbeatInterval time.Duration
	// Pick 可选的挑选钩子：从候选任务里挑一个认领。缺省按 ID 序取第一个
	// 可认领的（确定性——不依赖任务板返回顺序）。优先级/公平性等挑选策略
	// 由宿主在此接线（判据面见 scheduling 包；本驱动不内嵌第二份）。
	Pick func(candidates []taskboard.Task) (taskboard.TaskID, bool)
}

// NewLoop 校验配置并构造驱动。
func NewLoop(board taskboard.RenewingBoard, registry Registry, handler Handler, cfg Config) (*Loop, error) {
	switch {
	case board == nil:
		return nil, errors.Wrap(ErrInvalidConfig, "worker: 缺任务板（领取与续期无处落地）")
	case registry == nil:
		return nil, errors.Wrap(ErrInvalidConfig, "worker: 缺注册表（生命周期无处落地）")
	case handler == nil:
		return nil, errors.Wrap(ErrInvalidConfig, "worker: 缺执行体（领取了没人做）")
	case cfg.Agent == "":
		return nil, errors.Wrap(ErrInvalidConfig, "worker: 配置缺 Agent 标识")
	case cfg.TTL <= 0:
		return nil, errors.Wrap(ErrInvalidConfig, "worker: TTL 必须为正（零租约 = 永久占用面）")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = cfg.TTL / 3
	}
	if cfg.HeartbeatInterval >= cfg.TTL {
		return nil, errors.Wrap(ErrInvalidConfig,
			"worker: 心跳间隔必须短于 TTL——否则注册必然周期性过期",
			"interval", cfg.HeartbeatInterval, "ttl", cfg.TTL)
	}
	return &Loop{board: board, registry: registry, handler: handler, cfg: cfg}, nil
}

// Loop 六段生命周期的驱动。Run 之外的可见状态一律经任务板与注册表读取
// ——派生状态只有一个真相（板里是什么状态，外面看到的就是什么状态）。
type Loop struct {
	board    taskboard.RenewingBoard
	registry Registry
	handler  Handler
	cfg      Config
}

// claimed 一次在途认领（主循环与心跳协程的共享面，由 mu 护住）。
type claimed struct {
	id     taskboard.TaskID
	token  string
	cancel context.CancelFunc // 租约易主时取消在途执行
}

// Run 跑完整生命周期（阻塞到外层 ctx 取消或身份丢失）。
//
// 返回 nil = 优雅退出（在途已收场、注册已注销）；
// 返回 ErrNotRegistered = 身份在途丢失（已尽力注销）；
// 其余错误 = 装配或基础设施故障。
func (l *Loop) Run(ctx context.Context) error {
	reg, err := l.registry.Register(ctx, Spec{ID: string(l.cfg.Agent), TTL: l.cfg.TTL})
	if err != nil {
		return errors.Wrap(err, "worker: 注册失败（没有身份就别开工）", "agent", string(l.cfg.Agent))
	}

	// claimCtx 是领取环的闸：身份丢失时心跳协程拉它停领取（外层 ctx 不动
	// ——在途任务的收场还要用它派生 WithoutCancel 的终态写入）。
	claimCtx, claimCancel := context.WithCancel(ctx)
	defer claimCancel()

	var (
		mu      sync.Mutex
		current *claimed
		lost    bool // 心跳协程写、Run 在心跳退出后读（close(done) 建立先行发生）
	)
	stopping := make(chan struct{}) // 主循环收刀信号（在途已收敛）
	done := make(chan struct{})     // 心跳协程退出信号

	// 心跳/续期协程：注册心跳 + 在途任务的租约续期，共用一个节拍。
	// 走 async.GoSafe 而非裸 go func（本仓门禁：panic 无法被外层 recover）。
	async.GoSafe(func() {
		defer close(done)
		ticker := time.NewTicker(l.cfg.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stopping:
				return
			case <-ticker.C:
			}
			// 注册心跳：身份的资格检查。非身份类失败（含 ctx 取消的抖动）
			// 不判丢失——把存储抖动当成身份丢失，驱动会在任务板正常时自杀。
			if _, hbErr := l.registry.Heartbeat(ctx, reg.ID, reg.Token, l.cfg.TTL); hbErr != nil {
				if errors.Is(hbErr, ErrNotRegistered) {
					lost = true
					claimCancel()
					return
				}
				if ctx.Err() != nil {
					return
				}
				continue // 存储抖动：下一拍重试
			}
			// 任务租约续期（taskboard.Renew = 资格检查 + 推后到期时刻）。
			mu.Lock()
			cur := current
			mu.Unlock()
			if cur == nil {
				continue
			}
			_, rErr := l.board.Renew(ctx, cur.id, cur.token, taskboard.ClaimRequest{TTL: l.cfg.TTL})
			if rErr != nil && errors.Is(rErr, taskboard.ErrLeaseLost) {
				cur.cancel() // 资格没了：取消在途执行（施加之前封口）
			}
			// 其它续期错误：下一拍重试（租约尚未到期的前提下仍有出路）。
		}
	})

	runErr := l.runClaims(claimCtx, &mu, &current)
	close(stopping) // 在途已收敛：心跳可以收刀（排水期不再保活）
	<-done

	// 收尾：注销注册（WithoutCancel——ctx 已取消也要把身份还回去，
	// 否则注册表里留下一个「永不心跳」的幽灵）。
	if dErr := l.registry.Deregister(context.WithoutCancel(ctx), reg.ID, reg.Token); dErr != nil {
		runErr = errors.Wrap(dErr, "worker: 注销失败（注册表里可能残留幽灵记录）")
	}
	if lost {
		return errors.Wrap(ErrNotRegistered, "worker: 注册在途中失效，驱动已收刀退出")
	}
	return runErr
}

// runClaims 领取-执行主循环（claimCtx 取消即停止领取）。
func (l *Loop) runClaims(ctx context.Context, mu *sync.Mutex, current **claimed) error {
	for ctx.Err() == nil {
		task, ok, err := l.claimNext(ctx)
		if err != nil {
			return err
		}
		if ok {
			if err := l.executeOne(ctx, mu, current, task); err != nil {
				return err
			}
		}
		// 轮询节拍：ctx 取消立即醒来（优雅退出不等满一个间隔）。
		select {
		case <-ctx.Done():
		case <-time.After(l.cfg.PollInterval):
		}
	}
	return nil
}

// claimNext 列任务板 → 选候选 → 认领（资格判定归任务板：ErrTaken /
// ErrBlocked / ErrTerminal 是「这次没轮上」，下一拍再来）。
func (l *Loop) claimNext(ctx context.Context) (taskboard.Task, bool, error) {
	tasks, err := l.board.List(ctx)
	if err != nil {
		return taskboard.Task{}, false, errors.Wrap(err, "worker: 任务板不可用")
	}
	cands := candidates(tasks)
	if len(cands) == 0 {
		return taskboard.Task{}, false, nil
	}
	var pick taskboard.TaskID
	if l.cfg.Pick != nil {
		p, ok := l.cfg.Pick(cands)
		if !ok {
			return taskboard.Task{}, false, nil
		}
		pick = p
	} else {
		pick = cands[0].ID // 缺省按 ID 序（候选已排序）
	}
	t, err := l.board.Claim(ctx, pick, l.cfg.Agent, taskboard.ClaimRequest{TTL: l.cfg.TTL})
	if err != nil {
		if errors.Is(err, taskboard.ErrTaken) || errors.Is(err, taskboard.ErrBlocked) ||
			errors.Is(err, taskboard.ErrTerminal) || errors.Is(err, taskboard.ErrNotFound) {
			return taskboard.Task{}, false, nil // 竞争性拒绝：不是故障
		}
		return taskboard.Task{}, false, errors.Wrap(err, "worker: 认领失败", "task", string(pick))
	}
	return t, true, nil
}

// executeOne 执行一个在途任务：登记给心跳协程 → 跑 handler → 收场。
//
// 收场动作走 WithoutCancel：外层停机时在途任务的 Complete/Release 仍要落地
// （任务板的状态不能因为停机而悬在 claimed）。收场被 ErrLeaseLost 拒
// = 闸已易主——任务归新持有者，如实接受（不重试、不吞）。
func (l *Loop) executeOne(ctx context.Context, mu *sync.Mutex, current **claimed, task taskboard.Task) error {
	execCtx, cancel := context.WithCancel(ctx)
	c := &claimed{id: task.ID, token: task.Lease.Token, cancel: cancel}
	mu.Lock()
	*current = c
	mu.Unlock()

	execErr := l.handler.Execute(execCtx, task)

	mu.Lock()
	*current = nil
	mu.Unlock()
	cancel()

	finalCtx := context.WithoutCancel(ctx)
	if execErr == nil {
		if err := l.board.Complete(finalCtx, task.ID, c.token); err != nil &&
			!errors.Is(err, taskboard.ErrLeaseLost) {
			return errors.Wrap(err, "worker: 完成任务失败", "task", string(task.ID))
		}
	} else {
		if err := l.board.Release(finalCtx, task.ID, c.token); err != nil &&
			!errors.Is(err, taskboard.ErrLeaseLost) {
			return errors.Wrap(err, "worker: 释放任务失败", "task", string(task.ID))
		}
	}
	return nil
}

// candidates 从快照里挑可认领的任务（open 与 failed 都可重做；
// 依赖完成度按同一份快照本地预判——最终原子性归 Board.Claim）。
// 结果按 ID 序：缺省挑选不依赖任务板的返回顺序。
func candidates(tasks []taskboard.Task) []taskboard.Task {
	done := make(map[taskboard.TaskID]bool, len(tasks))
	for _, t := range tasks {
		done[t.ID] = t.State.Terminal()
	}
	var out []taskboard.Task
	for _, t := range tasks {
		if t.State != taskboard.StateOpen && t.State != taskboard.StateFailed {
			continue
		}
		ready := true
		for _, d := range t.Deps {
			if !done[d] {
				ready = false
				break
			}
		}
		if ready {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(x, y taskboard.Task) int {
		return strings.Compare(string(x.ID), string(y.ID))
	})
	return out
}
