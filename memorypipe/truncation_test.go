package memorypipe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 裁剪必须落在 rune 边界：注入体是不可信正文的入口，切坏字符产出的非法
// UTF-8 会跟着一起进模型上下文，而两侧都读不出原因。
func TestClipKeepsUTF8Boundary(t *testing.T) {
	// 每个汉字 3 字节 ⇒ 预算 100 = 3*33 + 1，落在第 34 个字符的中间。
	got := clip(strings.Repeat("忆", 100), 100)
	if !utf8.ValidString(got) {
		t.Fatalf("裁剪产出了非法 UTF-8（%d 字节）: %q", len(got), got)
	}
	body := strings.TrimSuffix(got, "…")
	if len(body) > 100 {
		t.Fatalf("裁剪后正文 %d 字节，超出预算 100", len(body))
	}
	if n := utf8.RuneCountInString(body); n != 33 {
		t.Fatalf("边界回退应留下 33 个完整字符，实得 %d", n)
	}

	// 对照：没超预算原样返回，不追加省略号（否则每条记忆都带尾巴）。
	if out := clip("abc", 3); out != "abc" {
		t.Fatalf("未超预算不该改动，实得 %q", out)
	}
}

// 端到端：MaxBytes 落在多字节字符中间时，注入片段与渲染结果都必须是合法 UTF-8。
func TestBuildMaxBytesKeepsUTF8Boundary(t *testing.T) {
	res, err := Build([]Entry{
		entry("m1", strings.Repeat("忆", 40), "scope", "s1", base), // 120 字节
	}, Query{Scope: "scope", MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("超出 MaxBytes 必须标 truncated")
	}
	if len(res.Pieces) != 1 {
		t.Fatalf("应注入 1 条，实得 %d", len(res.Pieces))
	}
	if !utf8.ValidString(res.Pieces[0].Text) {
		t.Fatal("注入片段含非法 UTF-8")
	}
	if !utf8.ValidString(res.Prompt()) {
		t.Fatal("渲染结果含非法 UTF-8")
	}
}
