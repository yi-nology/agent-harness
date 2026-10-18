// 绑定面：把宿主绑定挂到沙箱的 host 对象上，并挂程序内工具发现入口。
//
// 从 ptc.go 拆出：Run 里只剩编排流程（编译 → 建 VM → 挂载 → 执行 → 收口），
// 而「一个工具调用在沙箱里长什么样」——预算闸、调用记账、标识符净化、调用形态包装——
// 是另一件事，单独成文件才读得动。拆分是为了过 max_file_lines，但接缝本身是自然的。

package ptc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// mountBindings 把全部绑定挂到 vm 的 host 对象上，并挂程序内发现入口。
//
// 三预算闸在**包装层**（超限即中断收口，不给程序「越限继续跑」）。返回非 nil 的
// Result 表示挂载失败（装配缺陷，不是程序问题），调用方直接回包。
func mountBindings(
	vm *goja.Runtime,
	bindings []Binding,
	runCtx context.Context,
	res *Result,
	maxCalls, maxBytes int,
	discover Discoverer,
) *Result {
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
		mountable := vm.ToValue(fn)
		if b.Signature != "" {
			// 有调用形态：把「形参 → JSON 对象」的包装交给 JS 侧，
			// 三预算闸仍在本包的 Go 包装层（fn），包装只是再包一层壳。
			ps, _ := parseSignature(b.Signature) // 预检已过，这里不会失败
			w, werr := compileWrapper(vm, wrapperSource(ps), mountable, name, len(ps.params))
			if werr != nil {
				return &Result{
					Error:     fmt.Sprintf("绑定 %q 的调用形态包装失败: %v", b.Name, werr),
					ErrorKind: "binding_signature",
				}
			}
			mountable = w
		}
		if err := host.Set(name, mountable); err != nil {
			return &Result{
				Error:     fmt.Sprintf("绑定 %q 挂载失败: %v", b.Name, err),
				ErrorKind: "runtime",
			}
		}
	}
	_ = vm.Set("host", host)

	// 程序内工具发现：装配了才挂，没装配就不存在这个全局。
	if derr := mountDiscover(vm, discover); derr != nil {
		return &Result{Error: fmt.Sprintf("发现入口挂载失败: %v", derr), ErrorKind: "runtime"}
	}

	return nil
}

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
