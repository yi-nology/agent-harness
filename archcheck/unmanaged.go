// unmanaged.go —— 未受管逃逸门禁：生产代码不得显式选择「无判定环执行」。
//
// # 这条门禁拦的失效形态
//
// PTC 的安全默认是 fail-closed：**装配了工具绑定却没装判定环 ⇒ 拒绝执行**
// （见 ptc.Options.Stages）。但 fail-closed 留了一个**逃逸口**：
// `AllowUnmanaged: true` —— 宿主显式声明「我这一层的工具另有一套管控」。
//
// 逃逸口的通病是**真违规一起被放过**：一旦逃逸口可以在生产代码里随手写，
// 它就从「显式声明」退化成「默认路径」——而默认路径是没人会去看的地方。
//
// 于是本门禁把逃逸口**变成可见的**：生产代码里出现 ptc.Options/Provider/
// SubprocessOptions 的 AllowUnmanaged 字段，即报违规。
//
// # 为什么按「字段出现」判，而不是按「字段值为 true」判
//
// 按值判只能抓住字面量 true；`AllowUnmanaged: cfg.Unmanaged`（配置驱动）
// 与 `AllowUnmanaged: someVar` 都判不出来——而配置驱动恰恰是最该被看见的
// 那种形态（「这个部署可能跑未受管执行」）。故按**字段出现**判：
// 生产代码要表达这个意图，就得先在 policy 里显式登记豁免。
//
// # 这条门禁判不了什么（写明，而不是假装覆盖）
//
//   - 判不了通过**变量赋值**开启逃逸口的情形：`opts.AllowUnmanaged = true`
//     里的 opts 类型要靠 go/types 推导，AST 拿不到。
//   - 判不了宿主**自建**的同名结构体字段（那不是本包的逃逸口）。
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

// unmanagedEscapeRuleID 未受管逃逸门禁的规则 ID。
const unmanagedEscapeRuleID = "harness-ptc-unmanaged-escape"

// unmanagedEscapeTypes 带逃逸口的类型名（限定在 ptc 包内）。
var unmanagedEscapeTypes = map[string]bool{
	"Options":           true,
	"Provider":          true,
	"SubprocessOptions": true,
}

// unmanagedEscapeField 逃逸口字段名。
const unmanagedEscapeField = "AllowUnmanaged"

// unmanagedEscapeViolations 判生产代码里是否显式选择了未受管执行。
func unmanagedEscapeViolations(root string) ([]Violation, error) {
	fset := token.NewFileSet()
	dirs, err := packageDirs(root, nil)
	if err != nil {
		return nil, err
	}
	var out []Violation
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, de := range entries {
			name := de.Name()
			// 只判生产代码：测试夹具里的逃逸口是**用例的一部分**
			//（例如「未受管执行必须被拒」的反向验证），不是生产路径。
			if de.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				continue
			}
			path := filepath.Join(dir, name)
			f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil, fmt.Errorf("解析 %s 失败（门禁给不出结论，不得静默跳过）: %w", path, perr)
			}
			imports := importPathByName(f)
			for _, lit := range compositeLiterals(f) {
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					continue
				}
				imp, ok := imports[ident.Name]
				if !ok {
					continue // 限定符不是 import（是变量）⇒ 类型不明，判据不认
				}
				// 按 import 路径的**最后一段**（包名）匹配，别名不影响命中，
				// module 路径迁移也不会让判据静默失配（与执行路径门禁同一条理由）。
				if pathLastSegment(imp) != "ptc" {
					continue
				}
				if !unmanagedEscapeTypes[sel.Sel.Name] {
					continue
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != unmanagedEscapeField {
						continue
					}
					out = append(out, Violation{
						Rule: unmanagedEscapeRuleID,
						Path: relTo(root, path),
						Line: fset.Position(lit.Pos()).Line,
						Kind: "unmanaged_escape",
						Text: fmt.Sprintf("生产代码显式选择了未受管执行（%s.%s 的 %s 字段）："+
							"这条路径上的工具调用不经过披露、审计与权限判定。"+
							"请接上 invoke.Stage 判定环（与普通工具调用同一套）；"+
							"确需逃逸时在 policy 的 exceptions 里显式登记豁免与拆除条件",
							pathLastSegment(imp), sel.Sel.Name, unmanagedEscapeField),
					})
				}
			}
		}
	}
	return out, nil
}

// compositeLiterals 取一个文件里所有复合字面量。
func compositeLiterals(f *ast.File) []*ast.CompositeLit {
	var out []*ast.CompositeLit
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			out = append(out, lit)
		}
		return true
	})
	return out
}
