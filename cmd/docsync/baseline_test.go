package main

// 本文件补的是「工程底座」表判据（issue #31）的两侧：
// parseBaseline（把声明读出来）与 diffBaseline（与实测比）。
//
// # 为什么每一条都要成对
//
// 这条判据的失效形态有两种，方向相反：
//   - 该红的没红（数字漂了没人报）⇒ 表重新变成散文；
//   - 不该红的红了（差一行就报）⇒ 训练人忽略门禁，与常红等价。
// 只写「一致时通过」的用例，一个把比对整个跳过的实现照样全绿；只写「漂移时红」
// 的用例，一个见到数字就红（永不通过）的实现也全绿。故两者都要钉。

import (
	"path/filepath"
	"strings"
	"testing"
)

// baselineTableHead 表头（用例只写数据行，避免各处重复抄表头）。
const baselineTableHead = "### 工程底座\n\n| 指标 | 实测 |\n|---|---|\n"

// TestParseBaselineReadsBothRows 两行都读得出来，千分位逗号照收。
func TestParseBaselineReadsBothRows(t *testing.T) {
	lines := strings.Split(baselineTableHead+
		"| 生产代码 | 19,624 行 / 113 文件（不含 `_test.go`） |\n"+
		"| 测试 | 141 个测试文件 |\n", "\n")
	claim, present, err := parseBaseline(lines)
	if err != nil {
		t.Fatalf("合法表不该报错: %v", err)
	}
	if !present {
		t.Fatal("表存在时 present 必须为 true（false 表示「这个仓没声明」，会被跳过）")
	}
	want := baseline{prodFiles: 113, prodLines: 19624, testFiles: 141}
	if claim != want {
		t.Fatalf("解析结果 %+v，应为 %+v", claim, want)
	}
}

// TestParseBaselineAbsentSectionIsNotAnError 仓里没有这张表 = 没有声明，不判。
//
// 这条与下一条（有标题但没有行 ⇒ 红）互为反面。没有它，「不声明」会被判成
// 「声明错了」，任何没有这张表的宿主仓都会被 docsync 拦下来。
func TestParseBaselineAbsentSectionIsNotAnError(t *testing.T) {
	_, present, err := parseBaseline([]string{"# 一个 README", "| `alpha` | 3 | 无 | 示例 |"})
	if err != nil {
		t.Fatalf("没有工程底座表时不该报错: %v", err)
	}
	if present {
		t.Fatal("没有该表时 present 必须为 false")
	}
}

// TestParseBaselineHeadingWithoutRowsIsRed 小节在、行没了 = 表结构变了。
//
// 这是本条判据最容易静默变绿的地方：整表被截断或列被改之后，一行都解析不出来，
// 「没有行就没有比对」的实现会一声不吭地通过——而表恰好是在那时失真的。
func TestParseBaselineHeadingWithoutRowsIsRed(t *testing.T) {
	_, _, err := parseBaseline([]string{"### 工程底座", "| 指标 | 实测 |", "|---|---|"})
	if err == nil || !strings.Contains(err.Error(), "解析不到") {
		t.Fatalf("小节存在但行解析不出时必须报错，实得 %v", err)
	}
}

// TestParseBaselineHalfTableIsRed 只有一半的行同样红：剩下那行会永远没人核对。
func TestParseBaselineHalfTableIsRed(t *testing.T) {
	_, _, err := parseBaseline([]string{
		"### 工程底座",
		"| 生产代码 | 19,624 行 / 113 文件（不含 `_test.go`） |",
	})
	if err == nil || !strings.Contains(err.Error(), "只解析到一半") {
		t.Fatalf("缺「测试」行时必须报错，实得 %v", err)
	}
}

// TestDiffBaselineExactNoTolerance 确定性量判相等：差 1 也红。
//
// 这是与包表那 25% 容差**刻意不同**的地方。包表容差滤的是逐行涨落；这张表的
// 历史漂移量恰好落在容差里（895 / 573 / 578 行这些量，相对 1.9 万行都在 2~5%），
// 给它容差等于让「静默过期」这件事继续成立。
func TestDiffBaselineExactNoTolerance(t *testing.T) {
	claim := baseline{prodFiles: 113, prodLines: 19624, testFiles: 141}

	if got := diffBaseline(claim, claim); len(got) != 0 {
		t.Fatalf("完全一致时不得报告，实得 %v", got)
	}

	off := claim
	off.prodLines++ // 只差 1 行
	got := diffBaseline(claim, off)
	if len(got) != 1 {
		t.Fatalf("差 1 行必须报 1 项，实得 %d 项: %v", len(got), got)
	}
	if !strings.Contains(got[0], "生产代码行数") || !strings.Contains(got[0], "判相等") {
		t.Fatalf("报错必须点名是哪一项、并说明判相等，实得 %q", got[0])
	}
}

// TestDiffBaselineReportsEveryItem 三项同时漂时逐项报。
//
// 只报第一项会让人陷入「改一项、还是红、再改」的循环，而门禁本该一次说全。
func TestDiffBaselineReportsEveryItem(t *testing.T) {
	claim := baseline{prodFiles: 100, prodLines: 1000, testFiles: 100}
	actual := baseline{prodFiles: 101, prodLines: 1001, testFiles: 101}
	got := diffBaseline(claim, actual)
	if len(got) != 3 {
		t.Fatalf("三项都漂必须报 3 项（一次说全），实得 %d 项: %v", len(got), got)
	}
}

// TestMeasureBaselineMatchesFindCommand 实测口径必须与 README 里那三条命令一致。
//
// 用例自己按**同一口径**摆夹具：2 个生产文件（3 + 2 行）、1 个测试文件，
// 外加隐藏目录与 vendor 目录里的 .go——它们不该被计入。
// 口径不一致时人会怀疑门禁，而真正的错在口径。
func TestMeasureBaselineMatchesFindCommand(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "alpha", "a.go"), "package alpha\n\nvar X = 1\n") // 3 行
	mustWriteFile(t, filepath.Join(dir, "beta", "b.go"), "package beta\nvar Y = 2\n")     // 2 行
	mustWriteFile(t, filepath.Join(dir, "alpha", "a_test.go"), "package alpha\n")
	mustWriteFile(t, filepath.Join(dir, ".hidden", "h.go"), "package h\n")
	mustWriteFile(t, filepath.Join(dir, "vendor", "v.go"), "package v\n")
	t.Chdir(dir)

	got, err := measureBaseline()
	if err != nil {
		t.Fatal(err)
	}
	want := baseline{prodFiles: 2, prodLines: 5, testFiles: 1}
	if got != want {
		t.Fatalf("实测 %+v，应为 %+v（口径：排除隐藏目录与 vendor，行数按换行符计）", got, want)
	}
}

// TestBaselineRowRegexDoesNotSwallowOtherRows 只认「生产代码」与「测试」两行。
//
// 「生产代码」这条正则若写成宽松的「以 | 开头、含数字」，覆盖率那行
// （「87.0%」）也会被吞进来当行数——而覆盖率正是**刻意不判**的那一格
// （它依赖跑的是哪个用例集合，是测量不是声明）。
func TestBaselineRowRegexDoesNotSwallowOtherRows(t *testing.T) {
	lines := []string{
		"### 工程底座",
		"| 生产代码 | 19,624 行 / 113 文件（不含 `_test.go`） |",
		"| 测试 | 141 个测试文件 |",
		"| 覆盖率 | **87.0%**（全局门槛 60%） |",
		"| 结构阈值 | `max_file_lines: 400` |",
	}
	claim, present, err := parseBaseline(lines)
	if err != nil || !present {
		t.Fatalf("合法表不该报错，实得 present=%v err=%v", present, err)
	}
	want := baseline{prodFiles: 113, prodLines: 19624, testFiles: 141}
	if claim != want {
		t.Fatalf("覆盖率/阈值行不得被吞进声明值，实得 %+v", claim)
	}
}
