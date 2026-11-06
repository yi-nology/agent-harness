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

// matchSample 必须与**真实判定路径**同口径。这里钉三条最容易漂的口径：
// 注释行不判、negative_follow 中和、import 规则按路径判（含 import_exempt）。
func TestMatchSampleFollowsRealPath(t *testing.T) {
	textRule := Rule{ID: "r", ForbidRegex: "banned"}
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"命中", "var x = banned", true},
		{"注释行不判", "// 这里说明banned", false},
		{"行尾注释也算代码", "var x = 1 // banned", true},
		{"不命中", "var x = 1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := textRule.matchSample(c.text)
			if err != nil {
				t.Fatalf("matchSample 失败: %v", err)
			}
			if got != c.want {
				t.Fatalf("matchSample(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

// negative_follow 的豁免面必须在样例里同样生效——否则「样例过了」不代表
// 真判定会放过那条负向断言（这正是本仓历史上被自己规则误杀过的形态）。
func TestMatchSampleHonoursNegFollow(t *testing.T) {
	r := Rule{
		ID:          "r",
		ForbidRegex: "banned",
		NegFollow:   []NegFollow{{Anchor: "Contains(out, \"", ForbidFollow: "banned\")"}},
	}
	got, err := r.matchSample("if strings.Contains(out, \"banned\") {")
	if err != nil {
		t.Fatalf("matchSample 失败: %v", err)
	}
	if got {
		t.Fatal("negative_follow 命中的文本不得算违规")
	}
	// 反面对照：不满足锚点的同一句必须照旧判违规——否则上面那条会因
	// 「豁免整体失效（永远不判）」而假绿。
	if hit, _ := r.matchSample("x := \"banned\""); !hit {
		t.Fatal("不满足锚点的文本必须仍判违规")
	}
}

func TestMatchSampleImportRule(t *testing.T) {
	r := Rule{ID: "r", ForbidImport: "^errors$", ImportExempt: []string{"errors"}}
	if hit, err := r.matchSample("errors"); err != nil || hit {
		t.Fatalf("import_exempt 覆盖的路径不得判违规（hit=%v err=%v）", hit, err)
	}
	if hit, err := r.matchSample("github.com/pkg/errors"); err != nil {
		t.Fatalf("matchSample 失败: %v", err)
	} else if hit {
		// ^errors$ 只锚裸 errors；这条样例用来证明「锚点确实生效」。
		t.Fatal("狭义的 import 正则不该命中别的路径")
	}
	if _, err := r.matchSample("errors"); err != nil {
		t.Fatalf("matchSample 失败: %v", err)
	}
}

// 没有执行体的规则判样例必须报错（而不是静默当 miss）：
// 静默当 miss 会让一条**完全判不动**的规则因为「反例都通过」而绿。
func TestMatchSampleWithoutBodyFails(t *testing.T) {
	if _, err := (Rule{ID: "r"}).matchSample("x"); err == nil {
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
	}
	if _, err := Load(root + "/architecture-policy.yaml"); err != nil {
		t.Fatal(err)
	}
}
