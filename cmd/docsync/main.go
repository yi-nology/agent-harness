// Command docsync 校验 README 的两张声明表与仓库实际状态一致：
//
//   - 包清单表：包名 / 生产行数（相对容差）/ 外部依赖（见 pkgtable.go）
//   - 「工程底座」表：生产行数、生产文件数、测试文件数（**判相等**，见 baseline.go）
//
// # 为什么要有这个命令
//
// README 的包表是本仓唯一的对外规模声明，历史上长期手工维护，已经漂到
// 「netguard 声称 ~86 行 / 纯 stdlib 叶子层」而实际是 412 行且依赖 guardx
// 这种程度——而 make check 完全不覆盖文档，漂移没有任何信号。
//
// 按本仓既有的门禁口径（见 architecture-policy.yaml 头注：门禁不认文档里
// 说过要迁，不写进机器约束的约定半年后必然回退），这张表也必须有机器校验。
//
// # 判定口径
//
//   - 生产行数：pkg/*.go 且不含 _test.go（表头已声明该口径）
//   - 外部依赖：生产代码 import 的仓外模块，取 module 前两段归并
//   - 本仓自身 import（github.com/yi-nology/goagent-kernel/...）不算外部依赖
//
// 两张表用**两套口径**是刻意的：包表的行数是约数（25% 容差滤逐行涨落），
// 工程底座的三个数是声明值（判相等）。混用一套口径，等于让「跨量级的误述」
// 与「差一行」享受同一个宽容度——而后者正是前者藏身的地方。
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// row 解析出来的一行包表记录。
//
// 只有 pkg / loc / deps 参与机械校验。职责列是散文（「协议 + 五级容错解析 +
// 白名单校验」这类描述），**刻意不比对**——把代码事实写成散文再拿去和散文
// 比，测的是措辞不是实现。能机械判定的只有一件事：不能是空的（见 run）。
type row struct {
	pkg  string
	loc  int
	deps []string
	desc string
}

var (
	// 表头：| `pkg` | loc | deps | 职责 |
	rowRe = regexp.MustCompile("^\\|\\s*`([a-z]+)`\\s*\\|\\s*([0-9]+)\\s*\\|\\s*([^|]*?)\\s*\\|\\s*(.*?)\\s*\\|$")
	// 外部依赖判定：第二段起表示子路径；仓内自身的 module 不算外部。
	selfModule = "github.com/yi-nology/goagent-kernel"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "docsync: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	readme := "README.md"
	f, err := os.Open(readme)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var rows []row
	var lines []string // 原文另存一份：工程底座表按行解析，与包表的行形态不同
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		lines = append(lines, line)
		m := rowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		loc, convErr := strconv.Atoi(m[2])
		if convErr != nil {
			return fmt.Errorf("包 %s 的生产行数不是整数: %q", m[1], m[2])
		}
		rows = append(rows, row{
			pkg:  m[1],
			loc:  loc,
			deps: splitDeps(m[3]),
			desc: m[4],
		})
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if len(rows) == 0 {
		return fmt.Errorf("README.md 里没解析到任何包表行——表结构变了？")
	}

	var problems []string
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.pkg] {
			problems = append(problems, fmt.Sprintf("%s: 包表里出现重复行", r.pkg))
			continue
		}
		seen[r.pkg] = true

		// 职责列散文不比对内容，但空列是机械可判的：整列被误删或截断时拦下。
		if strings.TrimSpace(r.desc) == "" {
			problems = append(problems, fmt.Sprintf("%s: 职责列为空", r.pkg))
		}

		wantLoc, wantDeps, err := measure(r.pkg)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if d := locDeviation(r.loc, wantLoc); d > locTolerance {
			problems = append(problems, fmt.Sprintf(
				"%s: README 写 %d 行，实际 %d 行（偏差 %.0f%%，超过容差 %.0f%%）",
				r.pkg, r.loc, wantLoc, d*100, locTolerance*100))
		}
		if !equalDeps(r.deps, wantDeps) {
			problems = append(problems, fmt.Sprintf(
				"%s: README 依赖写 %q，实际 %q", r.pkg,
				join(r.deps), join(wantDeps)))
		}
	}

	// 反向：仓里有包、README 没写，同样是漂移。
	onDisk, err := listPackages()
	if err != nil {
		return err
	}
	for _, p := range onDisk {
		if !seen[p] {
			problems = append(problems, fmt.Sprintf(
				"%s: 仓里有该包，README 包表未收录", p))
		}
	}

	// ── 工程底座表（issue #31）：确定性量直接判相等 ──
	//
	// 与包表**分两套口径**，这一条是刻意的：包表的行数是约数（25% 容差滤逐行
	// 噪声），工程底座的数字是声明值（判相等）。混用一套口径，等于让「跨量级的
	// 误述」与「差一行」享受同一个宽容度——而后者正是前者藏身的地方。
	claim, present, berr := parseBaseline(lines)
	if berr != nil {
		problems = append(problems, berr.Error())
	} else if present {
		actual, merr := measureBaseline()
		if merr != nil {
			return merr
		}
		problems = append(problems, diffBaseline(claim, actual)...)
	}

	// ── USAGE.md 的机器口径块（包数 / policy 登记数 / 安装行版本）──
	//
	// USAGE 里的数字此前无人判，漂了几个月（27/24 → 实际 41/43）。
	// 判据只管**仓库事实**那类数字（包表行数、policy 登记数、安装行版本）；
	// 「本文覆盖哪几个包」是文档取舍，机器判不了，不硬判。
	usageProblems, usageChecked, err := checkUsage(len(rows))
	if err != nil {
		return err
	}
	problems = append(problems, usageProblems...)

	if len(problems) > 0 {
		return fmt.Errorf("README 与仓库实际状态不一致（%d 项）：\n  - %s\n"+
			"修法：包表按各包 *.go（不含 _test.go）重算行数与外部依赖；"+
			"工程底座表按报错里给的三条命令重算，改 README；"+
			"USAGE.md 的机器口径块按报错里给的三个数改（包数 / policy 登记数 / 安装行版本）。\n"+
			"两套口径别混：包表行数按 %.0f%% 相对容差判（容差内的逐行涨落不报），"+
			"工程底座的三项是确定性量，**判相等**。\n"+
			"本门禁不自动改文档——改表是要人判断的动作。",
			len(problems), strings.Join(problems, "\n  - "), locTolerance*100)
	}

	if present {
		actual, _ := measureBaseline()
		fmt.Printf("README 与仓库一致（%d 个包；工程底座 %d 行 / %d 文件 / %d 个测试文件）%s✓\n",
			len(rows), actual.prodLines, actual.prodFiles, actual.testFiles, usageNote(usageChecked))
		return nil
	}
	fmt.Printf("README 包表与仓库一致（%d 个包；无工程底座表）%s✓\n", len(rows), usageNote(usageChecked))
	return nil
}

// usageNote 说明 USAGE 口径校验是否参与本次判定。
//
// 为什么要在成功输出里也说明：判据被跳过（合成仓没有 USAGE.md）是合法状态，
// 但**静默跳过**与「判过了」在日志里长得一样——一旦判据面被误删，
// 看到「✓」的人不会想到「其实那段没判」。故跳过时显式说出来。
func usageNote(checked bool) string {
	if checked {
		return "；USAGE 口径已判"
	}
	return "；USAGE 口径未判（本仓无 docs/USAGE.md）"
}

// locTolerance README 行数与实际值的相对偏差容忍上限。
//
// 为什么是容差而不是逐行相等：docsync 最初把行数当强断言，结果是**改一行代码
// 就红一次**——2026-10-18 连续三次 CI 失败（a18b89d / 80ca6d7 / 77b979d）
// 全部源于 protocol 差 1 行（README 1822 / 实际 1821），随后又得单独提交
// 「同步 protocol 行数」去消它。这种仪式性噪声的真实代价不是多写一次 README，
// 而是**训练人忽略 CI 红**——规则常红等于没有规则。
//
// 但也不能退到不校验：这道门禁存在的初衷正是抓「README 声称 netguard ~86 行、
// 实际 412 行」这类文档误导（见文件头）。所以这里只滤掉噪声、不砍断言：
// 偏差在容差内不报，跨越量级才报。
//
// 25% 的取法：小于四分之一的偏差不构成对规模的误述（README 那列本就是约数），
// 而历史上真实发生的漂移都在 2~5 倍量级，离这个阈值很远——所以放宽它不会
// 放过任何一次真问题，却挡掉了全部的逐行噪声。
const locTolerance = 0.25

// locDeviation 相对偏差，以较大者为分母。
//
// 分母取较大者而非 README 写的值，是为了让「少写」与「夸大」同等对待：
// 写 86 而实际 412（少写）和写 412 而实际 86（夸大）都是对规模的误述，
// 严重程度相同。
func locDeviation(readme, actual int) float64 {
	hi, lo := readme, actual
	if hi < lo {
		hi, lo = lo, hi
	}
	if hi == 0 {
		return 0
	}
	return float64(hi-lo) / float64(hi)
}

// measure 实测一个包的生产行数与外部依赖。
