package hookengine

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// 截断必须落在 rune 边界——按字节硬切会产出**非法 UTF-8**。
//
// 对照组是「没超预算」：它原样返回、不追加任何提示，两者一起才能证明
// 「边界处理」不是靠「总是截短一点」蒙过去的。
func TestClipKeepsUTF8Boundary(t *testing.T) {
	e := &Engine{maxChars: DefaultMaxChars}

	// 每个汉字 3 字节 ⇒ 512 = 3*170 + 2，预算恰好落在第 171 个字符的中间。
	got := e.clip(strings.Repeat("拒", 400))
	if !utf8.ValidString(got) {
		t.Fatalf("截断产出了非法 UTF-8（%d 字节）: %q", len(got), got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatal("截断把字符切坏成了替换字符")
	}
	if !strings.HasSuffix(got, "…（已截断）") {
		t.Fatalf("超预算必须带截断提示，实得 %q", got[len(got)-16:])
	}
	body := strings.TrimSuffix(got, "…（已截断）")
	if len(body) > DefaultMaxChars {
		t.Fatalf("截断后正文 %d 字节，超出预算 %d", len(body), DefaultMaxChars)
	}
	// 「退到边界」最多退 2 字节，退多了等于白扔预算。
	if len(body) < DefaultMaxChars-2 {
		t.Fatalf("边界回退过头：正文只有 %d 字节，预算是 %d", len(body), DefaultMaxChars)
	}

	// 对照：没超预算时逐字返回，不带提示、不留痕迹。
	short := strings.Repeat("拒", 5)
	if out := e.clip(short); out != short {
		t.Fatalf("未超预算不该改动，实得 %q", out)
	}
}

// 裁决路径上的理由同样过这套截断（clip 是唯一入口，此处钉住接线）。
func TestDispatchReasonKeepsUTF8Boundary(t *testing.T) {
	e, err := New([]Hook{{
		Name: "h", Event: "pre_tool", Command: "c", Blocking: true, Timeout: time.Second,
	}}, truncFailRunner{stderr: strings.Repeat("拒", 400)})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Dispatch(context.Background(), Payload{Event: "pre_tool"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictDeny {
		t.Fatalf("阻塞 hook 非零退出必须 deny，实得 %s", res.Verdict)
	}
	for _, r := range res.Reasons {
		if !utf8.ValidString(r) {
			t.Fatalf("裁决理由含非法 UTF-8: %q", r)
		}
	}
}

// truncFailRunner 恒定返回一条超长 stderr（只为把内容喂进裁决的截断路径）。
type truncFailRunner struct{ stderr string }

func (r truncFailRunner) Run(ctx context.Context, h Hook, p Payload) (Outcome, error) {
	return Outcome{ExitCode: 1, Stderr: r.stderr}, nil
}
