// baseline.go —— README「工程底座」表的机械判定（issue #31）。
//
// # 为什么这张表需要门禁
//
// 它是本仓第二张规模声明表，历史上**八轮**全部靠「作者记得回来手算」：
// 11,491 → 13,336 → 16,974 → 18,100 → 18,678 → 19,573 → 19,679 → 19,624，
// 每轮都漂几十到上千行，期间没有任何信号。这正是本仓那条纪律的反面案例：
// **没人负责的东西会静默失真**。
//
// 更糟的是它连「测得准」都做不到：2026-11-06 同一批用例、生产代码一行未改，
// 覆盖率量出 85.5 / 85.0 / 87.0 三个值——因为覆盖率随**测的是哪个用例集合**变。
// 故本条只判**确定性量**（行数 / 文件数），覆盖率一格从表里删掉手抄的百分数
// （改为「以 make cover-gate 输出为准」）：让散文去复述一个由门禁实时给出的
// 数字，只会制造两处不一致。
//
// # 判据
//
// 三项都是确定值，**直接判相等，不留容差**。包表那 25% 容差是给「逐行涨落」留的
// 噪声滤波，而这表一旦容差就又能漂——它的历史漂移量本来就落在容差里。
//
// 三项口径与 README 里那三条重算命令逐字一致（非 _test.go 的 .go 文件数 / 行数、
// _test.go 文件数；排除隐藏目录与 vendor）。
//
// # 不判什么（写明，而不是假装覆盖）
//
//   - 覆盖率：依赖跑的是哪个用例集合，是**测量**不是**声明**，见上。
//   - 结构阈值 / 门禁条数 / 例外登记数那几格：它们各自有执行体（policy + 门禁），
//     数字写错不会让门禁失效，只是描述不准——判定成本高于收益。
//
// # 表不在时怎么办
//
// 本条的语义是「**你声明了就必须是真的**」：README 里没有「工程底座」小节 ⇒
// docsync 不判它（不声明就不会失真）。但小节存在而**行解析不出来**一定是坏了
// （表被截断/改名/列数变了）⇒ 当场红，不能靠「没有行就没有比对」静默变绿。
package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// baseline 是「工程底座」表里三项确定性量。
type baseline struct {
	prodFiles int
	prodLines int
	testFiles int
}

// baselineRows 表里可判定行的解析式。
//
// 只认这两行：其余各行（覆盖率 / 结构阈值 / 门禁 / 例外登记）不参与判定，
// 见文件头「不判什么」。
var (
	prodRowRe = regexp.MustCompile(`^\|\s*生产代码\s*\|\s*([0-9,]+)\s*行\s*/\s*([0-9,]+)\s*文件`)
	testRowRe = regexp.MustCompile(`^\|\s*测试\s*\|\s*([0-9,]+)\s*个测试文件`)
)

// baselineHeading 是小节标题里的关键词。
const baselineHeading = "工程底座"

// parseBaseline 从 README 的行里取出声明值。
//
// 返回 present=false 表示**这个仓没有这张表**（合法：不声明就不失真）；
// 返回 error 表示表在那儿但读不出来（列被改、行被截断、数字不是数字）。
func parseBaseline(lines []string) (claim baseline, present bool, err error) {
	var heading bool
	var prodSeen, testSeen bool
	for _, line := range lines {
		if !heading && strings.Contains(line, baselineHeading) {
			heading = true
			continue
		}
		if m := prodRowRe.FindStringSubmatch(line); m != nil {
			present = true
			prodSeen = true
			if claim.prodLines, err = atoiThousands(m[1]); err != nil {
				return claim, true, fmt.Errorf("工程底座表的「生产代码」行数不是整数: %q", m[1])
			}
			if claim.prodFiles, err = atoiThousands(m[2]); err != nil {
				return claim, true, fmt.Errorf("工程底座表的生产文件数不是整数: %q", m[2])
			}
		}
		if m := testRowRe.FindStringSubmatch(line); m != nil {
			present = true
			testSeen = true
			if claim.testFiles, err = atoiThousands(m[1]); err != nil {
				return claim, true, fmt.Errorf("工程底座表的测试文件数不是整数: %q", m[1])
			}
		}
	}

	// 小节存在却一行都解析不出来 = 表结构变了，必须红。
	// 这条防的是「没有行就没有比对」：整表被截断时，判据会一声不吭地通过。
	if heading && !prodSeen && !testSeen {
		return claim, true, fmt.Errorf("README 里有「%s」小节，但解析不到「生产代码」与「测试」两行——"+
			"表结构变了（列被改/行被截断）？判定面认不出这两行时，这张表就重新变成散文", baselineHeading)
	}
	if present && (!prodSeen || !testSeen) {
		return claim, true, fmt.Errorf("工程底座表只解析到一半（生产代码=%v，测试=%v）："+
			"两行必须同时存在，否则剩下那行永远没人核对", prodSeen, testSeen)
	}
	return claim, present, nil
}

// atoiThousands 解析可能带千分位逗号的整数。
//
// 表里写 19,624 是给人读的，判据照收——但严格只认「数字与逗号」，
// 让「写错格式」与「写错数值」分得开。
func atoiThousands(s string) (int, error) {
	clean := strings.ReplaceAll(s, ",", "")
	return strconv.Atoi(clean)
}

// measureBaseline 按 README 里写明的口径实测三项。
//
// 口径就是那三条命令本身（复制过来、逐条对应）：
//
//	find . -name '*.go' -not -name '*_test.go' -not -path './.*' -not -path './vendor/*'
//	find . -name '*.go' -not -name '*_test.go' -not -path './.*' -not -path './vendor/*' -print0 | xargs -0 cat | wc -l
//	find . -name '*_test.go' -not -path './.*' -not -path './vendor/*'
//
// 逐条对应是为了让「门禁报的数」与「人按命令手算的数」不可能不一致——两者不一致时
// 人会怀疑门禁，而真正的错在口径。
func measureBaseline() (baseline, error) {
	var b baseline
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 隐藏目录（.git / .gomodcache-mavis / .*-work）与 vendor 不在口径内。
			if path != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "vendor") {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		if strings.HasSuffix(name, "_test.go") {
			b.testFiles++
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		b.prodFiles++
		// wc -l 数的是换行符个数，不是「行数」的直觉值（末行无换行时不算）。
		b.prodLines += bytes.Count(body, []byte("\n"))
		return nil
	})
	return b, err
}

// diffBaseline 比对声明值与实测值，逐项出问题描述。
//
// 逐项报而不是「合起来报一条」：三项一起漂时，只报第一项会让人改完一项仍是红。
func diffBaseline(claim, actual baseline) []string {
	var out []string
	add := func(label string, got, want int, recompute string) {
		if got == want {
			return
		}
		out = append(out, fmt.Sprintf(
			"工程底座表：%s README 写 %d，实际 %d（差 %+d）。百分数可以约，这三个数不能约——"+
				"确定性量**判相等**，不留容差（历史漂移量恰好落在容差里）。重算：%s",
			label, got, want, want-got, recompute))
	}
	add("生产代码行数", claim.prodLines, actual.prodLines,
		"find . -name '*.go' -not -name '*_test.go' -not -path './.*' -not -path './vendor/*' -print0 | xargs -0 cat | wc -l")
	add("生产代码文件数", claim.prodFiles, actual.prodFiles,
		"find . -name '*.go' -not -name '*_test.go' -not -path './.*' -not -path './vendor/*' | wc -l")
	add("测试文件数", claim.testFiles, actual.testFiles,
		"find . -name '*_test.go' -not -path './.*' -not -path './vendor/*' | wc -l")
	return out
}
