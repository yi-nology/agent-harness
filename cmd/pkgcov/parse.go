// parse.go —— `go test` 输出的解析面：把仪器输出变成**事实**。
//
// # 形态必须照抄仪器，不能照抄文档
//
// 本命令第一版把「无测试文件」认成 `? <pkg> [no test files]`——那确实是真形态，
// 但它只在**不带** `-coverprofile` 时出现。cover-gate 的管道带 `-coverprofile`，
// 此时同一个包变成一行**无动词**的 `<pkg>\t\tcoverage: 0.0% of statements`。
//
// 后果不是「偶尔漏一个包」，而是**零测试包整体失明**：`?` 分支一次都没命中，
// PKG_NO_TEST_EXEMPT 里那两条豁免于是被判「失效（该包已有测试或不存在）」——
// 门禁红在「豁免该拆了」，而真相是它压根没看见那两个包。假红比静默好，但两者
// 都是判据没对齐现实；而这类偏差只会出现在**真跑过一遍**之后。
//
// 故这一面的纪律：形态照抄仪器输出的字节，并写成用例。夹具若比现实好看，
// 判据就在测一个不存在的世界。
//
// # 四种行形态（前两条是「有测试」的两种，后两条是「无测试」的两种）
//
// \tok  <pkg>\t<time>\tcoverage: 85.5% of statements   ⇒ 可测且有覆盖率
// \tok  <pkg>\t<time>\tcoverage: [no statements]        ⇒ 可测但无语句（不适用）
// \t<pkg>\t\tcoverage: 0.0% of statements              ⇒ 无测试文件（-coverprofile 模式）
// \t?   <pkg>\t[no test files]                        ⇒ 无测试文件（不带 -coverprofile）
//
// # 不认识的形态怎么办：认识就认，不认识且**长得像**覆盖率事实才红
//
// 制表符开头的行有两种合法来源，不能一刀切成「形态漂移」：
//
//   - 带 coverage 列的无动词行           ⇒ 无测试文件（认它，见上表第 3 条）；
//   - 测试自身的日志 / 编译告警的续行（`t.Log` 也可能带制表符）⇒ 又不是覆盖率事实，
//     必须**放过**——把日志当漂移会让任何一条 `t.Log` 变成门禁的假红。
//
// 两条**报错**（而不是跳过）的是：
//
//   - `ok` 行没有 `coverage:` 列 ⇒ 调用面少传了 `-cover`。此时每个包都会变成
//     「无覆盖率数值」而判据一声不吭——这是最危险的一种静默；
//   - 其余行里出现 `coverage:` 字样 ⇒ 形态漂移（例如新加了别的动词）。
//
// 跳过 = 变绿。门禁的失败形态必须是变红；但假红也会训练人忽略门禁，故「长得像」
// 这个限定是刻意的：判据只在**能证明自己在看覆盖率事实**时才拒绝前进。
package main

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// pctRe 覆盖率数值的形态：`85.5%` / `100.0%` / `0.0%`。
//
// 必须校验形态而不是直接 `ParseFloat` 兜底：`coverage: [no statements]` 的下一个
// token 是 `[no`，宽松解析会把它当「0%」——纯接口包（contract）因此被判红过。
// 这类「不适用被误判成 0」是本命令唯一踩过的真 bug，故单独立成一个正则。
var pctRe = regexp.MustCompile(`^[0-9]+(?:[.][0-9]+)?%$`)

// pkgResult 一个包的测试输出摘要。
type pkgResult struct {
	// pkg 包的 import 路径（go test 输出里那一列）。
	pkg string
	// coverage 语句覆盖率（百分比数值，不含 %）。
	coverage float64
	// hasCoverage false 表示这一行带测试但没带覆盖率数值（`[no statements]`）。
	hasCoverage bool
	// noTestFiles true 表示这个包没有任何测试文件（两种形态都归到这里）。
	noTestFiles bool
}

// coverageToken 覆盖率列的字面量。
const coverageToken = "coverage:"

// parse 解析 `go test ./... -cover` 的输出。
//
// 判的是覆盖率，不是「测试有没有通过」——后者是 test 目标的事，故 `--- FAIL`
// 这类行一律忽略。但「无法归类的 coverage 行」必须报错，见文件头。
func parse(r io.Reader) ([]pkgResult, error) {
	var out []pkgResult
	sc := bufio.NewScanner(r)
	// go test 的单行输出可能很长（包路径 + 时间 + 覆盖率），放宽到 1MB。
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch {
		case fields[0] == "?":
			// 不带 -coverprofile 时「无测试文件」的形态。
			if len(fields) < 2 {
				continue
			}
			out = append(out, pkgResult{pkg: fields[1], noTestFiles: true})
		case fields[0] == "ok":
			if len(fields) < 2 {
				continue
			}
			res, found, err := coverageOf(fields[1], fields[2:])
			if err != nil {
				return nil, err
			}
			if !found {
				return nil, fmt.Errorf("ok 行没有 coverage: 列（go test 少传了 -cover？）: %q", line)
			}
			out = append(out, res)
		case strings.HasPrefix(line, "\t"):
			// 制表符开头的行有两个合法来源，必须分开处理：
			//
			//  1. `-coverprofile` 模式下「该包没有测试文件」的形态（带 coverage 列）；
			//  2. 测试自身的日志输出 / 编译告警的续行（没有 coverage 列）。
			//
			// 第 1 种是覆盖率事实：判据只看「有没有测试文件」，行里的 0.0% 不算覆盖率
			// （没跑任何测试，覆盖率必然是 0，报它没有意义）。
			//
			// 第 2 种必须**放过**——把日志当形态漂移，会让任何一条 t.Log 变成门禁的
			// 假红，而假红会训练人忽略门禁。故这里的口径是：认识就认，不认识且**长得
			// 像**覆盖率事实（含 coverage: 字样）才报错。
			if pkg, ok := noTestLine(fields); ok {
				out = append(out, pkgResult{pkg: pkg, noTestFiles: true})
				continue
			}
			if strings.Contains(line, coverageToken) {
				return nil, fmt.Errorf("无法归类的覆盖率行（go 输出形态变了？）: %q", line)
			}
		case strings.Contains(line, coverageToken):
			// 形态漂移：认识不了就别装作没看见——跳过即变绿。
			return nil, fmt.Errorf("无法归类的覆盖率行（go 输出形态变了？）: %q", line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// coverageOf 从一行的剩余字段里取覆盖率。
//
// found=false 表示这一行**根本没有** `coverage:` 列（调用面可能少传了 -cover），
// 与「有列但值是 `[no statements]`（不适用）」是两件事，必须分开表达：
// 前者是判据没跑对，后者是判据正常工作的结论。
func coverageOf(pkg string, rest []string) (res pkgResult, found bool, err error) {
	res.pkg = pkg
	for i := 0; i < len(rest)-1; i++ {
		if rest[i] != coverageToken {
			continue
		}
		tok := rest[i+1]
		if !pctRe.MatchString(tok) {
			// `[no statements]`：包可测但无语句（纯接口面）。
			// 不是 0% 覆盖，是「不适用」⇒ 不参与判定，也不算违规。
			return res, true, nil
		}
		v, perr := strconv.ParseFloat(strings.TrimSuffix(tok, "%"), 64)
		if perr != nil {
			return res, true, fmt.Errorf("包 %s 的覆盖率 %q 解析失败: %w", pkg, tok, perr)
		}
		res.coverage, res.hasCoverage = v, true
		return res, true, nil
	}
	return res, false, nil
}

// noTestLine 判定一条制表符开头的行是不是「无测试文件」的覆盖率行。
//
// 形态锁得比「能解析出覆盖率」更死：`\t<pkg>\t\tcoverage: <pct> of statements`，
// 五个字段逐字对齐。松一点（例如只找 coverage: 列）会把测试自身的日志当成包，
// 代价是「凭空多出一个零测试包」——于是门禁要求为它补一条豁免，而人去找那个
// 不存在的包。这类假红的成本比它拦下的东西高得多。
func noTestLine(fields []string) (string, bool) {
	if len(fields) != 5 || fields[1] != coverageToken || fields[3] != "of" || fields[4] != "statements" {
		return "", false
	}
	if !pctRe.MatchString(fields[2]) {
		return "", false
	}
	return fields[0], true
}

// dedupe 同一个包只留一条事实，`ok`（有测试结果）优先。
//
// 为什么需要：两条判据对同一份 input 各数一次（覆盖率计入数、零测试豁免命中数），
// 同一个包出现两次会让「1 个豁免生效」变成「2 个」。真跑 go test 时不会出现
// （`ok` 与无动词行互斥），但判据不该依赖「仪器保证不重叠」这种未写下的前提。
func dedupe(results []pkgResult) []pkgResult {
	seen := make(map[string]int, len(results))
	out := make([]pkgResult, 0, len(results))
	for _, res := range results {
		if i, ok := seen[res.pkg]; ok {
			// 已有条目是「无测试」而新条目带测试结果 ⇒ 以带测试结果为准。
			if out[i].noTestFiles && !res.noTestFiles {
				out[i] = res
			}
			continue
		}
		seen[res.pkg] = len(out)
		out = append(out, res)
	}
	return out
}
