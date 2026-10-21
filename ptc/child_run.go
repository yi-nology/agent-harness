package ptc

import (
	"context"
	"fmt"

	"github.com/oagentkit/guardx/wire"
	"github.com/yi-nology/agent-harness/toolname"
)

// runProgramInChild 在子进程里跑一段程序。
//
// 与进程内的 Run 是**同一套**执行面（同一个 execute：语法预检 → 建 VM → 挂绑定
// → 跑 → 收口），差别只有一处：绑定调用走**远程**（父侧执行），而不是本地闭包。
//
// 判定环不在这里——它在**父侧**（宿主工具与判定环都在父侧，子进程里只有一个
// 发请求的桩）。故子进程形态不弱化门禁。
func runProgramInChild(start *wire.Start, call bindingCaller) wire.Result {
	timeout := start.Budget.WallClock
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxCalls := start.Budget.MaxCalls
	if maxCalls <= 0 {
		maxCalls = DefaultMaxCalls
	}
	maxBytes := start.Budget.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	// 绑定清单还原成本地 Binding：只有名字与调用形态，**Call 是远程桩**。
	bindings := bindingsFromStart(start.Bindings)

	// 装配期预检：与进程内同一套（撞名 / 调用形态）。
	//
	// 预检在**子进程**做：子进程拿到的清单是父侧给的完整清单，形态与撞名
	// 在这里就能判出来，不必等第一次调用才暴露。
	if ident, first, dup, ok := findBindingCollision(bindings); ok {
		return resultToWire(Result{
			Error: fmt.Sprintf("绑定标识符冲突：工具 %q 与 %q 净化后同为 host.%s，程序无法区分二者，故拒绝执行",
				first, dup, ident),
			ErrorKind: "binding_collision",
		}, start.ResultID)
	}
	for _, b := range bindings {
		if b.Signature == "" {
			continue
		}
		ps, serr := parseSignature(b.Signature)
		if serr != nil {
			return resultToWire(Result{
				Error:     fmt.Sprintf("绑定 %q 的调用形态非法: %v", b.Name, serr),
				ErrorKind: "binding_signature",
			}, start.ResultID)
		}
		if id := toolname.Normalize(b.Name); ps.name != id {
			return resultToWire(Result{
				Error: fmt.Sprintf("绑定 %q 的调用形态函数名 %s 与其在 host 下的名字 %s 不一致",
					b.Name, ps.name, id),
				ErrorKind: "binding_signature",
			}, start.ResultID)
		}
	}

	// 墙钟：子进程侧的 ctx 期限。
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	res := execute(ctx, start.Program, bindings, timeout, maxCalls, maxBytes, nil, nil, call, &Result{})
	return resultToWire(res, start.ResultID)
}

// bindingsFromStart 把协议形态的绑定清单还原成本地 Binding。
//
// Call 字段留空——子进程里**没有**绑定实现，调用一律走远程桩（call 参数）。
// 但 mountBindings 需要 Binding 的 Name 与 Signature（形态包装要读它）。
func bindingsFromStart(list []wire.StartBinding) []Binding {
	out := make([]Binding, 0, len(list))
	for _, b := range list {
		out = append(out, Binding{Name: b.Name, Signature: b.Signature})
	}
	return out
}

// resultToWire 把本地 Result 转成协议形态。
//
// **只传能跨进程传的字段**：CallLog 与类型化判定结论（stageErr）留在**父侧**
// 重建——父侧是记账与判定的一方（不变量 3），子进程报的账不可信。
func resultToWire(r Result, resultID string) wire.Result {
	out := wire.Result{
		ResultID: resultID,
		OK:       r.OK,
		Value:    r.Value,
		Error:    r.Error,
		Calls:    r.Calls,
		Bytes:    r.Bytes,
	}
	if !r.OK {
		// 失败分类：本地 ErrorKind 映射到 toolfail 的分类。
		out.Kind = kindForErrorKind(r.ErrorKind)
	}
	return out
}
