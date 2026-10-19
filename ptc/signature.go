// 类型化绑定：模型按普通函数调用，不必在程序里手写 JSON 文本。
//
// # 解决的问题
//
// 形态一（无 Signature）：模型必须写 `host.get_logs('{"host":"h1","n":3600}')`。
// 形态二（本文件）：模型写 `host.get_logs("h1", 3600)`，ptc 把形参打包成 JSON 交给宿主。
//
// 后者少一层引号转义、少一份重复的参数名、对长参数表尤其明显；同时宿主那侧
// 拿到的是**结构化**的入参，省掉「再解析一次模型手写的 JSON」。
//
// # 为什么 Signature 由宿主给，而不是 ptc 从 schema 生成
//
// 工具的参数模型是**宿主的资产**（tool 实现、目录、描述都在宿主侧）。ptc 若要
// 从 schema 生成调用形态，就得依赖宿主的 schema 类型——那等于把工具目录的形状
// 泄漏进内核。故这里只约定一个极小的形态串（`name(a, b)`），由宿主用自己的
// 目录翻译过来。ptc 侧不认识任何 schema 类型。
//
// # 与形态一并存
//
// Signature 为空的绑定走原路径，行为逐字不变。两者可混用一张表：有的工具适合
// 结构化调用（参数多且固定），有的本来就是自由文本（传一段 query）。

package ptc

import (
	"strings"

	"github.com/dop251/goja"
	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/agent-harness/toolname"
)

// ErrInvalidSignature 形态串非法（装配期缺陷，非程序问题）。
var ErrInvalidSignature = errors.New("绑定调用形态非法")

// param 一个形参。optional 由尾部的 `?` 标记。
type param struct {
	name     string
	optional bool
}

// parsedSignature 解析后的调用形态。
type parsedSignature struct {
	name     string // JS 侧函数名（须等于绑定名净化后的标识符）
	params   []param
	required int // 必填形参个数
}

// parseSignature 解析 `name(a, b)` 形态串。
//
// 刻意只支持这一种最简形态：调用形态是**给人（模型）看的签名**，不是表达式语言。
// 支持默认值、对象解构、剩余参数只会让它变成一个要单独写文档的小语言，而宿主
// 完全可以在 JS 侧用一行闭包覆盖掉这些需求。
func parseSignature(sig string) (parsedSignature, error) {
	s := strings.TrimSpace(sig)
	open := strings.IndexByte(s, '(')
	if open <= 0 || !strings.HasSuffix(s, ")") {
		return parsedSignature{}, errors.Wrap(ErrInvalidSignature, "调用形态应为 name(a, b) 形态", "sig", sig)
	}
	ps := parsedSignature{name: strings.TrimSpace(s[:open])}
	if !toolname.IsJSIdent(ps.name) {
		return parsedSignature{}, errors.Wrap(ErrInvalidSignature, "函数名不是合法 JS 标识符", "sig", sig)
	}
	inner := strings.TrimSpace(s[open+1 : len(s)-1])
	if inner == "" {
		return ps, nil
	}
	for _, raw := range strings.Split(inner, ",") {
		p := strings.TrimSpace(raw)
		optional := strings.HasSuffix(p, "?")
		if optional {
			p = strings.TrimSpace(strings.TrimSuffix(p, "?"))
		}
		if !toolname.IsJSIdent(p) {
			return parsedSignature{}, errors.Wrap(ErrInvalidSignature, "形参名不是合法 JS 标识符", "param", p)
		}
		for _, seen := range ps.params {
			if seen.name == p {
				// 重复形参在 JS 里是合法的（后者遮蔽前者），但在这里意味着宿主
				// 的目录翻译有 bug：打包时后者会覆盖前者，前一个参数静默消失。
				return parsedSignature{}, errors.Wrap(ErrInvalidSignature, "形参重名，打包时后者会覆盖前者", "param", p)
			}
		}
		ps.params = append(ps.params, param{name: p, optional: optional})
	}
	// 可选形参必须排在必填形参**之后**。中间出现可选、后面又有必填时，模型为了
	// 传后面的必填项就得先把前面的空位填上——而那正是「为不填的槽位瞎填值」，
	// 比直接判非法更坏：它看起来能用，填进去的值却是编的。
	// 全必填时 required 就是 len(params)。默认值必须显式写成 len——写成零值的话，
	// 「没有可选形参」这条最常见的路径会落到 required=0，元数下限形同不存在，
	// 少传参数照样放行，而这种形态是绝大多数工具的样子。
	ps.required = len(ps.params)
	for i, p := range ps.params {
		if p.optional {
			ps.required = i
			break
		}
	}
	for i, p := range ps.params {
		if p.optional && i+1 < len(ps.params) && !ps.params[i+1].optional {
			return parsedSignature{}, errors.Wrap(ErrInvalidSignature,
				"可选形参必须排在必填形参之后", "param", p.name)
		}
	}
	return ps, nil
}

// wrapperSource 生成形参打包 + 元数校验的包装函数。
//
// 用「编译一小段常量形状的 JS 再执行」的方式，而不是把生成文本拼进主程序：
// 参数名来自宿主但已过 toolname.IsJSIdent，且**从不**与被编译的主程序源码拼接——
// 这里唯一被编译的是本包自己拼出的固定形状。
//
// 元数校验放在 JS 侧：报错直接指向形参名与实参个数；交给宿主的话，宿主看到的是
// 缺字段的 JSON，报错多半指向内部反序列化，读起来像宿主坏了。
func wrapperSource(ps parsedSignature) string {
	var b strings.Builder
	names := make([]string, len(ps.params))
	for i, p := range ps.params {
		names[i] = p.name
	}
	b.WriteString("(function(__ptcRaw,__ptcName,__ptcLo,__ptcHi){return function(")
	b.WriteString(strings.Join(names, ","))
	b.WriteString("){")
	// 全必填时不打区间（「期望 2 个参数」而不是「期望 2-2 个」）：这条文案是给
	// 模型读的，多余的区间只会让它停下来想「2-2 是什么意思」。
	b.WriteString("if(arguments.length<__ptcLo||arguments.length>__ptcHi){throw new TypeError(" +
		"__ptcName+' 期望 '+(__ptcLo===__ptcHi?__ptcLo:__ptcLo+'-'+__ptcHi)+" +
		"' 个参数，实得 '+arguments.length)}")
	if len(ps.params) == 0 {
		b.WriteString("return __ptcRaw({});};})")
		return b.String()
	}
	// 可选槽位**只在真的给了值时才写入**。写进去一个 undefined 到 JSON 里就是
	// null，宿主收到 {"count":null} 与「没给 count」是两件事：前者可能走进显式的
	// 空值分支，后者才是走默认值——这两条路的后果不一样，不能混。
	b.WriteString("var __ptcO={")
	b.WriteString(ps.params[0].name)
	b.WriteString(":")
	b.WriteString(ps.params[0].name)
	b.WriteString("}")
	for _, p := range ps.params[1:] {
		b.WriteString(";if(")
		b.WriteString(p.name)
		b.WriteString("!==undefined){__ptcO.")
		b.WriteString(p.name)
		b.WriteString("=")
		b.WriteString(p.name)
		b.WriteString("}")
	}
	b.WriteString(";return __ptcRaw(__ptcO);};})")
	return b.String()
}

// compileWrapper 编译并执行包装函数，返回挂到 host 上的 JS 函数值。
//
// 编译的源码只由本包生成（形参名已过 toolname.IsJSIdent），宿主文本不参与拼接。
func compileWrapper(vm *goja.Runtime, src string, raw goja.Value, name string, lo, hi int) (goja.Value, error) {
	fn, err := goja.Compile("", src, false)
	if err != nil {
		return nil, err
	}
	holder, err := vm.RunProgram(fn)
	if err != nil {
		return nil, err
	}
	maker, ok := goja.AssertFunction(holder)
	if !ok {
		return nil, errors.New("调用形态包装器不是可调用值")
	}
	return maker(goja.Undefined(), raw, vm.ToValue(name), vm.ToValue(lo), vm.ToValue(hi))
}

// discoverGlobalName 程序内发现入口的全局名。
const discoverGlobalName = "discover"

// mountDiscover 挂程序内工具发现入口。未装配 Discoverer 时**不挂**：
// 挂一个永远返回空数组的空壳，会让模型以为「没有别的工具了」而放弃检索。
func mountDiscover(vm *goja.Runtime, d Discoverer) error {
	if d == nil {
		return nil
	}
	return vm.Set(discoverGlobalName, func(call goja.FunctionCall) goja.Value {
		q, _ := argString(call.Argument(0))
		found := d(q)
		// 转成 map 再交出去：goja 未配置 FieldNameMapper 时按**字段原名**暴露
		// （Name 而非 name），JS 侧读到的是 undefined——模型会以为没查到，
		// 于是退回逐个试，而那正是本机制要替掉的行为。
		out := make([]map[string]any, 0, len(found))
		for _, t := range found {
			m := map[string]any{"name": t.Name}
			if t.Signature != "" {
				m["signature"] = t.Signature
			}
			if t.Description != "" {
				m["description"] = t.Description
			}
			out = append(out, m)
		}
		return vm.ToValue(out)
	})
}
