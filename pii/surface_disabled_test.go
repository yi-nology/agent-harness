package pii

// 「已登记但显式关闭」这条分支的护栏。19:46 的 fail-closed 修复把它变成了一条
// 独立的语义路径——面名拼错要 fail-closed，而面名正确、宿主显式关闭则必须
// 原样透传。两者一旦塌缩，宿主就没法既拼错即知、又能主动关面。

import (
	"strings"
	"testing"
)

// restoreLocal 独立于其他测试文件的清理助手（本文件自洽，不依赖别处的 helper）。
func restoreLocal(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		SetEnabled(true)
		SetSurfaces(SurfaceAll)
		_ = SetStrategy(StrategyMask)
	})
}

// TestApplyValueRegisteredButDisabledPassthrough 已登记的面被宿主显式关闭时，
// 载荷必须**原样透传**（含结构），且不得报阻断。
//
// 关键在于把策略设成 block：若实现错误地走了处置路径，这里会拿到 blocked=true；
// 正确实现下「面关闭」先于「策略」生效，所以 blocked 必须是 false。
func TestApplyValueRegisteredButDisabledPassthrough(t *testing.T) {
	restoreLocal(t)
	if err := SetStrategy(StrategyBlock); err != nil {
		t.Fatal(err)
	}
	// 只留 input 与 output：tool_results 被显式关掉。
	ss, err := ParseSurfaces("input", "output")
	if err != nil {
		t.Fatal(err)
	}
	SetSurfaces(ss)

	in := map[string]any{
		"id":    synMobile,
		"items": []string{synMobile, "harmless"},
		"n":     42,
		// nil 容器：钉住「透传」在这条路径上确实保结构。
		//
		// ⚠️ 别把这两条当成 ApplyValue 里那个早返回分支的守卫——2026-10-18 实测
		// 摘掉该分支后本用例仍全绿：applyValue 对每种容器类型都有 `if t == nil`
		// 早退，nil 照样原样返回，故早返回是可摘的纯快路径、无法被行为断言覆盖
		// 见 pii/policy.go ApplyValue 处注释。两者的口径必须一致。
		"absent": []string(nil),
		"noMap":  map[string]any(nil),
	}
	out, blocked := ApplyValue(SurfaceToolResults, in)

	if blocked {
		t.Fatal("面被显式关闭时不得报阻断（面关闭先于策略生效）")
	}
	m := out.(map[string]any)
	if m["id"] != in["id"] {
		t.Fatalf("面关闭时应原样透传字符串值: %v", m["id"])
	}
	items, ok := m["items"].([]string)
	if !ok {
		t.Fatalf("结构必须保持 []string: %T", m["items"])
	}
	if items[0] != synMobile || items[1] != "harmless" {
		t.Fatalf("面关闭时应原样透传切片: %v", items)
	}
	if m["n"].(int) != 42 {
		t.Fatalf("标量应原样: %v", m["n"])
	}
	// 容器保持 nil——这是「透传」契约本身的一部分（JSON `null` 不得静默变
	// `[]`/`{}`），但如上所述它并不区分早返回与递归回退两条路径。
	if v, ok := m["absent"].([]string); !ok || v != nil {
		t.Fatalf("nil []string 必须保持 nil: %#v", m["absent"])
	}
	if v, ok := m["noMap"].(map[string]any); !ok || v != nil {
		t.Fatalf("nil map 必须保持 nil: %#v", m["noMap"])
	}
}

// TestApplyValueRegisteredAndEnabledStillDisposes 与上一条互为对照：
// 面开着就照样处置。否则「显式关闭」可能被实现成「永远透传」。
func TestApplyValueRegisteredAndEnabledStillDisposes(t *testing.T) {
	restoreLocal(t)
	SetSurfaces(SurfaceAll)

	out, blocked := ApplyValue(SurfaceToolResults, map[string]string{"k": synMobile})
	if blocked {
		t.Fatal("默认 mask 策略不应报阻断")
	}
	m := out.(map[string]string)
	if strings.Contains(m["k"], synMobile) {
		t.Fatalf("面开启时应处置: %v", m)
	}
}

// TestApplyValueUnknownSurfaceStillBlocks 未登记面名（拼错）仍须 fail-closed，
// 哪怕其他面都开着——这条是 19:46 修复的核心，不能被「显式关闭」这条路径吃掉。
func TestApplyValueUnknownSurfaceStillBlocks(t *testing.T) {
	restoreLocal(t)
	SetSurfaces(SurfaceAll)

	in := map[string]any{"id": synMobile, "n": 1}
	out, blocked := ApplyValue(Surface("tool_result"), in)
	if !blocked {
		t.Fatal("未登记面名必须报阻断，不得静默放行")
	}
	m := out.(map[string]any)
	if strings.Contains(m["id"].(string), synMobile) {
		t.Fatalf("未登记面名下原文不得出去: %v", m["id"])
	}
	if m["n"].(int) != 1 {
		t.Fatalf("非字符串标量应原样: %v", m["n"])
	}
}
