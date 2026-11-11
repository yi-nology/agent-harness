// unmanaged_assign.go —— 未受管逃逸门禁的**赋值路径**判定（v0.33.0，issue #92）。
//
// # 盲区在哪
//
// unmanaged.go 的字面量判据按「复合字面量里的字段出现」判——它判不出**变量
// 赋值**：`opts.AllowUnmanaged = true`（或 `= cfg.Unmanaged`，配置驱动同理）
// 里没有复合字面量，而接收方 opts 的类型要靠类型信息推导。而「另有一套管控，
// 开关在配置文件里」恰恰是生产代码最常见的写法——盲区等于没有判。
//
// # 判据：AST 级类型追踪（go/types 的等价手段）
//
// 不引 go/types（它要把整仓类型检查一遍，且要加载依赖的导出数据——门禁的
// 成本与脆弱面都会失控）。等价手段是**三趟 AST 追踪**，覆盖静态可辨的形态：
//
//  1. **类型层**：谁**是**逃逸类型（`type T ptc.Options`），谁的字段**带着**
//     逃逸类型（`type Cfg struct { O ptc.Options }`；`type Cfg struct { ptc.Options }`
//     嵌入同判——嵌入把字段提升到了外层）；
//  2. **变量层**：谁持着它们——带类型的 var 声明 / 短声明（含取址、切片与
//     map 容器）/ 函数参数、结果与接收者；
//  3. **赋值层**：对持有着逃逸类型的变量做 `….AllowUnmanaged = …` 赋值即报。
//
// 按赋值路径判、**不按值判**（与字面量判据同一条纪律）：`opts.AllowUnmanaged =
// cfg.X` 与 `= false` 都报——逃逸口的开与关不该由数据流决定，生产代码要表达
// 这个意图就得先在 policy 里登记豁免。
//
// # 这条判据判不了什么（写明，而不是假装覆盖）
//
//   - 接收方类型**静态不可溯**的形态：从无类型标注的返回值 / 接口 / 深层容器
//     元素里拿到的值。这类赋值不报——但**宿主自建**的同名字段也因此不误报，
//     这是类型感知换来的「报得准」。
//   - 宿主自建的同名结构体字段：那不是本包的逃逸口（unmanaged.go 头注同款）。
//     类型追踪正是为了把两者分开：字段名一样、类型不同，判据只认 ptc 的类型。
//
// 反向验证见 unmanaged_test.go：注入赋值与配置驱动形态 ⇒ 红；宿主自建同名字段
// ⇒ 不误伤。
package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// unmanagedAssignViolations 判生产代码里「对持有着 ptc 逃逸类型的变量做
// AllowUnmanaged 赋值」的形态。与字面量判据同根（unmanagedEscapeRuleID），
// 报的是同一类违规。
func unmanagedAssignViolations(root string) ([]Violation, error) {
	dirs, err := packageDirs(root, nil)
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, dir := range dirs {
		vs, err := unmanagedAssignInPackage(dir, root)
		if err != nil {
			return nil, err
		}
		out = append(out, vs...)
	}
	return out, nil
}

// unmanagedAssignInPackage 对一个包目录跑三趟追踪。
func unmanagedAssignInPackage(dir, root string) ([]Violation, error) {
	fset := token.NewFileSet()
	type pf struct {
		file *ast.File
		path string
	}
	var files []pf
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, de := range entries {
		name := de.Name()
		if !isGoFile(name) || isTestFile(name) || hasUnderscoreDotPrefix(name) {
			// 与字面量判据同一口径：只判生产代码（测试夹具的逃逸口是用例的一部分）。
			continue
		}
		path := filepath.Join(dir, name)
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil, fmt.Errorf("解析 %s 失败（门禁给不出结论，不得静默跳过）: %w", path, perr)
		}
		files = append(files, pf{file: f, path: path})
	}
	if len(files) == 0 {
		return nil, nil
	}

	// 第一趟：类型层——谁是 / 谁带着 ptc 的逃逸类型。
	structs := map[string]*escapeVar{}
	for _, pf := range files {
		tr := &escapeTracker{imports: importPathByName(pf.file), structs: structs}
		for _, d := range pf.file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				switch t := ts.Type.(type) {
				case *ast.SelectorExpr: // type T ptc.Options
					if tr.selectorIsEscape(t) {
						structs[ts.Name.Name] = &escapeVar{kind: escapeDirect, typeName: ts.Name.Name}
					}
				case *ast.StructType:
					if ev := tr.structEscapeFields(t, ts.Name.Name); ev != nil {
						structs[ts.Name.Name] = ev
					}
				}
			}
		}
	}

	// 第二趟：变量层——包级 var 收齐（跨文件赋值不漏）。
	pkgVars := map[string]escapeVar{}
	for _, pf := range files {
		tr := &escapeTracker{imports: importPathByName(pf.file), structs: structs}
		tr.collectVars(pf.file.Decls, pkgVars)
	}

	// 第三趟：赋值层——逐文件判。
	var out []Violation
	for _, pf := range files {
		tr := &escapeTracker{imports: importPathByName(pf.file), structs: structs}
		out = append(out, tr.assignViolations(fset, pf.path, root, pf.file, pkgVars)...)
	}
	return out, nil
}

// collectVars 收集一段声明里的变量形态（包级与函数级共用）。
func (tr *escapeTracker) collectVars(decls []ast.Decl, vars map[string]escapeVar) {
	for _, d := range decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name == "_" {
					continue
				}
				if k := tr.kindOfType(vs.Type); k.kind != 0 {
					vars[name.Name] = k
					continue
				}
				// 无类型标注时看初值：var x = ptc.Options{…} / &ptc.Options{…} / 容器。
				if i < len(vs.Values) {
					if k, ok := tr.kindOfExpr(vs.Values[i]); ok {
						vars[name.Name] = k
					}
				}
			}
		}
	}
}

// assignViolations 找出一个文件里的逃逸赋值。
func (tr *escapeTracker) assignViolations(fset *token.FileSet, path, root string, f *ast.File, pkgVars map[string]escapeVar) []Violation {
	var out []Violation
	report := func(n ast.Node, recv string) {
		out = append(out, Violation{
			Rule: unmanagedEscapeRuleID,
			Path: relTo(root, path),
			Line: fset.Position(n.Pos()).Line,
			Kind: "unmanaged_escape",
			Text: fmt.Sprintf("生产代码通过对 %s 的赋值选择了未受管执行（%s 字段）：%s。"+
				"请接上 invoke.Stage 判定环（与普通工具调用同一套）；"+
				"确需逃逸时在 policy 的 exceptions 里显式登记豁免与拆除条件。"+
				"注意：经无类型返回值 / 接口 / 深层容器流转的赋值本判据看不到，"+
				"登记豁免时请顺带自查这类路径",
				recv, unmanagedEscapeField,
				"这条路径上的工具调用不经过披露、审计与权限判定"),
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			return true
		}
		// 函数作用域内的变量追踪（函数级平铺：同名遮蔽宁可误报不漏报——
		// 误报走显式豁免，漏报是静默的）。
		vars := map[string]escapeVar{}
		for k, v := range pkgVars {
			vars[k] = v
		}
		trackNames := func(field *ast.Field) {
			k := tr.kindOfType(field.Type)
			if k.kind == 0 {
				return
			}
			for _, nm := range field.Names {
				vars[nm.Name] = k
			}
		}
		if fn.Recv != nil {
			for _, field := range fn.Recv.List {
				trackNames(field)
			}
		}
		if fn.Type.Params != nil {
			for _, field := range fn.Type.Params.List {
				trackNames(field)
			}
		}
		if fn.Type.Results != nil {
			for _, field := range fn.Type.Results.List {
				trackNames(field)
			}
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			switch st := n.(type) {
			case *ast.DeclStmt:
				if gd, ok := st.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
					tr.collectVars([]ast.Decl{gd}, vars)
				}
			case *ast.AssignStmt:
				// 先判这批赋值里有没有逃逸写（对已追踪变量的字段赋值），
				// 再把「定义/赋值逃逸值」的变量收进追踪集——两种先后次序都报。
				for _, lhs := range st.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == unmanagedEscapeField {
						if recv, ok := tr.escapeReceiver(sel.X, vars); ok {
							report(st, recv)
						}
					}
				}
				for i, rhs := range st.Rhs {
					if i >= len(st.Lhs) {
						break
					}
					id, ok := st.Lhs[i].(*ast.Ident)
					if !ok || id.Name == "_" {
						continue
					}
					if k, ok := tr.kindOfExpr(rhs); ok {
						vars[id.Name] = k
					}
				}
			}
			return true
		})
		return false
	})
	return out
}

// escapeReceiver 判一个赋值接收链是否落在追踪的变量上，返回可读的接收方描述。
//
// 三种链形：
//
//	x.AllowUnmanaged    — x 是逃逸类型，或 x 的类型**嵌入**了它（字段提升）
//	x.o.AllowUnmanaged  — x 的类型带着名为 o 的逃逸字段
//	x[…].AllowUnmanaged — x 是逃逸类型的容器
func (tr *escapeTracker) escapeReceiver(x ast.Expr, vars map[string]escapeVar) (string, bool) {
	switch r := x.(type) {
	case *ast.Ident:
		v, ok := vars[r.Name]
		if !ok {
			return "", false
		}
		switch v.kind {
		case escapeDirect:
			return r.Name, true
		case escapeStruct:
			if v.embedded {
				return r.Name + "（嵌入了 " + v.typeName + "）", true
			}
		}
	case *ast.SelectorExpr:
		if base, ok := r.X.(*ast.Ident); ok {
			v, ok := vars[base.Name]
			if ok && v.kind == escapeStruct && v.fields[r.Sel.Name] {
				return base.Name + "." + r.Sel.Name, true
			}
		}
	case *ast.IndexExpr:
		if base, ok := r.X.(*ast.Ident); ok {
			v, ok := vars[base.Name]
			if ok && v.kind == escapeContainer {
				return base.Name + "[…]（逃逸类型的容器）", true
			}
		}
	case *ast.StarExpr:
		// *p.AllowUnmanaged 形态极少见；穿透一层解引用。
		return tr.escapeReceiver(r.X, vars)
	}
	return "", false
}

// 本包结构体字面量、容器字面量（`[]ptc.Options{…}` / `map[K]ptc.Options{…}`）。
func (tr *escapeTracker) kindOfExpr(e ast.Expr) (escapeVar, bool) {
	switch ex := e.(type) {
	case *ast.UnaryExpr:
		if ex.Op == token.AND {
			return tr.kindOfExpr(ex.X)
		}
	case *ast.CompositeLit:
		switch ct := ex.Type.(type) {
		case *ast.SelectorExpr:
			if tr.selectorIsEscape(ct) {
				return escapeVar{kind: escapeDirect}, true
			}
		case *ast.Ident:
			if s, ok := tr.structs[ct.Name]; ok {
				return *s, true
			}
		case *ast.ArrayType:
			if k := tr.kindOfType(ct); k.kind != escapeNone {
				return k, true
			}
		case *ast.MapType:
			if k := tr.kindOfType(ct); k.kind != escapeNone {
				return k, true
			}
		}
	}
	return escapeVar{}, false
}

// 按 import 路径的最后一段（包名）匹配，别名绕不过（与字面量判据同一条理由）。
func (tr *escapeTracker) selectorIsEscape(sel *ast.SelectorExpr) bool {
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	imp, ok := tr.imports[id.Name]
	if !ok || pathLastSegment(imp) != "ptc" {
		return false
	}
	return unmanagedEscapeTypes[sel.Sel.Name]
}

// isGoFile 文件名是否为 Go 源文件。
func isGoFile(name string) bool {
	return strings.HasSuffix(name, ".go")
}

// isTestFile 文件名是否为测试文件。
func isTestFile(name string) bool {
	return strings.HasSuffix(name, "_test.go")
}

// hasUnderscoreDotPrefix go 工具链跳过的文件前缀（与既有判据同款）。
func hasUnderscoreDotPrefix(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}
