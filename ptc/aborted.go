package ptc

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oekit/pkg/errors"
)

// ExecutionAborted 执行域异常终止：子进程没给出结果就结束了。
//
// # 为什么把它做成结构化错误，而不是一句文案
//
// 「子进程没了」有**两种完全不同的原因**，宿主对它们的处置也不同：
//
//   - **被内核杀掉**（信号终止）：多半是它申请了超限资源——内存兜底**生效了**。
//     这是**预期行为**，不是故障；宿主该记一笔「兜底起作用了」。
//   - **自己退出**（非零退出码）：子进程**启动失败**——设不上地址空间上限、
//     fd 无效、协议不匹配。这是**故障**，宿主该报「这个环境不支持这种执行形态」。
//
// 只回一句「执行域异常终止」的话，宿主分不出这两者，只能一律当故障处理——
// 于是「兜底生效」这种正常结局会被误报成故障，而真正该修的启动失败被淹没。
//
// # 为什么父侧不能先 Kill 再取状态
//
// 我们自己 Kill 掉的子进程与被内核杀掉的子进程，退出状态**都是**「信号终止」。
// 只有当子进程**自己先结束**时，取到的才是真死因。故取状态必须**先于**收刀
// （见 subprocess.go 的收尾顺序）。
type ExecutionAborted struct {
	// Err 底层错误（管道 EOF 等）。
	Err error
	// Exit 子进程退出状态描述（如 "signal: killed" / "exit status 1"）。
	Exit string
	// Signaled 子进程是否被**信号**终止（被内核杀掉）。
	//
	// Unix 上被信号终止时 `ExitCode()` 返回 -1；Windows 上退出码恒为实际值。
	// 故这个判据跨平台成立，不需要按平台分实现。
	Signaled bool
	// Stderr 子进程 stderr 的尾部（崩溃原因就在这里），**已脱敏**。
	//
	// 没有它，宿主只看到一个退出码——而退出码分不清「被内核杀掉（兜底生效）」
	// 与「自己 fatal 退出（启动失败或 runtime 报错）」。这是**死因**，不是日志。
	//
	// 为什么是脱敏后的：子进程常把连接串、API key、token 打进 stderr
	// （MCP server 启动时的配置回显最常见），而这段文本会**原样**进诊断报告
	// 与错误消息——那等于凭据泄漏面。脱敏用上游 guardx/logredact（凭据形态打码），
	// 本仓 pii 管的是个人信息，两者互补不重叠。
	//
	// 脱敏发生在**写入这个字段时**（不是 Error() 渲染时）：字段是公开的，
	// 宿主可以直接读 e.Stderr——只在渲染面脱敏等于给宿主留了原文出口。
	Stderr string
}

// Error 实现 error。
func (e *ExecutionAborted) Error() string {
	msg := fmt.Sprintf("ptc: 执行域异常终止（子进程状态：%s）: %v", e.Exit, e.Err)
	if s := trimTail(e.Stderr); s != "" {
		msg += "；子进程输出：" + s
	}
	return msg
}

// Unwrap 让调用方能取到底层错误（管道 EOF）。
func (e *ExecutionAborted) Unwrap() error { return e.Err }

// trimTail 裁掉 stderr 尾部的换行与空白，让错误消息更紧凑。
func trimTail(s string) string {
	return strings.TrimRight(s, "\n\r\t ")
}

// redactChildStderr 给子进程 stderr 落盘前脱敏（凭据形态打码）。
//
// 为什么必须脱敏：子进程常把连接串、API key、token 打进 stderr——
// MCP server 启动时回显配置是最高频形态。这段文本会原样进错误消息与
// 诊断报告，等于把凭据抄送一份给日志系统与模型上下文。
//
// 为什么用上游 guardx/logredact：凭据形态打码是一张**持续维护的规则表**
// （URL 内嵌凭据、Bearer 头、JWT、各家厂商 token 前缀、长 hex、键值对…）。
// 本仓 pii 管的是个人信息（手机号、身份证），与凭据面互补不重叠——
// 两者判的不是同一件事，不能互相替代。
//
// 误伤方向是**过度打码**而非漏打：上游表里像 SHA 校验和、UUID 这类
// 合法长 hex 会被一并掩掉。诊断面宁可少几个字符，也不能让凭据原样出去——
// 漏打才是事故方向。
func redactChildStderr(s string) string {
	return logredact.Redact(s)
}

// abortedError 把子进程的退出状态归类成 ExecutionAborted。
//
// waitErr 是 `child.Wait()` 的返回值（子进程**自己先结束**时取到的真死因）；
// cause 是管道 EOF（子进程没交回结果）。
//
// # 为什么这样判「是不是被信号杀的」
//
// Unix 上被信号终止时 `ExitCode()` 返回 -1；Windows 上退出码恒为实际值。
// 故判据是「退出码为 -1」⇒ 信号终止 ⇒ **被内核杀掉**（内存兜底生效的形态）。
//
// 宿主据此能分清两种完全不同的处置：
//   - 被信号杀 ⇒ 多半是申请了超限资源 ⇒ 兜底**生效了**（预期行为）；
//   - 自己非零退出 ⇒ 启动失败 ⇒ **故障**（该换环境或改配置）。
func abortedError(waitErr, cause error) error {
	ab := &ExecutionAborted{Err: cause}
	if waitErr == nil {
		// 子进程正常退出却没交回结果：罕见（协议不匹配的形态）。
		ab.Exit = "退出但未交回结果"
		return ab
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		ab.Exit = ee.Error()
		ab.Signaled = ee.ExitCode() == -1
		return ab
	}
	ab.Exit = waitErr.Error()
	return ab
}
