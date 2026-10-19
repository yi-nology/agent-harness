// facade.go —— 门面包不可变门禁（AST 判定，不是正则逐行看文本）。
//
// 为什么单开一个文件而不是写成 YAML 里的正则规则：正则只能看单行文本，判不出
// 「一个函数体是不是只有一条转发语句」。而「保持门面」这条纪律恰恰是结构性的，
// 结构性的东西该用语法树判。
//
// 为什么需要它：docs/OAGENTKIT-OVERLAP.md 里用散文写了「dispatch / policy /
// reminder 保持门面，别再在本仓加逻辑」——散文不是门禁。现有规则查的全部是「写法」
// （用了什么词、import 了什么、goroutine 怎么起），没有一条查「本仓自己的门面
// 有没有被自己改胖」。真有人往 policy/ 里加一份实现，规则不会响，而结果是与上游
// 并存的第二份实现——那正是 OVERLAP 文档警告的事，却只靠一句人话拦着。
//
// 判据（门面包的顶层声明只允许四种形态）：
//
//	1. 类型别名          type X = upstream.X
//	2. 常量/变量别名      const X = upstream.X（可成块）
//	3. 函数体恰好一条语句，且该语句是对上游的调用：return upstream.F(...) 或 upstream.F(...)
//	4. import 声明，且路径 ∈ upstream ∪ allowed_imports
//
// 任何类型定义、两句及以上的函数体、方法、循环/分支、对非上游包的调用，一律违规。
// 「加真逻辑」三者必居其一（要新类型、要多条语句、或要新依赖），所以这条规则不能
// 被「顺手写两行」绕过去——这是它区别于正则门禁的地方。
//
// 与「反向验证」纪律配套：改这条判定逻辑时，同样要注入违规确认门禁会红，
// 没触发过的规则等于没有规则。

package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// FacadeRule 声明一个门面包：它转发到哪、还允许带哪些签名所需的额外依赖。
type FacadeRule struct {
	Package        string   `yaml:"package"`
	Desc           string   `yaml:"desc"`
	Severity       string   `yaml:"severity"`
	Upstream       []string `yaml:"upstream"`        // 允许作为转发目标的 import 路径
	AllowedImports []string `yaml:"allowed_imports"` // 签名所需的额外 import（不含 upstream）
}

// verSuffixRe 匹配 Go 主版本后缀目录（…/v2），该段不是包名。
var verSuffixRe = regexp.MustCompile(`^v[0-9]+$`)

// facadeViolations 判定一个门面包是否仍然只是门面。
func facadeViolations(root string, fr FacadeRule) ([]Violation, error) {
	dir := filepath.Join(root, fr.Package)
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("门面规则 %s 扫描 %s 失败: %w", fr.Package, fr.Package, err)
	}

	allowed := map[string]bool{}
	for _, p := range append(append([]string{}, fr.Upstream...), fr.AllowedImports...) {
		allowed[p] = true
	}

	var out []Violation
	fset := token.NewFileSet()
	for _, path := range entries {
		// 测试文件允许做别的事（构造违规样例本身就是验证手段），只扫生产代码。
		if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(filepath.Base(path), "_") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("门面规则 %s 解析 %s 失败: %w", fr.Package, path, err)
		}

		aliases := importAliases(f)
		up := map[string]bool{}
		for name, path := range aliases {
			if allowed[path] && containsPath(fr.Upstream, path) {
				up[name] = true
			}
		}

		rel, _ := filepath.Rel(root, path)
		report := func(pos token.Pos, format string, args ...any) {
			out = append(out, Violation{
				Rule: facadeRuleID,
				Path: rel,
				Line: fset.Position(pos).Line,
				Text: fmt.Sprintf(format, args...),
				Kind: "facade",
			})
		}

		// 导入面：路径必须在允许集合内，且不得是 . / _ 形态（那样转发目标无从解析，
		// 门禁也就失去了「转发必须指名道姓」这条判据）。
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if !allowed[p] {
				report(im.Pos(), "import %q 不在允许集合（upstream=%v allowed=%v）", p, fr.Upstream, fr.AllowedImports)
			}
			if im.Name != nil && (im.Name.Name == "." || im.Name.Name == "_") {
				report(im.Pos(), "门面包不得使用 %q 导入：转发目标必须写明是哪个上游包", im.Name.Name)
			}
		}

		for _, d := range f.Decls {
			switch decl := d.(type) {
			case *ast.FuncDecl:
				checkFacadeFunc(decl, up, report)
			case *ast.GenDecl:
				checkFacadeGen(decl, up, report)
			default:
				report(d.Pos(), "门面包出现顶层声明 %T，门面包只允许别名与转发函数", d)
			}
		}
	}
	return out, nil
}

// checkFacadeFunc 判一个函数是否仍是「单条上游转发」。
func checkFacadeFunc(fn *ast.FuncDecl, up map[string]bool, report func(token.Pos, string, ...any)) {
	name := fn.Name.Name
	if fn.Recv != nil {
		report(fn.Pos(), "方法 %s：门面包不得给类型挂方法（会绕开上游的类型演进）", name)
		return
	}
	if fn.Body == nil {
		// 外部实现的汇编函数声明；门面包里没有合法用途，直接判违规。
		report(fn.Pos(), "函数 %s 声明无函数体，门面包不承载实现", name)
		return
	}
	switch len(fn.Body.List) {
	case 1:
	case 0:
		report(fn.Pos(), "函数 %s 函数体为空（空壳不是转发，删掉比留着好）", name)
		return
	default:
		report(fn.Body.List[1].Pos(), "函数 %s 有 %d 条语句，门面包只允许单条上游转发", name, len(fn.Body.List))
		return
	}

	stmt := fn.Body.List[0]
	var call ast.Expr
	switch s := stmt.(type) {
	case *ast.ReturnStmt:
		if len(s.Results) != 1 {
			report(stmt.Pos(), "函数 %s 返回 %d 个值，门面包只允许单条 `return 上游.F(...)`", name, len(s.Results))
			return
		}
		call = s.Results[0]
	case *ast.ExprStmt:
		call = s.X
	default:
		report(stmt.Pos(), "函数 %s 的唯一语句是 %T，门面包只允许 return 转发或上游调用", name, stmt)
		return
	}

	if !isUpstreamCall(call, up) {
		report(stmt.Pos(), "函数 %s 的唯一语句不是对上游包的调用——门面不承载实现", name)
	}
}

// checkFacadeGen 判一组声明是否全是别名。
func checkFacadeGen(decl *ast.GenDecl, up map[string]bool, report func(token.Pos, string, ...any)) {
	// import 块已在导入面单独校验过（路径是否在允许集合、有无 . / _ 形态），
	// 这里再判一遍只会把合法导入报成违规。
	if decl.Tok == token.IMPORT {
		return
	}
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			// 关键在 Assign.IsValid()：type X = Y 是别名，type X struct{...} 是定义。
			if !s.Assign.IsValid() {
				report(s.Pos(), "类型 %s 是**定义**不是别名，门面包不得声明新类型（实现归上游）", s.Name.Name)
				continue
			}
			if !isUpstreamIdent(s.Type, up) {
				report(s.Pos(), "类型别名 %s 未指向声明的上游包", s.Name.Name)
			}
		case *ast.ValueSpec:
			if len(s.Values) == 0 {
				report(s.Pos(), "值声明 %v 无初始值，门面包只允许上游常量别名", valueNames(s))
				continue
			}
			for i, v := range s.Values {
				nm := "?"
				if i < len(s.Names) {
					nm = s.Names[i].Name
				}
				if !isUpstreamIdent(v, up) {
					report(v.Pos(), "常量/变量 %s 未取自上游包，门包只允许别名", nm)
				}
			}
		default:
			report(spec.Pos(), "门面包出现 %T 声明，只允许别名", spec)
		}
	}
}

// isUpstreamCall 判一个表达式是否为「对上游包的调用」。
func isUpstreamCall(e ast.Expr, up map[string]bool) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	return isUpstreamIdent(call.Fun, up)
}

// isUpstreamIdent 判一个表达式是否为「上游包限定名」。
func isUpstreamIdent(e ast.Expr, up map[string]bool) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return up[id.Name]
}

// importAliases 收集「本地名 → import 路径」。
func importAliases(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil {
			continue
		}
		if im.Name != nil {
			if im.Name.Name == "_" || im.Name.Name == "." {
				continue
			}
			out[im.Name.Name] = p
			continue
		}
		out[defaultPkgName(p)] = p
	}
	return out
}

// defaultPkgName 推断未显式取别名时的包名（Go 的取名规则：末段，且跳过 vN 目录）。
func defaultPkgName(p string) string {
	parts := strings.Split(p, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && verSuffixRe.MatchString(last) {
		last = parts[len(parts)-2]
	}
	return last
}

func containsPath(list []string, p string) bool {
	for _, v := range list {
		if v == p {
			return true
		}
	}
	return false
}

func valueNames(s *ast.ValueSpec) []string {
	out := make([]string, 0, len(s.Names))
	for _, n := range s.Names {
		out = append(out, n.Name)
	}
	return out
}
