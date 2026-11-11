package sessionref_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yi-nology/goagent-kernel/sessionref"
)

// 字节裁剪必须落在 rune 边界，且**仍然贴着预算**——退多了等于白扔上下文。
//
// 对照组是「按行裁剪」：它不该被字节判据牵连（量纲不同各管各）。
func TestBoundsCutKeepsUTF8Boundary(t *testing.T) {
	body := strings.Repeat("段", 10) // 30 字节
	r := mustCapture(t, body, sessionref.Bounds{MaxBytes: 10})
	if !utf8.ValidString(r.Body) {
		t.Fatalf("裁剪产出了非法 UTF-8（%d 字节）: %q", len(r.Body), r.Body)
	}
	if len(r.Body) > 10 {
		t.Fatalf("裁剪后 %d 字节，超出上限 10", len(r.Body))
	}
	if n := utf8.RuneCountInString(r.Body); n != 3 {
		t.Fatalf("10 字节预算 3 字节步长应留下 3 个字符，实得 %d", n)
	}
	if !r.Truncated || r.OriginalBytes != len(body) {
		t.Fatalf("截断旗标与原文长度必须如实上报，实得 truncated=%v original=%d",
			r.Truncated, r.OriginalBytes)
	}

	// 对照：按行裁剪不牵扯字节边界。
	lines := mustCapture(t, "a\nb\nc", sessionref.Bounds{MaxLines: 2})
	if lines.Truncated != true || lines.Body != "a\nb" {
		t.Fatalf("按行裁剪实得 %q（truncated=%v）", lines.Body, lines.Truncated)
	}
}
