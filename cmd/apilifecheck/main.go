// Command apilifecheck 执行 api-lifecycle.yaml 的可执行面。
//
// 存在意义（issue #113 / #104）：分级若只写在包注释与文档里，半年后就与
// 清单漂移——「文档里写了但代码里没改」正是本仓反复强调的失效形态。故与
// architecture-check / docsync 同一条纪律：声明必须有机器判据。
//
// 四类判据：
//
//  1. 清单 ↔ 包注释：每个登记包的包注释里必须有且只有一行生命周期标记，
//     且级别与清单一致；
//  2. 覆盖：清单必须覆盖**全部**生产包（新增包不登记即红，双向判定——
//     清单里的包被删了也红）；
//  3. Deprecated 条目必须给替代指向，且指向的符号真实存在；
//  4. 错误登记：docs/error-semantics.md 的哨兵/结构化错误类型表必须与
//     各包代码里真实声明的集合一致（issue #104「新增错误必须登记」）。
//
// 判定口径见 docs/api-compat.md；本命令只做装配（找根 → 装载 → 判 → 报告 →
// 退出码），判定逻辑尽量留在可导入的粒度上，但**刻意不做成本仓的可导入包**：
// 它判的是「本仓清单 ↔ 本仓源码」，宿主仓有自己的清单与包集，复用方要的是
// 判据思路而不是这份清单本身。
//
// 退出码（与 cmd/archcheck 同口径）：
//
//	0  无违反
//	1  判了，有问题
//	2  给不出结论（清单/源码解析失败）
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	root, err := findRoot(".")
	if err != nil {
		fmt.Fprintf(os.Stderr, "apilifecheck: %v\n", err)
		return 2
	}
	code, err := run(root, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apilifecheck: %v\n", err)
		return 2
	}
	return code
}

// findRoot 自 cwd 向上找含 api-lifecycle.yaml 的目录。
//
// 与 cmd/archcheck 找 policy 同一条理由：门禁必须在仓库任意子目录里都能跑，
// 否则「在子目录里跑了一下没红」会被当成「没有违规」。
func findRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, manifestName)); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("从 %s 向上找不到 %s——请在仓库内运行", start, manifestName)
		}
		dir = parent
	}
}

// run 完整判定：返回退出码。err 非空 = 给不出结论（退出码 2）。
//
// 问题清单只**聚合不截断**：门禁报错的成本在「人要去改」，把问题藏进
// 「还有 N 条没显示」只会逼人多跑几轮。
func run(root string, out io.Writer) (int, error) {
	manifest, err := LoadManifest(filepath.Join(root, manifestName))
	if err != nil {
		return 0, err
	}
	probs, err := checkAll(root, manifest)
	if err != nil {
		return 0, err
	}

	counts := map[string]int{}
	for _, e := range manifest.Packages {
		counts[e.Level]++
	}
	// 输出面的写失败不吞（管道关闭等场景让错误冒出去），但也不改判定路径：
	// 判定结果只取决于 probs，不取决于报告写到了谁。
	_, _ = fmt.Fprintf(out, "API 生命周期清单：共 %d 个包（Stable %d / Experimental %d / Deprecated %d）\n",
		len(manifest.Packages), counts[LevelStable], counts[LevelExperimental], counts[LevelDeprecated])

	if len(probs) == 0 {
		_, _ = fmt.Fprintln(out, "清单 ↔ 包注释 ↔ 错误登记 全部一致 ✓")
		return 0, nil
	}
	for _, p := range probs {
		_, _ = fmt.Fprintf(out, "✗ %s\n", p)
	}
	_, _ = fmt.Fprintf(out, "共 %d 处违反。判定口径见 docs/api-compat.md。\n", len(probs))
	return 1, nil
}

// checkAll 四类判据的聚合。拆出来是为了 run() 只管装配与输出，
// 判据的排列在这里一眼可数。
func checkAll(root string, m *Manifest) ([]string, error) {
	var probs []string
	probs = append(probs, m.Validate()...)

	pkgDirs, err := DiscoverPackages(root)
	if err != nil {
		return nil, err
	}
	probs = append(probs, checkCoverage(m, pkgDirs)...)

	scanned := make(map[string]*PkgInfo, len(pkgDirs))
	for _, dir := range pkgDirs {
		info, scanErr := ScanPackage(root, dir)
		if scanErr != nil {
			return nil, scanErr // 源码解析失败 = 给不出结论，不是「无违规」
		}
		scanned[dir] = info
	}
	probs = append(probs, checkMarkers(m, scanned)...)

	sents, types, err := extractAllSentinels(root, pkgDirs)
	if err != nil {
		return nil, err
	}
	replProbs, err := checkReplacements(root, m)
	if err != nil {
		return nil, err
	}
	probs = append(probs, replProbs...)
	probs = append(probs, checkFollowsUpstream(m, scanned)...)
	probs = append(probs, checkErrorRegistry(root, m, sents, types)...)
	probs = append(probs, checkDocsBlocks(root, m)...)
	return probs, nil
}
