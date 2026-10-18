package pii

import (
	"strings"
	"testing"
)

// TestUnknownSurfaceFailsClosed 钉住「未登记面名不静默放行」。
//
// 背景：SurfaceSet.Has 对未登记面返回 false，而 Apply/ApplyValue 原先把这个
// false 当成「面未启用」直接原文返回。于是宿主把面名做成配置后拼错一个字符
// （tool_result vs tool_results、output vs outputs），该面就**完全不脱敏**，
// 且无报错、无日志、无阻断信号——而 tool_results 正是工具产出回喂模型的面，
// PII 最需要兜底的地方。
//
// 本测试断言两件事：原文没有出去，且调用方拿到了可履行的阻断信号。
func TestUnknownSurfaceFailsClosed(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatalf("SetStrategy: %v", err)
	}
	SetSurfaces(SurfaceAll)

	const secret = "联系 13800138000"
	// 故意用未登记的面名——正是拼错时的现场。
	bad := Surface("tool_result")

	out, blocked := Apply(bad, secret)
	if out == secret {
		t.Fatalf("未登记面名不得原文放行：拼错面名会让该面完全不脱敏（实得 %q）", out)
	}
	if !blocked {
		t.Fatalf("未登记面名应同时给出阻断信号，让宿主能感知异常而非静默通过")
	}

	// 值型载荷同理：嵌套里的敏感串也不能原样出去。
	v, b2 := ApplyValue(bad, map[string]any{"tool_output": secret})
	if b2 != true {
		t.Fatalf("ApplyValue 对未登记面名同样应给出阻断信号")
	}
	blob, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("ApplyValue 应保持载荷结构，实得 %T", v)
	}
	if got, _ := blob["tool_output"].(string); got == secret {
		t.Fatalf("未登记面名下嵌套敏感串不得原样返回：实得 %q", got)
	}
}

// TestValidateSurfaceCatchesTypos 覆盖接线期自查入口。
func TestValidateSurfaceCatchesTypos(t *testing.T) {
	for _, ok := range []Surface{SurfaceInput, SurfaceOutput, SurfaceToolResults} {
		if err := ValidateSurface(ok); err != nil {
			t.Fatalf("已登记面 %q 不该报错: %v", ok, err)
		}
	}
	for _, bad := range []Surface{"", "tool_result", "outputs", "TOOL_RESULTS", " 输入"} {
		if err := ValidateSurface(bad); err == nil {
			t.Fatalf("未登记面名 %q 应被 ValidateSurface 拦下（拼写错误要在接线期暴露）", bad)
		}
	}
}

// TestExplicitlyDisabledSurfaceStillPassesThrough 确认这次收紧没有伤到既定语义：
// 宿主**显式**关闭某个面时，仍然是原文返回、不阻断。fail-closed 只针对「未登记」，
// 不能把「显式关闭」也一并改掉——那会让宿主无法关掉某个面。
func TestExplicitlyDisabledSurfaceStillPassesThrough(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatalf("SetStrategy: %v", err)
	}

	const text = "联系 13800138000"
	// 只开 input 面：tool_results 是**已登记但未启用**。
	// 注意 ParseSurfaces 只是解析器，必须再 SetSurfaces 才真正生效。
	ss, err := ParseSurfaces("input")
	if err != nil {
		t.Fatalf("ParseSurfaces 应接受已登记面名: %v", err)
	}
	SetSurfaces(ss)
	t.Cleanup(func() { SetSurfaces(SurfaceAll) })

	out, blocked := Apply(SurfaceToolResults, text)
	if out != text {
		t.Fatalf("显式关闭的面应原文返回，实得 %q", out)
	}
	if blocked {
		t.Fatalf("显式关闭的面不应给出阻断信号")
	}
}

// TestUnknownSurfaceStillRedactsWhenStrategyIsMask 确认 fail-closed 分支不依赖策略：
// 即使策略是 mask（而非 redact），未登记面也不该放行原文。
func TestUnknownSurfaceStillRedactsWhenStrategyIsMask(t *testing.T) {
	SetEnabled(true)
	t.Cleanup(func() { SetEnabled(false) })
	if err := SetStrategy(StrategyMask); err != nil {
		t.Fatalf("SetStrategy: %v", err)
	}
	SetSurfaces(SurfaceAll)

	const secret = "联系 13800138000"
	out, blocked := Apply(Surface("nonexistent"), secret)
	if strings.Contains(out, "13800138000") {
		t.Fatalf("mask 策略下未登记面名同样不得放行原文：实得 %q", out)
	}
	if !blocked {
		t.Fatalf("未登记面名应给出阻断信号")
	}
}
