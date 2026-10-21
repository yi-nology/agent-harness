// imports.go —— import 规则门禁（AST 判定，不是逐行正则）。
//
// # 为什么 import 规则不能用正则逐行判
//
// import 声明的书写形态远不止一种，逐行正则必然漏：
//
//	import "errors"                  单行
//	import ("errors")                单行带括号
//	import (                         块
//	    "errors"                      ← 整行就是一个裸路径
//	)
//	import e "errors"                别名
//	import _ "errors"                空白导入
//	import ( e "errors" )            块 + 别名
//
// 旧实现用 `^\s*"errors"$` 这类正则去匹配**整行文本**，于是：
//
//   - 单行写法 `import "errors"` 漏（行首不是引号）
//   - 任何带别名的写法 `stderrors "errors"` 漏（行首是别名不是引号）
//   - 块里的裸路径能匹配上，但只要加了别名就漏
//
// 漏掉的形态不是「少见写法」，而是**本仓自己在用的主流写法**：本仓大量使用
// 单行 import（`import "context"`、`import "strings"`）。也就是说这条规则
// 对相当一部分文件从来没生效过。
//
// 失败形态是门禁**变绿**：违反规则的代码就在仓里，规则却报「无阻断级违反」。
//
// # 判据
//
// forbid_import_regex 的匹配对象是 **import 路径字符串**（不含引号），不是源码行。
// 这个语义本就是字段名的意思（forbid_**import**_regex），旧实现把匹配对象
// 错记成了行文本——是实现偏离了字段名，不是字段名写错了。
//
// 路径匹配同时覆盖：单行/块、别名/空白导入、点导入，以及任何未来新增的书写形态。
// 判据一旦落到路径上，就不再随书写形态漂移。

package archcheck

import (
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// importViolations 按 import 路径判定 forbid_import_regex 规则。
//
// 解析失败必须响亮失败（返回 error → 门禁退出码 2），不得静默跳过该文件：
// 静默跳过等于「该文件对本规则隐形」，而语法坏掉的文件本就编译不过，
// 真正的风险是「门禁在这个文件上给不出任何结论，却显示成通过」。
//
// # 豁免口径：判据面是「被匹配的那段文本」
//
// negative_follow 在两个分支都要生效，且**判据面相同**——都是「正则匹配到的那
// 段文本」：文本分支是源码行，import 分支是 import 路径。
//
// 此前 import 分支**不应用** negative_follow，于是同一个字段在两个分支语义不同：
// policy 作者按字段名的自然用法（import 规则配 import 豁免）写负向前置，实际
// 一条都豁免不掉——**假红**（该放行的 import 照样报违规）。
//
// 豁免锚点写在哪也随判据面而定：import 分支里锚点是**路径片段**，例如
//
//	forbid_import_regex: "example\\.com/demo/internal/(engine|service)"
//	negative_follow:
//	  - anchor: "example.com/demo/internal/engine/"
//	    forbid_follow: statemachine
//
// 即「路径以 engine/ 开头、紧接着是 statemachine 的那条 import 豁免」。
func importViolations(root string, r Rule) ([]Violation, error) {
	re, err := regexp.Compile(r.ForbidImport)
	if err != nil {
		return nil, fmt.Errorf("规则 %s forbid_import_regex 非法: %w", r.ID, err)
	}
	files, err := ruleFiles(root, r)
	if err != nil {
		return nil, err
	}

	var out []Violation
	fset := token.NewFileSet()
	for _, path := range files {
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil, fmt.Errorf("规则 %s 解析 %s 失败（门禁给不出结论，不得静默跳过）: %w", r.ID, path, perr)
		}
		rel, _ := filepath.Rel(root, path)
		for _, im := range f.Imports {
			p, uerr := strconv.Unquote(im.Path.Value)
			if uerr != nil {
				continue
			}
			loc := re.FindStringIndex(p)
			if loc == nil {
				continue
			}
			// import 路径级的**精确**例外（issue #15）。
			//
			// 粒度是「**恰好这条 import 路径**」——不是前缀、不是子串：
			// 若豁免 `a/b` 时连 `a/b/c` 一起放行，那它等价于「放宽整棵子树」，
			// 而上游将来在 `a/b` 下新增一个包就会被**静默放行**
			// （一条被静默放宽的声明等于没有声明）。
			if importExempt(r.ImportExempt, p) {
				continue
			}
			// 豁免判据面 = import 路径（与文本分支同构：都是被匹配的那段文本）。
			if neutralized(p, loc[0], loc[1], r.NegFollow) {
				continue
			}
			out = append(out, Violation{
				Rule: r.ID,
				Path: rel,
				Line: fset.Position(im.Path.Pos()).Line,
				Text: fmt.Sprintf("import %q（禁止的依赖路径）", p),
				Kind: "forbid_import",
			})
		}
	}
	return out, nil
}

// importExempt 判定一条 import 路径是否被**精确**豁免。
//
// # 为什么是「精确」而不是「前缀」
//
// 前缀豁免（`a/b` 豁免 `a/b/...`）等于「放宽整棵子树」——
// 上游将来在 `a/b` 下新增一个包就会被**静默放行**。
// 而一条被静默放宽的声明等于没有声明：宿主的「内核概念面不得渗进业务层」
// 这条不变式就悄悄破了，且**没有任何信号**（门禁绿着）。
//
// 故语义是：豁免表里写 `a/b/c` 只放行 `a/b/c` 这**一条**；
// `a/b/d` 仍被拒（要放行就自己登记）。
//
// # 尾斜杠
//
// 豁免项写成 `a/b/`（带尾斜杠，常见于「这是一个包前缀」的直觉写法）
// 与写成 `a/b`（恰好这条路径）**语义不同**，故不做归一：
// 带尾斜杠的项不匹配任何具体 import 路径 ⇒ 它是**无效的**豁免。
// 这不是 bug 而是刻意的：让写错的人看到「豁免没生效」，
// 而不是让他以为「整棵子树放行了」。
func importExempt(exempt []string, path string) bool {
	for _, e := range exempt {
		if e == path {
			return true
		}
	}
	return false
}

// importExemptPatternChars 例外项里出现这些字符 ⇒ 它**看着像模式**，
// 而匹配口径是「整条路径相等」，该写法不会生效。
//
// 这批字符在 Go import 路径里都不出现，故判它们不会误报真路径。
const importExemptPatternChars = "*?()[]|^${}\\"

// importExemptDefect 判定一条 import_exempt 项是否**形状上就不可能生效**。
//
// 返回空串表示形状没问题；否则返回一句人话，说明它为什么无效。
//
// # 为什么判「形状」而不是判「当前有没有命中」
//
// 「当前没命中」有两种原因：豁免写错了（真问题），或这条豁免登记的是
// 将来才会用到的包（不是问题）。机器判不了这两者的区别——硬判就是误报，
// 而误报会让人学会忽略这条告警，等于把判据关掉。
//
// 「形状」则是**确定性**的：
//
//   - 带尾斜杠的路径不匹配任何具体 import 路径（import 路径不会以 / 结尾）；
//   - 含通配/正则字符的项也不匹配（匹配口径不是正则也不是通配）。
//
// 这两种写法在任何时刻都不可能生效，判它们不误报，且恰好覆盖最常见的两类笔误。
//
// # 为什么这类笔误值得单独判
//
// 例外的语义是「放行这一条」，而笔误出来的效果是「**一条都没放行**」——
// 宿主会看到 import 被拒，却不知道是自己登记的那条豁免没生效。
// 失败形态是**误诊**：报错指向 import，真正的问题在 policy 里。
func importExemptDefect(exempt string) string {
	trimmed := strings.TrimSpace(exempt)
	switch {
	case trimmed == "":
		return "它是空串，不匹配任何 import 路径 ⇒ 该例外无效"
	case strings.HasSuffix(trimmed, "/"):
		return "它带尾斜杠：例外按「整条路径相等」匹配，带尾斜杠不匹配任何具体 import 路径" +
			"（想放行多个包请逐个登记；本仓刻意不做前缀归一）⇒ 该例外无效"
	default:
		if i := strings.IndexAny(trimmed, importExemptPatternChars); i >= 0 {
			return fmt.Sprintf("它含模式字符 %q：例外按「整条路径相等」匹配（不是正则也不是通配），"+
				"该写法不会生效 ⇒ 该例外无效", trimmed[i])
		}
	}
	return ""
}
