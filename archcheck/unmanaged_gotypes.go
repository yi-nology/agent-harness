// unmanaged_gotypes.go —— 未受管逃逸门禁的**类型感知严格档**（issue #103）。
//
// # 为什么有了 AST 追踪还要这一档
//
// unmanaged_assign.go 的三趟 AST 追踪是 go/types 的「等价手段」——轻，但头注
// 自述了盲区：接收方类型**静态不可溯**的赋值（经无类型标注的函数返回值、
// 接口断言、深层容器元素流转）判不到。门禁越严，宿主越会去找「判不到的写法」
// ——盲区不是靠把边界写成文档就能关上的：那等于把「怎么绕过」写成了文档。
//
// 严格档用 go/packages + go/types 做**真类型解析**（可选档，默认关）：
//
//   - 代价：整仓连同依赖做一遍类型检查（比 AST 判据慢一个量级，还要加载
//     依赖的导出数据）。这就是它必须是「可选的严格档」而不是默认档的原因。
//   - 换来：赋值接收方的类型由 types 推导——函数返回值、接口断言、map /
//     slice 元素、别名、defined type 包装，凡 types 解得出来的都判得到。
//
// # 开法与失效形态
//
//   - `cmd/archcheck -types` / `make architecture-check-types`（opt-in，不在
//     check 快速路径——成本原因，见上）。
//   - **类型解析失败必须显式报错**（本文件的纪律与 unmanaged.go 同源：
//     「给不出结论」与「没违规」严格分开）：packages.Load 失败、任何包带
//     类型错误、解析出零个包——一律返回 error（cmd 侧退出码 2，即
//     「门禁自己坏了」，不是「无违规」）。
//   - 与 AST 判据同报一个规则（unmanagedEscapeRuleID），按 (path, line) 去重
//     ——两档都看得见的形态只报一次，报两次会让豁免登记也变成两份。
package archcheck

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"strings"

	"golang.org/x/tools/go/packages"
)

// typeAwareViolations 用 go/types 对 root 全仓类型检查后，判「对持有 ptc
// 逃逸类型的接收方做 AllowUnmanaged 赋值」的形态。
//
// 返回的第二个值是实际类型检查的包数——它是「严格档真的跑了」的凭据：
// RunWithOptions 在开关开着而包数为 0（仓库明明有包目录）时显式报错，
// 「开了却静默没跑」的形态不存在。
func typeAwareViolations(root string) ([]Violation, int, error) {
	cfg := &packages.Config{
		Dir: root,
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedImports | packages.NeedDeps,
		// Tests: false —— 只装非测试变体（`pkg [pkg.test]` 的测试变体不出现在
		// 结果里），与既有判据「只判生产代码」同一口径。
		Tests: false,
		// 显式关 workspace：本机的 go.work（devlink 生成的）只对根仓生效，
		// 对被扫的宿主仓是意外干扰。在 os.Environ() 上追加而不是替换——
		// 换掉整个环境会连 PATH 一起丢，go list 直接跑不起来。
		Env: append(os.Environ(), "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, 0, fmt.Errorf("类型感知档加载失败（门禁给不出结论，不得静默跳过）: %w", err)
	}

	var out []Violation
	var errs []string
	checked := 0
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			errs = append(errs, fmt.Sprintf("%s: %s", pkg.PkgPath, e.Msg))
		}
		if len(pkg.Errors) > 0 {
			continue // 这个包的 TypesInfo 不可信，不判——整体走显式报错路径
		}
		if pkg.TypesInfo == nil || pkg.Types == nil {
			continue
		}
		checked++
		for _, f := range pkg.Syntax {
			out = append(out, typeAwareAssignInFile(pkg, f, root)...)
		}
	}
	if len(errs) > 0 {
		return nil, 0, fmt.Errorf("类型感知档解析失败（给不出结论，不得当作无违规放行；"+
			"修复源码/依赖使 go/packages 可类型检查，或退回默认档）——前几条: %s",
			strings.Join(errs[:min(len(errs), 5)], "; "))
	}
	if checked == 0 {
		return nil, 0, fmt.Errorf("类型感知档未解析到任何包（go/packages 给不出结论，不得当作无违规放行）")
	}
	return out, checked, nil
}

// typeAwareAssignInFile 判一个已类型检查的文件里的逃逸赋值。
//
// 判据与 AST 档同一条纪律：按**赋值形态**判、不按值判（=false 也报——
// 逃逸口的开与关不该由数据流决定）；但接收方类型由 types 推导，AST 档的
// 静态不可溯盲区（返回值 / 接口断言 / 容器元素）在此覆盖。
func typeAwareAssignInFile(pkg *packages.Package, f *ast.File, root string) []Violation {
	var out []Violation
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != unmanagedEscapeField {
				continue
			}
			t := pkg.TypesInfo.TypeOf(sel.X)
			if t == nil {
				continue // types 自己也给不出类型：解析失败面，整体报错路径兜底
			}
			if !holdsEscapeType(t, map[types.Type]bool{}) {
				continue
			}
			out = append(out, Violation{
				Rule: unmanagedEscapeRuleID,
				Path: relTo(root, pkg.Fset.Position(sel.Pos()).Filename),
				Line: pkg.Fset.Position(sel.Pos()).Line,
				Kind: "unmanaged_escape",
				Text: fmt.Sprintf("生产代码通过对 %s 的赋值选择了未受管执行（%s 字段，类型感知档判定）："+
					"这条路径上的工具调用不经过披露、审计与权限判定。"+
					"请接上 invoke.Stage 判定环（与普通工具调用同一套）；"+
					"确需逃逸时在 policy 的 exceptions 里显式登记豁免与拆除条件",
					types.TypeString(t, nil), unmanagedEscapeField),
			})
		}
		return true
	})
	return out
}

// holdsEscapeType 类型 t（或其元素 / 字段 / 底层）是否持有 ptc 的逃逸类型。
//
// 覆盖的形态（每条都是 AST 档的残余盲区或既有判据的 types 化重述）：
//
//   - ptc 的逃逸类型本身与**别名**（alias 在 types 里就是同一个 Named）；
//   - `type T ptc.Options` 这类 defined type 包装（走 Underlying）；
//   - 结构体字段持有（含**嵌入**——字段提升后赋值形态就是 x.AllowUnmanaged）；
//   - 容器（slice / array / map）元素与指针指向的类型。
//
// seen 断循环（递归类型：type Node struct { Next *Node }）。
// 宿主自建的**同名**字段不误报：判定锚在 types 的具名类型上，不是字段名。
func holdsEscapeType(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.(type) {
	case *types.Pointer:
		return holdsEscapeType(u.Elem(), seen)
	case *types.Slice:
		return holdsEscapeType(u.Elem(), seen)
	case *types.Array:
		return holdsEscapeType(u.Elem(), seen)
	case *types.Map:
		return holdsEscapeType(u.Elem(), seen)
	case *types.Named:
		if isEscapeNamed(u) {
			return true
		}
		return holdsEscapeType(u.Underlying(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if holdsEscapeType(u.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

// isEscapeNamed types.Named 是否 ptc 的逃逸类型本体（含别名——alias 不产生
// 新的 Named）。按 import 路径的最后一段（包名）匹配，与既有判据同一口径：
// 别名 import 与 module 路径迁移都不会让判据静默失配。
func isEscapeNamed(n *types.Named) bool {
	obj := n.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return pathLastSegment(obj.Pkg().Path()) == "ptc" && unmanagedEscapeTypes[obj.Name()]
}

// dedupUnmanagedEscape 两档判据同报一个规则（字面量 / AST 赋值 / types 严格档），
// 按 (rule, path, line) 三元组去重——同一行只报一次。报两次的后果不只是噪音：
// 例外按同一三元组豁免，重复违反会让「登记了一条例外却还红着」成为常态。
func dedupUnmanagedEscape(vs []Violation) []Violation {
	seen := map[string]bool{}
	out := vs[:0]
	for _, v := range vs {
		key := exceptionKey(v.Rule, v.Path, v.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}
