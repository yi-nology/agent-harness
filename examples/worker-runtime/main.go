// Package main —— worker 生命周期的**参考运行时示例**（issue #106）。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// 它是**示例**不是能力包：机制面在 worker / coord / taskboard 里，本目录
// 只演示六段生命周期怎么接线——注册 → 心跳 → 领取 → 执行 → 续期/恢复 →
// 优雅退出。存储用 coord 的适配器 + 内存后端（跨进程原子性由真实后端保证，
// 见 coord 包文档的形态约定）。
//
// 演示脚本（runDemo，端到端用例直接调它并断言输出）：
//
//  1. 建任务板（coord 适配器）与注册表（内存参考实现）；
//  2. 播种一条 3 步依赖链；
//  3. worker-a 认领 t1 后「崩溃」（认领记录留在板上，租约 1 分钟）——
//     真实崩溃在存储侧留下的就是这一条过期认领；
//  4. 时钟推过租约；worker-b 启动：先 Recover（接管 t1，活租约不抢），
//     恢复的任务就地执行并 Complete，然后跑正常领取环完成 t2/t3；
//  5. 优雅退出（ctx 取消 → 停止领取 → 在途收敛 → 注销），打印终态。
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/oekit/concurrency/async"

	"github.com/yi-nology/goagent-kernel/coord"
	"github.com/yi-nology/goagent-kernel/taskboard"
	"github.com/yi-nology/goagent-kernel/worker"
)

func main() {
	if err := runDemo(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "worker 生命周期示例执行失败:", err)
		os.Exit(1)
	}
}

// runDemo 跑完生命周期演示，每步打印一行记录。
func runDemo(ctx context.Context, out io.Writer) error {
	// 1) 任务板 + 注册表（可注入时钟：过期场景要能确定性触发）。
	boardClock := newWallClock()
	board := coord.NewBoardAdapter(coord.NewMemoryBackend())
	board.UseClock(boardClock.Now)
	registry := newMemRegistry(boardClock.Now)

	// 2) 播种依赖链 t1 ← t2 ← t3。
	board.Seed(
		taskboard.Task{ID: "t1", State: taskboard.StateOpen},
		taskboard.Task{ID: "t2", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"t1"}},
		taskboard.Task{ID: "t3", State: taskboard.StateOpen, Deps: []taskboard.TaskID{"t2"}},
	)
	if err := writef(out, "[1] 建任务板与注册表：3 步依赖链 t1<-t2<-t3\n"); err != nil {
		return err
	}

	// 3) worker-a 认领 t1 后崩溃：认领记录（活租约）留在板上。
	if _, err := board.Claim(ctx, "t1", "worker-a", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		return err
	}
	if err := writef(out, "[2] worker-a 认领 t1 后崩溃（过期认领留在板上）\n"); err != nil {
		return err
	}

	// 4) 时钟推过租约：t1 成为可接管任务。
	boardClock.Advance(2 * time.Minute)

	// 5) worker-b：先恢复（接管过期任务），恢复的任务就地执行。
	h := &printHandler{out: out}
	took, err := worker.Recover(ctx, board, "worker-b", time.Minute)
	if err != nil {
		return err
	}
	for _, id := range took {
		if err := executeRecovered(ctx, board, h, id); err != nil {
			return err
		}
	}
	if err := writef(out, "[3] worker-b 恢复接管: %v（已执行并完成）\n", took); err != nil {
		return err
	}

	// 6) 正常领取环完成剩余任务，ctx 取消后优雅退出。
	loop, err := worker.NewLoop(board, registry, h, worker.Config{
		Agent: "worker-b", TTL: time.Minute, PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	// 走 async.GoSafe 而非裸 go func（本仓门禁：panic 无法被外层 recover）。
	async.GoSafe(func() { done <- loop.Run(runCtx) })

	// 等依赖链收尾（t2/t3 由领取环完成）。
	if err := waitBoardDone(board, 3, 10*time.Second); err != nil {
		cancel()
		return err
	}
	cancel()
	if err := <-done; err != nil {
		return err
	}

	// 7) 终态：任务全 done、注册表已注销。
	tasks, _ := board.List(ctx)
	for _, t := range tasks {
		if err := writef(out, "[4] 终态 %s = %s\n", t.ID, t.State); err != nil {
			return err
		}
	}
	st, _ := registry.List(ctx)
	return writef(out, "[5] 注册表余留 %d 条（优雅退出已注销）\n", len(st))
}

// printHandler 演示执行体：打印一行「做了什么」。
type printHandler struct{ out io.Writer }

func (h *printHandler) Execute(_ context.Context, task taskboard.Task) error {
	return writef(h.out, "    执行 %s（%s）\n", task.ID, task.Title)
}

// executeRecovered 恢复任务的接线范式：接管 → 执行 → Complete。
// （Recover 只负责接管；恢复出的活怎么干是宿主的编排。）
func executeRecovered(ctx context.Context, board taskboard.Board, h worker.Handler, id taskboard.TaskID) error {
	tasks, err := board.List(ctx)
	if err != nil {
		return err
	}
	var recovered taskboard.Task
	for _, t := range tasks {
		if t.ID == id {
			recovered = t
		}
	}
	if err := h.Execute(ctx, recovered); err != nil {
		return err
	}
	return board.Complete(ctx, id, recovered.Lease.Token)
}

// waitBoardDone 等到 done 任务数达到 want（演示辅助；超时报错不挂死）。
func waitBoardDone(board taskboard.Board, want int, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		tasks, err := board.List(context.Background())
		if err != nil {
			return err
		}
		n := 0
		for _, t := range tasks {
			if t.State == taskboard.StateDone {
				n++
			}
		}
		if n >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %d 个任务完成超时（当前 %d）", want, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writef 写一行输出（errcheck：写失败如实上抛）。
func writef(out io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(out, format, args...)
	return err
}
