// execution.go —— 执行路径门禁：执行必须经 `sandbox` 登记簿。
//
// # 这条门禁拦的失效形态
//
// 上游 `sandbox` 的核心语义是「选不到满足档位的后端即拒绝，**绝不降级**」，
// 且「兜不住预算的后端不被选中」。这两条由 `Registry.Resolve` 保证。
//
// 但**执行路径**可以完全绕开登记簿：宿主直接调后端的裸执行入口
// （本仓的 `ptc.Run`），于是——
//
//   - 「要求了 OS 级隔离」却跑在进程内解释器上；
//   - 「要求了内存兜底」而后端根本兜不住；
//   - 审批门（`gatecheck.Managed`）整层被跳过。
//
// 而**门禁全绿**：档位登记门禁只核对「登记 ↔ 代码事实」，不核对
// 「**实际执行用的后端**是不是登记过的那个」。说谎的不是档位表，是**执行路径**。
//
// # 判据
//
// 扫生产代码里的**限定符调用** `pkg.Fn(...)`：命中**裸执行入口**登记 ⇒ 违规。
//
// # 为什么按「包名 + 符号」匹配，而不是按完整 import 路径
//
//  1. **别名改不掉**：按 import 路径匹配时，宿主 `import p "…/ptc"` 换个别名就
//     绕过了。按 import 路径的**最后一段**（包名）匹配，别名不影响命中。
//  2. **module 路径迁移不会陈旧**：本仓的 module 路径迁移过一次
//     （私有域名 → github.com）。若内置表写死完整 import 路径，迁移那天
//     忘了改门禁 ⇒ 判定**静默失配**（本仓纪律明令禁止写死 module 路径）。
//     按包名匹配则与 module 路径无关。
//
// 需要更精确时，登记项可填 `import_path` 二次收紧（限定到某一个具体模块）。
//
// # 为什么同包调用不需要豁免
//
// 同包内的调用**没有限定符**（`Run(...)` 而不是 `ptc.Run(...)`）⇒ 根本不会被
// 扫到。实现内部的调用（`ptc.Provider.Exec` 调 `Run`）天然不构成绕过。
//
// # 为什么裸入口是**内置表**而不是每个仓各登记一份
//
// 内置表记的是**本仓自己的 API 事实**（`ptc.Run` 不经登记簿）。
// 让每个仓在自己的 policy 里各登记一遍，就是**两份真相**——宿主改了登记、
// 本仓没改，两边各判一套，漂移到没人发现为止（且**没有任何信号**：门禁都绿）。
//
// 故：内置表（本仓的事实，宿主白拿）+ policy 段（宿主自己的事实）**并集**判。
//
// # 这条门禁判不了什么（写明，而不是假装覆盖）
//
// 判不了「持有后端实例直接调 `provider.Exec(...)`」——那要判**接收者的静态类型**，
// 而 AST 拿不到变量的类型（要 `go/types` 才能推导）。
// 门禁不能 import 被判的包，也就不能靠类型信息定位调用点。
// 故：这类绕过**不在判定面内**，靠人工 review 与接线纪律兜。
package archcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// executionPathRuleID 执行路径门禁的规则 ID。
const executionPathRuleID = "harness-sandbox-execution-path"

// builtinRawEntries 本仓自己的裸执行入口。
//
// 「裸」的判据：**不经 `sandbox` 登记簿就能跑起一段程序**。
// `ptc.Run` 正是这样一个入口——它直接跑 goja 程序，不经 `Registry.Resolve`，
// 于是档位下限、预算可兑现性、审批门三层全部被跳过。
//
// 而 `ptc.Provider.Exec` **不是**裸入口：它是 `sandbox.Provider` 协议的实现，
// 正规执行路径是 `sandbox.Execute(ctx, reg, min, req)` → `Resolve` → `Exec`。
var builtinRawEntries = []ExecutionEntry{
	{
		PackageName: "ptc",
		Symbol:      "Run",
		Notes:       "裸执行入口：不经 sandbox 登记簿 ⇒ 档位下限/预算可兑现性/审批门三层全被跳过",
	},
}

// executionPathsDesc 取执行路径规则的说明，policy 未写时用兜底文案。
func executionPathsDesc(cfg ExecutionPaths) string {
	if cfg.Desc != "" {
		return cfg.Desc
	}
	return "执行必须经 sandbox 登记簿（sandbox.Execute）：直接调用裸执行入口会同时跳过档位下限、预算可兑现性与审批门"
}

// executionPathViolations 判定「执行是否经登记簿」。
func executionPathViolations(root string, cfg ExecutionPaths) ([]Violation, error) {
	if !cfg.on() {
		return nil, nil
	}
	raw := mergeExecutionEntries(builtinRawEntries, cfg.RawEntries)
	var out []Violation

	// 登记的裸入口必须**真的存在**——指向已删除或已改名的符号时，登记是陈旧的，
	// 而陈旧的登记会让「这条执行路径没人管」这件事**看起来有人管过**。
	// 与「陈旧包根当天即红」同一条纪律。
	for _, e := range raw {
		dir, ok := packageDirByName(root, e.PackageName)
		if !ok {
			continue // 仓里没有同名包（典型：宿主仓里没有本仓的 ptc 包目录）
		}
		if !symbolDeclared(dir, e.Symbol) {
			out = append(out, Violation{
				Rule: executionPathRuleID, Path: relTo(root, dir), Line: 1,
				Kind: "execution_entry_stale",
				Text: fmt.Sprintf("裸执行入口登记 %s.%s 在仓内的 %s 包里不存在——"+
					"要么该符号被删/改名了（登记须同步），要么那个同名包不是这个入口"+
					"（那说明按包名匹配太宽，请给登记补 import_path 收紧）",
					e.PackageName, e.Symbol, e.PackageName),
			})
		}
	}

	fset := token.NewFileSet()
	dirs := packageDirs(root, ignoredPaths(cfg.IgnoreDirs))
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, de := range entries {
			name := de.Name()
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
			for _, call := range qualifiedCalls(fset, f) {
				imp, ok := imports[call.qualifier]
				if !ok {
					continue // 限定符不是 import（是变量）⇒ 方法调用，判据不认
				}
				for _, e := range raw {
					if !executionEntryMatches(e, imp, call.qualifier, call.sel) {
						continue
					}
					out = append(out, Violation{
						Rule: executionPathRuleID, Path: relTo(root, path), Line: call.line,
						Kind: "execution_bypasses_registry",
						Text: fmt.Sprintf("直接调用裸执行入口 %s.%s：它不经 sandbox 登记簿，"+
							"档位下限、预算可兑现性与审批门三层全部被跳过——"+
							"请改走 sandbox.Execute(ctx, reg, 要求的档位, req)",
							imp, call.sel),
					})
				}
			}
		}
	}
	return out, nil
}

// executionEntryMatches 判定一次限定符调用是否命中某条裸入口登记。
func executionEntryMatches(e ExecutionEntry, impPath, qualifier, symbol string) bool {
	if e.Symbol != symbol {
		return false
	}
	// 填了 import_path ⇒ 必须逐字相等（更精确）。
	if e.ImportPath != "" {
		return e.ImportPath == impPath
	}
	// 没填 ⇒ 按 import 路径的**最后一段**（包名）匹配。
	//
	// 为什么不是按限定符（别名）：别名是调用方随手起的，改个名就绕过了。
	// 为什么不是按完整 import 路径：本仓的 module 路径迁移过一次，写死它会静默失配。
	return pathLastSegment(impPath) == e.PackageName
}

// pathLastSegment 取 import 路径的最后一段（包名）。
func pathLastSegment(imp string) string {
	if i := strings.LastIndex(imp, "/"); i >= 0 {
		return imp[i+1:]
	}
	return imp
}

// mergeExecutionEntries 把内置表与 policy 登记**并集**。
//
// 内置在前：policy 不能把内置条目「注销」——那会让本仓自己的事实从判定面消失。
func mergeExecutionEntries(builtin, declared []ExecutionEntry) []ExecutionEntry {
	out := make([]ExecutionEntry, 0, len(builtin)+len(declared))
	seen := map[string]bool{}
	for _, e := range append(append([]ExecutionEntry{}, builtin...), declared...) {
		key := e.PackageName + "." + e.Symbol + "|" + e.ImportPath
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, e)
	}
	return out
}

// packageDirByName 找出仓内**包名**等于给定名字的包目录。
//
// 按包名（package clause）而不是目录名找：目录名与包名可以不同。
func packageDirByName(root, pkgName string) (string, bool) {
	for _, dir := range packageDirs(root, nil) {
		if packageNameOf(dir) == pkgName {
			return dir, true
		}
	}
	return "", false
}

// packageNameOf 取一个包目录的包名（package clause）。
func packageNameOf(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.PackageClauseOnly)
		if perr != nil || f.Name == nil {
			continue
		}
		return f.Name.Name
	}
	return ""
}

// symbolDeclared 报告某个包里是否声明了这个顶层符号（函数/类型/常量/变量）。
func symbolDeclared(dir, symbol string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if perr != nil {
			continue
		}
		for _, d := range f.Decls {
			switch decl := d.(type) {
			case *ast.FuncDecl:
				if decl.Recv == nil && decl.Name.Name == symbol {
					return true
				}
			case *ast.GenDecl:
				for _, sp := range decl.Specs {
					switch spec := sp.(type) {
					case *ast.TypeSpec:
						if spec.Name.Name == symbol {
							return true
						}
					case *ast.ValueSpec:
						for _, n := range spec.Names {
							if n.Name == symbol {
								return true
							}
						}
					}
				}
			}
		}
	}
	return false
}

// qualifiedCall 一个限定符调用的两个要素。
type qualifiedCall struct {
	qualifier string // 包名或 import 别名
	sel       string // 被调用的符号名
	line      int
}

// qualifiedCalls 取一个文件里所有「限定符调用」（`pkg.Fn(...)`）。
//
// 只认 `X.Sel(...)` 且 X 是**标识符**的形态：X 是包名或 import 别名。
// `x.Sel(...)`（X 是变量）不是限定符调用——那是方法调用，判据不认
// （见文件头「判不了什么」）。
func qualifiedCalls(fset *token.FileSet, f *ast.File) []qualifiedCall {
	var out []qualifiedCall
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		out = append(out, qualifiedCall{
			qualifier: ident.Name,
			sel:       sel.Sel.Name,
			line:      fset.Position(call.Pos()).Line,
		})
		return true
	})
	return out
}

// importPathByName 取一个文件里「包名/别名 → import 路径」的映射。
func importPathByName(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			name = pathLastSegment(path)
		}
		if name == "_" || name == "." {
			continue // 空白导入 / 点导入不是限定符
		}
		out[name] = path
	}
	return out
}
