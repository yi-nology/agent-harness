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
// # 判据必须复用真实判定路径
//
// 样例不算「另写一套近似判定」——那样样例过了也说明不了真判定能过
// （两套实现各自漂移，样例那边漂得慢一点，于是绿灯更久，更糟）。
// matchSample 逐行复刻 checkRule / importViolations 的判定顺序：
// 注释跳过 → 正则定位 → import_exempt（仅 import 面）→ negative_follow 中和。
//
// # Want 的取值域必须枚举校验
//
// 把 want 写成 `hitt` 时，若实现按「非 hit 即 miss」处理，一条彻底坏掉的
// 规则会因为「反例全都通过」而绿得理直气壮。故拼错即报错。

package archcheck

import (
	"fmt"
	"regexp"
	"strings"
)

// matchSample 用**真实判定路径**判一条样例是否违规。
//
// text 的判据面随规则类型而定（源码行 / import 路径），与 importViolations
// 和 checkRule 各自实际匹配的对象一致。
func (r Rule) matchSample(text string) (bool, error) {
	if r.ForbidImport != "" {
		re, err := regexp.Compile(r.ForbidImport)
		if err != nil {
			return false, fmt.Errorf("规则 %s forbid_import_regex 非法: %w", r.ID, err)
		}
		loc := re.FindStringIndex(text)
		if loc == nil {
			return false, nil
		}
		if importExempt(r.ImportExempt, text) {
			return false, nil
		}
		return !neutralized(text, loc[0], loc[1], r.NegFollow), nil
	}
	if r.ForbidRegex == "" {
		// 没有执行体的规则由 selfcheck.go 单独报（口径只有一处）。
		return false, fmt.Errorf("规则 %s 没有执行体，样例无从判定", r.ID)
	}
	re, err := regexp.Compile(r.ForbidRegex)
	if err != nil {
		return false, fmt.Errorf("规则 %s forbid_regex 非法: %w", r.ID, err)
	}
	// 注释行不判——与 checkRule 同口径（policy 的说明性中文里出现领域词属正常）。
	if strings.HasPrefix(strings.TrimSpace(text), "//") {
		return false, nil
	}
	loc := re.FindStringIndex(text)
	if loc == nil {
		return false, nil
	}
	return !neutralized(text, loc[0], loc[1], r.NegFollow), nil
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

		got, err := r.matchSample(text)
		if err != nil {
			out = append(out, err.Error())
			continue
		}
		if want == exampleWantHit && !got {
			out = append(out, fmt.Sprintf(
				"规则 %q 的正例 %q **没有**被判违规——判据与它自己的声明已经脱钩（规则还挂着 error，却判不动任何东西）",
				r.ID, text))
		}
		if want == exampleWantMiss && got {
			out = append(out, fmt.Sprintf(
				"规则 %q 的反例 %q **被判成**违规——判据比它声明的宽（这类误判会让人学会忽略门禁输出）",
				r.ID, text))
		}
	}
	return out
}
