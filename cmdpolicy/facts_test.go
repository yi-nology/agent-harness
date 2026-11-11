package cmdpolicy_test

import (
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
	"github.com/yi-nology/goagent-kernel/cmdpolicy"
)

// ⭐ 前缀判据的三类典型绕过，由「全调用 AST 事实面」闭合（事实面收编上游
// oagentkit/tools/bashguard，见 facts.go）。每类一组用例，正例钉住绕过被堵、
// 反例钉住引号内的字面量不被误伤。
//
// 反向验证（注入对应退化 → 必须红）：
//  · 去掉事实面下限 → TestRedirectBypassClosed 判红；
//  · 判定只看第一个调用 → TestCommandSubstitutionBypassClosed /
//    TestMultiSegmentBypassClosed 判红。

// 写重定向绕过：git status > /etc/passwd 的前两个 token 仍是 git status，
// 前缀规则结构上看不见 > ——事实面下限把它抬到 prompt。
func TestRedirectBypassClosed(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "git-status", Prefix: cmdpolicy.Prefix{{"git"}, {"status"}}, Decision: cmdpolicy.DecisionAllow},
	)
	for _, cmd := range []string{
		"git status > /etc/passwd",
		"git status >/etc/passwd",
		"git status >> /etc/passwd",
		"git status 2>/dev/null",
	} {
		v, err := p.DecideCommand(cmd)
		if err != nil {
			t.Fatalf("DecideCommand(%q): %v", cmd, err)
		}
		if v.Decision != cmdpolicy.DecisionPrompt {
			t.Fatalf("%q: 写重定向必须抬到 prompt，实得 %v", cmd, v.Decision)
		}
		if !v.Wrote {
			t.Fatalf("%q: Verdict.Wrote 必须置位", cmd)
		}
	}
	// 反例：引号内的 > 是字面参数，不是重定向——不得误伤。
	for _, cmd := range []string{"echo \"a>b\"", "echo 'a>b'", "git status"} {
		v, err := p.DecideCommand(cmd)
		if err != nil {
			t.Fatalf("DecideCommand(%q): %v", cmd, err)
		}
		if v.Decision != cmdpolicy.DecisionAllow || v.Wrote {
			t.Fatalf("%q: 无写重定向应照常 allow，实得 %+v", cmd, v)
		}
	}
}

// 命令替换绕过：echo $(rm -rf /) 里的 rm 是嵌套调用，全调用面必须判到它。
func TestCommandSubstitutionBypassClosed(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "wipe", Prefix: cmdpolicy.Prefix{{"rm"}}, Decision: cmdpolicy.DecisionForbidden},
	)
	for _, cmd := range []string{
		"echo $(rm -rf /)",
		"echo `rm -rf /`",
		"echo hi $(inner $(rm -rf /))",
		"a | rm -rf /",
	} {
		v, err := p.DecideCommand(cmd)
		if err != nil {
			t.Fatalf("DecideCommand(%q): %v", cmd, err)
		}
		if v.Decision != cmdpolicy.DecisionForbidden || v.Rule != "wipe" {
			t.Fatalf("%q: 嵌套 rm 必须 forbidden，实得 %+v", cmd, v)
		}
	}
	// 反例：单引号内一切字面——$(rm -rf /) 不是调用。
	v, err := p.DecideCommand("echo '$(rm -rf /)'")
	if err != nil {
		t.Fatalf("DecideCommand: %v", err)
	}
	if v.Decision != cmdpolicy.DecisionAllow {
		t.Fatalf("单引号字面不得误判，实得 %+v", v)
	}
	// 动态词（无嵌套调用）同样不许静默 allow：判不了就不放行（三态形态 = prompt）。
	v, err = p.DecideCommand("ls $WHO")
	if err != nil {
		t.Fatalf("DecideCommand: %v", err)
	}
	if v.Decision != cmdpolicy.DecisionPrompt || !v.Dynamic {
		t.Fatalf("动态展开必须抬到 prompt，实得 %+v", v)
	}
}

// 多段绕过：git status; rm -rf / 的前缀是 git——全调用面把 rm 那段也判了。
func TestMultiSegmentBypassClosed(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "git-status", Prefix: cmdpolicy.Prefix{{"git"}, {"status"}}, Decision: cmdpolicy.DecisionAllow},
		cmdpolicy.Rule{Name: "wipe", Prefix: cmdpolicy.Prefix{{"rm"}}, Decision: cmdpolicy.DecisionForbidden},
	)
	for _, cmd := range []string{
		"git status; rm -rf /",
		"git status && rm -rf /",
		"git status || rm -rf /",
	} {
		v, err := p.DecideCommand(cmd)
		if err != nil {
			t.Fatalf("DecideCommand(%q): %v", cmd, err)
		}
		if v.Decision != cmdpolicy.DecisionForbidden || v.Rule != "wipe" {
			t.Fatalf("%q: 后段 rm 必须判到，实得 %+v", cmd, v)
		}
	}
}

// 判不了就不放行：AST 未评估（超长 / 解析失败）⇒ 报错，不当作「没命中规则」。
func TestUnjudgeableFailsClosed(t *testing.T) {
	p := mustPolicy(t)
	_, err := p.DecideCommand("echo )")
	if err == nil || !errors.Is(err, cmdpolicy.ErrUnjudgeable) {
		t.Fatalf("解析失败必须报 ErrUnjudgeable，实得 %v", err)
	}
	_, err = p.DecideCommand("x " + strings.Repeat("y", 11000))
	if err == nil || !errors.Is(err, cmdpolicy.ErrUnjudgeable) {
		t.Fatalf("超长必须报 ErrUnjudgeable，实得 %v", err)
	}
	// 分词错误契约不变：未闭合引号仍报本包哨兵。
	_, err = p.DecideCommand("echo \"abc")
	if err == nil || !errors.Is(err, cmdpolicy.ErrUnclosedQuote) {
		t.Fatalf("未闭合引号必须报 ErrUnclosedQuote，实得 %v", err)
	}
}

// Explain 必须写出事实面信号：prompt 常常是下限抬上来的，
// 不写出来，模型会学会「绕开规则」而不是「别写重定向」。
func TestExplainMentionsFactSignals(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "git-status", Prefix: cmdpolicy.Prefix{{"git"}, {"status"}}, Decision: cmdpolicy.DecisionAllow, Justification: "只读命令"},
	)
	v, err := p.DecideCommand("git status > /etc/passwd")
	if err != nil {
		t.Fatalf("DecideCommand: %v", err)
	}
	msg := v.Explain()
	for _, want := range []string{"prompt", "写重定向", "只读命令"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Explain 应含 %q，实得 %q", want, msg)
		}
	}
	v, err = p.DecideCommand("ls $WHO")
	if err != nil {
		t.Fatalf("DecideCommand: %v", err)
	}
	msg = v.Explain()
	for _, want := range []string{"prompt", "动态展开", "no rule matched"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Explain 应含 %q，实得 %q", want, msg)
		}
	}
}

// Decide(argv) 保持纯前缀语义：调用方自报 argv 时不做事实面判定（文档承诺）。
func TestDecideArgvStaysPure(t *testing.T) {
	p := mustPolicy(t,
		cmdpolicy.Rule{Name: "git-status", Prefix: cmdpolicy.Prefix{{"git"}, {"status"}}, Decision: cmdpolicy.DecisionAllow},
	)
	v := p.Decide([]string{"git", "status", ">", "/etc/passwd"})
	if v.Decision != cmdpolicy.DecisionAllow || v.Wrote || v.Dynamic {
		t.Fatalf("Decide(argv) 不做事实面判定，实得 %+v", v)
	}
}
