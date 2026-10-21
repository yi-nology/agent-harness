package ptc

import (
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/yi-nology/agent-harness/toolname"
)

// 本文件验证子进程后端的**纯契约**部分（不真的起进程——端到端在
// subprocess_exec_test.go，那是外部测试包）。

// TestSubprocessProviderBasics 子进程后端的基本契约。
func TestSubprocessProviderBasics(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{})
	if p.ID() != SubprocessProviderID {
		t.Errorf("ID=%q，应为 %q", p.ID(), SubprocessProviderID)
	}
	// 档位仍是 LevelProcess（能力面没变，强的只是资源面）。
	if p.Level() != sandbox.LevelProcess {
		t.Errorf("档位=%v，应为 process", p.Level())
	}
	// 支持面：**没配上限时不声称能兑现**——那是撒谎。
	// （配了上限才声明支持，见下一个用例。）
	if p.Supports(sandbox.Budget{MaxMemory: 1 << 20}) {
		t.Error("没配内存上限时不应声称兑现")
	}
}

// TestSubprocessProviderWithMemoryConfigured 配了上限才声明支持。
func TestSubprocessProviderWithMemoryConfigured(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{MaxMemory: 256 << 20})
	if !p.Supports(sandbox.Budget{MaxMemory: 1 << 20}) {
		t.Error("配了内存上限且本后端能兑现，应声明支持")
	}
	if !p.Supports(sandbox.Budget{}) {
		t.Error("零要求应视为支持")
	}
}

// TestSubprocessProviderUnsetMemoryNotSupported 没配 MaxMemory 时不声称兑现内存。
//
// 「漏声明得到的是兜不住，不是兜得住」——保守的那一侧。
func TestSubprocessProviderUnsetMemoryNotSupported(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{})
	if p.Supports(sandbox.Budget{MaxMemory: 1 << 20}) {
		t.Error("没配内存上限时不应声称兑现内存")
	}
	if !p.Supports(sandbox.Budget{}) {
		t.Error("零要求应视为支持")
	}
}

// TestKindForErrorKind 失败分类映射。
func TestKindForErrorKind(t *testing.T) {
	cases := map[string]string{
		"budget_calls":      "rate_limited",
		"budget_bytes":      "rate_limited",
		"syntax":            "permanent",
		"binding_signature": "permanent",
		"bad_result":        "permanent",
		"deadline":          "transient",
		"runtime":           "transient",
		"permission_denied": "permission_denied",
		"":                  "unknown",
		"没见过的":              "unknown",
	}
	for in, want := range cases {
		if got := kindForErrorKind(in); got != want {
			t.Errorf("kindForErrorKind(%q)=%q，应为 %q", in, got, want)
		}
	}
}

// TestRemoteFailureCarriesKind 父侧回传的失败带分类。
func TestRemoteFailureCarriesKind(t *testing.T) {
	f := &remoteFailure{kind: "permission_denied", msg: "没权限"}
	if f.Kind() != "permission_denied" {
		t.Errorf("Kind=%q，应为 permission_denied", f.Kind())
	}
	if f.Error() != "没权限" {
		t.Errorf("错误信息=%q，应为 没权限", f.Error())
	}
}

// TestToolNameNormalizeStable 工具名净化规则（形态与挂载名必须逐字相同）。
func TestToolNameNormalizeStable(t *testing.T) {
	if toolname.Normalize("get-logs") != "get_logs" {
		t.Error("工具名净化规则变了")
	}
}
