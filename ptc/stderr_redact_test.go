package ptc

import (
	"strings"
	"testing"
)

// ⭐ 子进程 stderr 进诊断报告前必须脱敏：子进程常把连接串、API key、
// token 打进 stderr（MCP server 启动时回显配置是最高频形态），
// 而这段文本会原样进错误消息与诊断报告——等于凭据泄漏面。
//
// 脱敏用上游 guardx/logredact（本仓 pii 管个人信息，两者互补不重叠）。
//
// 反向验证：把 subprocess.go 的写入点改回原文赋值（不脱敏），
// TestChildStderrRedactsCredentials 必红。

// 反向验证：凭据形态的 stderr 必须被打码。
func TestChildStderrRedactsCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "URL 内嵌凭据",
			in:   "connecting to postgres://user:s3cr3t@db.internal:5432/app",
			want: "s3cr3t",
		},
		{
			name: "键值对形态",
			in:   "startup ok api_key=abcd1234efgh5678",
			want: "abcd1234efgh5678",
		},
		{
			name: "Bearer 头形态",
			in:   "authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijkl",
			want: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijkl",
		},
		{
			name: "厂商 token 前缀",
			in:   "using token ghp_abcdefghijklmnopqrstuvwxyz012345",
			want: "ghp_abcdefghijklmnopqrstuvwxyz012345",
		},
	}
	for _, c := range cases {
		got := redactChildStderr(c.in)
		if strings.Contains(got, c.want) {
			t.Fatalf("%s: 凭据原文泄漏：%q → %q", c.name, c.in, got)
		}
		if got == c.in {
			t.Fatalf("%s: stderr 竟逐字未变：%q", c.name, got)
		}
	}
}

// 脱敏不得毁掉**死因**：崩溃原因（Go runtime fatal error、panic 栈）
// 是这个字段存在的理由，打码不能把它一起吃掉。
func TestChildStderrKeepsCrashReason(t *testing.T) {
	in := "fatal error: runtime: out of memory\n\ngoroutine 1 [running]:\nmain.main()\n"
	got := redactChildStderr(in)
	for _, want := range []string{"fatal error", "out of memory", "goroutine"} {
		if !strings.Contains(got, want) {
			t.Fatalf("死因片段 %q 被误伤：%q", want, got)
		}
	}
}

// 无凭据的 stderr 必须**逐字不变**：脱敏是「只打码凭据」，
// 不是「重写一切」。误伤方向是安全方向的错，但不能无节制地改写诊断文本。
func TestChildStderrUnchangedWhenNoCredential(t *testing.T) {
	in := "fatal error: runtime: out of memory\n"
	if got := redactChildStderr(in); got != in {
		t.Fatalf("无凭据的 stderr 应逐字不变：%q → %q", in, got)
	}
}
