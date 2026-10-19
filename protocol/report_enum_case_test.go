// report_enum_case_test.go —— 枚举位大小写/空白归一的反向验证。
//
// # 为什么这一组要单独钉
//
// 报告协议里有一批枚举位（decision / scope / action / confidence / stance），
// 它们的取值是**有限且固定**的，而模型写它们时最爱改大小写、加空白。
//
// 每一处未归一的枚举位，代价都不同，但有一个共同点：**结局由写法决定，
// 不由内容决定**。同一个词，一个 ASCII 大小写之差，报告就从「收口通过」
// 变成「整份丢弃」，或从「裁决保留」变成「静默消失」。
//
// 这一组此前漏了三处（action / confidence / stance），而同包的
// normalizeBoolField 与 normalizeDecision 早已各修过一次同类错——
// 说明这不是「没想过」，是**逐个字段补**的必然漏网。
//
// 所以这里不只钉这三个字段，而是把「枚举位必须归一」这条纪律本身钉住：
// 下面有一条用例枚举了协议的全部枚举位，逐个检查大小写变体。

package protocol

import (
	"strings"
	"testing"
)

// TestActionCaseNormalized action 的大小写/空白变体必须收口，不得整份丢弃。
//
// 旧行为：action="none" → 归一为 tune + requires_approval=true → 通过；
// action="None" → 白名单不命中 → critical → **整份报告被拒**。
func TestActionCaseNormalized(t *testing.T) {
	for _, act := range []string{"none", "None", "NONE", " none ", " none"} {
		raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
			`"recommendation":{"action":"` + act + `","steps":["ls -la"]}}`
		r, err := ParseReport(raw)
		if err != nil {
			t.Fatalf("action=%q 解析失败: %v", act, err)
		}
		if r.Recommendation.Action != ActionTune {
			t.Fatalf("action=%q 应收口为 %q（有步骤必须走审批），实得 %q",
				act, ActionTune, r.Recommendation.Action)
		}
		if !r.Recommendation.RequiresApproval {
			t.Fatalf("action=%q：有可运行步骤时 requires_approval 必须为 true", act)
		}
		if _, verr := r.Validate(); verr != nil {
			t.Fatalf("action=%q 归一后应通过校验: %v", act, verr)
		}
	}
}

// TestActionBlankWithStepsStillRequiresApproval action 规整成空串时仍必须走审批。
//
// 旧实现的矛盾特判只认 `a == "none"` 精确比较，于是 action 写成空白这类形态
// 不触发修复——步骤在、审批位没置上，正是那条特判要杜绝的事。
func TestActionBlankWithStepsStillRequiresApproval(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
		`"recommendation":{"action":" ","steps":["ls -la"]}}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Recommendation.RequiresApproval {
		t.Fatal("action 规整为空串且有步骤时，必须要求审批")
	}
	if r.Recommendation.Action != ActionTune {
		t.Fatalf("action 应收口为 %q，实得 %q", ActionTune, r.Recommendation.Action)
	}
}

// TestConfidenceCaseNormalized confidence 的大小写/空白变体必须收口。
//
// 旧行为：confidence="high" 通过；"High"/"HIGH"/" high " → 白名单不命中
// → critical → **整份报告被拒**。
func TestConfidenceCaseNormalized(t *testing.T) {
	for _, c := range []string{"high", "High", "HIGH", " high ", "high "} {
		raw := `{"agent_name":"X","confidence":"` + c + `","conclusion":"c","summary":"s"}`
		r, err := ParseReport(raw)
		if err != nil {
			t.Fatalf("confidence=%q 解析失败: %v", c, err)
		}
		if r.Confidence != ConfidenceHigh {
			t.Fatalf("confidence=%q 应归一为 %q，实得 %q", c, ConfidenceHigh, r.Confidence)
		}
		if _, verr := r.Validate(); verr != nil {
			t.Fatalf("confidence=%q 归一后应通过校验: %v", c, verr)
		}
	}
}

// TestVerdictStanceCaseNormalized 裁决 stance 的大小写变体不得被静默丢弃。
//
// 旧行为：stance="adopt" 保留；"Adopt"/"ADOPT" → 条目**被静默丢弃**
// （连 warn 都没有）。合议记录少一条，而事后无法区分「模型没给裁决」与
// 「裁决被大小写吃掉」——静默的丢比显式的拒更难查。
func TestVerdictStanceCaseNormalized(t *testing.T) {
	for _, st := range []string{"adopt", "Adopt", "ADOPT", " adopt "} {
		raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
			`"verdicts":[{"agent":"a1","stance":"` + st + `","reason":"r"}]}`
		r, err := ParseReport(raw)
		if err != nil {
			t.Fatalf("stance=%q 解析失败: %v", st, err)
		}
		if len(r.Verdicts) != 1 {
			t.Fatalf("stance=%q：裁决条目不得被丢弃，实得 %d 条", st, len(r.Verdicts))
		}
		if r.Verdicts[0].Stance != VerdictAdopt {
			t.Fatalf("stance=%q 应归一为 %q，实得 %q", st, VerdictAdopt, r.Verdicts[0].Stance)
		}
	}
}

// TestAllEnumFieldsAreCaseNormalized 协议里的枚举位必须**全都**归一。
//
// 这一条是本组用例真正的价值：逐个字段补，必然漏网（action / confidence /
// stance 三处就是漏网的）。改成**枚举式**检查后，新增枚举位若忘了归一，
// 这条用例直接红。
//
// 判据：把该枚举位写成大写形态后，解析结果里不应再出现「原样的大写值」，
// 且报告不得因它被整份拒。
func TestAllEnumFieldsAreCaseNormalized(t *testing.T) {
	cases := []struct {
		field string
		// 注入位置：顶层键或 recommendation 子键
		nested bool
		// 期望归一后的取值
		want string
		// 取值所在的字段（顶层用 Report 字段，嵌套用 Recommendation 字段）
		read func(*Report) string
	}{
		{"confidence", false, ConfidenceHigh, func(r *Report) string { return r.Confidence }},
		{"decision", false, "", func(r *Report) string { return r.Decision }},
		{"scope", false, "in", func(r *Report) string { return r.Scope }},
		{"action", true, ActionTune, func(r *Report) string { return r.Recommendation.Action }},
	}
	for _, c := range cases {
		upper := strings.ToUpper(c.field)
		raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s"`
		if c.nested {
			raw += `,"recommendation":{"action":"` + upper + `","steps":["ls"]}`
		} else {
			raw += `,"` + c.field + `":"` + upper + `"`
		}
		raw += `}`
		r, err := ParseReport(raw)
		if err != nil {
			t.Fatalf("%s=%q 解析失败: %v", c.field, upper, err)
		}
		got := c.read(r)
		if got == upper {
			t.Fatalf("%s=%q 未被归一（原样保留）——枚举位的大小写变体必须归一", c.field, upper)
		}
	}
}

// TestScopeCaseNotInverted scope 的大小写变体不得被**语义反转**。
//
// 这一条比同组其它几条都重：scope 是路由信号（out = 转出/转派），
// 旧实现只认精确的 "in"/"out"，其余一律落 "in"——于是模型写 "OUT" 时，
// **它说「超出范围、要转人工」，解析器却记成「在范围内」**。
//
// 后果有两条，都可验证：
//   - 呈现口径与模型的实际判断相反；
//   - Validate 的「scope=out 时 redirect 必填」不再触发——out 形态缺 redirect
//     会被整份拒（正确），等价的 "OUT" 形态却畅通无阻（错误）。
func TestScopeCaseNotInverted(t *testing.T) {
	for _, sc := range []string{"out", "OUT", "Out", " out "} {
		raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
			`"scope":"` + sc + `","redirect":"转出到人工"}`
		r, err := ParseReport(raw)
		if err != nil {
			t.Fatalf("scope=%q 解析失败: %v", sc, err)
		}
		if r.Scope != "out" {
			t.Fatalf("scope=%q 必须保持 out 语义，实得 %q（语义被反转）", sc, r.Scope)
		}
	}
	// out 形态缺 redirect 必须被拒（与 "out" 形态同口径）。
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s","scope":"OUT"}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Scope != "out" {
		t.Fatalf("scope=OUT 应归一为 out，实得 %q", r.Scope)
	}
	if _, verr := r.Validate(); verr == nil {
		t.Fatal("scope=out 且缺 redirect 必须被拒（当前被放行，说明语义仍被反转）")
	}
}

// TestNeedsFollowupScalarCaseNormalized needs_followup 的布尔变体不得留下乱码跟进项。
//
// 模型写 ["True"] / [" TRUE "] 是「不要跟进」的意思。不归一的话它会作为一条
// 内容为 "True" 的跟进项留在报告里——跟进项是要人去执行的东西，混进一条乱码
// 比少一条更坏。
func TestNeedsFollowupScalarCaseNormalized(t *testing.T) {
	for _, v := range []string{"true", "True", "TRUE", " true "} {
		raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
			`"needs_followup":["` + v + `"]}`
		r, err := ParseReport(raw)
		if err != nil {
			t.Fatalf("needs_followup=%q 解析失败: %v", v, err)
		}
		if len(r.NeedsFollowup) != 0 {
			t.Fatalf("needs_followup=%q 应归一为空列表，实得 %v（留下了一条乱码跟进项）",
				v, r.NeedsFollowup)
		}
	}
}
