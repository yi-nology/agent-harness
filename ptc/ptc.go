// Package ptc 程序化工具编排运行时（批三百零五，deepseek-harness ptc-runtime 对标 P1
// 试点，设计档 docs/PTC_RUNTIME_DESIGN_20261011.md）：模型写一段程序，进程内 goja VM
// 执行，程序内经 bindings 调用宿主工具，只把归并后的小结果带回上下文——N 轮 ReAct
// 往返塌缩成 1 轮，原始数据不过模型面。
//
// 与 dsh 的取舍（spec §2）：goja 进程内 VM（纯 Go 零 CGO，无 Node 分发面）/保守 JS
// 子集（无 fetch/fs/require，能力只来自 bindings）/VM 边界+三重预算替代 OS 沙盘：
//
//   - 墙钟 deadline（vm.Interrupt 中断运行中的代码）；
//   - 绑定调用次数上限（MaxCalls）；
//   - 绑定输出累计字节上限（MaxBytes）。
//
// 契约（dsh 同款）：程序作为 async 函数体执行（顶层 await/return 可用），完成值即
// 结果且必须 JSON 可序列化；**失败是结果字段不是异常**（Result.Error/ErrorKind，
// kind 含 abort 语义）——报告失败是调用方的事，不是崩溃路径。沙盘事实（预算余量/
// 中断原因/调用清单）与程序成败分开陈述——程序跑通不等于限制都被执行了。
//
// 返回值超限不在本包治理：run_program 是装饰链一员，budgetTool 输出预算+spill 存档
// +read_spill 取回同一套预算语义自动生效（spec §2「不另造面」）。
package ptc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"
)

// 三预算缺省（conf ptc.* 覆盖；run_program 构造时注入）。
const (
	DefaultTimeout  = 30 * time.Second
	DefaultMaxCalls = 50
	DefaultMaxBytes = 256 << 10 // 256KB：程序可归并的原始数据合理上限（超限=该缩小采集）
)

// 预算/中断哨兵（vm.Interrupt 载荷；结果分类用 errors.Is）。
var (
	errDeadline   = errors.New("ptc deadline exceeded")
	errBudgetCall = errors.New("ptc budget: binding calls exceeded")
	errBudgetByte = errors.New("ptc budget: binding output bytes exceeded")
)

// Binding 一个宿主函数绑定（工具即绑定）：Call 收程序传来的 JSON 参数文本，回工具
// 输出原文（不截断——与模型直读同一信任级；注入嫌疑扫描在装饰链上照常生效）。
type Binding struct {
	Name string
	Call func(ctx context.Context, argsJSON string) (string, error)
}

// Options 三预算闸（≤0=各自缺省）。
type Options struct {
	Timeout  time.Duration
	MaxCalls int
	MaxBytes int
}

// CallRecord 一次绑定调用记账（ptc_run 明细事件与审计的账目面：程序把大输出「洗」成
// 小结果时，调用与返回字节数在此不缺）。
type CallRecord struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok|error
	Bytes  int    `json:"bytes"`
	MS     int64  `json:"ms"`
	Error  string `json:"error,omitempty"`
}

// Result 执行结果：失败是结果字段不是异常。ErrorKind：
// ""=成功 / deadline / budget_calls / budget_bytes / runtime（JS 异常或拒绝）/
// pending（await 永不落定——VM 无定时器，此类程序永不完成）/ bad_result（return 值
// 不可 JSON 序列化）/ syntax（程序体语法错误，预检期即拒，带 goja 行号）/
// binding_collision（绑定撞名，见 findBindingCollision）。
//
// Location 与 Suggestions 是给**模型**读的诊断面：调用方是模型，只回一个类别串
// 对它几乎不可用——它不知道该改哪一行，也不知道该改成什么。详见 diagnostic.go。
type Result struct {
	OK        bool
	Value     string // return 值的 JSON 文本（成功时恒合法 JSON，undefined→"null"）
	Error     string
	ErrorKind string
	// Location 出错位置（1-based 行列）。取不到时为零值——不猜：假的行号会让模型
	// 去改一行根本没问题的代码，比没有行号更坏。
	Location Position
	// Suggestions 面向模型的修复建议，由 ErrorKind 唯一决定（成功时为 nil）。
	Suggestions []string
	Calls       int
	Bytes       int
	CallLog     []CallRecord
}

// findBindingCollision 找绑定面的标识符冲突：工具名经 jsIdent 净化后可能撞名。
//
// # 为什么必须撞名即拒，而不是「后绑覆盖前绑」
//
// jsIdent 把 `-` `.` 等非标识符字符一律映射成 `_`，于是 `get-logs` 与 `get_logs`
// 净化后同为 `get_logs`。若放任覆盖，程序里 `host.get_logs()` 拿到的是**另一个工具**：
// 不是「那个工具调不到」，是**顶替**——模型以为调了 A，实际执行的是 B 的副作用，
// 无报错、无日志、CallLog 里还照常记着 B 的账。
//
// 对只读工具这只是结果不对；对有副作用的工具（重启、删除、写入），这是执行了
// 调用方没打算执行的东西。
//
// 故照「撞名即定义错误」处置：装配期就拒，不进 VM。对应 OpenCode 的
// `Extension "<name>" global "<g>" is already defined`。
//
// 空标识符（工具名全是非标识符字符）不算冲突——那类绑定本就挂不上，见挂载处的跳过。
func findBindingCollision(bindings []Binding) (ident, first, dup string, ok bool) {
	seen := make(map[string]string, len(bindings))
	for _, b := range bindings {
		id := jsIdent(b.Name)
		if id == "" {
			continue
		}
		if prev, hit := seen[id]; hit {
			return id, prev, b.Name, true
		}
		seen[id] = b.Name
	}
	return "", "", "", false
}

// Run 执行一段 PTC 程序：program 作 async 函数体（顶层 await/return 可用）。
// ctx 取消=中断（ErrorKind=runtime 包 ctx 错误语义）；deadline 到点=中断。
// 恒返回 Result 不 panic——失控循环/内存风险由三闸治理（spec §5）。
//
// 绑定调用 deadline 语义（批三百零七，P2）：阻塞中的 Go 绑定调用（如变更类工具的
// 审批等待）vm.Interrupt 打不断——中断驱动 goroutine 先 cancel 运行期 ctx 再
// Interrupt，绑定调用带 runCtx 出去：墙钟到点阻塞调用被 ctx 断掉（CallRecord 留
// error 痕），程序随后被 Interrupt 收口。审批挂起=终止+已执行步骤留痕（spec §4 P2
// 取舍：暂停恢复复杂度不值）。
func Run(ctx context.Context, program string, bindings []Binding, opts Options) (res Result) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxCalls := opts.MaxCalls
	if maxCalls <= 0 {
		maxCalls = DefaultMaxCalls
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	// 建议在唯一的收口处统一补，不在每个 return 分支里各写一遍——分支一多必然
	// 漏一处，而漏掉的后果是「模型收到一条没有修复建议的失败」这种安静退化。
	defer func() {
		if !res.OK {
			res.Suggestions = suggestionsFor(res.ErrorKind)
		}
	}()

	// 绑定面冲突预检：装配期缺陷，先于语法预检暴露——程序还没编译就已经跑不到
	// 「那个工具」了，先报语法错误只会把根因往后推一层。
	if ident, first, dup, ok := findBindingCollision(bindings); ok {
		res.Error = fmt.Sprintf("绑定标识符冲突：工具 %q 与 %q 净化后同为 host.%s，程序无法区分二者，故拒绝执行",
			first, dup, ident)
		res.ErrorKind = "binding_collision"
		return res
	}

	// 语法预检（批三百零七）：先编译后建 VM——语法错误带 goja 行号在 syntax kind 里
	// 如实回包，不烧预算不进 VM（运行期 RunString 的同类错误混在 runtime kind 里
	// 面目不清）。编译产物直接 RunProgram，不二次编译。
	// 包裹源码抽成变量：诊断折算行号必须用与编译**同一份**字符串。
	wrapped := wrapProgram(program)
	prog, cerr := goja.Compile(programSourceName, wrapped, false)
	if cerr != nil {
		res.Location = locationOfSyntax(cerr, wrapLines)
		res.Error, res.ErrorKind = fmt.Sprintf("程序语法错误%s: %v", describePosition(res.Location), cerr), "syntax"
		return res
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

	// host.<tool> 绑定面：三预算闸在包装层（超限=中断收口，不给程序「越限继续跑」）。
	host := vm.NewObject()
	for _, b := range bindings {
		b := b
		fn := func(call goja.FunctionCall) goja.Value {
			if res.Calls >= maxCalls {
				vm.Interrupt(errBudgetCall)
				return goja.Undefined()
			}
			args, aerr := argString(call.Argument(0))
			if aerr != nil {
				panic(vm.NewTypeError("绑定参数须为 JSON 文本或可序列化对象: %v", aerr))
			}
			start := time.Now()
			out, err := b.Call(runCtx, args) // runCtx：墙钟到点断阻塞调用（批三百零七）
			rec := CallRecord{Name: b.Name, Bytes: len(out), MS: time.Since(start).Milliseconds()}
			res.Calls++
			res.Bytes += len(out)
			if err != nil {
				rec.Status = "error"
				rec.Error = err.Error()
			} else {
				rec.Status = "ok"
			}
			res.CallLog = append(res.CallLog, rec)
			// 字节闸必须盖住**错误路径**：报错调用的 out 同样记了账，而错误分支
			// 紧接着就 panic 出去——判定若留在其后等于没有闸。程序 try/catch 掉
			// 每次报错，字节就能无限攒下去，只剩 MaxCalls 还拦着。
			if res.Bytes > maxBytes {
				vm.Interrupt(errBudgetByte)
				return goja.Undefined()
			}
			if err != nil {
				panic(vm.NewGoError(err)) // 程序可 try/catch；未捕获=拒绝落 Result
			}
			return vm.ToValue(out)
		}
		// 工具名→JS 标识符（-/. 等归 _；数字开头加 _ 前缀；空名跳过）。
		// 撞名已在装配期由 findBindingCollision 拒掉，这里不可能再撞——
		// 但仍不丢弃 Set 的错误：goja 的 Set 在只读属性等情形下会返回 error，
		// `_ =` 扔掉它就等于让「绑定没挂上」这件事没有任何信号。
		name := jsIdent(b.Name)
		if name == "" {
			continue
		}
		if err := host.Set(name, fn); err != nil {
			return Result{
				Error:     fmt.Sprintf("绑定 %q 挂载失败: %v", b.Name, err),
				ErrorKind: "runtime",
			}
		}
	}
	_ = vm.Set("host", host)

	// async IIFE：同步绑定的 await 全部在 RunProgram 的 job queue 自排内落定；
	// return 值即程序结果（undefined→null）。语法已预检，此处只跑编译产物。
	v, err := vm.RunProgram(prog)
	if err != nil {
		return failFrom(res, err)
	}
	p, ok := v.Export().(*goja.Promise)
	if !ok {
		// 理论不可达（IIFE 恒返回 Promise）；防御性按坏返回处理。
		res.Error, res.ErrorKind = "程序返回值不是 Promise（IIFE 包装失效）", "bad_result"
		return res
	}
	switch p.State() {
	case goja.PromiseStateFulfilled:
		var buf strings.Builder
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // 工具输出常含 <>——\u003c 转义是噪音，值面给人看
		if merr := enc.Encode(normalizeValue(p.Result())); merr != nil {
			res.Error, res.ErrorKind = fmt.Sprintf("return 值不可 JSON 序列化: %v", merr), "bad_result"
			return res
		}
		res.OK = true
		res.Value = strings.TrimSpace(buf.String())
	case goja.PromiseStateRejected:
		res.Error, res.ErrorKind = fmt.Sprintf("程序拒绝: %v", p.Result()), "runtime"
	default:
		res.Error, res.ErrorKind = "await 永不落定的 Promise（VM 无定时器，绑定均为同步调用）", "pending"
	}
	return res
}

// failFrom 执行期错误分类：中断哨兵→预算/超时 kind；ctx 取消→runtime（带原文）；
// 其余 JS 异常→runtime，并尽力带上程序内位置。
func failFrom(res Result, err error) Result {
	switch {
	case errors.Is(err, errDeadline):
		res.Error, res.ErrorKind = errDeadline.Error(), "deadline"
	case errors.Is(err, errBudgetCall):
		res.Error, res.ErrorKind = errBudgetCall.Error(), "budget_calls"
	case errors.Is(err, errBudgetByte):
		res.Error, res.ErrorKind = errBudgetByte.Error(), "budget_bytes"
	default:
		var intErr *goja.InterruptedError
		if errors.As(err, &intErr) {
			res.Error, res.ErrorKind = fmt.Sprintf("执行被中断: %v", intErr.Value()), "runtime"
			return res
		}
		if loc := locationOfException(err, wrapLines); loc.Valid() {
			res.Location = loc
			res.Error = fmt.Sprintf("程序异常%s: %v", describePosition(loc), err)
		} else {
			res.Error = err.Error()
		}
		res.ErrorKind = "runtime"
	}
	return res
}

// argString 绑定参数归一：字符串原样（JSON 文本契约）；对象/数组等 json.Marshal 序列化
// （模型写 host.x({cmd:'uptime'}) 也能通）；undefined/null=空 JSON 对象。
func argString(v goja.Value) (string, error) {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return "{}", nil
	}
	if s, ok := v.Export().(string); ok {
		return s, nil
	}
	b, err := json.Marshal(v.Export())
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// normalizeValue goja 导出值→JSON 友好形态（map[interface{}] 等非规整键统一字符串化
// 交给 json.Marshal 报错面——不可序列化=bad_result 如实回）。
func normalizeValue(v goja.Value) any {
	if v == nil || goja.IsUndefined(v) {
		return nil
	}
	return v.Export()
}

// jsIdent 工具名→合法 JS 标识符（宿主对象方法名）：非法字符归 _，前导非法字符跳过
// （-x-→x_），数字开头补 _（9lives→_9lives）；首字符未起前不写 _（防 -x-→_x_）。
func jsIdent(name string) string {
	var b strings.Builder
	started := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r == '$':
			b.WriteRune(r)
			started = true
		case r >= '0' && r <= '9':
			if !started {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			started = true
		default:
			if started {
				b.WriteByte('_')
			}
		}
	}
	return b.String()
}
