package cmdpolicy_test

import (
	"strings"
	"testing"

	"github.com/yi-nology/agent-harness/cmdpolicy"
)

func mustPolicy(t *testing.T, rules ...cmdpolicy.Rule) *cmdpolicy.Policy {
	t.Helper()
	p, err := cmdpolicy.New(rules)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestDecideThreeStates(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "push", Prefix: cmdpolicy.Prefix{{"git"}, {"push"}}, Decision: cmdpolicy.DecisionPrompt, Justification: "走 PR 而不是直推",
			Match: []string{"git push", "git push origin main"}, NotMatch: []string{"git status"}},
		cmdpolicy.Rule{Name: "wipe", Prefix: cmdpolicy.Prefix{{"rm"}}, Decision: cmdpolicy.DecisionForbidden,
			Match: []string{"rm -rf /tmp/x"}, NotMatch: []string{"rmdir /tmp/x"}},
	)
	cases := []struct {
		cmd  string
		want cmdpolicy.Decision
		rule string
	}{
		{"git push origin main", cmdpolicy.DecisionPrompt, "push"},
		{"rm -rf /tmp/x", cmdpolicy.DecisionForbidden, "wipe"},
		{"ls -la", cmdpolicy.DecisionAllow, ""},
	}
	for _, c := range cases {
		v, err := p.DecideCommand(c.cmd)
		if err != nil {
			t.Fatalf("DecideCommand(%q): %v", c.cmd, err)
		}
		if v.Decision != c.want || v.Rule != c.rule {
			t.Fatalf("%q => (%v,%q), want (%v,%q)", c.cmd, v.Decision, v.Rule, c.want, c.rule)
		}
	}
}

// ⭐ 前缀判据最典型的绕过：只对整串判一次，前半段的 allow 会把后半段一起放行。
func TestSplitCommandsBlocksPrefixBypass(t *testing.T) {
	segs, err := cmdpolicy.SplitCommands("git status; rm -rf /tmp/x")
	if err != nil {
		t.Fatalf("SplitCommands: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %v", segs)
	}
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "wipe", Prefix: cmdpolicy.Prefix{{"rm"}}, Decision: cmdpolicy.DecisionForbidden},
	)
	worst := cmdpolicy.DecisionAllow
	for _, s := range segs {
		v, verr := p.DecideCommand(s)
		if verr != nil {
			t.Fatalf("DecideCommand(%q): %v", s, verr)
		}
		if v.Decision > worst {
			worst = v.Decision
		}
	}
	if worst != cmdpolicy.DecisionForbidden {
		t.Fatalf("second segment must be forbidden, got %v", worst)
	}
}

// ⭐ 段文本是**可往返**的：段再 Split 一次得到与原段逐项相同的 token。
// 这不是美观问题——段若被摊平成空格分隔，token 边界就变了：
// git 'a b' status 摊平后是 "git a b status"，再分词得 4 个 token（原 3 个），
// 含空格的备选前缀（如 git + a b）从此永不命中 ⇒ 安全方向的漏判。
//
// 反向验证：把段拼接换成空格拼接，本用例必红。
func TestSplitCommandsMergesAmpersand(t *testing.T) {
	segs, err := cmdpolicy.SplitCommands("a && b || c & d")
	if err != nil {
		t.Fatalf("SplitCommands: %v", err)
	}
	want := [][]string{{"a"}, {"b"}, {"c"}, {"d"}}
	if len(segs) != len(want) {
		t.Fatalf("want %d segments, got %v", len(want), segs)
	}
	for i, seg := range segs {
		argv, err := cmdpolicy.Split(seg)
		if err != nil {
			t.Fatalf("段 %q 再分词失败: %v", seg, err)
		}
		if len(argv) != len(want[i]) || argv[0] != want[i][0] {
			t.Fatalf("段 %d 往返后 token 变了：段=%q 得=%v 期望=%v", i, seg, argv, want[i])
		}
	}
}

func TestSplitQuoting(t *testing.T) {
	argv, err := cmdpolicy.Split(`git commit -m "fix: a b" --author 'x y'`)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	want := []string{"git", "commit", "-m", "fix: a b", "--author", "x y"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", argv, want)
	}
}

func TestSplitRejectsUnbalanced(t *testing.T) {
	if _, err := cmdpolicy.Split(`echo "abc`); err == nil {
		t.Fatal("未闭合的引号必须报错（判不了就不放行）")
	}
	if _, err := cmdpolicy.Split("echo x\\"); err == nil {
		t.Fatal("以转义符结尾必须报错")
	}
}

// 更具体者胜：长前缀压过短前缀，即便短前缀更严。
func TestSpecificityWins(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "git-any", Prefix: cmdpolicy.Prefix{{"git"}}, Decision: cmdpolicy.DecisionForbidden},
		cmdpolicy.Rule{Name: "git-status", Prefix: cmdpolicy.Prefix{{"git"}, {"status"}}, Decision: cmdpolicy.DecisionAllow},
	)
	v := p.Decide([]string{"git", "status"})
	if v.Rule != "git-status" || v.Decision != cmdpolicy.DecisionAllow {
		t.Fatalf("更具体者应胜出，got %+v", v)
	}
	v = p.Decide([]string{"git", "push"})
	if v.Rule != "git-any" || v.Decision != cmdpolicy.DecisionForbidden {
		t.Fatalf("短前缀仍应命中其余命令，got %+v", v)
	}
}

// 同等具体取更严；同档取名字字典序最小（只为确定性）。
func TestTieBreakIsDeterministic(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "b-soft", Prefix: cmdpolicy.Prefix{{"nuke"}}, Decision: cmdpolicy.DecisionAllow},
		cmdpolicy.Rule{Name: "a-hard", Prefix: cmdpolicy.Prefix{{"nuke"}}, Decision: cmdpolicy.DecisionForbidden},
	)
	first := p.Decide([]string{"nuke"})
	if first.Rule != "a-hard" || first.Decision != cmdpolicy.DecisionForbidden {
		t.Fatalf("同长度应取更严，got %+v", first)
	}
	for i := 0; i < 20; i++ {
		if got := p.Decide([]string{"nuke"}); got.Rule != first.Rule {
			t.Fatalf("判定必须确定，第 %d 次得到 %q", i, got.Rule)
		}
	}
	p2 := mustPolicy(t,
		cmdpolicy.Rule{Name: "zeta", Prefix: cmdpolicy.Prefix{{"x"}}, Decision: cmdpolicy.DecisionPrompt},
		cmdpolicy.Rule{Name: "alpha", Prefix: cmdpolicy.Prefix{{"x"}}, Decision: cmdpolicy.DecisionPrompt},
	)
	if got := p2.Decide([]string{"x"}); got.Rule != "alpha" {
		t.Fatalf("同档应取名字最小，got %q", got.Rule)
	}
}

func TestAlternativesSegment(t *testing.T) {
	p := mustPolicy(t, cmdpolicy.Rule{
		Name:     "mutating",
		Prefix:   cmdpolicy.Prefix{{"git"}, {"push", "commit"}},
		Decision: cmdpolicy.DecisionPrompt,
		Match:    []string{"git push", "git commit -m x"},
		NotMatch: []string{"git log", "git"},
	})
	for _, cmd := range []string{"git push", "git commit -m x"} {
		if v := p.Decide(strings.Fields(cmd)); v.Rule != "mutating" {
			t.Fatalf("%q 应命中 mutating", cmd)
		}
	}
	if v := p.Decide([]string{"git", "log"}); v.Rule != "" {
		t.Fatalf("git log 不该命中，got %+v", v)
	}
	// 段数不足不得命中（`git` 单独不成规则）
	if v := p.Decide([]string{"git"}); v.Rule != "" {
		t.Fatalf("token 不足不该命中，got %+v", v)
	}
}

func TestNewRejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		rules []cmdpolicy.Rule
		want  string
	}{
		{"empty-name", []cmdpolicy.Rule{{Prefix: cmdpolicy.Prefix{{"a"}}}}, "规则名不得为空"},
		{"dup-name", []cmdpolicy.Rule{{Name: "x", Prefix: cmdpolicy.Prefix{{"a"}}}, {Name: "x", Prefix: cmdpolicy.Prefix{{"b"}}}}, "重复"},
		{"bad-decision", []cmdpolicy.Rule{{Name: "x", Prefix: cmdpolicy.Prefix{{"a"}}, Decision: cmdpolicy.Decision(9)}}, "不在词表内"},
		{"empty-prefix", []cmdpolicy.Rule{{Name: "x"}}, "前缀为空"},
		{"empty-segment", []cmdpolicy.Rule{{Name: "x", Prefix: cmdpolicy.Prefix{{}}}}, "没有备选"},
		{"empty-token", []cmdpolicy.Rule{{Name: "x", Prefix: cmdpolicy.Prefix{{""}}}}, "空 token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := cmdpolicy.New(c.rules)
			if err == nil {
				t.Fatalf("应当报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("错误信息应含 %q，got %v", c.want, err)
			}
		})
	}
}

// 加载期自检必须真的能抓到「规则漂移」——正例不再命中、反例被命中。
func TestSelfCheckCatchesDrift(t *testing.T) {
	_, err := cmdpolicy.New([]cmdpolicy.Rule{{
		Name: "old-cmd", Prefix: cmdpolicy.Prefix{{"legacy-tool"}},
		Decision: cmdpolicy.DecisionForbidden,
		Match:    []string{"legacy-tool --flag"},
	}})
	if err != nil {
		t.Fatalf("正例应当命中: %v", err)
	}
	_, err = cmdpolicy.New([]cmdpolicy.Rule{{
		Name: "old-cmd", Prefix: cmdpolicy.Prefix{{"legacy-tool"}},
		Decision: cmdpolicy.DecisionForbidden,
		Match:    []string{"renamed-tool --flag"},
	}})
	if err == nil || !strings.Contains(err.Error(), "没被它命中") {
		t.Fatalf("正例不命中必须报错，got %v", err)
	}
	_, err = cmdpolicy.New([]cmdpolicy.Rule{{
		Name: "narrow", Prefix: cmdpolicy.Prefix{{"tool"}},
		Decision: cmdpolicy.DecisionForbidden,
		NotMatch: []string{"tool --safe"},
	}})
	if err == nil || !strings.Contains(err.Error(), "被它命中了") {
		t.Fatalf("反例被命中必须报错，got %v", err)
	}
}

func TestRulesSnapshotSorted(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "b", Prefix: cmdpolicy.Prefix{{"b"}}},
		cmdpolicy.Rule{Name: "a", Prefix: cmdpolicy.Prefix{{"a"}}},
	)
	got := p.Rules()
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("快照应按名字排序，got %+v", got)
	}
}

func TestExplain(t *testing.T) {
	if got := (cmdpolicy.Verdict{}).Explain(); got != "no rule matched" {
		t.Fatalf("got %q", got)
	}
	v := cmdpolicy.Verdict{Decision: cmdpolicy.DecisionPrompt, Rule: "push", Justification: "走 PR"}
	if got := v.Explain(); !strings.Contains(got, "prompt") || !strings.Contains(got, "走 PR") {
		t.Fatalf("got %q", got)
	}
	if cmdpolicy.DecisionForbidden.String() != "forbidden" {
		t.Fatal("String 词表漂移")
	}
	if cmdpolicy.Decision(42).String() != "unknown" {
		t.Fatal("未知档应报 unknown")
	}
}
