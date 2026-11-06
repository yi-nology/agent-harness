package pii

// 单一处置路径的回归护栏：面感知的处置路径不得被策略绕过，也不得因收敛而
// 丢掉结构下探能力。
//
// 出处：本文件原为 facade_test.go，守的是面无关的历史门面（Redact / RedactValue）。
// 那两个门面已于 2026-11-06 破坏性删除——面无关入口本身就是「策略配成 block 也
// 拿不到阻断信号」的洞。三条护栏原样迁到 Apply / ApplyValue 上：护栏守的是
// **行为**，不是某个函数名。

import (
	"strings"
	"testing"
)

// TestApplyHonorsStrategy 处置形态必须跟随策略。
//
// 历史：面无关门面曾硬编码 mask，于是宿主把策略配成 redact/hash 之后调它仍然
// 静默打码——策略被绕过。现在两条路径共用同一处置实现，这条护栏继续盯住
// 「策略改了，输出形态必须跟着改」。
func TestApplyHonorsStrategy(t *testing.T) {
	restore(t)

	// mask：保留前段+星号
	if out, _ := Apply(SurfaceInput, synMobile); strings.Contains(out, synMobile) || !strings.Contains(out, "****") {
		t.Fatalf("mask 应保长打码，实得 %q", out)
	}

	// redact：整段换占位
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatal(err)
	}
	if out, _ := Apply(SurfaceInput, synMobile); !strings.Contains(out, redactedPlaceholder) {
		t.Fatalf("Apply 未跟随策略 redact，实得 %q", out)
	}
	if out, _ := ApplyValue(SurfaceInput, map[string]string{"k": synMobile}); !strings.Contains(out.(map[string]string)["k"], redactedPlaceholder) {
		t.Fatalf("ApplyValue 未跟随策略 redact，实得 %q", out)
	}

	// hash：不可逆摘要
	if err := SetStrategy(StrategyHash); err != nil {
		t.Fatal(err)
	}
	if out, _ := Apply(SurfaceInput, synMobile); out == synMobile || strings.Contains(out, "138") {
		t.Fatalf("Apply 未跟随策略 hash，实得 %q", out)
	}
}

// TestApplyRespectsDisabledSurfaces 全部面关闭时必须透传——
// 否则「关掉某个敏感面」会被处置路径绕过。
func TestApplyRespectsDisabledSurfaces(t *testing.T) {
	restore(t)
	SetSurfaces(0)
	raw := "tel " + synMobile
	if out, blocked := Apply(SurfaceInput, raw); out != raw || blocked {
		t.Fatalf("全部面关闭时应透传且不阻断，实得 %q blocked=%v", out, blocked)
	}
	v, blocked := ApplyValue(SurfaceInput, map[string]string{"k": raw})
	if v.(map[string]string)["k"] != raw || blocked {
		t.Fatalf("全部面关闭时 ApplyValue 应透传且不阻断，实得 %v blocked=%v", v, blocked)
	}
}

// TestApplyValueStillStructures 结构下探能力不得因收敛而丢失：
// map[string]string 是历史盲区，[]any / []string 也必须覆盖。
func TestApplyValueStillStructures(t *testing.T) {
	restore(t)
	in := map[string]any{
		"a": []string{synMobile, "ok"},
		"b": []any{map[string]string{"c": synMobile}},
		"n": 7,
	}
	out, _ := ApplyValue(SurfaceInput, in)
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("应保持映射结构，实得 %T", out)
	}
	if strings.Contains(m["a"].([]string)[0], synMobile) {
		t.Fatalf("[]string 未处置: %v", m["a"])
	}
	nested := m["b"].([]any)[0].(map[string]string)
	if strings.Contains(nested["c"], synMobile) {
		t.Fatalf("嵌套 map[string]string 未处置: %v", nested)
	}
	if m["n"].(int) != 7 {
		t.Fatalf("非字符串标量应原样: %v", m["n"])
	}
}
