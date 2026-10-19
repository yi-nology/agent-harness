// deps.go —— 包间依赖方向门禁（modules[].requires 的执行体，AST 判定）。
//
// 背景：architecture-policy.yaml 的 modules 段逐包登记 {id, roots, requires}，
// 其中 requires 是各包之间**允许**的依赖方向。这张表此前只是声明面——policy.go
// 把 modules 反序列化成 `[]struct{}`，requires 连内存都没进，于是 README 宣称的
// 「域词污染 + 依赖方向」里「依赖方向」那一半没有执行体。声明也确实已经漂移：
// watchdog 声明 requires: []，实际 import 了 eventbus。
//
// 为什么这条纪律值得做成门禁而不是 review 习惯：依赖缠绕是最难靠 review 发现的
// 债类型——它零报错、零警告、构建全绿，每个单点 import 看起来都合理，只有把全图
// 摊开才看得出 A→B→C→A。其余七条规则查的全是「写法」（用什么词、import 什么、
// goroutine 怎么起），没有一条查「包与包之间允许连成什么形状」。
//
// 判定面只含**生产代码**（跳过 _test.go）。理由：依赖方向决定交付物里出现什么，
// 而测试依赖是临时的、可随用例删掉的；把测试一并纳入会逼出一批「只为共享夹具
// 而做的注册」噪声，反而削弱这条规则的可信度。
//
// 顺带拦「未登记包」：modules 段里没有条目的包目录，意味着任何包都能 import 它而
// 门禁无从判断方向——那是这条规则最大的盲区，也是新机制包落地时最容易踩的坑。
//
// # 跨仓依赖：ext: 前缀
//
// 机制面迁到外仓之后，宿主的依赖拓扑表需要能表达「我依赖外仓的哪个包」，
// 而只认本仓模块 id 的判据表达不了。故 requires 的条目有两种形态：
//
//	<模块 id>                    本仓内部模块，必须在 modules 段登记过
//	ext:<import 路径前缀>        外部包面，如 ext:github.com/yi-nology/agent-harness/netguard
//
// 外部依赖分两层声明，与内部模块同构：
//
//	受管面（policy 级）：dependency_direction.ext_prefixes —— 哪些外部包纳入判定
//	逐模块的边（模块级）：requires 里的 ext: 条目 —— 本模块用了其中哪些
//
// 分两层是必须的：若「哪些外部包受管」也只由各模块自己声明，那么最后一个
// 声明它的模块撤掉声明时，整片包面随之消失，那些 import 重新变成不判定的——
// 而外部包没有「未登记目录」那道兜底（本仓没有它的目录），这条边就静默失管了。
// 门禁的失败形态必须是变红，不能是变绿。
//
// 纪律：**外部包必须写完整 import 路径前缀**，不接受短名。短名需要一张「短名 →
// 路径」的解析表，而那张表本身又是一份会漂移的声明；且短名与本仓模块 id 长得
// 一样（都叫 netguard），解析歧义会静默判错对象。

package archcheck

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

// extPrefix 是 requires 里外部包标识的前缀。
const extPrefix = "ext:"

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

// moduleIndex 是 modules 段的索引：目录 → 模块 ID、import 前缀 → 模块 ID，
// 以及每个模块的**允许面**（allowed）。
type moduleIndex struct {
	byRoot     map[string]string // 目录 → 模块 ID
	byPrefix   map[string]string // import 前缀 → 模块 ID
	prefixes   []string          // 按长度降序，供最长前缀匹配用
	requires   map[string]map[string]bool
	rootsOf    map[string][]string // 模块 ID → 它的 roots
	extSurface map[string]bool     // 受管外部包面（全体模块登记过的 ext: 前缀）
	allowed    map[string][]string // 模块 ID → 允许的 import 前缀
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

// allows 报告 import 路径是否落在该模块的允许面内。
//
// 允许面 = 自身包 + requires 解析出的前缀（内部模块的包根 / 外部包面）。
//
// 为什么用「前缀集合」而不是「模块 id 集合」：两种形态的依赖（内部模块、外部包面）
// 在判定上其实是同一件事——「这个 import 路径是不是我声明过的那个东西」，
// 统一成前缀才能让 ext: 与内部模块走同一条判据，而不是两套逻辑各判各的。
func (mi moduleIndex) allows(id, imp string) bool {
	for _, p := range mi.allowed[id] {
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return true
		}
	}
	return false
}

// extMatch 返回 imp 命中的受管外部包面（最长优先）。
func (mi moduleIndex) extMatch(imp string) (string, bool) {
	best := ""
	for p := range mi.extSurface {
		if imp != p && !strings.HasPrefix(imp, p+"/") {
			continue
		}
		if len(p) > len(best) {
			best = p
		}
	}
	return best, best != ""
}

// buildModuleIndex 建索引，并**校验声明自身**是否自洽。
//
// 为什么要校验：requires 里写错一个模块 id（多打一个字）不会让任何人看到错误，
// 但会让「这条依赖是否被允许」变成永远为假的判定——门禁看似在跑，实则对该边
// 完全放行。故声明出错一律当成 policy 解析失败（非零退出），不静默降级。
//
// 外部包面（ext:）不参与这项校验：它按定义就不在 modules 段里，它是**别的仓**
// 的包。受管面由 policy 的 ext_prefixes 给出；requires 里的 ext: 条目必须落在
// 某个受管面之内，否则这条边声明在受管面之外——同样响亮失败。
func buildModuleIndex(mods []Module, modPath string, universe []string) (moduleIndex, error) {
	mi := moduleIndex{
		byRoot:     map[string]string{},
		byPrefix:   map[string]string{},
		requires:   map[string]map[string]bool{},
		rootsOf:    map[string][]string{},
		extSurface: map[string]bool{},
		allowed:    map[string][]string{},
	}
	for _, u := range universe {
		u = strings.TrimSpace(u)
		if u == "" {
			return mi, fmt.Errorf("dependency_direction.ext_prefixes 里有空条目")
		}
		mi.extSurface[u] = true
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
			if strings.TrimSpace(r) == "" {
				return mi, fmt.Errorf("模块 %s 的 requires 里有空条目", m.ID)
			}
			allow[r] = true
		}
		mi.requires[m.ID] = allow
		mi.rootsOf[m.ID] = append([]string{}, m.Roots...)
		for _, r := range m.Roots {
			if prev, dup := mi.byRoot[r]; dup {
				return mi, fmt.Errorf("modules 段 roots 重复: %s 同时属于 %s 与 %s", r, prev, m.ID)
			}
			mi.byRoot[r] = m.ID
		}
	}
	// requires 的引用校验 + 允许面解析。
	for _, m := range mods {
		for _, r := range m.Requires {
			if ext, ok := externalPrefix(r); ok {
				if ext == "" {
					return mi, fmt.Errorf("模块 %s 的 requires 条目 %q 缺少外部 import 路径前缀（%s 后不能为空）", m.ID, r, extPrefix)
				}
				if !coveredBySurface(ext, mi.extSurface) {
					return mi, fmt.Errorf("模块 %s 的 requires 条目 %q 不在受管外部包面内"+
						"（请先在 dependency_direction.ext_prefixes 登记 %s）", m.ID, r, ext)
				}
				mi.allowed[m.ID] = append(mi.allowed[m.ID], ext)
				continue
			}
			// requires 里引用的模块必须都已登记，否则该条声明是空转。
			if _, ok := mi.requires[r]; !ok {
				return mi, fmt.Errorf("模块 %s 的 requires 引用了未登记的模块 %s（外部包须写成 %s<import 路径前缀>）", m.ID, r, extPrefix)
			}
			for _, root := range mi.rootsOf[r] {
				mi.allowed[m.ID] = append(mi.allowed[m.ID], modPath+"/"+root)
			}
		}
		// 自身包永远允许（包内自引用不是「依赖方向」问题）。
		for _, root := range mi.rootsOf[m.ID] {
			mi.allowed[m.ID] = append(mi.allowed[m.ID], modPath+"/"+root)
		}
	}
	// import 前缀表：内部包的 import 前缀。
	for _, m := range mods {
		for _, root := range m.Roots {
			p := modPath + "/" + root
			mi.byPrefix[p] = m.ID
			mi.prefixes = append(mi.prefixes, p)
		}
	}
	// 最长前缀优先：roots 允许嵌套，先命中外层会把内层包的边判成外层的边。
	sort.Slice(mi.prefixes, func(i, j int) bool {
		return len(mi.prefixes[i]) > len(mi.prefixes[j])
	})
	return mi, nil
}

// externalPrefix 判定 requires 条目是不是外部包标识，并取出其 import 路径前缀。
func externalPrefix(entry string) (string, bool) {
	if !strings.HasPrefix(entry, extPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(entry, extPrefix)), true
}

// coveredBySurface 报告前缀是否落在某个受管包面之内。
//
// 方向是单向的：requires 里声明的前缀必须**被**某个受管面包住（相等或为其子路径），
// 反过来不行。让 requires 声明一个比受管面更宽的前缀，等于让某个模块越过受管面
// 领取权限——它多领的那部分本该受判，却因为不在受管面内而不被判定。
func coveredBySurface(prefix string, surface map[string]bool) bool {
	for u := range surface {
		if prefix == u || strings.HasPrefix(prefix, u+"/") {
			return true
		}
	}
	return false
}

// dependencyViolations 判定包间依赖方向，并顺带拦下未登记的包目录。
func dependencyViolations(root string, mods []Module, cfg DependencyDirection) ([]Violation, error) {
	if len(mods) == 0 {
		return nil, nil
	}
	modPath, err := moduleImportPath(root)
	if err != nil {
		return nil, fmt.Errorf("依赖方向门禁读取 module 路径失败: %w", err)
	}
	mi, err := buildModuleIndex(mods, modPath, cfg.ExtPrefixes)
	if err != nil {
		return nil, fmt.Errorf("依赖方向门禁: %w", err)
	}

	var out []Violation
	out = append(out, unregisteredPackageViolations(root, mi, cfg)...)

	fset := token.NewFileSet()
	for _, m := range mods {
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
					if mi.allows(m.ID, p) {
						continue
					}
					if target, ok := mi.longestPrefix(p); ok {
						out = append(out, Violation{
							Rule: depRuleID,
							Path: rel,
							Line: fset.Position(im.Pos()).Line,
							Text: fmt.Sprintf("包 %s 依赖 %s（包 %s），但 modules 段未在 requires 中登记 %s；"+
								"新增内部依赖先改 architecture-policy.yaml 再写代码", m.ID, p, target, target),
							Kind: "module_dep",
						})
						continue
					}
					if ext, ok := mi.extMatch(p); ok {
						out = append(out, Violation{
							Rule: depRuleID,
							Path: rel,
							Line: fset.Position(im.Pos()).Line,
							Text: fmt.Sprintf("包 %s 依赖外部包 %s（受管包面 %s），但 modules 段未在 requires 中登记 %s；"+
								"跨仓依赖先在 policy 登记 ext: 前缀再写代码", m.ID, p, ext, extPrefix+ext),
							Kind: "module_dep_ext",
						})
						continue
					}
					// 非本表判定的第三方依赖，不归这张表管（外部依赖由
					// host-dependency / runtime-deps 规则管）。
				}
			}
		}
	}
	return out, nil
}
