package ptc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/guardx/sandbox"
)

// 本文件钉住**内存兜底的 fail-closed 语义**。
//
// # 为什么这些用例需要「注入缝」
//
// 内存兜底的机制（cgroup v2）在多数开发环境里**根本不可用**：
//   - macOS 没有 cgroup（seatbelt 裁能力面，不裁内存）；
//   - Linux 容器里 cgroup 常是只读挂载。
//
// 于是「建不出 cgroup」「attach 失败」这两条分支在开发机上**走不到**——
// 没有注入缝的话，它们就是**恒不执行**的代码（写了也没人验证过）。
//
// `newMemCgroupFn` 这个包级变量就是那道缝：生产路径不替换它，
// 测试替换它以构造出「建不出 / attach 失败」的形态。
//
// # 这些用例守的是什么
//
// 一句话：**「以为兜住了、实际没有」必须是不可能发生的**。
//
// 若建不出 cgroup 却继续跑，宿主以为这个子进程受内存约束，
// 实际它能吃光宿主的物理内存——正是这套机制存在的理由被推翻。
// 若 attach 失败却放它裸跑，同理。

// fakeCgroup 一个可控的 memCgroup 替身。
type fakeCgroup struct {
	attachErr   error
	attachedPID int
	released    bool
}

func (f *fakeCgroup) attach(pid int) error {
	f.attachedPID = pid
	return f.attachErr
}

func (f *fakeCgroup) release() { f.released = true }

// withMemCgroupFactory 临时替换建 cgroup 的入口，测试结束恢复。
//
// 用 defer 恢复：漏恢复会让后续用例看到一个假的构造入口，
// 那种污染极难查（表现为「别的用例莫名其妙地失败」）。
func withMemCgroupFactory(t *testing.T, fn func(int64) (memCgroup, error)) {
	t.Helper()
	old := newMemCgroupFn
	newMemCgroupFn = fn
	t.Cleanup(func() { newMemCgroupFn = old })
}

// TestMemoryLimitFailsClosedWhenCannotBuild 钉住：**建不出**带上限的
// 执行环境 ⇒ 拒绝执行（fail closed），**不是**「建不出就跑」。
//
// 误用形态：建不出就跳过上限继续跑 ⇒ 宿主以为受约束，实际不受约束。
func TestMemoryLimitFailsClosedWhenCannotBuild(t *testing.T) {
	withMemCgroupFactory(t, func(int64) (memCgroup, error) {
		return nil, ErrMemoryLimitUnsupported
	})

	bin := buildChildProgramInPkg(t)
	p := NewSubprocessProvider(SubprocessOptions{
		ExePath:   bin,
		MaxMemory: 256 << 20,
		Program:   "return 1;",
		Timeout:   10 * time.Second,
	})
	_, err := p.Exec(context.Background(), sandbox.Request{Program: ""})
	if err == nil {
		t.Fatal("建不出带内存上限的执行环境时应拒绝执行，不该放行")
	}
	if !strings.Contains(err.Error(), "执行环境") {
		t.Errorf("错误应说明是执行环境的问题，实得 %q", err.Error())
	}
	// 关键：不该是「子进程异常终止」——那是另一类失败（执行到一半没了）。
	// 这里根本没起子进程。
	if strings.Contains(err.Error(), "执行域异常终止") {
		t.Errorf("还没起子进程，不该报执行域异常终止，实得 %q", err.Error())
	}
}

// TestAttachFailureKillsChild 钉住：**attach 失败** ⇒ 杀掉子进程并回收，
// **不放它裸跑**。
//
// 误用形态：attach 失败就继续 ⇒ 子进程带着「以为自己受约束」的前提
// 去执行一个不受约束的程序——比「压根不建 cgroup」更隐蔽，
// 因为宿主侧看起来一切正常。
func TestAttachFailureKillsChild(t *testing.T) {
	fake := &fakeCgroup{attachErr: errAttachFailed()}
	withMemCgroupFactory(t, func(int64) (memCgroup, error) { return fake, nil })

	bin := buildChildProgramInPkg(t)
	p := NewSubprocessProvider(SubprocessOptions{
		ExePath:   bin,
		MaxMemory: 256 << 20,
		Program:   "return 1;",
		Timeout:   10 * time.Second,
	})
	_, err := p.Exec(context.Background(), sandbox.Request{Program: ""})
	if err == nil {
		t.Fatal("attach 失败时应拒绝执行，不该放行")
	}
	if !strings.Contains(err.Error(), "执行环境") {
		t.Errorf("错误应说明是执行环境的问题，实得 %q", err.Error())
	}
	// 闸必须被回收（不留垃圾 cgroup）。
	if !fake.released {
		t.Error("attach 失败后应回收 cgroup")
	}
}

// TestCgroupReleasedAfterExecution 钉住：正常执行完也要回收 cgroup。
//
// 漏回收 ⇒ 每次执行留一个 cgroup 目录，累积成宿主的垃圾。
func TestCgroupReleasedAfterExecution(t *testing.T) {
	fake := &fakeCgroup{}
	withMemCgroupFactory(t, func(int64) (memCgroup, error) { return fake, nil })

	bin := buildChildProgramInPkg(t)
	p := NewSubprocessProvider(SubprocessOptions{
		ExePath:   bin,
		MaxMemory: 256 << 20,
		Program:   "return 1;",
		Timeout:   10 * time.Second,
	})
	if _, err := p.Exec(context.Background(), sandbox.Request{Program: ""}); err != nil {
		t.Fatalf("执行不该失败: %v", err)
	}
	if !fake.released {
		t.Error("执行完应回收 cgroup")
	}
	if fake.attachedPID == 0 {
		t.Error("应把子进程放进 cgroup（attach 被调用过）")
	}
}

// TestNoCgroupWhenNoMemoryLimit 钉住：不要求内存上限 ⇒ **不建** cgroup。
//
// 建一个「没有上限的 cgroup」是多余的（它不提供任何约束），
// 且凭空多一个要回收的资源。
func TestNoCgroupWhenNoMemoryLimit(t *testing.T) {
	built := false
	withMemCgroupFactory(t, func(int64) (memCgroup, error) {
		built = true
		return &fakeCgroup{}, nil
	})

	bin := buildChildProgramInPkg(t)
	p := NewSubprocessProvider(SubprocessOptions{
		ExePath: bin,
		Program: "return 1;",
		Timeout: 10 * time.Second,
	})
	if _, err := p.Exec(context.Background(), sandbox.Request{Program: ""}); err != nil {
		t.Fatalf("执行不该失败: %v", err)
	}
	if built {
		t.Error("不要求内存上限时不该建 cgroup")
	}
}

// childBinEnvPkg 预编译子进程二进制的路径（包内测试用）。
//
// 与外部测试包的同名常量对应——两份各写一遍是刻意的：
// 跨包共享测试夹具要把它导出成公开 API，那会把测试细节泄进生产面。
const childBinEnvPkg = "AGENT_HARNESS_PTC_CHILD_BIN"

// errAttachFailed 造一个「attach 失败」形态的错误。
func errAttachFailed() error { return exec.ErrNotFound }

// buildChildProgramInPkg 编译一个「子进程形态」的小程序，返回可执行文件路径。
//
// 与外部测试包那份（subprocess_exec_test.go 的 buildChildProgram）同款，
// 但本文件在**包内**——它要访问私有的 memCgroup / newMemCgroupFn。
// 两份各写一遍是刻意的：跨包共享测试夹具要把它导出成公开 API，
// 那会把内部机制暴露出去，不值当。
func buildChildProgramInPkg(t *testing.T) string {
	t.Helper()
	// 预编译子进程（目标环境无 Go 工具链时用）。
	if pre := os.Getenv(childBinEnvPkg); pre != "" {
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
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译子进程失败: %v\n%s", err, out)
	}
	return bin
}
