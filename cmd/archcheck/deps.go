// deps.go —— 包间依赖方向门禁（modules[].requires 的执行体，AST 判定）。
//
// 背景：architecture-policy.yaml 的 modules 段逐包登记 {id, roots, requires}，
// 其中 requires 是本仓 20+ 个包之间**允许**的内部依赖方向。这张表此前只是声明面
// ——policy.go 把 modules 反序列化成 `[]struct{}`，requires 连内存都没进，于是
// README 宣称的「域词污染 + 依赖方向」里「依赖方向」那一半没有执行体。声明也确实
// 已经漂移：watchdog 声明 requires: []，实际 import 了 eventbus。
//
// 为什么这条纪律值得做成门禁而不是 review 习惯：依赖缠绕是本仓最难靠 review
// 发现的债类型——它零报错、零告警、构建全绿，每个单点 import 看起来都合理，
// 只有把全图摊开才看得出 A→B→C→A。其余七条规则查的全是「写法」（用什么词、
// import 什么、goroutine 怎么起），没有一条查「包与包之间允许连成什么形状」。
//
// 判定面只含**生产代码**（跳过 _test.go）。理由：依赖方向决定交付物里出现什么，
// 而测试依赖是临时的、可随用例删掉的；把测试一并纳入会逼出一批「只为共享夹具
// 而做的注册」噪声，反而削弱这条规则的可信度。
//
// 顺带拦「未登记包」：modules 段里没有条目的新目录，意味着任何包都能 import 它而
// 门禁无从判断方向——那是这条规则最大的盲区，也是新机制包落地时最容易踩的坑。

package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// depRuleID 依赖方向门禁的规则 ID。
const depRuleID = "harness-module-dependency-direction"

// moduleImportPath 从 go.mod 读本仓的 module 路径，拼出各包的 import 前缀。
//
// 为什么不在代码里写死：module path 是迁移过一次的（私有域名 → github.com），
// 写死等于把同一个坑再挖一遍——迁移那天改 go.mod 忘了改门禁，门禁就静默失配。
func moduleImportPath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("go.mod 未找到 module 声明")
}

// moduleIndex 是 modules 段的两种索引：目录 → 模块 ID、import 前缀 → 模块 ID。
type moduleIndex struct {
	byRoot   map[string]string
	byPrefix map[string]string // import 前缀 → 模块 ID
	prefixes []string          // 按长度降序，供最长前缀匹配用
	requires map[string]map[string]bool
}

// longestPrefix 返回 imp 命中的**最长**已登记 import 前缀对应的模块 ID。
//
// 为什么必须是最长而不是首个命中：modules 段的 roots 允许嵌套（先命中外层就会
// 把内层包的边判成外层的边，方向判定直接判错对象）。前缀表在构造时按长度降序
// 排好，这里首个命中即最长命中——不能直接遍历 map，Go 的 map 迭代顺序是随机的。
func (mi moduleIndex) longestPrefix(imp string) (string, bool) {
	for _, p := range mi.prefixes {
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return mi.byPrefix[p], true
		}
	}
	return "", false
}

// ownerOf 返回仓库相对路径所属的模块（最长目录前缀匹配）。
//
// 为什么必须有它：遍历某模块的 root 时用的是**递归**扫描，而 roots 可以嵌套——
// 扫 outer 会连 outer/inner 一起扫到，若不按路径归属过滤，内层包的文件就会被
// 记到外层名下，于是「内层已登记的依赖」被判成「外层的越界依赖」。
// 这条判据与 import 的最长前缀匹配是同一条纪律的两处落地。
func (mi moduleIndex) ownerOf(rel string) (string, bool) {
	rel = filepath.ToSlash(rel)
	best, bestLen := "", -1
	for dir, id := range mi.byRoot {
		if rel != dir && !strings.HasPrefix(rel, dir+"/") {
			continue
		}
		if len(dir) > bestLen {
			best, bestLen = id, len(dir)
		}
	}
	if bestLen < 0 {
		return "", false
	}
	return best, true
}

// buildModuleIndex 建索引，并**校验声明自身**是否自洽。
//
// 为什么要校验：requires 里写错一个模块 id（多打一个字）不会让任何人看到错误，
// 但会让「这条依赖是否被允许」变成永远为假的判定——门禁看似在跑，实则对该边
// 完全放行。故声明出错一律当成 policy 解析失败（非零退出），不静默降级。
func buildModuleIndex(mods []moduleDecl) (moduleIndex, error) {
	mi := moduleIndex{
		byRoot:   map[string]string{},
		byPrefix: map[string]string{},
		requires: map[string]map[string]bool{},
	}
	for _, m := range mods {
		if m.ID == "" {
			return mi, fmt.Errorf("modules 段有条目缺 id")
		}
		if _, dup := mi.requires[m.ID]; dup {
			return mi, fmt.Errorf("modules 段模块 id 重复: %s", m.ID)
		}
		if len(m.Roots) == 0 {
			return mi, fmt.Errorf("modules 段模块 %s 未声明 roots", m.ID)
		}
		allow := map[string]bool{}
		for _, r := range m.Requires {
			allow[r] = true
		}
		mi.requires[m.ID] = allow
		for _, r := range m.Roots {
			if prev, dup := mi.byRoot[r]; dup {
				return mi, fmt.Errorf("modules 段 roots 重复: %s 同时属于 %s 与 %s", r, prev, m.ID)
			}
			mi.byRoot[r] = m.ID
		}
	}
	// requires 里引用的模块必须都已登记，否则该条声明是空转。
	for _, m := range mods {
		for _, r := range m.Requires {
			if _, ok := mi.requires[r]; !ok {
				return mi, fmt.Errorf("模块 %s 的 requires 引用了未登记的模块 %s", m.ID, r)
			}
		}
	}
	return mi, nil
}

// dependencyViolations 判定包间依赖方向，并顺带拦下未登记的包目录。
func dependencyViolations(root string, mods []moduleDecl, cfg dependencyDirection) ([]violation, error) {
	if len(mods) == 0 {
		return nil, nil
	}
	modPath, err := moduleImportPath(root)
	if err != nil {
		return nil, fmt.Errorf("依赖方向门禁读取 module 路径失败: %w", err)
	}
	mi, err := buildModuleIndex(mods)
	if err != nil {
		return nil, fmt.Errorf("依赖方向门禁: %w", err)
	}
	for m, r := range mi.byRoot {
		mi.byPrefix[modPath+"/"+m] = r
		mi.prefixes = append(mi.prefixes, modPath+"/"+m)
	}
	// 最长前缀优先：roots 允许嵌套，先命中外层会把内层包的边判成外层的边。
	sort.Slice(mi.prefixes, func(i, j int) bool {
		return len(mi.prefixes[i]) > len(mi.prefixes[j])
	})

	var out []violation
	out = append(out, unregisteredDirViolations(root, mi, cfg)...)

	fset := token.NewFileSet()
	for _, m := range mods {
		allow := mi.requires[m.ID]
		for _, rootDir := range m.Roots {
			for _, f := range iterFiles(filepath.Join(root, rootDir), []string{"*.go"}, true) {
				rel, _ := filepath.Rel(root, f)
				// 归属以路径为准而不是「正在扫谁的 root」：扫描是递归的，
				// roots 又允许嵌套，不过滤就会把内层包的文件记到外层名下。
				if owner, ok := mi.ownerOf(rel); !ok || owner != m.ID {
					continue
				}
				astFile, err := parser.ParseFile(fset, f, nil, parser.ImportsOnly|parser.SkipObjectResolution)
				if err != nil {
					return nil, fmt.Errorf("依赖方向门禁解析 %s 失败: %w", f, err)
				}
				for _, im := range astFile.Imports {
					p, err := strconv.Unquote(im.Path.Value)
					if err != nil {
						continue
					}
					target, ok := mi.longestPrefix(p)
					// 非本仓的依赖不归这张表判（外部依赖由 host-dependency / runtime-deps 规则管）。
					if !ok || target == m.ID || allow[target] {
						continue
					}
					out = append(out, violation{
						rule: depRuleID,
						path: rel,
						line: fset.Position(im.Pos()).Line,
						text: fmt.Sprintf("包 %s 依赖 %s（包 %s），但 modules 段未在 requires 中登记 %s；"+
							"新增内部依赖先改 architecture-policy.yaml 再写代码", m.ID, p, target, target),
						kind: "module_dep",
					})
				}
			}
		}
	}
	return out, nil
}

// unregisteredDirViolations 找出「有生产 Go 文件但未在 modules 段登记」的顶层目录。
//
// 为什么必须拦：未登记的包对这张规则是隐形的——任何模块都能 import 它，门禁既
// 不知道该边的方向，也不认为它违反了任何东西。新机制包落地时忘了登记，就会得到
// 一张看起来全绿、实际多了一个未受管方向的图。
func unregisteredDirViolations(root string, mi moduleIndex, cfg dependencyDirection) []violation {
	ignore := map[string]bool{}
	for _, d := range cfg.IgnoreDirs {
		ignore[d] = true
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []violation
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || ignore[name] {
			continue
		}
		if _, ok := mi.byRoot[name]; ok {
			continue
		}
		files := iterFiles(filepath.Join(root, name), []string{"*.go"}, true)
		if len(files) == 0 {
			continue // 纯文档/资产目录不必登记
		}
		sort.Strings(files)
		out = append(out, violation{
			rule: depRuleID,
			path: name,
			line: 1,
			text: fmt.Sprintf("目录 %s 含 %d 个生产 Go 文件但未在 modules 段登记；"+
				"未登记的包对本门禁隐形（任何模块都能 import 它而不被判违规），请补 {id, roots, requires, owner}",
				name, len(files)),
			kind: "module_unregistered",
		})
	}
	return out
}
