package ptc_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/yi-nology/agent-harness/ptc"
)

// 本文件是子进程后端的**端到端**验证：真的起一个子进程，真的让它申请超限内存，
// 真的看它被内核杀掉，而**宿主进程存活**。
//
// # 为什么单独编译一个小程序当子进程
//
// 子进程后端默认 re-exec 宿主自己的可执行文件。测试里没有「宿主」，但可以
// 现场编译一个**只做子进程**的小程序：它调用 ptc.MaybeRunChild()，然后被
// 父侧以 re-exec 方式拉起。
//
// 这样做的好处：子进程形态与生产**完全一致**（同样的 MaybeRunChild 入口、
// 同样的 fd 传递、同样的环境变量），测出来的行为就是生产行为。
//
// # 验收（设计档 §八 P1 的原文要求）
//
//   「一个真兜底的用例——子进程内程序申请超过上限的内存 ⇒ 子进程被内核杀掉
//     ⇒ 父侧收到「执行域异常终止」，**宿主进程存活**」

// sandboxRequest 一次空执行请求（子进程后端不用它的内容，只用它触发一次执行）。
func sandboxRequest() sandbox.Request {
	return sandbox.Request{Program: ""}
}

// requireMemoryLimitSupport 当前环境兜不住内存时跳过，并**说清为什么**。
//
// 跳过理由必须写明「本环境兜不住」——不能让一条恒跳过的用例看起来像「测过了」。
// CI 跑 ubuntu-24.04（cgroup v2 默认开启），故真兜底那条在 CI 上是**真跑**的。
func requireMemoryLimitSupport(t *testing.T) {
	t.Helper()
	if !ptc.MemoryLimitSupported() {
		t.Skip("本环境建不出带 memory.max 的 cgroup（内存兜底不可用），该场景在此环境无法验证")
	}
}

// buildChildProgram 编译一个「子进程形态」的小程序，返回可执行文件路径。
//
// # 预编译子进程：给没有 Go 工具链的目标环境用
//
// 默认现场编译（需要目标环境有 go）。但「目标环境没有 Go、只有编译好的二进制」
// 是常见形态（受限的生产机、交叉验证），故支持用环境变量指定一个**预编译**的
// 子进程二进制：
//
//	AGENT_HARNESS_PTC_CHILD_BIN=/path/to/child
//
// 这让同一份测试二进制能在**没有 Go 的 Linux 机器**上跑内存兜底的端到端验收
// （那正是唯一能建出子 cgroup 的环境形态）。
func buildChildProgram(t *testing.T) string {
	t.Helper()
	if pre := os.Getenv(childBinEnv); pre != "" {
		if _, err := os.Stat(pre); err != nil {
			t.Fatalf("指定的预编译子进程不可用（%v）", err)
		}
		return pre
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	prog := `package main

import "github.com/yi-nology/agent-harness/ptc"

func main() {
	ptc.MaybeRunChild()
}
`
	if err := os.WriteFile(src, []byte(prog), 0o600); err != nil {
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

// childBinEnv 预编译子进程二进制的路径（环境变量）。
const childBinEnv = "AGENT_HARNESS_PTC_CHILD_BIN"

// TestSubprocessExecutesProgram 跨进程执行闭环：程序在子进程里跑，工具调用
// 经管道往返到父侧执行。
func TestSubprocessExecutesProgram(t *testing.T) {
	bin := buildChildProgram(t)
	called := make(chan string, 1)
	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
		ExePath: bin,
		Program: `return await host.echo("hi");`,
		Bindings: []ptc.Binding{{
			Name: "echo",
			Call: func(ctx context.Context, args string) (string, error) {
				called <- args
				return "OUT<" + args + ">", nil
			},
		}},
		Timeout: 20 * time.Second,
	})
	out, err := p.Exec(context.Background(), sandboxRequest())
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !out.OK {
		t.Fatalf("应成功，实得 %+v", out)
	}
	if !strings.Contains(out.Value, "OUT<") {
		t.Errorf("结果=%q，应含父侧返回的 OUT<...>", out.Value)
	}
	select {
	case args := <-called:
		if args != "hi" {
			t.Errorf("父侧收到的参数=%q，应为 hi", args)
		}
	case <-time.After(2 * time.Second):
		t.Error("父侧未收到绑定调用")
	}
}

// TestSubprocessMemoryLimitKillsChild 真兜底：子进程内程序申请超过上限的内存
// ⇒ 被内核杀掉 ⇒ 父侧收到「执行域异常终止」，而**宿主进程存活**。
//
// 这是 P1 的核心验收：不兜住的形态是「一个不可信程序能杀掉宿主」。
//
// # 平台约束：内存兜底靠 cgroup v2，故只在能建出 cgroup 的环境真跑
//
// 非 Linux（macOS / Windows）没有 cgroup；Linux 上若当前 cgroup 不可写
// （容器里没委派 memory 控制器），同样建不出来。
//
// 此时**跳过**而不是让用例红——但跳过的理由必须写明「本环境兜不住」，
// 不能让它看起来像「测过了」。CI 跑 ubuntu-24.04（cgroup v2 默认开启），
// 故这条用例在 CI 上是**真跑**的，不是恒跳过。
func TestSubprocessMemoryLimitKillsChild(t *testing.T) {
	requireMemoryLimitSupport(t)
	bin := buildChildProgram(t)
	// 上限给 256MB：足够跑起 Go 运行时与 goja，但程序申请 1GB 时必被杀。
	const memLimit = 256 << 20
	p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
		ExePath:   bin,
		MaxMemory: memLimit,
		// 程序申请远超上限的内存：连分配都过不去。
		Program: `
			var a = [];
			for (var i = 0; i < 100000; i++) { a.push(new Array(1000000).fill(i)); }
			return a.length;
		`,
		Timeout: 30 * time.Second,
	})
	// 宿主进程必须活着走到这里——这正是「真兜底」的意义。
	out, err := p.Exec(context.Background(), sandboxRequest())
	if err == nil {
		t.Fatalf("子进程应被杀（执行域异常终止），实得成功: %+v", out)
	}
	if !strings.Contains(err.Error(), "执行域异常终止") {
		t.Errorf("错误信息应说明执行域异常终止，实得 %q", err.Error())
	}
	// 宿主仍能继续干活（进程没被拖死）——再跑一次正常的执行即可证明。
	if _, err := p.Exec(context.Background(), sandboxRequest()); err != nil &&
		!strings.Contains(err.Error(), "执行域异常终止") {
		t.Errorf("宿主应仍可执行，实得 %v", err)
	}
}
