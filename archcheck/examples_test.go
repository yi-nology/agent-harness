package archcheck

import (
	"strings"
	"testing"
)

func testPolicy(t *testing.T) *Policy {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("未找到仓根: %v", err)
	}
	pol, err := Load(root + "/architecture-policy.yaml")
	if err != nil {
		t.Fatalf("加载 policy 失败: %v", err)
	}
	return pol
}

// 样例必须与**真实判定核**同口径。这里钉四条最容易漂的口径：注释行不判、
// 行尾注释照判、负面锚点中和、import 规则按 AST 路径判（含 import_exempt）。
//
// 期望值本身也说明这是**共用**而不是复刻：`regexViolationsIn` 是按行扫的，
// 故多行样例的行号要能对上——复刻版只会回答「有没有命中」。
func TestSampleViolationsFollowsRealPath(t *testing.T) {
	textRule := Rule{ID: "r", ForbidRegex: "banned"}
	cases := []struct {
		name string
		text string
		want bool
		line int // want=true 时命中的行号（1-based），证明走的是逐行判定核
	}{
		{"命中", "var x = banned", true, 1},
		{"注释行不判", "// 这里说明banned", false, 0},
		{"行尾注释也算代码", "var x = 1 // banned", true, 1},
		{"多行只报命中行", "var a = 1\nvar b = banned\nvar c = 2", true, 2},
		{"不命中", "var x = 1", false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs, err := textRule.sampleViolations(c.text)
			if err != nil {
				t.Fatalf("sampleViolations 失败: %v", err)
			}
			if got := len(vs) > 0; got != c.want {
				t.Fatalf("sampleViolations(%q) 命中 = %v, want %v（%v）", c.text, got, c.want, vs)
			}
			if c.want && vs[0].Line != c.line {
				t.Fatalf("sampleViolations(%q) 报在第 %d 行, want %d", c.text, vs[0].Line, c.line)
			}
		})
	}
}

// negative_follow 的豁免面必须在样例里同样生效——否则「样例过了」不代表
// 真判定会放过那条负向断言（这正是本仓历史上被自己规则误杀过的形态）。
//
// 反面对照是必须的：只验「豁免生效」的用例，在豁免**整体失灵（永远不判）**
// 时同样会绿。
func TestSampleViolationsHonoursNegFollow(t *testing.T) {
	r := Rule{
		ID:          "r",
		ForbidRegex: "banned",
		NegFollow:   []NegFollow{{Anchor: "Contains(out, \"", ForbidFollow: "banned\")"}},
	}
	vs, err := r.sampleViolations("if strings.Contains(out, \"banned\") {")
	if err != nil {
		t.Fatalf("sampleViolations 失败: %v", err)
	}
	if len(vs) != 0 {
		t.Fatalf("negative_follow 命中的文本不得算违规: %v", vs)
	}
	// 反面对照：不满足锚点的同一句必须照旧判违规。
	if vs, _ := r.sampleViolations("x := \"banned\""); len(vs) == 0 {
		t.Fatal("不满足锚点的文本必须仍判违规")
	}
}

// import 样例必须走 AST 判定：这条用例同时钉住三件事——
// 空白导入形态能被解析、import_exempt 精确保留、正则锚点仍然生效。
func TestSampleViolationsImportRule(t *testing.T) {
	r := Rule{ID: "r", ForbidImport: "^errors$", ImportExempt: []string{"errors"}}
	if vs, err := r.sampleViolations("errors"); err != nil || len(vs) != 0 {
		t.Fatalf("import_exempt 覆盖的路径不得判违规（hit=%v err=%v）", vs, err)
	}
	// ^errors$ 只锚裸 errors；这条样例用来证明「锚点确实生效」。
	if vs, err := r.sampleViolations("github.com/pkg/errors"); err != nil {
		t.Fatalf("sampleViolations 失败: %v", err)
	} else if len(vs) != 0 {
		t.Fatal("狭义的 import 正则不该命中别的路径")
	}
	// 无豁免时同一路径必须命中——否则上面那条会因「判据整体失灵」而绿。
	hit := Rule{ID: "r", ForbidImport: "^errors$"}
	vs, err := hit.sampleViolations("errors")
	if err != nil {
		t.Fatalf("sampleViolations 失败: %v", err)
	}
	if len(vs) == 0 {
		t.Fatal("无 import_exempt 时裸 errors 必须判违规")
	}
	if !strings.Contains(vs[0].Text, "errors") {
		t.Fatalf("违规回显必须点出被禁的路径: %q", vs[0].Text)
	}
}

// 样例文本里出现引号/反斜杠/非 ASCII 时，包装成源码不能坏掉——
// 一旦坏掉，门禁会以「解析失败」收尾，等于把一条笔误升级成一次全局红。
func TestSampleViolationsImportRuleQuotesPath(t *testing.T) {
	r := Rule{ID: "r", ForbidImport: "^weird$"}
	vs, err := r.sampleViolations("weird")
	if err != nil {
		t.Fatalf("普通路径不该解析失败: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("want 1 violation, got %v", vs)
	}
	// 不含任何合法 import 路径的反例：必须判 miss 而不是解析失败。
	miss := Rule{ID: "r", ForbidImport: "^weird$"}
	if vs, err := miss.sampleViolations("a\"b"); err != nil {
		t.Fatalf("含引号的样例文本必须被 strconv.Quote 正确转义，而不是造出坏源码: %v（%v）", err, vs)
	}
}

// 没有执行体的规则判样例必须报错（而不是静默当 miss）：
// 静默当 miss 会让一条**完全判不动**的规则因为「反例都通过」而绿。
func TestSampleViolationsWithoutBodyFails(t *testing.T) {
	if _, err := (Rule{ID: "r"}).sampleViolations("x"); err == nil {
		t.Fatal("没有执行体的规则必须报错")
	}
}

// 反向验证：**坏掉的判据必须被报出来**。
// 只测「好实现能过」的用例钉不住这条机制（判据整体失灵时它也全绿）。
func TestExampleViolationsCatchesDeadAndLoose(t *testing.T) {
	dead := Rule{
		ID:          "dead",
		ForbidRegex: "完全不出现的词",
		Examples:    []RuleExample{{Text: "var x = 1", Want: exampleWantHit}},
	}
	if got := exampleViolations(dead); len(got) == 0 || !strings.Contains(got[0], "没有") {
		t.Fatalf("正例判不中必须报出来: %v", got)
	}

	loose := Rule{
		ID:          "loose",
		ForbidRegex: "banned",
		Examples:    []RuleExample{{Text: "var x = banned", Want: exampleWantMiss}},
	}
	if got := exampleViolations(loose); len(got) == 0 || !strings.Contains(got[0], "比它声明的宽") {
		t.Fatalf("反例被判违规必须报出来: %v", got)
	}

	typo := Rule{
		ID:          "typo",
		ForbidRegex: "banned",
		Examples:    []RuleExample{{Text: "var x = banned", Want: "hitt"}},
	}
	if got := exampleViolations(typo); len(got) == 0 || !strings.Contains(got[0], "want") {
		t.Fatalf("want 拼错必须报出来: %v", got)
	}

	empty := Rule{ID: "empty", ForbidRegex: "banned", Examples: []RuleExample{{Want: exampleWantHit}}}
	if got := exampleViolations(empty); len(got) == 0 || !strings.Contains(got[0], "为空") {
		t.Fatalf("空样例必须报出来: %v", got)
	}
}

// 正例：一份与判据一致的样例不得产生任何问题（否则上面那些负例可能只是因为
// 「这条机制整体坏了」而绿）。
func TestExampleViolationsPassesWhenConsistent(t *testing.T) {
	r := Rule{
		ID:          "ok",
		ForbidRegex: "banned",
		Examples: []RuleExample{
			{Text: "var x = banned", Want: exampleWantHit},
			{Text: "var y = 1", Want: exampleWantMiss},
		},
	}
	if got := exampleViolations(r); len(got) != 0 {
		t.Fatalf("一致的样例不该报问题: %v", got)
	}
	if got := exampleCoverageProblems(r); len(got) != 0 {
		t.Fatalf("一正一反的样例不该被报覆盖不全: %v", got)
	}
}

// 双向覆盖判据必须成对：只有 miss 时报缺 hit，只有 hit 时报缺 miss，两者都有时不报。
// 只测其中一个方向等于没钉住——实现写成「恒报」或「恒不报」都能骗过单向用例。
func TestExampleCoverageProblems(t *testing.T) {
	onlyMiss := Rule{
		ID:          "only-miss",
		ForbidRegex: "banned",
		Examples:    []RuleExample{{Text: "var y = 1", Want: exampleWantMiss}},
	}
	got := exampleCoverageProblems(onlyMiss)
	if len(got) != 1 || !strings.Contains(got[0], exampleWantHit) {
		t.Fatalf("只有反例时必须报「缺正例」: %v", got)
	}

	onlyHit := Rule{
		ID:          "only-hit",
		ForbidRegex: "banned",
		Examples:    []RuleExample{{Text: "var x = banned", Want: exampleWantHit}},
	}
	got = exampleCoverageProblems(onlyHit)
	if len(got) != 1 || !strings.Contains(got[0], exampleWantMiss) {
		t.Fatalf("只有正例时必须报「缺反例」: %v", got)
	}

	both := Rule{
		ID:          "both",
		ForbidRegex: "banned",
		Examples: []RuleExample{
			{Text: "var x = banned", Want: exampleWantHit},
			{Text: "var y = 1", Want: exampleWantMiss},
		},
	}
	if got := exampleCoverageProblems(both); len(got) != 0 {
		t.Fatalf("一正一反时不该报: %v", got)
	}

	// 无执行体的规则由 selfcheck 的「没有执行体」口径报，不在这里重复计数。
	if got := exampleCoverageProblems(Rule{ID: "no-body"}); len(got) != 0 {
		t.Fatalf("无执行体的规则不该在这里重复报: %v", got)
	}
}

// require_rule_examples 开关必须真的生效：关着的时候不报、开着的时候报。
// 单测「开着报」不够——它可能只是「无论开关都报」。
func TestRequireRuleExamplesSwitch(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	pol := testPolicy(t)
	if !pol.RequireRuleExamples {
		t.Fatal("本仓 policy 应开启 require_rule_examples（否则这条口径只是散文）")
	}
	for _, r := range pol.Rules {
		if r.ForbidRegex == "" && r.ForbidImport == "" {
			t.Fatalf("真实 policy 里出现了无执行体的规则 %q", r.ID)
		}
		if len(r.Examples) == 0 {
			t.Fatalf("规则 %q 在该仓的口径下必须自带 examples", r.ID)
		}
		if got := exampleViolations(r); len(got) != 0 {
			t.Fatalf("规则 %q 的样例与判据不一致: %v", r.ID, got)
		}
		if got := exampleCoverageProblems(r); len(got) != 0 {
			t.Fatalf("规则 %q 的样例覆盖不全: %v", r.ID, got)
		}
	}
	if _, err := Load(root + "/architecture-policy.yaml"); err != nil {
		t.Fatal(err)
	}
}
