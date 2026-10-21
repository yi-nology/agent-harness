// godobject.go —— god object 判据：按**实质方法数**判，而不是按方法总数判。
//
// # 为什么判据要区分「转发壳」与「实质方法」（issue #13）
//
// 方法数超阈值的**前提**是判据自己写明的那句：「该类型同时承担了过多关注点，
// 任何一次改动都要在它的方法全集上评估影响面」。
//
// 这个前提对**退化成 façade 的类型**不成立：把实现全部下沉到子包、自己只做转发的
// façade **承担了零关注点**——它挂在类型上的方法数 ≈ **公共 API 表面规模**，
// 是另一个量。于是同一个数在两种类型上含义完全不同：真 god object 上它是
// 复杂度集中度，façade 上它是 API 表面积。
//
// 旧判据把两者当同一件事，失败形态是**逼维护者做坏事**：一个已经拆得很干净的
// façade 撞上阈值时，只有两个选择——把子簇合回去（**撤销一次正确的重构**），
// 或调高阈值（**等于放弃判据**）。这是「假红」最坏的一种形态：报错不准，
// 而且**报错把正确的工作判成错的**。判据对最该被表扬的宿主最不友好。
//
// 修法：阈值只算**实质方法数** = 方法数 − 转发壳数。
// 但**原始信号不丢**——报告里同时给出方法总数与转发壳数，维护者仍能看到
// 「这个类型的 API 表面有多大」，只是不再被告知那是复杂度集中。
//
// # 为什么必须走 go/ast 而不是逐行正则
//
// 「一个函数体是不是只有一条转发语句」是**结构性**问题，只有语法树能判。
// 逐行正则拿到的是「某行长得像方法定义」，**拿不到函数体**——它分不出壳与实质。
//
// 本仓其余结构判定走的是同一条路：门包面（facade.go）、依赖方向（deps.go）、
// import 路径（imports.go）。理由一致——结构性的东西不该用正则逐行看文本。
//
// # 与「反向验证」纪律配套
//
// 改这条判定逻辑时，必须同时给「坏实现会红」的用例：本文件里的对照组
// （真 god object：方法体全是实质逻辑）必须仍然判红；而 façade 组
// （方法体全是单条转发）必须判绿。两条都绿或都红，说明判据没在判事。

package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// defaultForwardingShellMaxChars 是「转发壳」的方法体长度上限（字符）的缺省值。
//
// 刻度依据（issue #13 的实测样本）：一个 45 方法的纯 façade 里 39 个方法体
// ≤140 字符、只有 4 个超过 400 字符；而三个真 god object 对照组
// （47 / 46 / 42 方法）的转发壳占比是 **0%**——即 140 这个刻度能把
// 「拆干净的 façade」与「真复杂」分开，且**不改变对照组的判定**。
const defaultForwardingShellMaxChars = 140

// typeMethodCount 汇总一个类型的方法数，并区分转发壳与实质方法。
type typeMethodCount struct {
	total      int
	forwarding int
}

// substantive 返回实质方法数——god object 阈值比的就是它。
func (c typeMethodCount) substantive() int { return c.total - c.forwarding }

// godObjects 统计「同一类型挂了多少个**实质**方法」——god object 的机械判据。
//
// 为什么需要它：一个大类型的方法数是**可自动发现**的结构债，而「大文件」不是
// （1026 行的装配函数与 1026 行的纯声明文件，性质完全不同）。方法数超阈值意味着
// 该类型同时承担了过多关注点，任何一次改动都要在它的方法全集上评估影响面。
//
// 判据：形如 `func (recv *T)` / `func (recv T)` 的方法定义，按接收者基类型名聚合。
// 只看方法定义，排除函数调用与接口声明。
//
// 聚合键是**包路径 + 类型名**，不是裸类型名。
//
// 为什么必须带包路径：同名类型在不同包里是完全无关的两个类型，而方法数超阈值
// 讲的是「这一个类型承担了过多关注点」——把两个包的同名类型并成一个数，判出的
// 是「这两个类型加起来很大」，那不是 god object，是巧合。实测本仓就有多处同名
// 类型分处不同包（各包的测试接收器 rec、两个包的 Report），一旦阈值调低，
// 误报会先于真报出现——而误报是门禁最致命的失败形态：维护者学会忽略它。
//
// shellLimit 是「转发壳」的方法体长度上限（字符）。传 0 = 关闭该区分，
// 此时所有方法都算实质方法，判定退回到 2026-10-20 之前的行为。
//
// 返回 error 是因为**解析失败必须响亮**：某个 .go 文件解析不了，它的方法数
// 就没被计入，那个类型的方法数被低估 → 门禁对它隐形。这正是「门禁变绿」
// 最贵的形态（与 policy 解析失败同一条纪律：门禁给不出结论时不能装作通过）。
func godObjects(root string, scanRoots []string, limit, shellLimit int) ([]string, error) {
	counts := map[string]*typeMethodCount{}
	var parseErrs []string

	for _, sr := range scanRoots {
		p := filepath.Join(root, sr)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		_ = filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			// 与 Go 工具链同口径：忽略 `.` / `_` 前缀文件（工具链不编译它们，
			// 门禁也不该为其统计——否则残留文件会虚增 god object 方法数）。
			if name := info.Name(); strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			pkg := filepath.Dir(rel)

			fset := token.NewFileSet()
			file, perr := parser.ParseFile(fset, path, src, parser.ParseComments)
			if perr != nil {
				parseErrs = append(parseErrs, fmt.Sprintf("%s: %v", rel, perr))
				return nil
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil {
					continue // 普通函数，不是方法
				}
				recv := recvFullTypeName(fn.Recv)
				if recv == "" {
					continue
				}
				key := pkg + "." + recv
				c := counts[key]
				if c == nil {
					c = &typeMethodCount{}
					counts[key] = c
				}
				c.total++
				if isForwardingShell(fn.Body, effectiveBodyLen(fset, src, fn.Body), shellLimit) {
					c.forwarding++
				}
			}
			return nil
		})
	}

	if len(parseErrs) > 0 {
		return nil, fmt.Errorf("god object 判据无法解析以下文件，其方法数未被计入（门禁对该文件隐形）：\n    %s",
			strings.Join(parseErrs, "\n    "))
	}

	var out []string
	for name, c := range counts {
		sub := c.substantive()
		if sub > limit {
			out = append(out, fmt.Sprintf("%s (%d 个方法 = %d 转发壳 + %d 实质，实质 %d > %d)",
				name, c.total, c.forwarding, sub, sub, limit))
		}
	}
	sort.Strings(out)
	return out, nil
}

// recvTypeName 取接收者的基类型名。
//
// 形态：*Scheduler / Scheduler / Manager[T] / Manager[K, V]。
//
// 变量名不要——`(s *Scheduler)` 里的 `s` 是变量名不是类型名，取错了就是
// 「把两个类型并成一个数」，那不是 god object，是巧合。
//
// 多接收者（`func (a, b *T) M()`）按**类型**取，不按变量名取。
func recvTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return baseTypeName(recv.List[0].Type)
}

// recvFullTypeName 取接收者的**完整**类型名（保留泛型实参）。
//
// # 为什么要有它（与 baseTypeName 的区别）
//
// `baseTypeName` 剥掉泛型实参：`Manager[int]` 与 `Manager[string]` 都变成
// `Manager`。但按 Go 规范，**按实例特化的方法只属于该实例化**
// （`func (m *Manager[int]) C()` 是 `Manager[int]` 的方法，不是 `Manager` 的）。
//
// 于是剥掉实参会把**三个不同的类型**并成一个计数——两个实例化各挂 20 个
// 特化方法就被判成「Manager 有 40 个方法」，而它其实不胖。**方向是虚高**
// ⇒ 假红（issue #14）。
//
// # 形态
//
//   - 非泛型：与 baseTypeName **逐字相同**（`Plain` → `Plain`）——
//     这条必须成立，否则这条变更会波及所有宿主，而不只是泛型用户；
//   - 单实参：`Manager[int]`；
//   - 多实参：`Manager[K, V]`（实参各自的形态保留，如 `map[string]int`）。
//
// 报告里因此会出现方括号（`pa.Manager[int]`）——这是**刻意的**：
// 它让「哪个实例化胖」一眼可见，而不是被并成一个假象。
func recvFullTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	return fullTypeName(recv.List[0].Type)
}

// fullTypeName 保留泛型实参的类型名。
func fullTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return fullTypeName(t.X)
	case *ast.IndexExpr: // Manager[T]
		return fullTypeName(t.X) + "[" + fullTypeName(t.Index) + "]"
	case *ast.IndexListExpr: // Manager[K, V]
		return fullTypeName(t.X) + "[" + fullTypeName(t.Indices[0]) + ", " + fullTypeName(t.Indices[1]) + "]"
	case *ast.SelectorExpr: // pkg.T —— 接收者不可能是跨包的，形态上兜住
		return t.Sel.Name
	case *ast.ArrayType: // [N]T
		return "[]" + fullTypeName(t.Elt)
	case *ast.MapType: // map[K]V
		return "map[" + fullTypeName(t.Key) + "]" + fullTypeName(t.Value)
	}
	return ""
}

// baseTypeName 剥掉指针与泛型实参，取基类型名。
//
// 实现是「先取完整名，再剥实参」——**一条真相**：
// 两套独立的 switch 迟早会漂移（新增一种形态时改了一处忘了另一处），
// 而漂移的后果是「同一类型在两个判据里算成两个」或「并成一个」。
//
// 剥实参的实现：完整名里的方括号及其内容整体去掉。
func baseTypeName(expr ast.Expr) string {
	full := fullTypeName(expr)
	if i := strings.IndexByte(full, '['); i >= 0 {
		return full[:i]
	}
	return full
}

// isForwardingShell 判一个方法是不是「转发壳 / 访问器」。
//
// 判据两条，缺一不可：
//
//  1. 方法体**只有一条语句**，且该语句是转发形态（一次调用 / 一次返回转发值）；
//  2. 剥掉注释与空白后的方法体长度 ≤ shellLimit。
//
// 为什么两条都要：只看长度，会把「短但多语句」的实质方法误判成壳；
// 只看形态，会把「长但单条调用」的实质方法误判成壳。两种误判的方向相同——
// 把实质方法算成壳 → 真 god object 漏网。故形态判据在前：**宁可多算实质方法
// （假红），不可少算（假绿）**。
//
// shellLimit ≤ 0 时一律返回 false（关闭该区分）：此时「方法体长度 ≤ 0」不可达，
// 任何含调用的方法体都长于 0，于是**没有方法会被算成壳**——判定逐字退回旧行为。
func isForwardingShell(body *ast.BlockStmt, effLen, shellLimit int) bool {
	if body == nil || shellLimit <= 0 {
		return false
	}
	if effLen > shellLimit {
		return false
	}
	if len(body.List) != 1 {
		return false // 多语句 = 有实质逻辑
	}
	switch s := body.List[0].(type) {
	case *ast.ReturnStmt:
		if len(s.Results) != 1 {
			return false
		}
		return isForwardValue(s.Results[0])
	case *ast.ExprStmt:
		_, ok := s.X.(*ast.CallExpr)
		return ok
	}
	return false
}

// isForwardValue 判一个返回值表达式是不是「转发」形态。
//
// 接受的形态：一次调用（`return s.impl.Get()`）、一次取值（`return s.name`）、
// 以及它们的括号包裹 / 取地址 / 解引用。
//
// **不接受**复合字面量（`return Info{Name: x}`）——那是在**构造**东西，
// 是实质逻辑不是转发。构造一个结构体不该被算成「只是转发」。
func isForwardValue(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.CallExpr:
		return true
	case *ast.SelectorExpr:
		return true
	case *ast.Ident:
		return true
	case *ast.ParenExpr:
		return isForwardValue(x.X)
	case *ast.UnaryExpr:
		return isForwardValue(x.X)
	case *ast.StarExpr:
		return isForwardValue(x.X)
	}
	return false
}

// effectiveBodyLen 量方法体的「有效长度」：剥掉注释与空白后的字符数。
//
// 为什么不用「原始行数」或「原始字节数」：一行 200 字符的注释不该让一个方法
// 看起来很重，而三行密集的转发语句也不该被算轻。判据问的是「这个方法体里
// 真正有多少内容」。
//
// 走 token 扫描而不是正则：注释与字符串里都可能含空白与分号，逐行正则分不清
// 「代码里的分号」和「字符串里的分号」。
func effectiveBodyLen(fset *token.FileSet, src []byte, body *ast.BlockStmt) int {
	if body == nil {
		return 0
	}
	start := fset.Position(body.Lbrace).Offset
	end := fset.Position(body.Rbrace).Offset + 1
	if start < 0 || end > len(src) || start >= end {
		return 0
	}
	seg := src[start:end]
	sub := token.NewFileSet()
	tf := sub.AddFile("body", sub.Base(), len(seg))
	var s scanner.Scanner
	s.Init(tf, seg, nil, scanner.ScanComments)
	total := 0
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.COMMENT:
			continue // 注释不是内容
		case token.SEMICOLON:
			continue // 换行自动插入的分号不是内容
		}
		if lit != "" {
			total += len(lit)
			continue
		}
		total += len(tok.String())
	}
	return total
}
