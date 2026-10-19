// unregistered.go —— 未登记包目录的判定（modules 段之外的生产包目录）。

// 与 deps.go 分开是因为两者判的是不同的问题：deps.go 判「已登记的包之间允许连成什么
// 形状」，本文件判「哪些包根本没被登记」。前者缺了会判错方向，后者缺了会漏掉
// 整块方向不受管的地带——两者都是「依赖缠绕」这条债的入口。

package archcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// unregisteredPackageViolations 找出「含生产 Go 文件、但未被任何登记 root 覆盖」的包目录。
//
// 为什么必须拦：未登记的包对这张规则是隐形的——任何模块都能 import 它，门禁既
// 不知道该边的方向，也不认为它违反了任何东西。新机制包落地时忘了登记，就会得到
// 一张看起来全绿、实际多了一个未受管方向的图。
//
// 判定粒度是**包目录**而不是顶层目录：宿主仓的包根常常是嵌套的
// （internal/xxx），按顶层目录判定会把整棵 internal/ 树误报成「一个未登记的包」，
// 而按包目录判定才能指出到底是哪个包漏登记了。
func unregisteredPackageViolations(root string, mi moduleIndex, cfg DependencyDirection) []Violation {
	ignore := ignoredPaths(cfg.IgnoreDirs)
	var out []Violation
	for _, dir := range packageDirs(root, ignore) {
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		if _, ok := mi.ownerOf(rel); ok {
			continue
		}
		out = append(out, Violation{
			Rule: depRuleID,
			Path: rel,
			Line: 1,
			Text: fmt.Sprintf("包目录 %s 含生产 Go 文件但未被 modules 段的任何 root 覆盖；"+
				"未登记的包对本门禁隐形（任何模块都能 import 它而不被判违规），请补 {id, roots, requires, owner}",
				rel),
			Kind: "module_unregistered",
		})
	}
	return out
}

// packageDirs 列出仓内所有「含生产 Go 文件」的包目录（跳过仓库根自身）。
func packageDirs(root string, ignore map[string]bool) []string {
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return nil
		}
		if path == root {
			return nil
		}
		base := info.Name()
		if strings.HasPrefix(base, ".") || base == "node_modules" || base == "dist" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		if ignore[filepath.ToSlash(rel)] {
			return filepath.SkipDir
		}
		if hasProductionGo(path) {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// hasProductionGo 判断目录里是否直接含生产 Go 文件。
//
// 与 Go 工具链同口径：`_` / `.` 前缀的文件不参与编译，不算包的一部分；
// 只有 _test.go 的目录不是一个可被 import 的包。
func hasProductionGo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		return true
	}
	return false
}

// ignoredPaths 把 ignore_dirs 归一成「相对路径 → 忽略」。
//
// 按相对路径而非目录名匹配：这样「cmd」忽略顶层 cmd/，「internal/legacy」能
// 精确忽略某个嵌套子树，语义可预测。
func ignoredPaths(dirs []string) map[string]bool {
	out := map[string]bool{}
	for _, d := range dirs {
		d = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(d)), "/")
		if d != "" && d != "." {
			out[d] = true
		}
	}
	return out
}
