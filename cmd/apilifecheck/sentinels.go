// sentinels.go —— 从源码提取错误哨兵与结构化错误类型（issue #104 的数据源）。
//
// 提取判据（刻意保守，只认**声明**，不认使用点）：
//
//   - 哨兵：包级 var，显式 error 类型，或初始化表达式是 errors 包的
//     New/Errorf 调用（含 oekit/pkg/errors——本仓唯一允许的 errors 包）。
//     未导出哨兵一并登记：调用方拿 errors.Is 判包装链时不需要导出符号。
//   - 结构化错误类型：导出类型且带 Error() string 方法（值/指针接收者都算）。
//     只登记导出类型——未导出类型调用方拿不到引用，errors.As 判不了，
//     写进「调用方处置」表是误导。
//
// 只在**生产文件**里找：测试里的临时哨兵不是 API 面。

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Sentinel 一个包级错误哨兵。
type Sentinel struct {
	Pkg  string // 目录（相对仓库根）
	Name string // 变量名
}

// ErrType 一个导出的 error 实现类型。
type ErrType struct {
	Pkg  string
	Name string
}

// isErrorsPath import 路径是否指向 errors 语义包。
// 末段为 errors 的全部命中（stdlib errors、oekit/pkg/errors、pkg/errors）。
func isErrorsPath(p string) bool { return p == "errors" || strings.HasSuffix(p, "/errors") }

// extractAllSentinels 扫全部生产包，返回按（包、名）排序的哨兵与类型清单。
func extractAllSentinels(root string, pkgDirs []string) ([]Sentinel, []ErrType, error) {
	var sents []Sentinel
	var types []ErrType
	seenS := map[Sentinel]bool{}
	seenT := map[ErrType]bool{}
	for _, dir := range pkgDirs {
		files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			return nil, nil, err
		}
		fset := token.NewFileSet()
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, perr := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
			if perr != nil {
				return nil, nil, fmt.Errorf("解析 %s: %w", f, perr)
			}
			for _, s := range packageSentinels(af) {
				s.Pkg = dir
				if !seenS[s] {
					seenS[s] = true
					sents = append(sents, s)
				}
			}
			for _, t := range packageErrorTypes(af) {
				t.Pkg = dir
				if !seenT[t] {
					seenT[t] = true
					types = append(types, t)
				}
			}
		}
	}
	sort.Slice(sents, func(i, j int) bool {
		if sents[i].Pkg != sents[j].Pkg {
			return sents[i].Pkg < sents[j].Pkg
		}
		return sents[i].Name < sents[j].Name
	})
	sort.Slice(types, func(i, j int) bool {
		if types[i].Pkg != types[j].Pkg {
			return types[i].Pkg < types[j].Pkg
		}
		return types[i].Name < types[j].Name
	})
	return sents, types, nil
}

// packageSentinels 从单个文件提取包级哨兵声明。
//
// errors 包的识别按 **import 路径**而不是标识符名：`import e
// "github.com/oekit/pkg/errors"` 的别名形态与裸 `errors` 形态都命中，
// 而普通业务包恰好叫 errors 的误报面为零（路径不匹配）。
func packageSentinels(f *ast.File) []Sentinel {
	var out []Sentinel
	errorsNames := map[string]bool{} // 本文件里指向 errors 语义包的标识符
	for _, imp := range f.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil || !isErrorsPath(p) {
			continue
		}
		if imp.Name != nil {
			errorsNames[imp.Name.Name] = true
		} else {
			errorsNames[pathPkgName(p)] = true
		}
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 {
				continue
			}
			if isSentinelSpec(vs, errorsNames) {
				for _, n := range vs.Names {
					out = append(out, Sentinel{Name: n.Name})
				}
			}
		}
	}
	return out
}

// isSentinelSpec 判一个 var 声明是否哨兵：显式 error 类型，或初始化为
// errors 包 New/Errorf 调用。
func isSentinelSpec(vs *ast.ValueSpec, errorsNames map[string]bool) bool {
	if id, ok := vs.Type.(*ast.Ident); ok && id.Name == "error" {
		return true
	}
	for _, v := range vs.Values {
		call, ok := v.(*ast.CallExpr)
		if !ok {
			continue
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "New" && sel.Sel.Name != "Errorf") {
			continue
		}
		x, ok := sel.X.(*ast.Ident)
		if ok && errorsNames[x.Name] {
			return true
		}
	}
	return false
}

// pathPkgName 从 import 路径取缺省包名（末段）。
// 判据只要求「够用」：errors 语义包的末段就是包名（errors）。
func pathPkgName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// packageErrorTypes 从单个文件提取「导出类型 + Error() string 方法」。
// 接收者指针/值都算；跨文件的方法集不在此处合并——按文件独立判，
// 重复由 extractAllSentinels 的 seenT 去重。
func packageErrorTypes(f *ast.File) []ErrType {
	hasError := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Name.Name != "Error" ||
			fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
			continue
		}
		if res, ok := fd.Type.Results.List[0].Type.(*ast.Ident); !ok || res.Name != "string" {
			continue
		}
		if len(fd.Recv.List) == 0 {
			continue
		}
		name := recvTypeName(fd.Recv.List[0].Type)
		if name != "" {
			hasError[name] = true
		}
	}
	var out []ErrType
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts := spec.(*ast.TypeSpec)
			if !ts.Name.IsExported() || !hasError[ts.Name.Name] {
				continue
			}
			out = append(out, ErrType{Name: ts.Name.Name})
		}
	}
	return out
}

// recvTypeName 取接收者类型的名字（*T / T 都支持，泛型收窄不支持——
// 本仓没有泛型 error 类型，出现了也只是漏登记一个，方向可接受）。
func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // T[P] 泛型实例作接收者
		return recvTypeName(t.X)
	default:
		return ""
	}
}
