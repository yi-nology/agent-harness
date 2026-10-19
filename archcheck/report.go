// report.go —— 结果的人类可读报告。

// 单独成文件的原因：报告与判定是两件事。判定产出结构化结果（Result/Violation），
// 报告只负责把它变成人能读的文本。把两者混在一个文件里，改判定时会被迫读一遍
// 格式化细节，改报告时又会被迫读一遍 AST 判定——而这两件事的失败形态完全不同
// （前者是判错，后者是看不懂）。

package archcheck

import (
	"fmt"
	"io"
)

// WriteReport 把结果写成人类可读的报告。
//
// 报告形状与「哪些规则在什么范围内」无关——三类规则（rules 段正则、facades 段、
// modules 段 requires）的回显形状完全一样，复制三份意味着以后改截断口径要记得改三处。
//
// 返回写入错误：报告写了一半就失败（管道提前关闭、磁盘满），调用方需要知道
// 「这份报告不完整」——否则它会拿一份残缺的报告当结论。
func WriteReport(w io.Writer, res *Result, opts ReportOptions) error {
	if res == nil {
		return nil
	}
	rw := &reportWriter{w: w}
	if len(res.Violations) > 0 {
		switch {
		case res.Blocking > 0:
			rw.printf("架构门禁失败：%d 处阻断级违反", res.Blocking)
		case res.Warnings == 0 && res.Excused > 0:
			rw.printf("架构门禁：%d 处命中已登记例外，无未豁免的违反", res.Excused)
		default:
			rw.printf("架构门禁：%d 处警告级（不阻断）", res.Warnings)
		}
		if res.Warnings > 0 && res.Blocking > 0 {
			rw.printf("，另有 %d 处警告级", res.Warnings)
		}
		if res.Excused > 0 {
			rw.printf("，%d 处命中已登记例外（policy exceptions）", res.Excused)
		}
		rw.println()

		// 只回显**未被豁免**的违反：已命中例外的那些不该再出现在清单里，
		// 否则例外登记看起来像没生效。
		byRule := map[string][]Violation{}
		for _, v := range res.Violations {
			if res.excused[exceptionKey(v.Rule, v.Path, v.Line)] {
				continue
			}
			byRule[v.Rule] = append(byRule[v.Rule], v)
		}
		for _, ri := range res.Rules {
			vs := byRule[ri.ID]
			if len(vs) == 0 {
				continue
			}
			rw.printRuleViolations(ri.ID, ri.Severity, ri.Desc, vs, opts.Full)
		}
	}

	// 已登记例外回显：债是显式的，不静默。
	var shown int
	for _, ex := range res.Exceptions {
		if shown >= 3 && !opts.Full {
			rw.printf("\n已登记例外 %d 条（全量报告含全部），例：", len(res.Exceptions))
		}
		if shown < 3 || opts.Full {
			rw.printf("\n  %s:%d [%s] %s —— %s（拆除条件：%s）",
				ex.Path, ex.Line, ex.Rule, ex.Violation, firstLine(ex.Reason), firstLine(ex.Removal))
		}
		shown++
	}
	if shown > 0 {
		rw.println()
	}

	// 已到期例外：明确回显「它不再豁免」，不静默失效。
	for _, e := range res.ExpiredExceptions {
		rw.printf("\n  ⚠ 例外已到期，不再豁免：%s", e)
	}
	if len(res.ExpiredExceptions) > 0 {
		rw.println()
	}

	if len(res.ThresholdFindings) > 0 {
		verb := "报告口径，不阻断"
		if res.ThresholdEnforced {
			verb = "阻断"
		}
		rw.printf("\n结构阈值%s:\n", verb)
		for _, v := range res.ThresholdFindings {
			rw.printf("  - %s\n", v)
		}
		if res.ThresholdEnforced {
			rw.println("\n阈值超限：结构债已超阈值，先清偿或显式上调阈值再提交。")
		}
	}
	return rw.err
}

// reportWriter 收集报告写入过程中的第一个错误。
//
// 为什么不在每处 Fprintf 上单独判错：报告有二十几处写入，逐处判错会把
// 真正的失败埋在一堆 `if err != nil` 里。统一收集、末尾返回，既不漏也不噪。
type reportWriter struct {
	w   io.Writer
	err error
}

func (rw *reportWriter) printf(format string, args ...any) {
	if rw.err != nil {
		return
	}
	_, rw.err = fmt.Fprintf(rw.w, format, args...)
}

func (rw *reportWriter) println(args ...any) {
	if rw.err != nil {
		return
	}
	_, rw.err = fmt.Fprintln(rw.w, args...)
}

// printRuleViolations 回显一条规则的违反清单；full 为真时看全量，否则截断到 10 条。
func (rw *reportWriter) printRuleViolations(id, sev, desc string, vs []Violation, full bool) {
	if len(vs) == 0 {
		return
	}
	rw.printf("\n规则 %s [%s]：%s（%d 处）\n", id, sev, desc, len(vs))
	limit := len(vs)
	if !full && limit > 10 {
		limit = 10
	}
	for _, v := range vs[:limit] {
		rw.printf("  %s:%d  [%s] %s\n", v.Path, v.Line, v.Kind, v.Text)
	}
	if !full && len(vs) > limit {
		rw.printf("  … 另有 %d 处（全量报告含全部）\n", len(vs)-limit)
	}
}
