//go:build unix

package ptc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	ptc "github.com/yi-nology/goagent-kernel/ptc"
)

// issue #46：单程序后端收到与配置不匹配的请求程序必须 fail closed——
// 此前 req 是死参数，实际跑配置的程序却按请求上报（静默落差）。
func TestSubprocessExecRejectsProgramMismatch(t *testing.T) {
	child := buildChildProgram(t)
	prov := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
		Program: `return "from-opts"`,
		ExePath: child,
	})
	out, err := prov.Exec(context.Background(), sandbox.Request{
		Program: `return "from-request"`,
	})
	if err == nil {
		t.Fatalf("程序不匹配必须拒绝, got outcome %+v（此前静默跑配置的程序）", out)
	}
	if !strings.Contains(err.Error(), "不匹配") {
		t.Fatalf("错误应指明程序不匹配, got %v", err)
	}
}

// 空请求程序 = 显式「跑配置的那个」，放行且结果来自 opts.Program。
func TestSubprocessExecAcceptsEmptyRequestProgram(t *testing.T) {
	child := buildChildProgram(t)
	prov := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
		Program: `return "from-opts"`,
		ExePath: child,
	})
	out, err := prov.Exec(context.Background(), sandbox.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK || !strings.Contains(out.Value, "from-opts") {
		t.Fatalf("配置程序应正常执行, got %+v err=%v", out, err)
	}
}
