// judge.go —— 事实 + 阈值 + 豁免 → 报告（判据的另一半）。
//
// 这里没有任何 I/O：全程是纯函数，故判据的每条分支都能被表驱动用例直接钉住。
// 把「读日志」与「下判断」分开，是本命令从 awk 改成 Go 的主要理由——awk 版把
// 判定逻辑与 shell 调用面糊在一起，唯一能测的方式是真的跑一次 make check。
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/oekit/pkg/stringx"
)

// options 判据参数。
type options struct {
	// min 逐包覆盖率下限（百分比数值）。
	min float64
	// exempt 覆盖率豁免：包路径**后缀**匹配（写 `cmd/docsync` 比写全 module 路径可读）。
	exempt []string
	// noTestExempt 零测试文件豁免：同样按后缀匹配。
	noTestExempt []string
}

// report 判定结果。
type report struct {
	// counted 计入判定的包数（有覆盖率且未被豁免）。
	counted int
	// exempted 生效中的覆盖率豁免数。
	exempted int
	// noTestDeclared 生效中的零测试豁免数。
	noTestDeclared int
	// below 低于下限的包。
	below []string
	// staleExempt 已失效的覆盖率豁免。
	staleExempt []string
	// undeclaredNoTest 未声明的零测试包。
	undeclaredNoTest []string
	// staleNoTestExempt 已失效的零测试豁免（该包已有测试，或包不存在）。
	staleNoTestExempt []string
}

// ok 报告是否可以放行。
func (r report) ok() bool {
	return len(r.below) == 0 && len(r.staleExempt) == 0 &&
		len(r.undeclaredNoTest) == 0 && len(r.staleNoTestExempt) == 0
}

// hasSuffix 按后缀匹配包路径。
//
// 后缀匹配而不是前缀：README 与 Makefile 里写 `cmd/docsync` 可读，而全路径前缀
// （github.com/yi-nology/goagent-kernel）在每条豁免里都是噪音。边界必须对齐「/」
// 或整串相等——否则 `sync` 会匹配到 `cmd/docsync`，那是**放行方向**的漏判。
func hasSuffix(pkg, suffix string) bool {
	if suffix == "" {
		return false
	}
	if pkg == suffix {
		return true
	}
	return strings.HasSuffix(pkg, "/"+suffix)
}

// matchIndex 返回命中的豁免下标（未命中返回 -1）。
func matchIndex(list []string, pkg string) int {
	for i, s := range list {
		if hasSuffix(pkg, s) {
			return i
		}
	}
	return -1
}

// judge 判定（纯函数，用例直接调它，不经过进程）。
func judge(results []pkgResult, o options) report {
	var r report
	hitCoverage := make([]bool, len(o.exempt))
	hitNoTest := make([]bool, len(o.noTestExempt))

	for _, res := range results {
		if res.noTestFiles {
			if i := matchIndex(o.noTestExempt, res.pkg); i >= 0 {
				hitNoTest[i] = true
				r.noTestDeclared++
				continue
			}
			r.undeclaredNoTest = append(r.undeclaredNoTest, res.pkg)
			continue
		}
		if !res.hasCoverage {
			// `[no statements]`：不适用，既不计入分母也不判违规。
			continue
		}
		if i := matchIndex(o.exempt, res.pkg); i >= 0 {
			hitCoverage[i] = true
			if res.coverage >= o.min {
				r.staleExempt = append(r.staleExempt,
					fmt.Sprintf("%s: 已达 %.1f%%（下限 %.0f%%）", res.pkg, res.coverage, o.min))
				continue
			}
			r.exempted++
			continue
		}
		r.counted++
		if res.coverage < o.min {
			r.below = append(r.below, fmt.Sprintf("%s: %.1f%%", res.pkg, res.coverage))
		}
	}

	// 失效判定：豁免写了但没命中任何包（包改名/被删/本来就有测试）。
	for i, s := range o.exempt {
		if !hitCoverage[i] {
			r.staleExempt = append(r.staleExempt, fmt.Sprintf("%s: 没有包命中这条豁免", s))
		}
	}
	for i, s := range o.noTestExempt {
		if !hitNoTest[i] {
			r.staleNoTestExempt = append(r.staleNoTestExempt,
				fmt.Sprintf("%s: 该包已有测试文件或不存在，豁免该拆了", s))
		}
	}
	return r
}

// String 渲染报告（成功一行、失败分条）。
func (r report) String(o options) string {
	if r.ok() {
		return fmt.Sprintf("逐包覆盖率下限 %.0f%% ✓（%d 个包计入，%d 个覆盖率豁免，%d 个零测试豁免）\n",
			o.min, r.counted, r.exempted, r.noTestDeclared)
	}
	var b strings.Builder
	if len(r.below) > 0 {
		fmt.Fprintf(&b, "以下包低于逐包下限 %.0f%%：\n", o.min)
		for _, s := range r.below {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		b.WriteString("修法：补该包的用例（阈值是真阈值，不要靠调低 PKG_COV_MIN 变绿）。\n")
	}
	if len(r.undeclaredNoTest) > 0 {
		b.WriteString("以下包没有任何测试文件，且未声明豁免：\n")
		for _, s := range r.undeclaredNoTest {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		b.WriteString("修法：补一个冒烟用例；确属夹具/装配层的，登记到 Makefile 的 PKG_NO_TEST_EXEMPT 并写清理由。\n")
	}
	if len(r.staleExempt) > 0 {
		b.WriteString("逐包下限豁免已失效（必须当天拆除，不能静默留着）：\n")
		for _, s := range r.staleExempt {
			fmt.Fprintf(&b, "  - %s ⇒ 请从 PKG_COV_EXEMPT 移除\n", s)
		}
	}
	if len(r.staleNoTestExempt) > 0 {
		b.WriteString("零测试豁免已失效（必须当天拆除）：\n")
		for _, s := range r.staleNoTestExempt {
			fmt.Fprintf(&b, "  - %s ⇒ 请从 PKG_NO_TEST_EXEMPT 移除\n", s)
		}
	}
	return b.String()
}

// splitList 拆逗号分隔的豁免表（去空白、丢空项）。
func splitList(s string) []string {
	// 拆逗号 + 去空白 + 丢空项是上游 stringx.SplitCSVNonEmpty 的逐字语义；
	// 排序是本函数自己的要求（清单出口一律有序，免得两次输出两种顺序）。
	out := stringx.SplitCSVNonEmpty(s)
	sort.Strings(out)
	return out
}
