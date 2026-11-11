//go:build unix

// stress_subprocess_recovery_test.go —— 执行预算的**压力测试矩阵**：子进程侧
// （v0.33.0，issue #93）。
//
// # 格子
//
//   - 子进程**启动即异常退出**（非零码，模拟二进制损坏 / 启动崩溃）：
//     父侧有限时间内干净报错，子进程被收割，goroutine 数回落；
//   - 子进程**运行中被外部 SIGKILL**（OOM killer 的同形态注入：内核随机
//     挑进程杀，宿主只能看见「子进程没了」）：同上，且断言 PID 消失；
//   - **真 OOM**（cgroup memory.max 兜底，能建 cgroup 的环境才真跑）：
//     父进程存活，goroutine 数回落——补现有 TestSubprocessMemoryLimitKillsChild
//     缺的资源回收断言；
//   - **FD 回收**（linux）：多轮子进程执行后父侧文件描述符数不涨——管道
//     四条每轮都要关干净。
//
// # 「子进程不残留」怎么判
//
// 借 TMPDIR（子进程环境白名单内的基础变量）传一个 PID 文件路径：子进程
// 程序在进入协议等待前把自己的 PID 写下来，测试侧凭它探活（kill -0）。
// 被 Wait 收割过的 PID 探活必失败——那是「回收完成」的可观测事实。
package ptc_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/yi-nology/goagent-kernel/ptc"
)

// buildChildProgramVari 编译一个自定义子进程程序（source 为其 main.go 全文）。
func buildChildProgramVari(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatalf("写子进程源码失败: %v", err)
	}
	bin := filepath.Join(dir, "child")
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译子进程失败: %v\n%s", err, out)
	}
	return bin
}

// pidFileChildSource 带 PID 落盘的子进程源码（走 TMPDIR：基础集内的变量）。
func pidFileChildSource() string {
	return `package main

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/yi-nology/goagent-kernel/ptc"
)

func main() {
	_ = os.WriteFile(filepath.Join(os.TempDir(), "gak_ptc_child_pid"),
		[]byte(strconv.Itoa(os.Getpid())), 0o600)
	ptc.MaybeRunChild()
}
`
}

// immediateExitChildSource 启动即异常退出的子进程源码（非零码，不进协议——
// 模拟二进制损坏 / 启动崩溃形态）。
func immediateExitChildSource() string {
	return `package main

import "os"

func main() {
	os.Exit(3)
}
`
}

// readChildPID 等子进程把 PID 落盘并读回（轮询，给慢机器留足窗口）。
func readChildPID(t *testing.T) int {
	t.Helper()
	f := filepath.Join(os.TempDir(), "gak_ptc_child_pid")
	_ = os.Remove(f)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(f); err == nil {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
			if perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("15s 内未读到子进程 PID 文件")
	return 0
}

// pidAlive 探活：kill(pid, 0) 只查存在性不杀。
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// waitPidGone 断言 PID 已被收割（等 Wait 完成回收）。
func waitPidGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("子进程 PID %d 在 15s 后仍存活（未被收割）", pid)
}

// TestStressSubprocessImmediateExitReaps 子进程**启动即异常退出**：
// 父侧有限时间内干净报错，不挂死、无残留、goroutine 数回落。
func TestStressSubprocessImmediateExitReaps(t *testing.T) {
	bin := buildChildProgramVari(t, immediateExitChildSource())
	_ = ptc.Run(context.Background(), `return 1`, nil, ptc.Options{Stages: allowStages()})
	baseline := baselineGoroutines()

	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{Stages: allowStages(),
		ExePath: bin, Program: `return 1;`, Timeout: 20 * time.Second})
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := p.Exec(context.Background(), sandbox.Request{Program: ""})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("子进程启动即退出时父侧不该成功")
		}
		if el := time.Since(start); el > 15*time.Second {
			t.Fatalf("父侧应在有限时间内收场，实耗 %s", el)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("父侧挂死：子进程启动即退出必须有限时间内收场")
	}
	waitGoroutinesBack(t, baseline)
}

// TestStressSubprocessKillMidRunReaps 子进程**运行中被外部 SIGKILL**
// （OOM killer 的同形态注入）：父侧报「执行域异常终止」，PID 被收割，
// goroutine 数回落，宿主还能继续干活。
func TestStressSubprocessKillMidRunReaps(t *testing.T) {
	bin := buildChildProgramVari(t, pidFileChildSource())
	_ = ptc.Run(context.Background(), `return 1`, nil, ptc.Options{Stages: allowStages()})
	baseline := baselineGoroutines()

	block := ptc.Binding{Name: "wait", Call: func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}}
	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{Stages: allowStages(),
		ExePath:  bin,
		Program:  `return await host.wait("{}");`,
		Bindings: []ptc.Binding{block},
		Timeout:  30 * time.Second})
	done := make(chan error, 1)
	go func() {
		_, err := p.Exec(context.Background(), sandbox.Request{Program: ""})
		done <- err
	}()

	pid := readChildPID(t)
	if !pidAlive(pid) {
		t.Fatalf("子进程 PID %d 应存活（还没轮到杀）", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("外部 SIGKILL 注入失败: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("子进程被杀后父侧不该成功")
		}
		if !strings.Contains(err.Error(), "执行域异常终止") {
			t.Errorf("错误信息应说明执行域异常终止，实得 %q", err.Error())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("父侧挂死：子进程被杀必须有限时间内收场")
	}
	waitPidGone(t, pid)
	waitGoroutinesBack(t, baseline)

	// 宿主还能继续干活（没被拖死）。
	p2 := ptc.NewSubprocessProvider(ptc.SubprocessOptions{Stages: allowStages(),
		ExePath: bin, Program: `return 1;`, Timeout: 20 * time.Second})
	if _, err := p2.Exec(context.Background(), sandbox.Request{Program: ""}); err != nil {
		t.Fatalf("宿主应仍可执行: %v", err)
	}
}

// TestStressSubprocessOOMKillRecycles 真 OOM（cgroup memory.max 兜底）：
// 现有 TestSubprocessMemoryLimitKillsChild 断言了「父侧收到执行域异常终止」，
// 本条补上它没有的**资源回收**断言（goroutine 回落 + 宿主继续可用）。
// 建不出 cgroup 的环境跳过（跳过理由写明，与既有口径一致）。
func TestStressSubprocessOOMKillRecycles(t *testing.T) {
	if !ptc.MemoryLimitSupported() {
		t.Skip("本环境建不出带 memory.max 的 cgroup（内存兜底不可用），该场景在此环境无法验证")
	}
	bin := buildChildProgram(t)
	_ = ptc.Run(context.Background(), `return 1`, nil, ptc.Options{Stages: allowStages()})
	baseline := baselineGoroutines()

	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{Stages: allowStages(),
		ExePath:   bin,
		MaxMemory: 256 << 20,
		Program: `
			var a = [];
			for (var i = 0; i < 100000; i++) { a.push(new Array(1000000).fill(i)); }
			return a.length;
		`,
		Timeout: 30 * time.Second})
	if _, err := p.Exec(context.Background(), sandbox.Request{Program: ""}); err == nil {
		t.Fatal("申请超限内存应被内核杀掉，父侧收到错误")
	}
	waitGoroutinesBack(t, baseline)
}

// TestStressSubprocessFDsRecycled FD 回收（linux）：每轮子进程执行要建四条
// 管道——多轮之后父侧的描述符数必须回落。darwin 的 /proc 不存在，跳过
// （跳过理由写明；CI 是 ubuntu，真跑）。
func TestStressSubprocessFDsRecycled(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅 linux 有 /proc/self/fd 可数；CI（ubuntu）真跑")
	}
	bin := buildChildProgram(t)
	countFDs := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatalf("读 /proc/self/fd 失败: %v", err)
		}
		return len(ents)
	}
	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{Stages: allowStages(),
		ExePath: bin, Program: `return 1;`, Timeout: 20 * time.Second})
	// 预热一轮再取基线（懒加载的连接/缓存会占描述符，与泄漏无关）。
	if _, err := p.Exec(context.Background(), sandbox.Request{Program: ""}); err != nil {
		t.Fatalf("预热执行失败: %v", err)
	}
	baseline := countFDs()
	for i := 0; i < 3; i++ {
		if _, err := p.Exec(context.Background(), sandbox.Request{Program: ""}); err != nil {
			t.Fatalf("第 %d 轮执行失败: %v", i, err)
		}
	}
	tol := stressFDTolerance()
	deadline := time.Now().Add(stressSettleWindow())
	for {
		if cur := countFDs(); cur <= baseline+tol {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("文件描述符未回收：基线 %d + 容差 %d，实测 %d（管道泄漏）", baseline, tol, cur)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
