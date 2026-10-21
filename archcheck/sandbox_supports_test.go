package archcheck

import (
	"strings"
	"testing"
)

// 预算支持面判据的用例。夹具里的 Provider 与 sandbox_test.go 同款——
// 门禁**不能 import 被判的包**，所以判定面只认「方法指纹」：
// 有没有 Supports 方法。

// 支持面夹具：带 Supports 方法的后端。
const sandboxCapableSrc = `package host

import "context"

type Level int

const (
	LevelNone Level = iota
	LevelProcess
	LevelOS
)

type Request struct{}

type Outcome struct{}

type Budget struct {
	MaxMemory int64
}

type Provider struct{}

const ProviderID = "host-demo"

func (p Provider) ID() string { return ProviderID }

func (p Provider) Level() Level { return LevelProcess }

func (p Provider) Exec(ctx context.Context, req Request) (Outcome, error) {
	return Outcome{}, nil
}

func (p Provider) Supports(b Budget) bool { return b.MaxMemory <= 0 }
`

// 合法形态一：后端没有 Supports 方法 ⇒ 登记只写基本三闸 → 零违规。
func TestSandboxSupportsBasicOnlyIsClean(t *testing.T) {
	cfg := SandboxProviders{
		Providers: []SandboxProvider{{
			ID: "host-demo", Level: "LevelProcess", Package: "host",
			Supports: []string{"wall_clock", "max_calls", "max_bytes"},
		}},
	}
	vs := sandboxFixture(t, providerFixtureSrc, cfg)
	if len(vs) != 0 {
		t.Fatalf("应零违规，实得 %d: %s", len(vs), kindsOf(vs))
	}
}

// 合法形态二：后端有 Supports 方法 ⇒ 登记可含 max_memory → 零违规。
func TestSandboxSupportsMemoryWithCapabilityIsClean(t *testing.T) {
	cfg := SandboxProviders{
		Providers: []SandboxProvider{{
			ID: "host-demo", Level: "LevelProcess", Package: "host",
			Supports: []string{"wall_clock", "max_calls", "max_bytes", "max_memory"},
		}},
	}
	vs := sandboxFixture(t, sandboxCapableSrc, cfg)
	if len(vs) != 0 {
		t.Fatalf("应零违规，实得 %d: %s", len(vs), kindsOf(vs))
	}
}

// 违规形态：登记了 max_memory，但代码没有 Supports 方法 → 报红。
//
// 这是本条判据的核心：登记面说了兜得住，代码兜不住，要求与实际之间出现静默落差。
func TestSandboxSupportsMemoryWithoutCapabilityIsCaught(t *testing.T) {
	cfg := SandboxProviders{
		Providers: []SandboxProvider{{
			ID: "host-demo", Level: "LevelProcess", Package: "host",
			Supports: []string{"wall_clock", "max_calls", "max_bytes", "max_memory"},
		}},
	}
	vs := sandboxFixture(t, providerFixtureSrc, cfg)
	if !strings.Contains(kindsOf(vs), "sandbox_provider_supports_drift") {
		t.Fatalf("应报 supports 漂移，实得 %s", kindsOf(vs))
	}
}

// 违规形态：登记了未知预算项 → 报红。
func TestSandboxSupportsUnknownItemIsCaught(t *testing.T) {
	cfg := SandboxProviders{
		Providers: []SandboxProvider{{
			ID: "host-demo", Level: "LevelProcess", Package: "host",
			Supports: []string{"wall_clock", "gpu_memory"},
		}},
	}
	vs := sandboxFixture(t, providerFixtureSrc, cfg)
	if !strings.Contains(kindsOf(vs), "sandbox_provider_unknown_budget") {
		t.Fatalf("应报未知预算项，实得 %s", kindsOf(vs))
	}
}

// 反向形态：代码有 Supports 方法，而登记只写三闸 → **不报红**。
//
// 后端自报得比登记更保守不是撒谎（上游 Capable 的缺省语义就是「漏声明 ⇒ 兜不住」，
// 方向是保守的一侧）。真要兑现时改登记即可。
func TestSandboxSupportsConservativeRegistrationIsNotFlagged(t *testing.T) {
	cfg := SandboxProviders{
		Providers: []SandboxProvider{{
			ID: "host-demo", Level: "LevelProcess", Package: "host",
			Supports: []string{"wall_clock", "max_calls", "max_bytes"},
		}},
	}
	vs := sandboxFixture(t, sandboxCapableSrc, cfg)
	if len(vs) != 0 {
		t.Fatalf("保守登记不应报红，实得 %d: %s", len(vs), kindsOf(vs))
	}
}

// 未填 supports（零值）⇒ 视为只兑现基本三闸 ⇒ 零违规。
//
// 这是**缺省语义**——登记不填不等于声明了兜得住内存。
func TestSandboxSupportsOmittedIsClean(t *testing.T) {
	cfg := SandboxProviders{
		Providers: []SandboxProvider{{
			ID: "host-demo", Level: "LevelProcess", Package: "host",
		}},
	}
	vs := sandboxFixture(t, providerFixtureSrc, cfg)
	if len(vs) != 0 {
		t.Fatalf("未填 supports 应零违规，实得 %d: %s", len(vs), kindsOf(vs))
	}
}
