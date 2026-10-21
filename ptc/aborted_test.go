package ptc

import (
	"os/exec"
	"testing"

	"github.com/oekit/pkg/errors"
)

// errPipeEOF 造一个「管道 EOF」形态的底层错误。
func errPipeEOF() error { return errors.New("管道 EOF") }

// asExitError 断言并取出 *exec.ExitError。
func asExitError(err error, target **exec.ExitError) bool { return errors.As(err, target) }

// isErr 判定错误链里有没有目标错误。
func isErr(err, target error) bool { return errors.Is(err, target) }

// 本文件钉住**执行域异常终止**的死因分类。
//
// # 为什么它必须可单测（而不必起真子进程）
//
// 死因分类的判据是「退出码是否为 -1」（Unix 上被信号终止的形态），
// 而 `exec.ExitError` 的退出码只能从**真实的进程状态**读出来
// （`ProcessState` 是不可伪造的结构）。
//
// 故本文件**真跑两个一次性进程**来造出两种退出形态：
//   - 一个自己 `os.Exit(1)` → 退出码 1 → **不是**信号终止；
//   - 一个被自己 `Kill()` → 信号终止 → 退出码 -1。
//
// 这比伪造 ProcessState 可靠：它测的是 `exec` 包真实的退出码语义，
// 而那正是本包的判据依赖的东西。
//
// 真的「被杀一次」那条端到端用例在 subprocess_exec_test.go，
// 它需要内存兜底环境（Linux + 可写 cgroup），当前环境常常跑不了。
// 两者互补：这条钉住**分类口径**，那条钉住**端到端形态**。

// exitStateOf 真跑一个进程并取回它的 ProcessState。
//
// kill=true  → 进程被信号终止（退出码 -1）；
// kill=false → 进程**自己**退出（退出码为实际值）。
func exitStateOf(t *testing.T, kill bool) *exec.ExitError {
	t.Helper()
	var cmd *exec.Cmd
	if kill {
		// 睡着的进程被 Kill ⇒ 信号终止。
		cmd = exec.Command("/bin/sh", "-c", "sleep 30")
	} else {
		// 自己退出 ⇒ 非零退出码（启动失败的形态）。
		cmd = exec.Command("/bin/sh", "-c", "exit 3")
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("起不了测试进程（%v），该形态在本环境无法构造", err)
	}
	if kill {
		_ = cmd.Process.Kill()
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	if !asExitError(err, &ee) {
		t.Skipf("进程未以错误退出（err=%v），该形态在本环境无法构造", err)
	}
	return ee
}

// TestAbortedErrorSignaledVsExit 钉住最关键的区分：
// **被信号杀**（兜底生效了）与**自己非零退出**（启动失败）必须分开。
//
// 误用形态：两者都判成「异常终止」不分类 → 宿主只能一律当故障处理，
// 于是「兜底生效」这种正常结局被误报成故障，而真正该修的启动失败被淹没。
func TestAbortedErrorSignaledVsExit(t *testing.T) {
	// 被信号杀：Unix 上 ExitCode() 返回 -1。
	signaled := exitStateOf(t, true)
	got := abortedError(signaled, errPipeEOF())
	ab, ok := got.(*ExecutionAborted)
	if !ok {
		t.Fatalf("应归类为 ExecutionAborted，实得 %T", got)
	}
	if !ab.Signaled {
		t.Errorf("被信号终止的应判为 Signaled=true（兜底生效的形态），实得 Exit=%q", ab.Exit)
	}
	if ab.Exit == "" {
		t.Error("应带出退出状态描述")
	}
}

// TestAbortedErrorNonSignaled 钉住：非信号终止 ⇒ 不判 Signaled。
//
// 这条与上一条互补：上一条证明「信号终止会被认出来」，
// 这条证明「非信号终止不会被误认」——
// 后者更危险：把启动失败误判成「兜底生效」，宿主就不会去修环境。
func TestAbortedErrorNonSignaled(t *testing.T) {
	exited := exitStateOf(t, false)
	got := abortedError(exited, errPipeEOF())
	ab, ok := got.(*ExecutionAborted)
	if !ok {
		t.Fatalf("应归类为 ExecutionAborted，实得 %T", got)
	}
	if ab.Signaled {
		t.Error("非信号终止不应判为 Signaled（那是启动失败的形态）")
	}
}

// TestAbortedErrorKeepsCause 钉住：底层错误（管道 EOF）要能被取到。
//
// 宿主可能想进一步判别（EOF 是「管道断了」还是「别的」），
// 故 Unwrap 必须真的通到底层错误。
func TestAbortedErrorKeepsCause(t *testing.T) {
	cause := errPipeEOF()
	got := abortedError(exitStateOf(t, false), cause)
	if !isErr(got, cause) {
		t.Fatal("底层错误应能通过 Unwrap 取到")
	}
}

// TestAbortedErrorNormalExitWithoutResult 钉住边界：子进程**正常退出**
// 却没交回结果（协议不匹配的形态）——不是信号终止，要如实说。
func TestAbortedErrorNormalExitWithoutResult(t *testing.T) {
	got := abortedError(nil, errPipeEOF())
	ab, ok := got.(*ExecutionAborted)
	if !ok {
		t.Fatalf("应归类为 ExecutionAborted，实得 %T", got)
	}
	if ab.Signaled {
		t.Error("正常退出不该判为 Signaled")
	}
	if ab.Exit == "" {
		t.Error("应带出退出状态描述")
	}
}

// TestExecutionAbortedMessageIncludesStderr 钉住：崩溃原因（stderr 尾部）
// 必须进错误消息——退出码分不清「被内核杀掉」与「自己 fatal 退出」，
// stderr 才是**死因**。
func TestExecutionAbortedMessageIncludesStderr(t *testing.T) {
	ab := &ExecutionAborted{
		Err:    errPipeEOF(),
		Exit:   "signal: killed",
		Stderr: "fatal error: runtime: out of memory\n",
	}
	msg := ab.Error()
	if !containsStr(msg, "out of memory") {
		t.Fatalf("错误消息应含 stderr 尾部，实得 %q", msg)
	}
	if !containsStr(msg, "signal: killed") {
		t.Fatalf("错误消息应含退出状态，实得 %q", msg)
	}
	// 尾部换行应被裁掉（消息更紧凑）。
	if containsStr(msg, "\n\n") {
		t.Fatalf("stderr 尾部换行应被裁掉，实得 %q", msg)
	}
}

// TestExecutionAbortedUnwrap 钉住：Unwrap 通到底层错误。
func TestExecutionAbortedUnwrap(t *testing.T) {
	cause := errPipeEOF()
	ab := &ExecutionAborted{Err: cause}
	if !isErr(ab, cause) {
		t.Fatal("Unwrap 应通到底层错误")
	}
}

// containsStr 简易包含判断（不引入额外依赖面）。
func containsStr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
