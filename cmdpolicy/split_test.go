package cmdpolicy_test

import (
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/shellx"
	"github.com/yi-nology/agent-harness/cmdpolicy"
)

// 本包分词已收编上游 oekit/pkg/shellx.Split。这组用例钉住两件事：
//
//  1. 行为等价：本仓分词与上游逐项相同——口径只有一处真相；
//  2. POSIX 修正：收编顺带修掉本仓手写版的三处错误（双引号内反斜杠、
//     分隔符合并、CR 归空白）。这三处是修复而非语义变更——
//     bash 实测的正是上游口径。
//
// 反向验证：把本仓分词换回「丢弃双引号内全部反斜杠」的旧实现，
// TestSplitPOSIXDoubleQuoteBackslash 必红。

// 与上游逐项对照：本仓不该有第二套分词口径。
func TestSplitMatchesUpstreamShellx(t *testing.T) {
	cases := []string{
		"git commit -m \"fix: a b\" --author 'x y'",
		"echo \"abc",
		"echo x\\",
		"echo \"a\\b\"",
		"echo \"a\\nb\"",
		"echo \"\"",
		"echo ''",
		"echo a\"b\"c",
		"echo 'a'b'c'",
		"git status; rm -rf /",
		"a && b",
		"a || b",
		"a | b",
		"echo \"a;b\"",
		"echo $HOME",
		"echo 'it\\'s'",
		"echo \"a\\\"b\"",
		"  spaced   out  ",
		"echo \"\" x",
		"echo \"tab\\there\"",
		"echo \\\"quoted\\\"",
		"a & & b",
		"a | | b",
		"a ;; b",
		"echo a\rb",
		"echo \"$HOME\"",
		"echo $HOME",
		"echo a\\ b",
		"echo \"multi word arg\"",
		"a;b&c|d",
		"",
		"   ",
	}
	for _, c := range cases {
		got, gotErr := cmdpolicy.Split(c)
		want, wantErr := shellx.Split(c)
		if (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("%q: err 存在性不同：本仓=%v 上游=%v", c, gotErr, wantErr)
		}
		if gotErr != nil {
			// 错误类型不同是设计如此：本仓用本包哨兵包装上游哨兵。
			// 但本仓哨兵必须仍能判出上游哨兵（errors.Is 链不断）。
			if !errors.Is(gotErr, wantErr) {
				t.Fatalf("%q: 本仓错误应能 errors.Is 判为上游哨兵：%v", c, gotErr)
			}
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("%q: 分词不一致：本仓=%v 上游=%v", c, got, want)
		}
	}
}

// 反向验证：双引号内只该转义双引号、反斜杠、美元符、反引号与换行，
// 其余反斜杠字面保留。旧实现丢弃全部反斜杠（"a\b" 变成 "ab"），
// bash 实测是 "a\b"。
func TestSplitPOSIXDoubleQuoteBackslash(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"echo \"a\\b\"", "a\\b"},
		{"echo \"a\\nb\"", "a\\nb"},
		{"echo \"tab\\there\"", "tab\\there"},
		{"echo \"a\\$b\"", "a$b"},
		{"echo \"a\\\"b\"", "a\"b"},
	}
	for _, c := range cases {
		argv, err := cmdpolicy.Split(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if len(argv) != 2 || argv[1] != c.want {
			t.Fatalf("%q: 期望第二 token=%q，实得 %v", c.in, c.want, argv)
		}
	}
}

// 反向验证：分隔符只按字符相邻合并。
// 旧实现按 token 相邻合并，把 "a & & b" 变成 "a && b"——
// 那是两条独立的后台命令被伪装成一条 && 链（bash 里该串本身是语法错误）。
func TestSplitSeparatorMergesOnlyWhenAdjacent(t *testing.T) {
	argv, err := cmdpolicy.Split("a & & b")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	want := []string{"a", "&", "&", "b"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", argv, want)
	}
	argv, err = cmdpolicy.Split("a && b")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	want = []string{"a", "&&", "b"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", argv, want)
	}
}

// 反向验证：CR 归空白（从配置粘贴的 CRLF 命令行，
// 行尾 CR 不该粘进最后一个 token）。
func TestSplitTreatsCRAsWhitespace(t *testing.T) {
	argv, err := cmdpolicy.Split("echo a\rb")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	want := []string{"echo", "a", "b"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", argv, want)
	}
}

// 空参数必须保留：丢掉它会让后续参数整体错位。
func TestSplitKeepsEmptyArgument(t *testing.T) {
	argv, err := cmdpolicy.Split("cmd \"\" arg")
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	want := []string{"cmd", "", "arg"}
	if strings.Join(argv, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v want %v", argv, want)
	}
}

// 分词失败时判不了就不放行：错误必须能判出本包哨兵，
// 也必须能判出上游哨兵（两层判法都成立）。
func TestSplitErrorsAreIdentifiable(t *testing.T) {
	_, err := cmdpolicy.Split("echo \"abc")
	if err == nil {
		t.Fatal("未闭合的引号必须报错")
	}
	if !errors.Is(err, cmdpolicy.ErrUnclosedQuote) {
		t.Fatalf("应能判为本包哨兵：%v", err)
	}
	if !errors.Is(err, shellx.ErrUnclosedQuote) {
		t.Fatalf("应能判为上游哨兵：%v", err)
	}
	_, err = cmdpolicy.Split("echo x\\")
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

// 段必须可往返：段再 Split 一次得到与原段逐项相同的 token。
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
		argv, err := cmdpolicy.Split(seg)
		if err != nil {
			t.Fatalf("段 %d 再分词失败: %v", i, err)
		}
		if strings.Join(argv, "\x00") != strings.Join(wantTokens[i], "\x00") {
			t.Fatalf("段 %d 往返后 token 变了：段=%q 得=%v 期望=%v", i, seg, argv, wantTokens[i])
		}
	}
}
