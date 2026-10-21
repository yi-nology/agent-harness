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
//
// # 执行点与守卫的分工
//
// 程序里的 host.<tool>(...) 与普通工具调用**走同一套 invoke.Stage 判定环**
// （见 Options.Stages）。此前两者是分开的，于是本包成了整条链上唯一绕过披露门、
// 审计门的路径——而本包恰恰是对外推荐的那条路，于是「看起来更安全的路径反而最不安全」。
//
// **出站守卫（netguard）不在判定环上，这是有意的。** 判定环判的是「这次调用要不要
// 放行」，netguard 判的是「这个 URL 能不能发出去」，是两个执行点。把 URL 守卫塞进
// 逐调用的判定环，就得去猜「哪个参数字段是 URL」——那是领域知识，内核不该有。
//
// 两者能这么分工，靠的是一条沙箱性质：**程序本身够不着网络**（无 fetch /
// XMLHttpRequest / WebSocket / require / process …），出站只可能发生在宿主绑定的实现
// 里，而那是宿主自己的代码、由宿主自己接 netguard。这条性质由 sandbox_test.go 的
// 用例钉住：谁给 VM 加了 fetch，谁就得重新回答「netguard 该接在哪」。
package ptc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dop251/goja"
	"github.com/oagentkit/guardx/invoke"
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/toolname"
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
//
// Signature 可选，给出后模型按普通函数调用而不必手写 JSON：`get_logs(host, count)`
// 会被挂成 `host.get_logs(host, count)`，ptc 负责把形参打包成 JSON 再交给 Call。
// 为空则退化为 `host.<name>(argsJSON)`，与既有行为逐字一致。详见 signature.go。
type Binding struct {
	Name string
	// Signature JS 调用形态，如 "get_logs(host, count)"。由宿主用自己的工具目录
	// 翻译而来——ptc 不认识任何 schema 类型。
	Signature string
	Call      func(ctx context.Context, argsJSON string) (string, error)
}

// Discovered 程序内发现到的一个工具。
type Discovered struct {
	Name        string `json:"name"`
	Signature   string `json:"signature,omitempty"`
	Description string `json:"description,omitempty"`
}

// Discoverer 程序内的工具发现入口。
//
// # 为什么需要它
//
// 渐进披露（tooldisclose 那种检索换 schema）与程序化执行此前是**两半没接上**的：
// 检索在模型面（下一轮对话里查），程序在程序面（只能用装配时给的那张表）。
// 模型想在程序里换个工具，得退回一轮 ReAct——Code Mode 收益被抵掉一半。
//
// 宿主把自己的检索器接进来，程序内即可 discover("重启")。ptc 不依赖任何目录包：
// 「工具目录长什么样」是宿主的模型，不是内核的。
type Discoverer func(query string) []Discovered

// Options 三预算闸（≤0=各自缺省）。
type Options struct {
	Timeout  time.Duration
	MaxCalls int
	MaxBytes int
	// Discover 可选：暴露程序内工具发现入口 discover(query)。
	// 为 nil 则**根本不暴露**该全局——没装配的东西不该以空壳形式出现在沙箱里，
	// 那会让模型以为「没有别的工具了」而放弃检索。
	Discover Discoverer
	// Stages 每次绑定调用前必过的判定环，与普通工具调用走的是**同一套** Stage。
	//
	// # 为什么这条是必需的而不是可选优化
	//
	// 程序里的 host.<tool>(...) 直达宿主工具。若这里不接判定环，本包就成了
	// 「整条链上唯一绕过所有门禁的路径」：披露门不判、审计门不判、SSRF 出站门不判。
	// 而本包恰恰是对外推荐的那条路（N 轮 ReAct 塌缩成 1 轮、原始数据不过模型面），
	// 于是**看起来更安全的路径反而最不安全**——这正是 OpenCode 2.0 #52333 的形状：
	// 信任边界移动了，沙箱没跟着移动。
	//
	// 为 nil 时不接任何判定，行为与本包引入 Stages 之前逐字一致（不破坏既有调用方），
	// 但那条路径因此**不带门禁**——接入方须自行确认这是有意的。
	Stages []invoke.Stage
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
// binding_collision（绑定撞名，见 findBindingCollision）/
// stage_denied（判定环判定拒绝）/ stage_failed（判定环自身故障，见 stageOutcome）。
//
// stage_denied 与 stage_failed 分开不是分类洁癖：前者是**正常裁决**，程序改不掉它，
// 只能换工具或走审批；后者是**宿主门禁自己坏了**，程序更不该改它。合成一个
// runtime 的话，调用方会去改那段本来没问题的程序，而真正该修的是守卫代码。
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
	// stageErr 本次执行里判定环给出的类型化错误（nil 表示没有）。
	//
	// 刻意不导出、也不进 JSON：它给的是**收口处做分类**用的，不是给调用方读的。
	// JS 拒绝只保留字符串（goja 的 rejection reason 经格式化即失真），类型化判定
	// 结论走不了序列化那条路，只能由绑定闭包在这里夹带出来。
	stageErr error
}

// findBindingCollision 找绑定面的标识符冲突：工具名经 toolname.Normalize 净化后可能撞名。
//
// # 为什么必须撞名即拒，而不是「后绑覆盖前绑」
//
// 工具名经 toolname.Normalize 净化（`-` `.` 等非标识符字符一律映射成 `_`），
// 于是 `get-logs` 与 `get_logs` 净化后同为 `get_logs`。若放任覆盖，程序里 `host.get_logs()` 拿到的是**另一个工具**：
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

// Run 执行一段 PTC 程序：program 作 async 函数体（顶层 await/return 可用）。
// ctx 取消=中断（ErrorKind=runtime 包 ctx 错误语义）；deadline 到点=中断。
// 恒返回 Result 不 panic——失控循环由墙钟闸治理（spec §5）。
//
// # ⚠️ 本函数是**裸执行入口**：它不经 `sandbox` 登记簿
//
// 直调它会**同时**跳过三层强制：
//
//   - **档位下限**——`Registry.Resolve` 选不到满足要求的后端就拒绝；
//   - **预算可兑现性**——兜不住内存的后端不被选中；
//   - **审批门**——`gatecheck.Managed` 那层（将来接上时）。
//
// 声明还在、强制没了，而**门禁全绿**——说谎的不是档位表，是**执行路径**。
//
// 故**生产代码请走登记簿**：
//
//	sandbox.Execute(ctx, reg, 要求的档位, sandbox.Request{Program: program, Budget: b})
//
// 其中后端是 `ptc.Provider`（本包的执行面适配成 `sandbox.Provider` 协议）。
// 本仓的门禁 `harness-sandbox-execution-path` 会把生产代码里直调本函数的
// 调用点报出来（判定面：限定符调用；改 import 别名绕不过去）。
//
// 本函数**仍然公开**：它是「不经登记簿的进程内执行」这个能力本身，
// 测试与一次性脚本可以直接用；只是**生产路径**不该走它。
//
// ⚠️ **内存不受三闸治理**（此处此前写的是「失控循环/内存风险由三闸治理」，是错的）：
// 三闸是墙钟 / 绑定调用数 / 输出字节数，**没有内存闸**；而 goja 的公开 API
// 也没有任何内存限制（只有 SetMaxCallStackSize，那是调用栈深度）。
// 故一个快速分配的程序能在 deadline 到点**之前**把宿主进程撑爆——宿主里还跑着
// 编排引擎与别的环节。兜住它只能把执行搬出宿主进程（跨域执行），
// 设计见 docs/SANDBOX-DESIGN-20261021.md。
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

	// 调用形态预检：同样在装配期。形态串非法却放行，模型侧表现为
	// 「绑定了却怎么调都不对」——最难往形态串上想的那种故障。
	for _, b := range bindings {
		if b.Signature == "" {
			continue
		}
		ps, serr := parseSignature(b.Signature)
		if serr != nil {
			res.Error = fmt.Sprintf("绑定 %q 的调用形态非法: %v", b.Name, serr)
			res.ErrorKind = "binding_signature"
			return res
		}
		// 形态里的函数名必须等于绑定名净化后的标识符。不一致就静默取其中一个的话，
		// 宿主改了形态却没改绑定名（或反之）时，模型调用的名字和实际挂载的名字会
		// 对不上，而两侧都不报错——「怎么调都不对」是最难查的一类故障。
		if id := toolname.Normalize(b.Name); ps.name != id {
			res.Error = fmt.Sprintf("绑定 %q 的调用形态函数名 %q 与其在 host 下的名字 %q 不一致",
				b.Name, ps.name, id)
			res.ErrorKind = "binding_signature"
			return res
		}
	}

	return execute(ctx, program, bindings, timeout, maxCalls, maxBytes, opts.Discover, opts.Stages, localCaller(bindings), &res)
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
