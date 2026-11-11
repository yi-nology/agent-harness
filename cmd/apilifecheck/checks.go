// checks.go —— 清单 ↔ 仓库事实的三类集合级判定：
// 覆盖（清单 ↔ 生产包集合）、标记（清单 ↔ 包注释）、替代指向（符号存在性）
// 与门面跟随（follows_upstream ↔ import 面）。

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

// selfModule 本仓 module 路径（replacement 指向的符号必须在本仓内）。
const selfModule = "github.com/yi-nology/goagent-kernel"

// checkCoverage 清单 ↔ 生产包集合，双向判差。
//
// 两个方向都不许差：漏登记（新增包没进清单）与幽灵登记（包已删清单还在）
// 是同一种漂移的两面——前者让新包逍遥于承诺之外，后者让清单写着不存在的 API。
func checkCoverage(m *Manifest, pkgDirs []string) []string {
	var probs []string
	manifestSet := map[string]bool{}
	for _, e := range m.Packages {
		manifestSet[e.Pkg] = true
	}
	actualSet := map[string]bool{}
	for _, d := range pkgDirs {
		actualSet[d] = true
	}

	var missing, ghost []string
	for _, d := range pkgDirs {
		if !manifestSet[d] {
			missing = append(missing, d)
		}
	}
	for _, e := range m.Packages {
		if !actualSet[e.Pkg] {
			ghost = append(ghost, e.Pkg)
		}
	}
	sort.Strings(missing)
	sort.Strings(ghost)
	for _, d := range missing {
		probs = append(probs, d+": 生产包未登记进 "+manifestName+"——新增包必须显式定级，不允许逍遥于承诺之外")
	}
	for _, d := range ghost {
		probs = append(probs, d+": 清单登记的包不存在（或已无生产 .go 文件）——删包要同步删登记")
	}
	return probs
}

// checkMarkers 清单 level ↔ 包注释标记。
//
// 判三条：标记恰好一条；在包注释里（ScanPackage 只认 file.Doc，已保证）；
// 级别与清单一致。三条缺一都是「文档与代码漂移」的一种形态。
func checkMarkers(m *Manifest, scanned map[string]*PkgInfo) []string {
	var probs []string
	for _, e := range m.Packages {
		info, ok := scanned[e.Pkg]
		if !ok {
			continue // 覆盖判定已报，不重复
		}
		switch len(info.MarkerFiles) {
		case 0:
			probs = append(probs, e.Pkg+": 包注释里没有「生命周期: <级别>」标记——在包注释（package doc）里加一行，与清单 level 一致")
		case 1:
			if info.MarkerLevel != e.Level {
				probs = append(probs, fmt.Sprintf("%s: 包注释标记 %s 与清单 level %s 不一致（判据面在 %s）",
					e.Pkg, info.MarkerLevel, e.Level, info.MarkerFiles[0]))
			}
		default:
			probs = append(probs, e.Pkg+": 生命周期标记出现 "+fmt.Sprint(len(info.MarkerFiles))+
				" 次（"+strings.Join(info.MarkerFiles, "、")+"）——一个包只允许一处，多处等于没有真相")
		}
	}
	return probs
}

// checkReplacements Deprecated 条目的替代指向必须真实存在。
//
// 判据：完整符号路径（selfModule/<pkg>.<Symbol>），包目录存在且
// 生产代码里有同名导出声明（func/type/var/const）。指到不存在的符号
// 等于把调用方引向悬崖，比不给替代更糟。
func checkReplacements(root string, m *Manifest) ([]string, error) {
	var probs []string
	for _, e := range m.Packages {
		if e.Replacement == "" {
			continue
		}
		sym, ok := splitReplacement(e.Replacement)
		if !ok {
			probs = append(probs, e.Pkg+": replacement "+quote(e.Replacement)+
				" 不是「"+selfModule+"/<pkg>.<符号>」形态")
			continue
		}
		exists, err := symbolExists(root, sym)
		if err != nil {
			// 解析失败给不出结论，向上冒泡成退出码 2，而不是当成「不存在」。
			return nil, err
		}
		if !exists {
			probs = append(probs, e.Pkg+": replacement 指向的符号 "+e.Replacement+" 不存在——替代指向必须指到真的符号")
		}
	}
	return probs, nil
}

// splitReplacement 拆「import 路径.符号名」。import 路径必含 /，
// 符号名必是末段。用 LastIndex 而不是 Split——包路径本身可含点。
func splitReplacement(repl string) (sym replacementSymbol, ok bool) {
	i := strings.LastIndex(repl, ".")
	if i <= 0 || i == len(repl)-1 {
		return sym, false
	}
	sym.ImportPath = repl[:i]
	sym.Name = repl[i+1:]
	if !strings.HasPrefix(sym.ImportPath, selfModule+"/") {
		return sym, false
	}
	if !ast.IsExported(sym.Name) {
		return sym, false
	}
	return sym, true
}

type replacementSymbol struct {
	ImportPath string
	Name       string
}

// symbolExists 确认 selfModule 下的包里确有该导出顶层声明。
func symbolExists(root string, sym replacementSymbol) (bool, error) {
	rel := strings.TrimPrefix(sym.ImportPath, selfModule+"/")
	if rel == "" || strings.Contains(rel, "..") {
		return false, nil
	}
	dir := filepath.Join(root, filepath.FromSlash(rel))
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		return false, nil
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, perr := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if perr != nil {
			return false, fmt.Errorf("解析 %s: %w", f, perr)
		}
		for _, d := range af.Decls {
			var names []*ast.Ident
			switch dd := d.(type) {
			case *ast.FuncDecl:
				names = []*ast.Ident{dd.Name}
			case *ast.GenDecl:
				for _, spec := range dd.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names = append(names, s.Name)
					case *ast.ValueSpec:
						names = append(names, s.Names...)
					}
				}
			}
			for _, n := range names {
				if n != nil && n.IsExported() && n.Name == sym.Name {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// checkFollowsUpstream 门面包声明跟随的上游必须真的出现在 import 面里。
//
// 判据是**模块前缀**：声明 github.com/oagentkit/toolgate，则 import 该
// module 自身或其任意子包都算命中——门面往往转发到上游子包
// （guardx/injectband 一类），按全路径判等会误伤。
func checkFollowsUpstream(m *Manifest, scanned map[string]*PkgInfo) []string {
	var probs []string
	for _, e := range m.Packages {
		if e.FollowsUpstream == "" {
			continue
		}
		info, ok := scanned[e.Pkg]
		if !ok {
			continue
		}
		hit := false
		for _, imp := range info.Imports {
			if imp == e.FollowsUpstream || strings.HasPrefix(imp, e.FollowsUpstream+"/") {
				hit = true
				break
			}
		}
		if !hit {
			probs = append(probs, e.Pkg+": follows_upstream "+e.FollowsUpstream+
				" 未出现在该包 import 面——「跟随上游」声明与代码不符")
		}
	}
	return probs
}
