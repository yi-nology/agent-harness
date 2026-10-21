// sandbox_test.go —— 执行沙箱后端档位登记门禁本身的用例。
//
// ## 为什么这道门禁必须自带测试
//
// 它判的是「别人写不写得对」，而它自己的判据（方法指纹、ID/档位提取、登记↔代码
// 双向核对）每一处写错，失效形态都是**门禁变绿**：本仓多了一个未登记的后端、
// 或者档位漂移了，而门禁显示全绿。
//
// ## 特别要防的是「判据过严」
//
// 首版把 Exec 的入参写成「恰好一个」——而真实实现是 `(ctx context.Context,
// req Request)`，两个不同类型的字段占两个条目。于是本仓唯一的后端不被认出，
// 门禁对它**完全失明**，却仍然显示「通过」。
//
// 这就是「零缺口是可疑信号」的同一种病：判据过严与过松都表现为绿。
package archcheck

import (
	"strings"
	"testing"
)

// providerFixtureSrc 一份形状正确的后端实现（夹具用合成数据）。
const providerFixtureSrc = `package host

import "context"

type Level int

const (
	LevelNone Level = iota
	LevelProcess
	LevelOS
)

type Request struct{}

type Outcome struct{}

type Provider struct{}

const ProviderID = "host-demo"

func (p Provider) ID() string { return ProviderID }

func (p Provider) Level() Level { return LevelProcess }

func (p Provider) Exec(ctx context.Context, req Request) (Outcome, error) {
	return Outcome{}, nil
}
`

// sandboxFixture 造一个含后端实现的最小仓，跑判定，返回违规集。
func sandboxFixture(t *testing.T, src string, cfg SandboxProviders) []Violation {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"host/host.go": src})
	vs, err := sandboxProviderViolations(root, cfg)
	if err != nil {
		t.Fatalf("判定失败: %v", err)
	}
	return vs
}

// kindsOf 取违规集里出现的 kind（去重）。
func kindsOf(vs []Violation) string {
	seen := map[string]bool{}
	var out []string
	for _, v := range vs {
		if !seen[v.Kind] {
			seen[v.Kind] = true
			out = append(out, v.Kind)
		}
	}
	return strings.Join(out, ",")
}

// 合法形态：登记与代码事实一致 → 零违规。
func TestSandboxProviderRegisteredIsClean(t *testing.T) {
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{
		Providers: []SandboxProvider{{ID: "host-demo", Level: "LevelProcess", Package: "host"}},
	})
	if len(vs) != 0 {
		t.Fatalf("登记与代码一致时不应有违规: %s", textsOf(vs))
	}
}

// 档位漂移：代码改了档位而登记没改 → 红，且红在 drift 上。
func TestSandboxProviderLevelDriftIsCaught(t *testing.T) {
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{
		Providers: []SandboxProvider{{ID: "host-demo", Level: "LevelOS", Package: "host"}},
	})
	if len(vs) != 1 {
		t.Fatalf("应恰有 1 处违规（档位漂移），实得 %d: %s", len(vs), textsOf(vs))
	}
	if vs[0].Kind != "sandbox_provider_drift" {
		t.Fatalf("违规形态应为档位漂移，实得 %q: %s", vs[0].Kind, vs[0].Text)
	}
}

// 未登记：代码里有后端实现而登记里没有 → 红。
//
// 这一条是「未登记就不生效」的兜底：档位声明要有人签字，没人签的就得报出来，
// 而不是「没登记就当没这回事」。
func TestSandboxProviderUnregisteredIsCaught(t *testing.T) {
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{})
	if len(vs) != 1 {
		t.Fatalf("应恰有 1 处违规（未登记），实得 %d: %s", len(vs), textsOf(vs))
	}
	if vs[0].Kind != "sandbox_provider_unregistered" {
		t.Fatalf("违规形态应为未登记，实得 %q: %s", vs[0].Kind, vs[0].Text)
	}
}

// 登记的实现不存在（实现被删或改了标识）→ 红。
//
// 断言只看 kind、不看条数：此时「登记的实现缺失」与「代码里那个未登记」**两个事实
// 同时为真**，两条违规都是对的。卡条数会把正确的实现判成错实现——断言要选
// 区分性最强的那个特征，不是最容易被凑齐的那个。
func TestSandboxProviderMissingImplIsCaught(t *testing.T) {
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{
		Providers: []SandboxProvider{{ID: "host-other", Level: "LevelProcess", Package: "host"}},
	})
	if !strings.Contains(kindsOf(vs), "sandbox_provider_missing") {
		t.Fatalf("应报出「登记的实现缺失」: %s", textsOf(vs))
	}
}

// 陈旧登记（包目录不存在）→ 红当天。
func TestSandboxProviderStalePackageIsCaught(t *testing.T) {
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{
		Providers: []SandboxProvider{{ID: "host-demo", Level: "LevelProcess", Package: "gone"}},
	})
	if !strings.Contains(kindsOf(vs), "sandbox_provider_stale") {
		t.Fatalf("应报出「陈旧登记」: %s", textsOf(vs))
	}
}

// ID() 返回值静态不可识别 → 红（登记无从核对）。
func TestSandboxProviderUnreadableIDIsCaught(t *testing.T) {
	src := strings.Replace(providerFixtureSrc,
		"func (p Provider) ID() string { return ProviderID }",
		"func (p Provider) ID() string { return buildID() }", 1)
	vs := sandboxFixture(t, src, SandboxProviders{})
	if len(vs) != 1 {
		t.Fatalf("应恰有 1 处违规（ID 不可识别），实得 %d: %s", len(vs), textsOf(vs))
	}
	if vs[0].Kind != "sandbox_provider_id_unreadable" {
		t.Fatalf("违规形态应为 ID 不可识别，实得 %q: %s", vs[0].Kind, vs[0].Text)
	}
}

// Level() 返回非常量 → 红（档位无从核对）。
func TestSandboxProviderNonConstantLevelIsCaught(t *testing.T) {
	src := strings.Replace(providerFixtureSrc,
		"func (p Provider) Level() Level { return LevelProcess }",
		"func (p Provider) Level() Level { return pickLevel() }", 1)
	vs := sandboxFixture(t, src, SandboxProviders{})
	if len(vs) != 1 {
		t.Fatalf("应恰有 1 处违规（档位非常量），实得 %d: %s", len(vs), textsOf(vs))
	}
	if vs[0].Kind != "sandbox_provider_level_unreadable" {
		t.Fatalf("违规形态应为档位非常量，实得 %q: %s", vs[0].Kind, vs[0].Text)
	}
}

// 不得被误伤：只有 Level() 方法、不是后端实现的类型不算后端。
//
// 这一半与违规用例同等重要：判据过宽会把别的 API（某处表示「等级」的方法）
// 当成后端实现，于是登记面上冒出莫须有的条目，而真问题被淹没。
func TestSandboxProviderNonProviderTypeNotFlagged(t *testing.T) {
	src := `package host

type Level int

type Meter struct{}

func (m Meter) Level() Level { return 0 }
`
	vs := sandboxFixture(t, src, SandboxProviders{})
	if len(vs) != 0 {
		t.Fatalf("非后端实现不得被判违规: %s", textsOf(vs))
	}
}

// 段显式关闭 → 不判（宿主仓没有这套概念时不该被强制）。
func TestSandboxProviderSectionDisabledSkips(t *testing.T) {
	off := false
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{Enabled: &off})
	if len(vs) != 0 {
		t.Fatalf("段关闭时不应判违规: %s", textsOf(vs))
	}
}

// 重复登记 → 红（同一后端登记两次，登记面自相矛盾）。
func TestSandboxProviderDuplicateRegistrationIsCaught(t *testing.T) {
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{
		Providers: []SandboxProvider{
			{ID: "host-demo", Level: "LevelProcess", Package: "host"},
			{ID: "host-demo", Level: "LevelProcess", Package: "host"},
		},
	})
	if !strings.Contains(kindsOf(vs), "sandbox_provider_dup") {
		t.Fatalf("重复登记应报出: %s", textsOf(vs))
	}
}

// 方法指纹：Exec 的入参是两个字段时仍须被认出。
//
// 这一条钉的是「判据过严」的坑——首版写成「恰好一个入参」，于是本仓唯一的后端
// 不被认出，门禁对它完全失明却仍显示通过。
func TestSandboxProviderFingerprintAcceptsRealSignature(t *testing.T) {
	vs := sandboxFixture(t, providerFixtureSrc, SandboxProviders{})
	if len(vs) != 1 || vs[0].Kind != "sandbox_provider_unregistered" {
		t.Fatalf("真实签名形态的后端应被认出（报未登记），实得 %s", textsOf(vs))
	}
}
