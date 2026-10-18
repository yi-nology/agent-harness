package pii

// 单一处置路径的回归护栏：历史门面不得成为策略的后门。

import (
	"strings"
	"testing"
)

// TestLegacyFacadeHonorsStrategy Redact/RedactValue 曾硬编码 mask，
// 于是宿主把策略配成 redact/hash 之后调它仍然静默打码——策略被绕过。
// 现在两条路径共用同一处置实现，面板必须跟随。
func TestLegacyFacadeHonorsStrategy(t *testing.T) {
	restore(t)

	// mask：保留前段+星号
	if out := Redact(synMobile); strings.Contains(out, synMobile) || !strings.Contains(out, "****") {
		t.Fatalf("mask 门面应保长打码，实得 %q", out)
	}

	// redact：整段换占位
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatal(err)
	}
	if out := Redact(synMobile); !strings.Contains(out, redactedPlaceholder) {
		t.Fatalf("Redact 未跟随策略 redact，实得 %q", out)
	}
	if out := RedactValue(map[string]string{"k": synMobile}).(map[string]string)["k"]; !strings.Contains(out, redactedPlaceholder) {
		t.Fatalf("RedactValue 未跟随策略 redact，实得 %q", out)
	}

	// hash：不可逆摘要
	if err := SetStrategy(StrategyHash); err != nil {
		t.Fatal(err)
	}
	if out := Redact(synMobile); out == synMobile || strings.Contains(out, "138") {
		t.Fatalf("Redact 未跟随策略 hash，实得 %q", out)
	}
}

// TestLegacyFacadeRespectsDisabledSurfaces 全部面关闭时门面必须透传——
// 否则「关掉某个敏感面」会被历史门面绕过。
func TestLegacyFacadeRespectsDisabledSurfaces(t *testing.T) {
	restore(t)
	SetSurfaces(0)
	raw := "tel " + synMobile
	if out := Redact(raw); out != raw {
		t.Fatalf("全部面关闭时 Redact 应透传，实得 %q", out)
	}
	v := RedactValue(map[string]string{"k": raw}).(map[string]string)["k"]
	if v != raw {
		t.Fatalf("全部面关闭时 RedactValue 应透传，实得 %q", v)
	}
}

// TestLegacyFacadeStillStructures 结构下探能力不得因收敛而丢失：
// map[string]string 是历史盲区，[]any / []string 也必须覆盖。
func TestLegacyFacadeStillStructures(t *testing.T) {
	restore(t)
	in := map[string]any{
		"a": []string{synMobile, "ok"},
		"b": []any{map[string]string{"c": synMobile}},
		"n": 7,
	}
	out := RedactValue(in).(map[string]any)
	if strings.Contains(out["a"].([]string)[0], synMobile) {
		t.Fatalf("[]string 未处置: %v", out["a"])
	}
	nested := out["b"].([]any)[0].(map[string]string)
	if strings.Contains(nested["c"], synMobile) {
		t.Fatalf("嵌套 map[string]string 未处置: %v", nested)
	}
	if out["n"].(int) != 7 {
		t.Fatalf("非字符串标量应原样: %v", out["n"])
	}
}
