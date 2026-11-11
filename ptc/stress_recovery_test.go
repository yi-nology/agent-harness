//go:build unix

// stress_recovery_test.go —— 执行预算的**压力测试矩阵**：进程内部份
// （v0.33.0，issue #93）。
//
// # 这组用例答的问题
//
// 预算到点之后，**资源收回来了没有**。功能测试只看「结果对不对」，看不见
// 泄漏——而泄漏会累积成宿主的慢性故障（goroutine 数只增不减）。矩阵的
// 每一格断言的都是回收：goroutine 数回落到基线 + 容差之内。
//
// 子进程侧的格子（异常退出 / 外部 SIGKILL / 真 OOM / FD 回收）在
// stress_subprocess_recovery_test.go。
//
// # 阈值可配置（防慢机器 flaky）
//
// goroutine 基线取**多次采样的最小值**（最不受干扰的那次；负载抬高的是
// 均值），回落判据是「基线 + 容差」，两者都可经环境变量放宽：
//
//	GAK_STRESS_TOLERANCE  goroutine 容差（默认 3）
//	GAK_STRESS_SETTLE_MS  回落等待窗口（默认 5000；到点未回落即红，附栈快照）
//	GAK_STRESS_FD_TOL     文件描述符容差（默认 8）
package ptc_test

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/ptc"
)

func stressTolerance() int {
	if v, err := strconv.Atoi(os.Getenv("GAK_STRESS_TOLERANCE")); err == nil && v >= 0 {
		return v
	}
	return 3
}

func stressSettleWindow() time.Duration {
	if v, err := strconv.Atoi(os.Getenv("GAK_STRESS_SETTLE_MS")); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return 5 * time.Second
}

func stressFDTolerance() int {
	if v, err := strconv.Atoi(os.Getenv("GAK_STRESS_FD_TOL")); err == nil && v >= 0 {
		return v
	}
	return 8
}

// baselineGoroutines 取**最小值**基线：连采 5 次、每次间隔 50ms，取最小。
// 负载抬高的是均值与中位数；最小值是「最不受干扰」的那一次（本仓既有纪律）。
func baselineGoroutines() int {
	m := runtime.NumGoroutine()
	for i := 0; i < 4; i++ {
		time.Sleep(50 * time.Millisecond)
		if n := runtime.NumGoroutine(); n < m {
			m = n
		}
	}
	return m
}

// waitGoroutinesBack 断言 goroutine 数在窗口内回落到「基线 + 容差」之内。
// 到点未回落即红，附全量栈快照——红要能让人照着查是哪类 goroutine 没退。
func waitGoroutinesBack(t *testing.T, baseline int) {
	t.Helper()
	tol := stressTolerance()
	deadline := time.Now().Add(stressSettleWindow())
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+tol {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	t.Fatalf("goroutine 数未回落：基线 %d + 容差 %d，实测 %d。\n栈快照：\n%s",
		baseline, tol, runtime.NumGoroutine(), buf[:n])
}

// blockingBinding 一个只有 ctx 能打断的绑定（审批等待形态）。
func blockingBinding(onEnter func()) ptc.Binding {
	return ptc.Binding{
		Name: "wait_approval",
		Call: func(ctx context.Context, _ string) (string, error) {
			if onEnter != nil {
				onEnter()
			}
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
}

// TestStressInProcessDeadlineRecoversRecycles 工具阻塞到**墙钟耗尽**：
// Run 收口（deadline kind），中断驱动 goroutine 随 done 信号退出，
// goroutine 数回落。
func TestStressInProcessDeadlineRecoversRecycles(t *testing.T) {
	// 预热：让包级懒加载（logx 等）的常驻 goroutine 先起来，再取基线。
	_ = ptc.Run(context.Background(), `return 1`, nil, ptc.Options{Stages: allowStages()})
	baseline := baselineGoroutines()

	for i := 0; i < 3; i++ {
		res := ptc.Run(context.Background(),
			`return await host.wait_approval('{}')`,
			[]ptc.Binding{blockingBinding(nil)},
			ptc.Options{Stages: allowStages(), Timeout: 80 * time.Millisecond})
		if res.OK || (res.ErrorKind != "deadline" && res.ErrorKind != "runtime") {
			t.Fatalf("第 %d 轮应按超时收场: %+v", i, res)
		}
	}
	waitGoroutinesBack(t, baseline)
}

// TestStressInProcessCancelRecyclesRecycles 调用方**取消**：与墙钟是两条
// 独立防线，各自都要收口干净。
func TestStressInProcessCancelRecycles(t *testing.T) {
	_ = ptc.Run(context.Background(), `return 1`, nil, ptc.Options{Stages: allowStages()})
	baseline := baselineGoroutines()

	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan ptc.Result, 1)
		go func() {
			done <- ptc.Run(ctx, `return await host.wait_approval('{}')`,
				[]ptc.Binding{blockingBinding(nil)},
				ptc.Options{Stages: allowStages(), Timeout: 30 * time.Second})
			cancel()
		}()
		time.Sleep(40 * time.Millisecond)
		cancel()
		select {
		case res := <-done:
			if res.OK {
				t.Fatalf("第 %d 轮被取消后不应成功: %+v", i, res)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("第 %d 轮取消后 10s 未收口", i)
		}
	}
	waitGoroutinesBack(t, baseline)
}

// TestStressInProcessBudgetExhaustionRecycles 预算耗尽（调用数上限）：
// 拒绝不是泄漏路径——每次绑定调用都同步返回，程序被 budget_calls 收口。
func TestStressInProcessBudgetExhaustionRecycles(t *testing.T) {
	_ = ptc.Run(context.Background(), `return 1`, nil, ptc.Options{Stages: allowStages()})
	baseline := baselineGoroutines()

	echo := ptc.Binding{Name: "echo", Call: func(context.Context, string) (string, error) {
		return "ok", nil
	}}
	for i := 0; i < 10; i++ {
		res := ptc.Run(context.Background(),
			`for (var k = 0; k < 50; k++) { await host.echo("{}"); } return "done"`,
			[]ptc.Binding{echo},
			ptc.Options{Stages: allowStages(), MaxCalls: 5, Timeout: 10 * time.Second})
		if res.OK || res.ErrorKind != "budget_calls" {
			t.Fatalf("第 %d 轮应按 budget_calls 收场: %+v", i, res)
		}
	}
	waitGoroutinesBack(t, baseline)
}
