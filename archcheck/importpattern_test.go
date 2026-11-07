// importpattern_test.go —— issue #41：forbid_import_regex 结构上永不命中时必须红。
//
// 用例刻意成对：**能证明永不命中**的形态要红，**可能命中**的形态一条都不许红。
// 只测前一半（红的那些）等于没钉住——一个把所有正则都判红的错实现照样全过。
package archcheck

import (
	"strings"
	"testing"
)

// TestImportPatternDefect 判据本体：结构上命中不了 clean import 路径的形态。
//
// 三条 hit 各自钉住一条判定路径（字面量 '//' / 结尾锚点 / 边界组），
// 六条 miss 各自钉住一处**不该红**的地方——它们才是这条判据真正的风险面：
// 误报会让宿主关掉整条自校验，连带把真问题一起关掉。
func TestImportPatternDefect(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		wantHit bool
	}{
		// ── 可证明永不命中 ──
		{"字面量里相邻两个斜杠", `example\.com/demo/internal//x`, true},
		{"issue #41 的原形：前缀收尾斜杠 + 边界组", `example\.com/demo/internal/(/|$)`, true},
		{"边界组在前缀收尾斜杠之后（分支顺序反过来）", `example\.com/demo/internal/($|/)`, true},
		{"以 / 收尾再接结尾锚点", `example\.com/demo/internal/$`, true},
		{"斜杠至少重复两次", `example\.com/demo(/){2,}x`, true},

		// ── 可能命中：一条都不许红 ──
		{"正确形态：斜杠属于边界组", `example\.com/demo/internal(/|$)`, false},
		{"多包名 + 边界组", `example\.com/demo/(engine|sh)(/|$)`, false},
		{"尾斜杠但无结尾锚点（子串匹配照常成立）", `example\.com/demo/internal/`, false},
		{"字符类里重复写斜杠：文本含 '//' 却只匹配一个", `example\.com/demo[//]x`, false},
		{"两个单字符类先后出现：语义上确实是两个斜杠", `example\.com/demo[/][/]x`, true},
		{"只有一个分支写错：另一支仍可能命中", `(example\.com/demo//dead|example\.com/live)`, false},
		{"可省节点隔开两个斜杠，相邻并未被强制", `example\.com/demo(/)?/x`, false},
		{"边界组有三个分支，第三支仍可能命中", `example\.com/demo/(/|$|x)`, false},
		{"单个斜杠的重复至少一次（可以只出现一次）", `example\.com(/)+x`, false},
		{"普通锚定正则", `^errors$`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := importPatternDefect(c.pattern)
			if c.wantHit && got == "" {
				t.Fatalf("正则 %q 结构上命中不了任何 import 路径，判据却沉默了——"+
					"这正是 issue #41 的失败形态：规则在、severity 是 error、报告照常绿", c.pattern)
			}
			if !c.wantHit && got != "" {
				t.Fatalf("正则 %q 可能命中真正的 import 路径，判据却报「%s」——"+
					"误报会让人关掉整条自校验，然后连带把真问题一起关掉", c.pattern, got)
			}
		})
	}
}

// TestSelfCheckRejectsUnmatchableImportRegex 判据接在哪：policy 自校验。
//
// 一条永不命中的规则必须**在加载期**变成可定位的违反（点名规则 id + 给出修法），
// 而不是等宿主自己发现「这条规则怎么从没触发过」。
func TestSelfCheckRejectsUnmatchableImportRegex(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"pkg/a.go": "package pkg\n"})

	dead := Rule{
		ID:           "t-dead-import",
		Severity:     "error",
		Scope:        []string{"pkg"},
		Include:      []string{"*.go"},
		IgnoreTest:   true,
		ForbidImport: `example\.com/demo/internal/(/|$)`,
	}
	var located bool
	for _, v := range selfCheckViolations(root, &Policy{Rules: []Rule{dead}}) {
		if v.Rule == selfCheckRuleID && strings.Contains(v.Text, "t-dead-import") &&
			strings.Contains(v.Text, "forbid_import_regex") {
			located = true
		}
	}
	if !located {
		t.Fatalf("结构上永不命中的规则必须由 %s 报出并点名规则 id（否则门禁绿得毫无异常）", selfCheckRuleID)
	}

	// 反向：正确形态（斜杠属于边界组）不得误报。
	live := dead
	live.ForbidImport = `example\.com/demo/internal(/|$)`
	if vs := selfCheckViolations(root, &Policy{Rules: []Rule{live}}); len(vs) != 0 {
		t.Fatalf("正确形态不得报错，实得 %+v", vs)
	}
}
