// 命令事实面：把「这条命令里出现了什么」交给上游 AST（收编 oagentkit/tools/bashguard）。
//
// # 为什么事实面必须走 AST 而不能走词法
//
// 词法层（shellx.Split）看不见重定向运算符粘词（echo a>b 里的 > 是运算符不是参数）、
// 命令替换（$(…) / 反引号里的调用）、管道后半——而这些恰恰是前缀判据最典型的
// 绕过面：git status > /etc/passwd 的前两个 token 仍是 git status，一条只读规则
// 就放行了任意文件写。手写解析器补这一层必然漏（引号/转义/替换的组合形态太多），
// 而漏的形态是「判据比声明宽」——本包最贵的那种失效。
//
// 故事实面收编上游 bashguard（mvdan.cc/sh 真 AST）：全调用集（含嵌套 $(…)、
// 管道后半、子 shell、多段）与全重定向集都是它的结构化输出（tools#35/#79），
// 本仓不维护第二份 shell 解析。判据装配（前缀规则 / 三态裁决 / 下限）仍归本仓。
package cmdpolicy

import (
	"github.com/oagentkit/tools/bashguard"
	"github.com/oekit/pkg/errors"
)

// ErrUnjudgeable 命令未进 AST 判定（超长 / 解析失败）。
//
// 判不了就不放行：与 Split 的 ErrUnclosedQuote 同一处置口径——
// 报错让调用方按自己的 fail-closed 口径处置，而不是当成「没有命中规则」。
var ErrUnjudgeable = errors.New("cmdpolicy: 命令未进判定（超长/解析失败），判不了就不放行")

// commandFacts 一条命令行的结构化事实（AST 面）。
type commandFacts struct {
	// calls 全调用面 argv（每项含命令名）：管道后半、$(…) 嵌套、子 shell、
	// 多段里的调用全部在此——判定要对**每一次调用**取最严。
	calls [][]string
	// wrote 含写重定向（任一非读侧重定向）。写重定向是语句级事实，
	// 不在 argv 里——前缀规则结构上看不见它。
	wrote bool
	// dynamic 含动态展开（$() / 反引号 / $VAR，含赋值含展开）。
	// 判据只看字面，展开内容在判定时不可知。
	dynamic bool
}

// commandFactsOf 提取命令事实。未进判定（超长 / 解析失败）⇒ ErrUnjudgeable。
//
// 空 Table 只为取事实（AllCalls / Redirs）：bashguard 的只读/不可逆分类
// 是另一套语义（风险分级），本包不用——本包的裁决词表是 allow/prompt/forbidden，
// 由前缀规则给，事实面只负责把「规则看不见的语义」摆到裁决面前。
func commandFactsOf(cmdline string) (commandFacts, error) {
	d := bashguard.Analyze(cmdline, &bashguard.Table{})
	if d.Unevaluated {
		switch {
		case d.OverLength:
			return commandFacts{}, errors.Wrap(ErrUnjudgeable, "命令超长未进判定")
		case d.Unparsed:
			return commandFacts{}, errors.Wrap(ErrUnjudgeable, "命令解析失败")
		default:
			return commandFacts{}, ErrUnjudgeable
		}
	}
	f := commandFacts{}
	for _, inv := range d.AllCalls {
		if inv.Dynamic {
			f.dynamic = true
		}
		if inv.Name == "" {
			// 只有赋值没有命令（FOO=bar）：不构成调用，不进判定面。
			continue
		}
		argv := make([]string, 0, len(inv.Args)+1)
		argv = append(argv, inv.Name)
		argv = append(argv, inv.Args...)
		f.calls = append(f.calls, argv)
	}
	for _, r := range d.Redirs {
		if !r.Read {
			f.wrote = true
		}
		if r.Dynamic {
			f.dynamic = true
		}
	}
	return f, nil
}
