package main

import (
	"strings"
	"testing"
)

// TestJudge 覆盖三条判据的全部形态（下限 70%）。
//
// 每条红都断言**红在哪一栏**：只断言「不 ok」的用例挡不住「把 below 记进
// staleExempt」这类错位——报告仍是非零退出，但修法提示指向了错误的地方。
func TestJudge(t *testing.T) {
	cases := []struct {
		name   string
		exempt []string
		noTest []string
		wantOK bool
		// below/undeclared/staleCov/staleNoTst 断言红在哪一栏。
		below      int
		undeclared int
		staleCov   int
		staleNoTst int
	}{
		{
			name:   "零测试包已声明 + 低分包已豁免 ⇒ 绿",
			exempt: []string{"beta"},
			noTest: []string{"cmd/tool", "cmd/fixture", "cmd/plain"},
			wantOK: true,
		},
		{
			name:   "低分包未豁免 ⇒ 红在 below",
			noTest: []string{"cmd/tool", "cmd/fixture", "cmd/plain"},
			wantOK: false,
			below:  1,
		},
		{
			name:       "零测试包未声明 ⇒ 红在 undeclared",
			exempt:     []string{"beta"},
			noTest:     []string{"cmd/tool", "cmd/fixture"},
			wantOK:     false,
			undeclared: 1,
		},
		{
			name:     "豁免包自己达标 ⇒ 红在 staleCov（失效即拆）",
			exempt:   []string{"alpha", "beta"},
			noTest:   []string{"cmd/tool", "cmd/fixture", "cmd/plain"},
			wantOK:   false,
			staleCov: 1,
		},
		{
			name:     "豁免写了但没命中任何包 ⇒ 红在 staleCov",
			exempt:   []string{"beta", "gone"},
			noTest:   []string{"cmd/tool", "cmd/fixture", "cmd/plain"},
			wantOK:   false,
			staleCov: 1,
		},
		{
			name:       "零测试豁免失效（该包已有测试）⇒ 红在 staleNoTst",
			exempt:     []string{"beta"},
			noTest:     []string{"cmd/tool", "cmd/fixture", "cmd/plain", "alpha"},
			wantOK:     false,
			staleNoTst: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := judge(parseLog(t), options{min: 70, exempt: c.exempt, noTestExempt: c.noTest})
			if r.ok() != c.wantOK {
				t.Fatalf("ok() = %v，期望 %v（报告：%+v）", r.ok(), c.wantOK, r)
			}
			if len(r.below) != c.below {
				t.Errorf("below = %v，期望 %d 条", r.below, c.below)
			}
			if len(r.undeclaredNoTest) != c.undeclared {
				t.Errorf("undeclaredNoTest = %v，期望 %d 条", r.undeclaredNoTest, c.undeclared)
			}
			if len(r.staleExempt) != c.staleCov {
				t.Errorf("staleExempt = %v，期望 %d 条", r.staleExempt, c.staleCov)
			}
			if len(r.staleNoTestExempt) != c.staleNoTst {
				t.Errorf("staleNoTestExempt = %v，期望 %d 条", r.staleNoTestExempt, c.staleNoTst)
			}
		})
	}
}

// TestSuffixBoundary 锁死豁免匹配的**边界**：后缀必须对齐 `/` 或整串相等。
//
// 防的是「放行方向」的漏判：若实现写成裸 strings.HasSuffix，一条叫 sync 的豁免
// 会连 cmd/docsync 一起放行——豁免变成通配符，而报告里仍写「1 个豁免」。
func TestSuffixBoundary(t *testing.T) {
	if hasSuffix("example.com/m/cmd/docsync", "sync") {
		t.Fatal("后缀 sync 不应匹配 cmd/docsync（边界必须对齐 /）")
	}
	if !hasSuffix("example.com/m/cmd/docsync", "cmd/docsync") {
		t.Fatal("后缀 cmd/docsync 应匹配 cmd/docsync")
	}
	if !hasSuffix("docsync", "docsync") {
		t.Fatal("整串相等应命中")
	}
	if hasSuffix("example.com/m/x", "") {
		t.Fatal("空豁免项不该匹配任何包（否则 splitList 漏掉空项会变成通配符）")
	}
}

// TestReportString 锁死报告文案：绿只有一行，红必须**指名**修法。
//
// 门禁红得说不出原因，与常红同样会训练人忽略门禁（本仓在 cover-gate 的日志
// 保留上写过同一条）。故红文案必须带上「从哪个变量移除」这种可执行信息。
func TestReportString(t *testing.T) {
	o := options{min: 70, exempt: []string{"beta"}, noTestExempt: []string{"cmd/tool", "cmd/fixture", "cmd/plain"}}
	green := judge(parseLog(t), o)
	if !green.ok() {
		t.Fatalf("这组参数应放行，实际: %s", green.String(o))
	}
	if s := green.String(o); !strings.Contains(s, "✓") {
		t.Fatalf("绿报告应带 ✓，实际: %s", s)
	}

	red := judge(parseLog(t), options{min: 70})
	s := red.String(o)
	for _, want := range []string{
		"低于逐包下限 70%", "PKG_COV_MIN",
		"没有任何测试文件", "PKG_NO_TEST_EXEMPT",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("红报告应包含 %q，实际:\n%s", want, s)
		}
	}
}

// TestSplitList 锁死豁免表解析（空白与空项不该变成通配符）。
func TestSplitList(t *testing.T) {
	got := splitList(" b , ,a,  ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("splitList = %v，期望 [a b]（去空白、丢空项、排序）", got)
	}
	if got := splitList(""); len(got) != 0 {
		t.Fatalf("空串应拆出 0 项，实际 %v", got)
	}
}
