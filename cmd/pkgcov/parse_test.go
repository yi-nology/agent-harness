package main

import (
	"strings"
	"testing"
)

// 用例的组织方式（本仓门禁用例纪律）：
//
// 每条判据都要**成对**：能放行的形态绿、该拦的形态红。只有「好实现能过」的用例
// 等于没钉住——一个把比较写反的实现照样能让「达标包」全绿，直到某天真有人掉下去。
//
// 夹具的形态必须**逐字**照抄真实 `go test` 输出（见 parse.go 头注的四形态表）：
// 用空格代替制表符、或只写自己以为存在的那种形态，判据就在测一个不存在的世界。

// covLine 造一条「有测试」的真实形态行。
//
// `ok` 行必须**带 coverage 列**（少传 -cover 会被判红），故合成夹具不能只写
// `ok pkg time`——那会红在夹具自己身上，而不是红在被测代码上。
func covLine(pkg, pct string) string {
	return "ok  \t" + pkg + "\t0.012s\tcoverage: " + pct + " of statements"
}

// noTestFilesLineCoverprofile `-coverprofile` 模式下「无测试文件」的真实形态：
// **无动词**、以制表符开头、覆盖率恒为 0.0%。
//
// 这行是本命令第二版才认出来的形态。第一版只认 `? <pkg> [no test files]`，于是
// 在 cover-gate 的真实管道里，零测试包**整体失明**（详见 parse.go 头注）。
func noTestFilesLineCoverprofile(pkg string) string {
	return "\t" + pkg + "\t\tcoverage: 0.0% of statements"
}

// noTestFilesLinePlain 不带 `-coverprofile` 时的形态。
func noTestFilesLinePlain(pkg string) string {
	return "?   \t" + pkg + "\t[no test files]"
}

// syntheticLog 覆盖四形态 + 两类噪声行。
var syntheticLog = strings.Join([]string{
	covLine("example.com/m/alpha", "88.0%"),
	covLine("example.com/m/beta", "64.5%"),
	"ok  \texample.com/m/contract\t0.003s\tcoverage: [no statements]",
	noTestFilesLineCoverprofile("example.com/m/cmd/tool"),
	noTestFilesLineCoverprofile("example.com/m/cmd/fixture"),
	noTestFilesLinePlain("example.com/m/cmd/plain"),
	"--- FAIL: TestSomething (0.00s)",
	"\t\tsome_test.go:12: 用例自己打印的日志（制表符开头，但不是覆盖率事实）",
}, "\n")

// parseLog 解析合成日志（用例共用）。
func parseLog(t *testing.T) []pkgResult {
	t.Helper()
	res, err := parse(strings.NewReader(syntheticLog))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	return dedupe(res)
}

// TestParseNoStatementsIsNotZero 是**回归用例**：`coverage: [no statements]` 曾
// 被宽松解析成 0%，于是无语句的包（纯契约面）当场被判红。
//
// 这条判据的错误方向很贵：它把「不适用」当成「最差」，于是一个无语句的包永远
// 达不到下限、永远红，而修法只剩「给它加语句」这种破坏契约面的动作。
func TestParseNoStatementsIsNotZero(t *testing.T) {
	for _, r := range parseLog(t) {
		if r.pkg != "example.com/m/contract" {
			continue
		}
		if r.hasCoverage {
			t.Fatalf("[no statements] 被解析成覆盖率 %.1f%%（应为「不适用」）", r.coverage)
		}
		if r.noTestFiles {
			t.Fatal("contract 有测试文件，不该被判成零测试包")
		}
		return
	}
	t.Fatal("合成日志里没有 contract 包")
}

// TestParseRealShapes 锁死「四形态都能认」。
//
// 变异验证：把 `strings.HasPrefix(line, "\t")` 那条分支删掉，本用例立刻红在
// 「应识别 3 个零测试包」——而那正是这一版修掉的真 bug。
func TestParseRealShapes(t *testing.T) {
	res := parseLog(t)
	if len(res) != 6 {
		t.Fatalf("应解析 6 个包（3 有测试 + 3 无测试），实际 %d: %+v", len(res), res)
	}
	noTest := map[string]bool{}
	for _, r := range res {
		if r.noTestFiles {
			noTest[r.pkg] = true
		}
	}
	for _, pkg := range []string{
		"example.com/m/cmd/tool",    // -coverprofile 形态
		"example.com/m/cmd/fixture", // -coverprofile 形态
		"example.com/m/cmd/plain",   // 不带 -coverprofile 形态
	} {
		if !noTest[pkg] {
			t.Errorf("%s 应被判成零测试包，实际未识别", pkg)
		}
	}
	if len(noTest) != 3 {
		t.Fatalf("应识别 3 个零测试包，实际 %d: %v", len(noTest), noTest)
	}
}

// TestParseRejectsShapeDrift 锁死「认不出来就红」。
//
// 三种漂移各自防一件事：
//
//  1. 制表符行带 coverage: 但字段对不上（少 of statements）⇒ 无测试包那一形态变了。
//     注意「制表符开头但没有 coverage 列」**不在**这里：那是测试自身的日志输出，
//     必须放过（见 parse.go 头注）；
//  2. `ok` 行没有 coverage 列 ⇒ 调用面少传 -cover。此时每个包都会变成「无覆盖率
//     数值」而判据一声不吭——这是最危险的一种静默，故必须红；
//  3. 其余行里出现 coverage: ⇒ 形态漂移（例如新加了 `?` 之外的动词）。
func TestParseRejectsShapeDrift(t *testing.T) {
	cases := map[string]string{
		"tab 行少了 of statements": "\texample.com/m/x\t\tcoverage: 50.0%",
		"ok 行没有 coverage 列":     "ok  \texample.com/m/x\t0.010s",
		"陌生动词带 coverage 列":      "skip\texample.com/m/x\tcoverage: 50.0% of statements",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(strings.NewReader(line)); err == nil {
				t.Fatalf("应报错（跳过即变绿），实际放行: %q", line)
			}
		})
	}
}

// TestDedupeKeepsCoverage 锁死去重方向：`ok`（有测试结果）优先于无动词行。
//
// 去重不是洁癖：两条判据会对同一个包各数一次，重复条目会让「1 个豁免生效」变 2。
func TestDedupeKeepsCoverage(t *testing.T) {
	res := dedupe([]pkgResult{
		{pkg: "m/a", noTestFiles: true},
		{pkg: "m/a", coverage: 90, hasCoverage: true},
		{pkg: "m/b", coverage: 91, hasCoverage: true},
		{pkg: "m/b", noTestFiles: true},
		{pkg: "m/c", noTestFiles: true},
	})
	if len(res) != 3 {
		t.Fatalf("应去重成 3 个包，实际 %d: %+v", len(res), res)
	}
	if res[0].noTestFiles || !res[0].hasCoverage {
		t.Fatalf("m/a 应以 ok 行（有覆盖率）为准，实际: %+v", res[0])
	}
	if res[1].noTestFiles {
		t.Fatalf("m/b 已有覆盖率事实，不该被后到的无动词行覆盖: %+v", res[1])
	}
	if !res[2].noTestFiles {
		t.Fatalf("m/c 是零测试包: %+v", res[2])
	}
}

// TestEmptyInputParsesNothing 锁死「空输入不是解析错误」。
//
// 空输入的处理在 run 里（非零退出码）：判据没跑起来必须响亮失败，见 run_test.go。
func TestEmptyInputParsesNothing(t *testing.T) {
	res, err := parse(strings.NewReader(""))
	if err != nil {
		t.Fatalf("空输入不应报解析错误: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("空输入应解析出 0 个包，实际 %d", len(res))
	}
}
