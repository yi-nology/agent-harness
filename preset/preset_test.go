package preset_test

import (
	"strings"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"

	"github.com/yi-nology/goagent-kernel/egress"
	"github.com/yi-nology/goagent-kernel/gatecheck"
	"github.com/yi-nology/goagent-kernel/preset"
)

func strict() preset.Preset {
	return preset.Preset{
		Name:   "strict",
		Level:  sandbox.LevelOS,
		Mode:   policy.ModeConfirm,
		Egress: egress.Spec{Mode: egress.ModeDenied},
	}
}

func loose() preset.Preset {
	return preset.Preset{
		Name:   "open",
		Level:  sandbox.LevelProcess,
		Mode:   policy.ModeAuto,
		Egress: egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"},
	}
}

func mustNew(t *testing.T, ps ...preset.Preset) *preset.Set {
	t.Helper()
	s, err := preset.New(ps)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	return s
}

func TestNewAndLookup(t *testing.T) {
	s := mustNew(t, loose(), strict())
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	if got := s.Names(); len(got) != 2 || got[0] != "open" || got[1] != "strict" {
		t.Fatalf("Names = %v（应为字典序 open/strict）", got)
	}
	p, err := s.Resolve("strict")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if p.Level != sandbox.LevelOS || p.Mode != policy.ModeConfirm || p.Egress.Mode != egress.ModeDenied {
		t.Fatalf("Resolve 取到的不是整份: %+v", p)
	}
}

// Resolve 的负例必须真的报错：降级到缺省会让「打错字」变成「用了另一档权限」，
// 而这个方向不可预测（可能更松）。只有正例的用例钉不住这条。
func TestResolveUnknownFailsClosed(t *testing.T) {
	s := mustNew(t, strict())
	if _, err := s.Resolve("stict"); err == nil {
		t.Fatal("未登记的名字必须报错，不得降级")
	} else if !strings.Contains(err.Error(), "strcit") && !strings.Contains(err.Error(), "strict") {
		t.Fatalf("报错该列出已登记名字: %v", err)
	}
	var nilSet *preset.Set
	if _, err := nilSet.Resolve("strict"); err == nil {
		t.Fatal("nil 集合必须报错")
	}
	if nilSet.Names() != nil || nilSet.All() != nil || nilSet.Len() != 0 {
		t.Fatal("nil 集合的读面应为空")
	}
}

func TestNewRejectsBadPresets(t *testing.T) {
	cases := []struct {
		name string
		ps   []preset.Preset
		want string
	}{
		{"空集合", nil, "集合为空"},
		{"空名", []preset.Preset{{Level: sandbox.LevelOS, Mode: policy.ModeConfirm}}, "不得为空"},
		{"名字带空白", []preset.Preset{{
			Name: "read only", Level: sandbox.LevelOS, Mode: policy.ModeConfirm,
		}}, "含空白"},
		{"重名", []preset.Preset{strict(), {
			Name: "strict", Level: sandbox.LevelProcess, Mode: policy.ModeAuto,
		}}, "预设名重复"},
		{"档位越界", []preset.Preset{{
			Name: "x", Level: sandbox.LevelMachine + 1, Mode: policy.ModeConfirm,
		}}, "不在词表内"},
		{"模式越界", []preset.Preset{{
			Name: "x", Level: sandbox.LevelOS, Mode: policy.Mode("whatever"),
		}}, "审批模式"},
		{"出口声明不可兑现", []preset.Preset{{
			Name: "x", Level: sandbox.LevelOS, Mode: policy.ModeConfirm,
			Egress: egress.Spec{Mode: egress.ModeProxy},
		}}, "出口策略不可兑现"},
		{"三面全松未签字", []preset.Preset{{
			Name: "yolo", Level: sandbox.LevelNone, Mode: policy.ModeFull,
		}}, "三面全松"},
		{"签字不带理由", []preset.Preset{{
			Name: "yolo", Level: sandbox.LevelNone, Mode: policy.ModeFull, Unsafe: true,
		}}, "没写理由"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := preset.New(c.ps); err == nil {
				t.Fatalf("应当报错（%s）", c.name)
			} else if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("报错该含 %q，实际: %v", c.want, err)
			}
		})
	}
}

// 三面全松 + 显式签字 + 理由 ⇒ 放行。这是「逃逸口」的正例：
// 没有它，上面的负例可能只是因为「Unsafe 那条路整体坏了」而绿。
func TestUnsafePresetAcceptedWithSignature(t *testing.T) {
	s := mustNew(t, preset.Preset{
		Name: "yolo", Level: sandbox.LevelNone, Mode: policy.ModeFull,
		Unsafe: true, Justification: "一次性数据搬迁窗口，跑完即撤",
	})
	p, err := s.Resolve("yolo")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if !strings.Contains(p.String(), "unsafe") {
		t.Fatalf("String 应标出 unsafe: %s", p.String())
	}
}

// 语义相同的两个名字必须报错：否则留痕里两个名字指向同一档，
// 读者会以为执行过两种权限。
func TestSameSemanticsDifferentNamesRejected(t *testing.T) {
	a := strict()
	b := strict()
	b.Name = "strict-alias"
	if _, err := preset.New([]preset.Preset{a, b}); err == nil {
		t.Fatal("语义相同的两个名字必须报错")
	} else if !strings.Contains(err.Error(), "语义完全相同") {
		t.Fatalf("报错口径不对: %v", err)
	}
}

// Key 只由三维组成：换名字不改 Key，改任一维就改 Key。
func TestKeyIndependentOfName(t *testing.T) {
	a := strict()
	b := strict()
	b.Name = "另一个名字"
	if a.Key() != b.Key() {
		t.Fatalf("Key 不该受名字影响: %q vs %q", a.Key(), b.Key())
	}
	c := strict()
	c.Egress = egress.Spec{Mode: egress.ModeUnrestricted}
	if a.Key() == c.Key() {
		t.Fatalf("改出口档位后 Key 必须变: %q", a.Key())
	}
	d := strict()
	d.Level = sandbox.LevelProcess
	if a.Key() == d.Key() {
		t.Fatalf("改档位后 Key 必须变: %q", a.Key())
	}
}

func TestAllReturnsCopy(t *testing.T) {
	s := mustNew(t, strict())
	got := s.All()
	if len(got) != 1 {
		t.Fatalf("All 长度 = %d", len(got))
	}
	got[0].Name = "被改了"
	if s.All()[0].Name != "strict" {
		t.Fatal("All 返回的必须是快照，改它不得影响集合")
	}
}

// GateRequest 一次写全三维（这是「拼装漂移」的正面堵法），
// 且不得动这次调用的事实字段。
func TestGateRequestFillsAllThreeDimensions(t *testing.T) {
	s := mustNew(t, strict())
	base := gatecheck.Request{
		Op:       policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto, Tool: "exec", Risk: 2},
		MinLevel: sandbox.LevelNone,
		Program:  "return 1",
	}
	got, err := s.GateRequest(base, "strict")
	if err != nil {
		t.Fatalf("GateRequest 失败: %v", err)
	}
	if got.MinLevel != sandbox.LevelOS {
		t.Fatalf("档位没补齐: %v", got.MinLevel)
	}
	if got.Egress.Mode != egress.ModeDenied {
		t.Fatalf("出口没补齐: %v", got.Egress.Mode)
	}
	if got.Op.Mode != policy.ModeConfirm {
		t.Fatalf("审批模式没补齐: %v", got.Op.Mode)
	}
	// 调用事实原样保留。
	if got.Op.Tool != "exec" || got.Op.Risk != 2 || got.Op.Type != policy.OpToolCall || got.Program != "return 1" {
		t.Fatalf("GateRequest 改了不该动的字段: %+v", got.Op)
	}
}

func TestGateRequestUnknownName(t *testing.T) {
	s := mustNew(t, strict())
	if _, err := s.GateRequest(gatecheck.Request{}, "nope"); err == nil {
		t.Fatal("未登记的预设名必须报错")
	}
}
