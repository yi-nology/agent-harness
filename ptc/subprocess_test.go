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

// TestSubprocessProviderWithMemoryConfigured 配了上限**且当前环境兜得住**才声明支持。
//
// 判据是「当前环境兜不兜得住」而不是「配没配上限」：
// 要求了内存上限而当前环境建不出带 memory.max 的 cgroup（macOS 没有 cgroup；
// Linux 容器里没委派 memory 控制器也没有）⇒ **不支持**。
//
// 若这里报支持，登记簿就会选中本后端，然后 Exec 因建不出 cgroup 而失败——
// 宿主等到的却是「选了它却用不了」这种最难排查的形态。
func TestSubprocessProviderWithMemoryConfigured(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{MaxMemory: 256 << 20})
	// 零要求 → 支持（不要求内存上限就不受兜底能力约束）。
	if !p.Supports(sandbox.Budget{}) {
		t.Error("零要求应视为支持")
	}
	// 配了上限 → 是否支持取决于当前环境兜不兜得住。
	want := memLimitSupported()
	if got := p.Supports(sandbox.Budget{MaxMemory: 1 << 20}); got != want {
		t.Errorf("配了上限时的支持判定=%v，应等于当前环境兜不兜得住=%v", got, want)
	}
}

// TestSubprocessProviderWithoutMemoryConfigured 没配上限却要求兜底 ⇒ 不支持。
//
// 这是「不撒谎」的方向：宿主要内存兜底，而本后端没配 ⇒ 明确说兜不住，
// 让登记簿去选能兜的后端，而不是选一个装样子的。
//
// # 一条环境性事实：本条在「兜不住」的环境上无法区分两个分支
//
// `Supports` 的两个返回 false 的分支是：
//   - **没配上限**（配置问题）——本条要抓的；
//   - **配了上限但当前环境兜不住**（环境问题）。
//
// 在 macOS（无 cgroup）这类兜不住的环境上，两者**返回值相同**（都 false），
// 故「没配上限」这个缺陷在此环境**不可观测**——不是用例没写对，
// 是这两条分支在结果上无法区分。
//
// ⇒ 本条的区分力**依赖 Linux + 可写 cgroup 的环境**（CI 的 ubuntu-24.04）：
// 那里 `memLimitSupported()` 为 true，「没配」与「配了但兜不住」才分得开。
// 反向对照见 TestSubprocessProviderWithMemoryConfigured。
func TestSubprocessProviderWithoutMemoryConfigured(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{})
	if p.Supports(sandbox.Budget{MaxMemory: 1 << 20}) {
		t.Error("没配内存上限却声称能兑现，是撒谎")
	}
	// 反向对照：配了上限时的判定**只**取决于当前环境兜不兜得住。
	// 在兜得住的环境（Linux + 可写 cgroup）上，这两条分支才分得开。
	p2 := NewSubprocessProvider(SubprocessOptions{MaxMemory: 256 << 20})
	got := p2.Supports(sandbox.Budget{MaxMemory: 1 << 20})
	if got != memLimitSupported() {
		t.Errorf("配了上限时的判定=%v，应等于当前环境能力=%v", got, memLimitSupported())
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
