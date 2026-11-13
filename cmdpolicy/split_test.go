package cmdpolicy_test

import (
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/shellx"
	"github.com/yi-nology/goagent-kernel/cmdpolicy"
)

// v0.35.0：导出的 Split 转发已删（裸分词直连 oekit/pkg/shellx.Split）。
// 本文件只钉**本包还拥有的那一层**：SplitCommands 的切段与错误语义。
// 上游分词口径（POSIX 反斜杠 / 分隔符相邻合并 / CR / 空参数）由上游仓
// 自己的用例钉住——本仓不再为转发壳维持第二份口径用例。

// 分词失败时判不了就不放行：SplitCommands 的错误必须能判出本包哨兵，
// 也必须能判出上游哨兵（两层判法都成立）。
func TestSplitCommandsErrorsAreIdentifiable(t *testing.T) {
	_, err := cmdpolicy.SplitCommands("echo \"abc; still open")
	if err == nil {
		t.Fatal("未闭合的引号必须报错")
	}
	if !errors.Is(err, cmdpolicy.ErrUnclosedQuote) {
		t.Fatalf("应能判为本包哨兵：%v", err)
	}
	if !errors.Is(err, shellx.ErrUnclosedQuote) {
		t.Fatalf("应能判为上游哨兵：%v", err)
	}
	_, err = cmdpolicy.SplitCommands("echo x\\")
	if err == nil {
		t.Fatal("以转义符结尾必须报错")
	}
	if !errors.Is(err, cmdpolicy.ErrTrailingEscape) {
		t.Fatalf("应能判为本包哨兵：%v", err)
	}
	if !errors.Is(err, shellx.ErrTrailingEscape) {
		t.Fatalf("应能判为上游哨兵：%v", err)
	}
}

// 段必须可往返：段再分词一次得到与原段逐项相同的 token。
// 否则含空格的备选前缀（如 git + a b）会永不命中——
// 段若被摊平成空格分隔，token 边界就变了。
func TestSplitCommandsSegmentsRoundTrip(t *testing.T) {
	segs, err := cmdpolicy.SplitCommands("git 'a b' status; rm -rf /tmp/x")
	if err != nil {
		t.Fatalf("SplitCommands: %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("want 2 segments, got %v", segs)
	}
	wantTokens := [][]string{
		{"git", "a b", "status"},
		{"rm", "-rf", "/tmp/x"},
	}
	for i, seg := range segs {
		argv, err := shellx.Split(seg)
		if err != nil {
			t.Fatalf("段 %d 再分词失败: %v", i, err)
		}
		if strings.Join(argv, "\x00") != strings.Join(wantTokens[i], "\x00") {
			t.Fatalf("段 %d 往返后 token 变了：段=%q 得=%v 期望=%v", i, seg, argv, wantTokens[i])
		}
	}
}
