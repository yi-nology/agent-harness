// scan_test.go —— 规则判定面与 import 门禁的反向验证用例。
//
// # 为什么这些用例必须固化在 CI 里
//
// 门禁失效的失败形态永远是**变绿**，不是变红。手工注入一次只能证明「当时
// 生效过」，下一次有人改判定逻辑时，漏掉的形态照样会悄悄回来——而 CI
// 一路绿着，没人知道规则已经不判东西了。
//
// 本文件覆盖的是本仓真实发生过的三类失效：
//
//	1. 判定面只有二态（only_test 被当成「必须等于 isTest」）→ 测试文件被整体跳过，
//	   三条声明要覆盖测试文件的规则集体失效。
//	2. import 规则逐行正则 → 单行 import / 别名 import 漏检。
//	3. god object 按裸类型名聚合 → 不同包的同名类型被并成一个数。
//
// 每条用例都写成「错误实现会红」：只测正确实现能过的用例，一个把数字比较
// 写成字符串比较的错实现照样全过。

package main

import (
	"strings"
	"testing"
)

// ── 判定面三态 ──

// TestInTestScopeThreeStates 判定面必须是三态，不是二态。
//
// 旧实现用 `if r.OnlyTest != isTest` —— 把 only_test 当成「必须等于 isTest」。
// 于是「only_test=false 且 ignore_test=false」这一态（声明「生产与测试都扫」）
// 会把**所有测试文件整体跳过**。
//
// 三个用例各钉一态，少一态就红。
func TestInTestScopeThreeStates(t *testing.T) {
	cases := []struct {
		why  string
		r    rule
		path string
		want bool
	}{
		{"ignore_test=true → 只扫生产", rule{IgnoreTest: true}, "/x/a.go", true},
		{"ignore_test=true → 测试文件不在面内", rule{IgnoreTest: true}, "/x/a_test.go", false},
		{"only_test=true → 只扫测试", rule{OnlyTest: true}, "/x/a_test.go", true},
		{"only_test=true → 生产文件不在面内", rule{OnlyTest: true}, "/x/a.go", false},
		{"两者皆 false → 生产与测试都扫（这正是旧实现漏掉的那一态）", rule{}, "/x/a.go", true},
		{"两者皆 false → 测试文件也必须扫", rule{}, "/x/a_test.go", true},
	}
	for _, c := range cases {
		if got := inTestScope(c.r, c.path); got != c.want {
			t.Fatalf("%s：inTestScope(%v, %q) = %v，期望 %v", c.why, c.r, c.path, got, c.want)
		}
	}
}

// TestRuleFilesCoversTestFiles 端到端钉住「ignore_test=false 的规则能扫到测试文件」。
//
// 这一条是真实事故的回归守卫：本仓曾有一处 stdlib "errors" 躺在
// ptc/bytebudget_regression_test.go 里，门禁因判定面二态缺陷对它完全失明，
// 而 AGENTS.md 写着「全仓 24 个文件统一切到 pkg/errors」。
func TestRuleFilesCoversTestFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"pkg/a.go":      "package pkg\n",
		"pkg/a_test.go": "package pkg\n",
	})
	r := rule{Scope: []string{"."}, Include: []string{"*.go"}}
	files, err := ruleFiles(root, r)
	if err != nil {
		t.Fatal(err)
	}
	var sawProd, sawTest bool
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			sawTest = true
		} else {
			sawProd = true
		}
	}
	if !sawProd || !sawTest {
		t.Fatalf("ignore_test=false 的规则必须同时扫到生产与测试文件：生产=%v 测试=%v", sawProd, sawTest)
	}

	// 反向：ignore_test=true 时测试文件必须被排除。
	r2 := rule{Scope: []string{"."}, Include: []string{"*.go"}, IgnoreTest: true}
	files2, err := ruleFiles(root, r2)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files2 {
		if strings.HasSuffix(f, "_test.go") {
			t.Fatalf("ignore_test=true 时测试文件必须被排除，却拿到了 %s", f)
		}
	}
}

// ── import 门禁：书写形态全覆盖 ──

// TestImportViolationsCatchesAllForms import 门禁必须与书写形态无关。
//
// 旧实现逐行正则匹配源码文本，只认「整行就是一个裸路径」。于是本仓自己
// 大量使用的单行 import（`import "context"`）根本不在判据里——规则对相当
// 一部分文件从来没生效过。
//
// 这里逐个形态注入，每一种都必须被抓到。少认一种就红。
func TestImportViolationsCatchesAllForms(t *testing.T) {
	forms := map[string]string{
		"单行": `package p

import "errors"

var _ = errors.New("x")
`,
		"单行带括号": `package p

import ("errors")

var _ = errors.New("x")
`,
		"块": `package p

import (
	"errors"
)

var _ = errors.New("x")
`,
		"别名": `package p

import stderrors "errors"

var _ = stderrors.New("x")
`,
		"别名 + 块": `package p

import (
	stderrors "errors"
)

var _ = stderrors.New("x")
`,
		"空白导入": `package p

import _ "errors"

var _ = 1
`,
		"点导入": `package p

import . "errors"

var _ = 1
`,
	}
	for name, src := range forms {
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"p/a.go": src})
		r := rule{
			ID:           "t",
			Scope:        []string{"."},
			Include:      []string{"*.go"},
			ForbidImport: `^errors$`,
		}
		vs, err := importViolations(root, r)
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		if len(vs) != 1 {
			t.Fatalf("%s：import %q 必须被抓到，实得 %d 处违反", name, "errors", len(vs))
		}
	}
}

// TestImportViolationsIgnoresNonImportText 「errors」出现在非 import 位置不得误报。
//
// import 门禁的匹配对象是**路径**，不是源码文本。若实现退化成全文搜 "errors"，
// 下面这些都会被误报——误报是门禁最致命的失败形态（维护者学会忽略它）。
func TestImportViolationsIgnoresNonImportText(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"p/a.go": `package p

const kind = "errors"

var m = map[string]string{"errors": "1"}

func f() string { return "errors" }
`,
	})
	r := rule{ID: "t", Scope: []string{"."}, Include: []string{"*.go"}, ForbidImport: `^errors$`}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("非 import 位置的 \"errors\" 不得误报，实得 %d 处：%v", len(vs), vs[0].text)
	}
}

// TestImportViolationsCoversTestFiles import 规则必须覆盖测试文件。
//
// ignore_test=false 的规则声明的就是「也要覆盖测试文件」。这条与
// harness-no-host-dependency（宿主依赖）的红线直接相关：测试夹具 import 宿主仓
// 同样是「内核依赖宿主」，且测试文件往往是抄生产代码来的第一站。
func TestImportViolationsCoversTestFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"p/a_test.go": "package p\n\nimport \"github.com/yi-nology/bianque/x\"\n",
	})
	r := rule{
		ID:           "t",
		Scope:        []string{"."},
		Include:      []string{"*.go"},
		ForbidImport: `github\.com/yi-nology/bianque`,
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("测试文件里的禁用 import 必须被抓到，实得 %d 处", len(vs))
	}
}

// TestImportViolationsFailsLoudlyOnParseError 语法坏掉的文件不得被静默跳过。
//
// 静默跳过 = 该文件对规则隐形，而门禁会报「无阻断级违反」——这正是要防的
// 「给不出结论却显示成通过」。
func TestImportViolationsFailsLoudlyOnParseError(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"p/a.go": "package p\n\nfunc ( { }\n"})
	r := rule{ID: "t", Scope: []string{"."}, Include: []string{"*.go"}, ForbidImport: `^errors$`}
	if _, err := importViolations(root, r); err == nil {
		t.Fatal("语法坏掉的文件必须报错退出，不得静默跳过")
	}
}

// ── negative_follow：同一行多处 anchor ──

// TestNeutralizedHandlesMultipleAnchors 同一行出现多处 anchor 时不得漏判豁免。
//
// 「断言不得出现某词」的用例里，同一行常常有正反两处断言。旧实现只取 anchor
// 在本行的**第一次**出现，后面那处永远看不到——结果是门禁把合法的负向断言
// 报成违规（误报），而误报会让人直接关掉豁免机制。
func TestNeutralizedHandlesMultipleAnchors(t *testing.T) {
	nfs := []negFollow{{Anchor: `strings.Contains(out, "`, ForbidFollow: `运维")`}}
	// 命中位置在**第二处** anchor 之后。
	line := `if strings.Contains(out, "abc") || !strings.Contains(out, "运维") {`
	idx := strings.Index(line, "运维")
	if idx < 0 {
		t.Fatal("夹具构造错误：未找到命中位置")
	}
	if !neutralized(line, idx, idx+len("运维"), nfs) {
		t.Fatal("第二处 anchor 的负向断言必须被识别为豁免")
	}
}

// ── god object：按包聚合 ──

// TestGodObjectsAggregatesPerPackage 不同包的同名类型不得被并成一个数。
//
// 方法数超阈值讲的是「**这一个**类型承担了过多关注点」。把两个包的同名类型
// 加起来判，判出的不是 god object，是巧合——而误报是门禁最致命的失败形态。
// 本仓真实存在多处同名类型分处不同包（各包的测试接收器 rec、两个包的 Report）。
func TestGodObjectsAggregatesPerPackage(t *testing.T) {
	mk := func(n int) string {
		var sb strings.Builder
		sb.WriteString("package p\n\ntype T struct{}\n\n")
		for i := 0; i < n; i++ {
			sb.WriteString("func (t *T) M" + string(rune('a'+i)) + "() {}\n")
		}
		return sb.String()
	}
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"pa/a.go": mk(3),
		"pb/a.go": mk(3),
	})
	// 阈值 5：每包各 3 个方法，合并成 6 会误报超限。
	got := godObjects(root, []string{"pa", "pb"}, 5)
	if len(got) != 0 {
		t.Fatalf("两个包各 3 个方法的同名类型不得被并成 6 个方法（会误报），实得 %v", got)
	}
	// 同一包内 6 个方法必须仍被判超限（反向：不能因为改了聚合键就漏报）。
	root2 := t.TempDir()
	writeFixture(t, root2, map[string]string{"pa/a.go": mk(6)})
	got2 := godObjects(root2, []string{"pa"}, 5)
	if len(got2) != 1 {
		t.Fatalf("同包内 6 个方法必须被判超限，实得 %v", got2)
	}
}
