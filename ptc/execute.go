package ptc

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/oekit/pkg/errors"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/oagentkit/guardx/invoke"
	"github.com/oekit/concurrency/async"
)

// execute 执行一段程序的**共同执行面**：语法预检 → 建 VM → 挂绑定 → 跑 → 收口。
//
// # 为什么抽出来（两种执行形态必须共用同一份）
//
// 进程内（本地绑定）与子进程（远程绑定）的差别**只有一处**：绑定调用怎么落到
// 宿主工具上。其余（语法预检、三预算闸、中断驱动、调用形态包装、Promise 收口、
// 错误分类）**逐字相同**。
//
// 若不抽，子进程形态就得把这段复制一遍——那正是「同一机制两份实现 ⇒ 必然漂移」，
// 而漂移出来的往往是**安全语义**（比如子进程版漏了墙钟闸，就成了「看起来更安全
// 的路径反而更不安全」）。
//
// callIt 是唯一的差异点：进程内传 localCaller，子进程传 remoteCaller（远程请求）。
func execute(
	ctx context.Context,
	program string,
	bindings []Binding,
	timeout time.Duration,
	maxCalls, maxBytes int,
	discover Discoverer,
	stages []invoke.Stage,
	callIt bindingCaller,
	res *Result,
) Result {
	// 语法预检（批三百零七）：先编译后建 VM——语法错误带 goja 行号在 syntax kind 里
	// 如实回包，不烧预算不进 VM（运行期 RunString 的同类错误混在 runtime kind 里
	// 面目不清）。编译产物直接 RunProgram，不二次编译。
	// 包裹源码抽成变量：诊断折算行号必须用与编译**同一份**字符串。
	wrapped := wrapProgram(program)
	prog, cerr := goja.Compile(programSourceName, wrapped, false)
	if cerr != nil {
		res.Location = locationOfSyntax(cerr, wrapLines)
		res.Error, res.ErrorKind = fmt.Sprintf("程序语法错误%s: %v", describePosition(res.Location), cerr), "syntax"
		return *res
	}

	vm := goja.New()

	// 运行期 ctx：deadline/ctx 取消先断阻塞中的绑定调用（见头注），再 Interrupt 收口。
	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)

	// 中断驱动（goroutine 只管 Interrupt 与 runCtx，不触碰 VM 状态）：deadline/ctx
	// 取消到点即打断运行中的代码；Run 返回即收工。
	//
	// 走 async.GoSafe 而非裸 `go func`：本 goroutine 调 vm.Interrupt，一旦 goja 内部
	// panic，裸 goroutine 的 panic **无法被外层 recover 捕获**，会直接杀掉整个进程
	// （Run 主体自己的 defer 都救不回来）。GoSafe 在 goroutine 内 recover 并交给
	// 全局 PanicHandler 留痕，让「一次坏程序」降级成「一次坏运行」。
	done := make(chan struct{})
	defer close(done)
	async.GoSafe(func() {
		select {
		case <-time.After(timeout):
			cancelRun(errDeadline)
			vm.Interrupt(errDeadline)
		case <-ctx.Done():
			werr := errors.Wrap(ctx.Err(), "ptc aborted")
			cancelRun(werr)
			vm.Interrupt(werr)
		case <-done:
		}
	})

	// 绑定面与程序内发现入口的挂载（实现见 binding.go）。
	if mres := mountBindings(vm, bindings, runCtx, res, maxCalls, maxBytes, discover, stages, callIt); mres != nil {
		return *mres
	}

	// async IIFE：同步绑定的 await 全部在 RunProgram 的 job queue 自排内落定；
	// return 值即程序结果（undefined→null）。语法已预检，此处只跑编译产物。
	v, err := vm.RunProgram(prog)
	if err != nil {
		return failFrom(*res, err)
	}
	p, ok := v.Export().(*goja.Promise)
	if !ok {
		// 理论不可达（IIFE 恒返回 Promise）；防御性按坏返回处理。
		res.Error, res.ErrorKind = "程序返回值不是 Promise（IIFE 包装失效）", "bad_result"
		return *res
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // 工具输出常含 <>——\u003c 转义是噪音，值面给人看
		if merr := enc.Encode(normalizeValue(p.Result())); merr != nil {
			res.Error, res.ErrorKind = fmt.Sprintf("return 值不可 JSON 序列化: %v", merr), "bad_result"
			return *res
		}
		res.OK = true
		res.Value = strings.TrimSpace(buf.String())
	case goja.PromiseStateRejected:
		if kind, prefix := stageOutcome(res.stageErr); kind != "" {
			res.Error, res.ErrorKind = fmt.Sprintf("%s: %v", prefix, p.Result()), kind
		} else {
			res.Error, res.ErrorKind = fmt.Sprintf("程序拒绝: %v", p.Result()), "runtime"
		}
	default:
		res.Error, res.ErrorKind = "await 永不落定的 Promise（VM 无定时器，绑定均为同步调用）", "pending"
	}
	return *res
}
