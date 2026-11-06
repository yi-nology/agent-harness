// examples.go —— 规则的样例自检：把「这条规则还活着吗」变成**加载期**的断言。
//
// # 它补的是哪一类失效
//
// 本仓的纪律有一条是「从没见过它触发的规则等于没有规则」。前半句已经由
// selfcheck.go 兑现（规则必须有执行体、id 唯一、阈值判定面覆盖全部包），
// 但那解决的是「规则**有没有**执行体」，不解决「执行体**还判得动**吗」。
//
// 后者的失效形态更安静：
//
//   - 词表规则里的词被重命名（`运维` → `运营`），正则照样编译、照样跑，
//     只是**再也命中不到任何东西**；
//   - import 规则里的 module 路径迁走（私有域名 → github.com），前缀不再匹配，
//     于是宿主的依赖禁令**整整半年没生效过**；
//   - negative_follow 的锚点被格式化工具改写一个字，豁免面就整体错位——
//     该豁免的被报成违规（假红），或该报的躲进豁免（假绿）。
//
// 三种都不是「规则写错了」，是**规则与事实脱钩而无人察觉**。测试能证明
// 「今天它还判得动」，但测试跑在 CI 里、跑一次；而门禁跑在**每一个**复用
// 这份判据的仓、**每一次** `make architecture-check`。故断言要落在这里。
//
// # 判据必须复用真实判定核（不是「另写一套近似判定」）
//
// 前一版 matchSample 把 checkRule / importViolations 的判定顺序**逐行复刻**
// 了一遍，并在注释里自称「复用真实判定路径」。那不是复用，是**第二份真相**：
// 两套实现各自漂移，而样例那套只看几个字符串、漂得更慢，于是绿灯更久——
// 恰恰是「从没见过它触发的规则等于没有规则」最隐蔽的形态。
//
// 现在样例与生产判定**共用同一个判定核**（结构上不可能不一致）：
//
//	forbid_regex  规则 → regexViolationsIn  （checkRule 的判定核，见 scan.go）
//	forbid_import 规则 → importViolationsIn （importViolations 的判定核，见 imports.go）
//
// 于是同一条正则、同一条注释豁免、同一段 negative_follow 中和、同一份 AST
// import 遍历，样例与门禁用的是同一份代码。
//
// # Want 的取值域必须枚举校验
//
// 把 want 写成 `hitt` 时，若实现按「非 hit 即 miss」处理，一条彻底坏掉的
// 规则会因为「反例全都通过」而绿得理直气壮。故拼错即报错。
//
// # 正反两个方向各需至少一条
//
// 只有反例的规则只能证明「判据不乱报」，证明不了「判据还判得动」——
// 词表被改名之后它照样绿。只有正例的规则反过来：判据放宽（`^errors$` 松成
// `errors`）无从发现。两个方向各一条，才构成一次**判定**。

package archcheck

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// sampleFileName 给样例一个与规则判定面**同形态**的虚拟文件名。
//
// 名字不参与判定（判定面由 OnlyTest / IgnoreTest 在 ruleFiles 里决定），
// 但它会出现在违规回显与 parser 的位置信息里。取 sample_test.go 是为了让
// 读者一眼看出「这是一条测试面上的样例」——而不是去仓里找一个叫 sample.go 的文件。
func sampleFileName(r Rule) string {
	if r.OnlyTest {
		return "sample_test.go"
	}
	return "sample.go"
}

// sampleViolations 用**真实判定核**判一条样例，返回它命中的违规。
//
// 返回值是完整的 []Violation 而不是 bool：调用方要能回显「判在哪里、判成什么」，
// 否则样例失配时只能看到「正例没判中」，看不到判据把它判成了什么。
func (r Rule) sampleViolations(text string) ([]Violation, error) {
	if r.ForbidImport != "" {
		re, err := regexp.Compile(r.ForbidImport)
		if err != nil {
			return nil, fmt.Errorf("规则 %s forbid_import_regex 非法: %w", r.ID, err)
		}
		// import 判定的对象是**语法树里的 import 路径**，不是一段字符串。
		// 若样例直接把裸路径丢给正则，那么「别名 / 分组 / 点 / 空白导入」这些
		// 形态的处理差异全在 AST 那一侧——样例照样绿，真判定却可能早就不认那些形态。
		// 故先包成最小合法源码，再走同一条 AST 判定。
		//
		// 用 strconv.Quote 而不是手拼引号：样例文本来自 policy，里面出现 `"`、
		// `\` 或非 ASCII 时手拼会造出**语法坏掉的样例文件**，而解析失败要求门禁
		// 以「给不出结论」收尾——那是把一条笔误升级成一次全局红。
		src := "package sample\n\nimport _ " + strconv.Quote(text) + "\n"
		return importViolationsIn(r, re, sampleFileName(r), []byte(src))
	}
	if r.ForbidRegex == "" {
		// 没有执行体的规则由 selfcheck.go 单独报（口径只有一处）。
		return nil, fmt.Errorf("规则 %s 没有执行体，样例无从判定", r.ID)
	}
	re, err := regexp.Compile(r.ForbidRegex)
	if err != nil {
		return nil, fmt.Errorf("规则 %s forbid_regex 非法: %w", r.ID, err)
	}
	return regexViolationsIn(r, re, sampleFileName(r), text), nil
}

// exampleViolations 判一条规则的样例是否与判据一致，返回人话问题清单。
//
// 只返回文本，位置（path/line）由 selfcheck 统一填——样例的坐标在 policy 里，
// 而 policy 的行号本仓不解析（体系结构上不做 YAML 行号映射）。
func exampleViolations(r Rule) []string {
	var out []string
	seen := make(map[string]bool, len(r.Examples))
	for _, ex := range r.Examples {
		text := ex.Text
		if strings.TrimSpace(text) == "" {
			out = append(out, fmt.Sprintf("规则 %q 有一条样例的 text 为空（空样例判什么都说明不了）", r.ID))
			continue
		}
		want := ex.Want
		if want != exampleWantHit && want != exampleWantMiss {
			out = append(out, fmt.Sprintf(
				"规则 %q 的样例 %q 的 want 是 %q（只接受 %s / %s）："+
					"拼错的期望值会被当成「非 hit 即 miss」，于是判据坏掉的规则因为反例全过而变绿",
				r.ID, text, want, exampleWantHit, exampleWantMiss))
			continue
		}
		// 同一条样例重复登记且期望不同 ⇒ 自相矛盾，直接报（不要靠「后写覆盖」）。
		key := want + "\x00" + text
		if seen[key] {
			out = append(out, fmt.Sprintf("规则 %q 的样例 %q 重复登记（期望 %q）", r.ID, text, want))
			continue
		}
		seen[key] = true

		vs, err := r.sampleViolations(text)
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		hit := len(vs) > 0
		if want == exampleWantHit && !hit {
			out = append(out, fmt.Sprintf(
				"规则 %q 的正例 %q **没有**被判违规——判据与它自己的声明已经脱钩（规则还挂着 error，却判不动任何东西）",
				r.ID, text))
		}
		if want == exampleWantMiss && hit {
			out = append(out, fmt.Sprintf(
				"规则 %q 的反例 %q **被判成**违规（%s）——判据比它声明的宽（这类误判会让人学会忽略门禁输出）",
				r.ID, text, vs[0].Text))
		}
	}
	return out
}

// exampleCoverageProblems 判一条规则的样例是否**双向**覆盖（正例 + 反例各至少一条）。
//
// # 为什么「有样例」不等于「样例够」
//
// 只有反例（want: miss）的规则仍然绿着，而且绿得很有说服力：它证明「判据不乱报」。
// 它**完全没有**证明「判据还判得动」——一条词表被改名、module 路径被迁走的规则，
// 靠几条「不该命中」的样例可以永远保持绿灯。
//
// 只有正例的规则是另一个方向的洞：判据放宽（`^errors$` 松成 `errors`，
// import_exempt 写错一栏）时正例照样命中，而过度命中的代价是**所有人学会忽略门禁**。
//
// 故两个方向各需至少一条。这不是「样例越多越好」，是**一正一反才构成一次判定**。
func exampleCoverageProblems(r Rule) []string {
	if r.ForbidRegex == "" && r.ForbidImport == "" {
		return nil // 无执行体由 selfcheck 的另一条口径报，不在这里重复
	}
	var hit, miss int
	for _, ex := range r.Examples {
		switch ex.Want {
		case exampleWantHit:
			hit++
		case exampleWantMiss:
			miss++
		}
	}
	var out []string
	if hit == 0 {
		out = append(out, fmt.Sprintf(
			"规则 %q 没有一条 want: %s 的样例：它只能证明「判据不乱报」，"+
				"证明不了「判据还判得动」——词表被改名 / module 路径被迁走之后，"+
				"这条规则会静静地不再命中，而样例全绿",
			r.ID, exampleWantHit))
	}
	if miss == 0 {
		out = append(out, fmt.Sprintf(
			"规则 %q 没有一条 want: %s 的样例：判据放宽不会被发现——"+
				"过度命中的代价是所有人学会忽略门禁输出",
			r.ID, exampleWantMiss))
	}
	return out
}
