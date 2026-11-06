// Command pkgcov 是 cover-gate 的**逐包**判据执行体。
//
// # 为什么判据要落在逐包而不是只看全局
//
// 全局 85.5% 曾同时掩盖着 cmd/docsync 23.6% 与 sandbox 60.0%。全局阈值量的是
// 「整体没退步」，它对单个包的塌方完全不敏感——大盘涨 1% 就能把一个包从 70 掉到 20
// 的塌方抵消掉，而报告里只有一行「85.5%，绿」。两个判据判的是两件事，不能互相代替。
//
// # 为什么从 awk 改成 Go（issue #36）
//
// 判据本体此前是一份 80 行的 awk 脚本，`scripts/` 下只有它一个文件，没有任何用例
// 引用它——检出能力只能靠手工在 /tmp 合成日志验证。而它首版就出过一次真 bug：
// `coverage: [no statements]` 被 `pct+0` 转成 0%，纯接口包（contract）当场被判红。
//
// 假红还能靠「跑一次全量」发现；反方向（提前 exit 0 / 正则漏一类行）则没有任何
// 信号——make check 照样全绿、照样打印「逐包覆盖率下限 70% ✓」。而本仓纪律是
// 「门禁自己也要能被检查」「改任何门禁判定逻辑，必须同时给『坏实现会红』的用例」。
// awk 做不到：它的判定逻辑与 shell 调用面糊在一起，没法表驱动测。
//
// # 三条判据（判的是三件事，故都要跑）
//
//  1. 已计入的包低于下限             ⇒ 红（补该包的用例，别降阈值）
//  2. 豁免包自己达标了               ⇒ 红（豁免失效，必须当天拆除）
//  3. 包没有任何测试文件             ⇒ 红（见下）
//
// 第 3 条对应 issue #30：`go test` 对无测试文件的包输出 `? <pkg> [no test files]`，
// 旧判据跳过这类行——这是**刻意**的（它不是「0% 覆盖」，是「不适用」），但后果是
// **没有测试文件的包对逐包下限完全隐形**：新增包若整包不写测试，判据一声不吭。
// 判据分不出「合理的零测试」与「漏写测试」，那就别假装分得出——要求**显式声明**：
// 要么补测试，要么在 PKG_NO_TEST_EXEMPT 登记并写清为什么不需要（同样失效即红）。
//
// # 豁免的纪律
//
// 豁免一旦写成一次性的空条文就会永久留着——半年后没人记得它当初为什么存在，
// 也没人敢删。故两条豁免都「失效即红」：被豁免的对象自己达标了，就必须当天删掉
// 那一行。这与 architecture-policy.yaml 的 exceptions 必须写 until 是同一套口径：
// **豁免可以有，但不能静默，也不能无期限。**
//
// # 文件切分（按判据的一侧，不按行数）
//
//   - parse.go：`go test` 输出 → 事实（形态逐字照抄仪器输出，见该文件头注）
//   - judge.go：事实 + 阈值 + 豁免 → 报告
//   - 本文件：命令行装配（`run` 返回退出码而不是自己 os.Exit，用例才够得着）
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// run 是命令行主体。
//
// 返回退出码而不是在内部 os.Exit：这不是风格问题——判据里最贵的三条（空输入、
// 格式漂移、低于下限）都只体现为「退出码非零」，而 os.Exit 会把它们挡在用例之外。
// 本仓对这条的表述是「停机判据是有接缝吗，不是要不要先重构」：留一个 stdin/stdout
// 注入点，比事后为「命令行测不了」开一条豁免便宜一个量级。
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pkgcov", flag.ContinueOnError)
	fs.SetOutput(stderr)
	min := fs.Float64("min", 70, "逐包覆盖率下限（百分比）")
	exempt := fs.String("exempt", "", "覆盖率豁免：包路径后缀，逗号分隔")
	noTestExempt := fs.String("no-test-exempt", "", "零测试文件豁免：包路径后缀，逗号分隔")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	results, err := parse(stdin)
	if err != nil {
		reportf(stderr, "pkgcov: 读取 go test 输出失败: %v\n", err)
		return 1
	}
	results = dedupe(results)
	if len(results) == 0 {
		// 空输入不是「全绿」，是「判据没跑起来」——必须响亮失败，
		// 否则管道接错（例如 go test 提前失败）会让门禁静默变绿。
		reportf(stderr, "pkgcov: 输入里没有任何 go test 结果行——判据没跑起来（管道接错？）\n")
		return 1
	}

	o := options{min: *min, exempt: splitList(*exempt), noTestExempt: splitList(*noTestExempt)}
	r := judge(results, o)
	if !r.ok() {
		reportf(stderr, "%s", r.String(o))
		return 1
	}
	reportf(stdout, "%s", r.String(o))
	return 0
}

// reportf 往 w 写报告并**显式丢弃**写入错误。
//
// 报告面就是最终输出：写不出去也没有可做的事，而 `_, _ =` 直接写在四个调用点
// 只会让人以为是漏了处理。故统一成一个小函数，并在这里把「为什么不管」说清楚
// （errcheck 的口径是可配置的，判据对它让步只能靠显式表达，不能靠关规则）。
func reportf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
