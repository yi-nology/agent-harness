package hookengine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// fakeRunner 按 hook 名返回预置结果（可注入 error）。
type fakeRunner struct {
	out map[string]Outcome
	err map[string]error
	ran []string
}

func (f *fakeRunner) Run(ctx context.Context, h Hook, p Payload) (Outcome, error) {
	f.ran = append(f.ran, h.Name)
	if e, ok := f.err[h.Name]; ok {
		return Outcome{}, e
	}
	if o, ok := f.out[h.Name]; ok {
		return o, nil
	}
	return Outcome{}, nil
}

func blocking(name string) Hook {
	return Hook{Name: name, Event: "pre_tool", Command: "check", Blocking: true, Timeout: time.Second}
}

// TestNewValidates 加载期自检：写错的声明必须在装载时报错。
func TestNewValidates(t *testing.T) {
	r := &fakeRunner{}
	cases := []struct {
		name  string
		hooks []Hook
		run   Runner
	}{
		{"没有 runner", []Hook{blocking("a")}, nil},
		{"空名", []Hook{{Event: "e", Command: "c"}}, r},
		{"重名", []Hook{blocking("a"), blocking("a")}, r},
		{"空事件", []Hook{{Name: "a", Command: "c"}}, r},
		{"空命令", []Hook{{Name: "a", Event: "e"}}, r},
		{"阻塞没超时", []Hook{{Name: "a", Event: "e", Command: "c", Blocking: true}}, r},
		{"非阻塞却声明 OnTimeout", []Hook{{Name: "a", Event: "e", Command: "c", OnTimeout: VerdictAllow}}, r},
		{"OnTimeout 不在词表", []Hook{{Name: "a", Event: "e", Command: "c", Blocking: true, Timeout: time.Second, OnTimeout: "maybe"}}, r},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.hooks, c.run); err == nil {
				t.Fatal("坏声明必须报错（静默修正会让声明与实际不一致）")
			}
		})
	}
}

// TestNoMatchIsNotSilent 无匹配不是静默：裁决 allow，但必须写明「没有经过检查」。
func TestNoMatchIsNotSilent(t *testing.T) {
	e, err := New([]Hook{blocking("a")}, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Dispatch(context.Background(), Payload{Event: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictAllow || len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "没有经过任何检查") {
		t.Fatalf("无匹配必须留痕说明，实得 %+v", res)
	}
}

// TestBlockingDenyAndFailClosed 非零退出与执行失败都必须 deny。
func TestBlockingDenyAndFailClosed(t *testing.T) {
	// 名字决定求值顺序（确定性）：a_ 开头先跑，z_ 开头后跑。
	r := &fakeRunner{
		out: map[string]Outcome{"a_exit": {ExitCode: 1, Stderr: "不满足前提"}},
		err: map[string]error{"z_boom": errors.New("跑不起来")},
	}
	hooks := []Hook{blocking("a_exit"), blocking("z_boom")}
	e, err := New(hooks, r)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := e.Dispatch(context.Background(), Payload{Event: "pre_tool"})
	if res.Verdict != VerdictDeny || len(res.Reasons) == 0 || !strings.Contains(res.Reasons[0], "不满足前提") {
		t.Fatalf("非零退出必须 deny 且理由取 stderr，实得 %+v", res)
	}
	if len(res.Ran) != 1 {
		t.Fatalf("第一个拒绝之后不该再跑（收口语义），实得 %v", res.Ran)
	}

	// 单独跑 boom：执行失败 ⇒ deny（fail-closed，不许当放行）。
	e2, _ := New([]Hook{blocking("z_boom")}, r)
	res2, _ := e2.Dispatch(context.Background(), Payload{Event: "pre_tool"})
	if res2.Verdict != VerdictDeny {
		t.Fatalf("执行失败必须 deny，实得 %+v", res2)
	}
}

// TestTimeoutHonoursDeclaration 超时裁决必须按声明，缺省 deny。
//
// 成对：显式声明 allow 时必须放行——否则「缺省 deny」与「永远 deny」分不开。
func TestTimeoutHonoursDeclaration(t *testing.T) {
	r := &fakeRunner{err: map[string]error{"slow": context.DeadlineExceeded}}
	def, err := New([]Hook{blocking("slow")}, r)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := def.Dispatch(context.Background(), Payload{Event: "pre_tool"})
	if res.Verdict != VerdictDeny || !strings.Contains(res.Reasons[0], "OnTimeout=deny") {
		t.Fatalf("缺省超时必须 deny，实得 %+v", res)
	}

	lenient := blocking("slow")
	lenient.OnTimeout = VerdictAllow
	e2, err := New([]Hook{lenient}, r)
	if err != nil {
		t.Fatal(err)
	}
	res2, _ := e2.Dispatch(context.Background(), Payload{Event: "pre_tool"})
	if res2.Verdict != VerdictAllow {
		t.Fatalf("显式 allow 时必须放行，实得 %+v", res2)
	}
}

// TestNonBlockingDoesNotDecide 非阻塞 hook 不影响裁决，但失败要留痕。
func TestNonBlockingDoesNotDecide(t *testing.T) {
	r := &fakeRunner{
		out: map[string]Outcome{"audit": {ExitCode: 2}},
		err: map[string]error{"log": errors.New("写日志失败")},
	}
	hooks := []Hook{
		{Name: "audit", Event: "pre_tool", Command: "c"},
		{Name: "log", Event: "pre_tool", Command: "c"},
	}
	e, err := New(hooks, r)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := e.Dispatch(context.Background(), Payload{Event: "pre_tool"})
	if res.Verdict != VerdictAllow {
		t.Fatalf("非阻塞 hook 不得影响裁决，实得 %+v", res)
	}
	if len(res.Notes) != 2 {
		t.Fatalf("非阻塞的失败必须留痕（静默会让「旁路没生效」无从发现），实得 %v", res.Notes)
	}
}

// TestToolPrefixAndClip 前缀匹配与理由截断。
func TestToolPrefixAndClip(t *testing.T) {
	long := strings.Repeat("x", DefaultMaxChars+50)
	r := &fakeRunner{out: map[string]Outcome{"p": {ExitCode: 1, Stdout: long}}}
	e, err := New([]Hook{{
		Name: "p", Event: "pre_tool", ToolPrefix: "fs_", Command: "c",
		Blocking: true, Timeout: time.Second,
	}}, r)
	if err != nil {
		t.Fatal(err)
	}
	// 不匹配前缀 ⇒ 没有 hook 参与。
	res, _ := e.Dispatch(context.Background(), Payload{Event: "pre_tool", Tool: "net_get"})
	if res.Verdict != VerdictAllow {
		t.Fatal("前缀不匹配的 hook 不该参与裁决")
	}
	// 匹配 ⇒ deny 且理由被截断（原样带进日志会撑爆上下文）。
	res2, _ := e.Dispatch(context.Background(), Payload{Event: "pre_tool", Tool: "fs_write"})
	if res2.Verdict != VerdictDeny {
		t.Fatal("前缀匹配的 hook 必须参与裁决")
	}
	if !strings.Contains(res2.Reasons[0], "已截断") {
		t.Fatalf("超长理由必须截断，实得长度 %d", len(res2.Reasons[0]))
	}
}
