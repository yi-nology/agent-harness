package ptc_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/yi-nology/agent-harness/ptc"
)

// 本文件钉住「子进程不回结果」这一档的**可诊断性**与**不挂死**。
//
// # 为什么这一档重要
//
// 子进程不回结果有两种完全不同的原因，宿主的处置也完全不同：
//
//   - **子进程自己结束了**（被内核 OOM 杀 / 自己 fatal 退出）→ 管道 EOF，
//     父侧立刻拿到错误，并能从退出状态分清「兜底生效」与「启动失败」；
//   - **子进程还活着但没回结果**（典型：宿主忘了在 main 里调
//     `ptc.MaybeRunChild()`，于是子进程跑起了宿主的完整 main——它不退出，
//     也不回 Result 帧）→ 管道**不会** EOF，父侧会一直等下去。
//
// 第二种若不兜住，后果是**父侧永久挂死**（`ctx` 取消都不管用，见下），
// 而症状是「执行卡住」——极难定位。
//
// # 上游的缺口（本包兜住的部分）
//
// `guardx/wire` 的 `Starter.Start(ctx, ...)` **收了 ctx 却从不用它**：
// `ReadFrame` 是无超时的阻塞读。故「子进程不回结果也不退出」时，
// 父侧**永久阻塞**——`ctx` 取消无效。
//
// 本包兜住它：父侧**自己**给「等结果」加一个期限，超了就报错收场。
// 这不是改上游语义，是**在调用侧补一道闸**。

// noopBinEnv 预编译「不回结果」子进程二进制的路径（环境变量）。
//
// 与 childBinEnv 是**两个不同的程序**：前者是正常的子进程形态（会回 Result），
// 后者**不回**（模拟宿主忘了调 MaybeRunChild）。故分开两个环境变量，
// 免得装错。
const noopBinEnv = "AGENT_HARNESS_PTC_NOOP_BIN"

// childModeEnvName 控制 cmd/ptcchild 走哪个形态（正常子进程 / 不回结果）。
const childModeEnvName = "AGENT_HARNESS_PTC_CHILD_MODE"

// buildNoopChildProgram 编译一个「**不是**子进程形态」的小程序：
// 它会**睡一段时间然后退出**，但从不回 Result 帧。
//
// 这模拟「宿主忘了调 MaybeRunChild」的真实形态：子进程活着、不回结果。
func buildNoopChildProgram(t *testing.T) string {
	t.Helper()
	// 预编译版本（目标环境无 Go 工具链时用）——与 buildChildProgram 同一套思路。
	//
	// 注意：预编译的二进制是**同一个** cmd/ptcchild，它的「不回结果」行为由
	// 运行时的 AGENT_HARNESS_PTC_CHILD_MODE=noop 决定。故这里**同时**把它
	// 设进本进程的环境——子进程继承父进程环境。
	if pre := os.Getenv(noopBinEnv); pre != "" {
		if _, err := os.Stat(pre); err != nil {
			t.Fatalf("指定的预编译「不回结果」子进程不可用（%v）", err)
		}
		t.Setenv(childModeEnvName, "noop")
		return pre
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	// 注意：**不** import ptc、**不**调 MaybeRunChild——它就是个普通程序，
	// 读 stdin（等 Start 帧）却什么都不回。
	prog := `package main

import (
	"io"
	"os"
	"time"
)

func main() {
	// 读掉一点 stdin（模拟收到了 Start 帧），然后干等。
	_, _ = io.Copy(io.Discard, os.Stdin)
	time.Sleep(60 * time.Second)
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
		t.Fatalf("写子进程源码失败: %v", err)
	}
	bin := filepath.Join(dir, "noopchild")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译子进程失败: %v\n%s", err, out)
	}
	return bin
}

// TestSubprocessDoesNotHangWhenChildNeverReports 钉住**不挂死**：
// 子进程活着但从不回结果时，父侧必须在**有限时间**内报错收场，
// 而不是永久阻塞。
//
// 这条同时钉住了「ctx 取消能收场」——即使子进程一直活着。
func TestSubprocessDoesNotHangWhenChildNeverReports(t *testing.T) {
	bin := buildNoopChildProgram(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
		ExePath: bin,
		Program: "return 1;",
		Timeout: 30 * time.Second,
	})

	done := make(chan error, 1)
	go func() {
		_, err := p.Exec(ctx, sandbox.Request{Program: ""})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("子进程没回结果时不该成功")
		}
		t.Logf("父侧按时收场: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("父侧挂死了：子进程不回结果时必须有限时间内收场")
	}
}

// TestSubprocessHonorsCtxCancellation 钉住**调用方取消**这条独立防线：
// ctx 被取消时，父侧**立即**收场（不等墙钟预算）。
//
// # 为什么单独一条
//
// 前两条里 ctx 的 deadline（5s）与墙钟预算（2s）**接近**，
// 「靠 ctx 收场」与「靠墙钟收场」分不开——去掉任意一条都仍然绿。
// 本条让 ctx **远早于**墙钟预算到期（200ms vs 30s）⇒ 只能是 ctx 取消起的作用。
//
// 覆盖的场景也真实：宿主编排被上游取消（用户中断、请求超时）时，
// 子进程后端必须**立刻**放手，而不是继续等满墙钟预算。
func TestSubprocessHonorsCtxCancellation(t *testing.T) {
	bin := buildNoopChildProgram(t)
	// ctx **显式取消**（无 deadline！）⇒ 墙钟预算仍是 30s，
	// 期限计算取「min(ctx.deadline 剩余, timeout)」时没有 ctx.deadline 可取
	// ⇒ 期限 = 30s。收场只可能来自 **ctx 取消**这一条分支。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 100ms 后取消（模拟宿主编排被上游中断）。
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
		ExePath: bin,
		Program: "return 1;",
		Timeout: 30 * time.Second,
	})

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := p.Exec(ctx, sandbox.Request{Program: ""})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("子进程没回结果时不该成功")
		}
		elapsed := time.Since(start)
		// 必须在墙钟预算（30s）之前很久就收场 ⇒ 只能是 ctx 取消。
		if elapsed > 5*time.Second {
			t.Errorf("收场耗时 %v，远超 ctx 取消的时刻（说明不是 ctx 取消起的作用）", elapsed)
		}
		t.Logf("父侧按 ctx 取消收场（%v）: %v", elapsed, err)
	case <-time.After(20 * time.Second):
		t.Fatal("父侧挂死了：ctx 取消时必须立即收场")
	}
}

// TestSubprocessTimesOutWithoutCtxDeadline 钉住**墙钟预算**这条独立防线：
// 调用方的 ctx **没有** deadline（`context.Background()`）时，
// 父侧仍须按自己的墙钟预算（`Timeout`）收场。
//
// # 为什么单独一条
//
// 上一条里 ctx 带 5s deadline，与父侧的墙钟预算（30s）**不同时到期**，
// 于是「靠 ctx 收场」与「靠墙钟收场」**分不开**——去掉任意一条都仍然绿。
// 本条把 ctx 的 deadline 去掉 ⇒ **只剩墙钟预算**这一条路，
// 故它单独钉住 timer 分支。
//
// 覆盖的场景也真实：宿主的编排主流程常常用 `context.Background()`
// （不设 deadline），此时唯一的期限就是子进程后端自己的 `Timeout`。
func TestSubprocessTimesOutWithoutCtxDeadline(t *testing.T) {
	bin := buildNoopChildProgram(t)
	// ctx **不带** deadline ⇒ 只能靠墙钟预算收场。
	ctx := context.Background()

	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
		ExePath: bin,
		Program: "return 1;",
		// 墙钟预算压到 2s（真实默认是 DefaultTimeout，太长不适合测试）。
		Timeout: 2 * time.Second,
	})

	done := make(chan error, 1)
	go func() {
		_, err := p.Exec(ctx, sandbox.Request{Program: ""})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("子进程没回结果时不该成功")
		}
		t.Logf("父侧按墙钟预算收场: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("父侧挂死了：无 ctx deadline 时也必须按墙钟预算收场")
	}
}
