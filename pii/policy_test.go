package pii

// 合成夹具：面开关与处置策略。号码用合成值，不含任何领域味。

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

// 合成手机号与身份证（省码 11、1990 年生、校验位按 MOD 11-2 自洽——
// 校验位错的样例根本不会被检出，用它写断言等于断言一个空结果）。
const (
	synMobile = "13800001234"
	synID     = "110105199001011232"
)

// restore 在每个策略用例后还原全局，避免用例间串扰。
func restore(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		SetSurfaces(SurfaceAll)
		_ = SetStrategy(StrategyMask)
		SetEnabled(true)
	})
}

func TestSurfaceDefaultsAllOn(t *testing.T) {
	restore(t)
	if Surfaces() != SurfaceAll {
		t.Fatalf("缺省应为三面全开，实得 %d", Surfaces())
	}
	for _, s := range []Surface{SurfaceInput, SurfaceOutput, SurfaceToolResults} {
		if !SurfaceEnabled(s) {
			t.Fatalf("面 %q 缺省应开启", s)
		}
	}
}

func TestSurfaceParseAndHas(t *testing.T) {
	restore(t)
	ss, err := ParseSurfaces("input", "tool_results")
	if err != nil {
		t.Fatalf("已登记面应可解析: %v", err)
	}
	if !ss.Has(SurfaceInput) || !ss.Has(SurfaceToolResults) {
		t.Fatal("已启用的两面应命中")
	}
	if ss.Has(SurfaceOutput) {
		t.Fatal("未启用的面不应命中")
	}
}

func TestSurfaceParseUnknownFailsClosed(t *testing.T) {
	restore(t)
	if _, err := ParseSurfaces("input", "no_such_surface"); !errors.Is(err, ErrUnknownSurface) {
		t.Fatalf("未知面名应报错而非静默丢弃，实得 %v", err)
	}
}

func TestSurfaceBitUnknownNotContained(t *testing.T) {
	if SurfaceAll.Has(Surface("nope")) {
		t.Fatal("未登记面按「不含」处理，不得 panic 也不得放行")
	}
}

func TestStrategyDefaultsMask(t *testing.T) {
	restore(t)
	if Strategy() != StrategyMask {
		t.Fatalf("缺省策略必须是 mask（不得默认 block），实得 %q", Strategy())
	}
}

func TestStrategyUnknownRejected(t *testing.T) {
	restore(t)
	if err := SetStrategy("nonsense"); !errors.Is(err, ErrUnknownStrategy) {
		t.Fatalf("未知策略应报错，实得 %v", err)
	}
	if Strategy() != StrategyMask {
		t.Fatalf("被拒的策略不应改变当前值，实得 %q", Strategy())
	}
}

// TestApplyMaskKeepsLength mask 保长：日志对齐与残留定位依赖这个性质。
func TestApplyMaskKeepsLength(t *testing.T) {
	restore(t)
	out, blocked := Apply(SurfaceOutput, "call me at "+synMobile)
	if blocked {
		t.Fatal("mask 策略不应阻断")
	}
	if len(out) != len("call me at "+synMobile) {
		t.Fatalf("mask 应保长，实得 %d vs %d", len(out), len("call me at "+synMobile))
	}
	if strings.Contains(out, synMobile) {
		t.Fatalf("原值不应残留: %q", out)
	}
}

// TestApplyRedactAndHash 三种非阻断策略各按其形态。
func TestApplyRedactAndHash(t *testing.T) {
	restore(t)
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatal(err)
	}
	out, blocked := Apply(SurfaceOutput, synID)
	if blocked {
		t.Fatal("redact 不应阻断")
	}
	if strings.Contains(out, "110105") || !strings.Contains(out, redactedPlaceholder) {
		t.Fatalf("redact 应整段替换为占位，实得 %q", out)
	}

	if err := SetStrategy(StrategyHash); err != nil {
		t.Fatal(err)
	}
	h1, _ := Apply(SurfaceOutput, synID)
	h2, _ := Apply(SurfaceOutput, synID)
	if h1 != h2 {
		t.Fatalf("hash 应对同一值稳定: %q vs %q", h1, h2)
	}
	if strings.Contains(h1, "110105") {
		t.Fatalf("hash 不应残留原值: %q", h1)
	}
	other, _ := Apply(SurfaceOutput, synMobile)
	if other == h1 {
		t.Fatal("不同值应产出不同摘要")
	}
}

// TestApplyBlockReportsBlocked block 策略下检出即回报，且**本包不代抛**。
func TestApplyBlockReportsBlocked(t *testing.T) {
	restore(t)
	if err := SetStrategy(StrategyBlock); err != nil {
		t.Fatal(err)
	}
	_, blocked := Apply(SurfaceInput, "tel "+synMobile)
	if !blocked {
		t.Fatal("block 策略检出应回报 blocked=true")
	}
	_, clean := Apply(SurfaceInput, "nothing sensitive here")
	if clean {
		t.Fatal("未检出时不得报阻断")
	}
}

// TestApplyDisabledSurfacePassthrough 面未启用 → 原样透传且不阻断。
func TestApplyDisabledSurfacePassthrough(t *testing.T) {
	restore(t)
	if err := SetStrategy(StrategyBlock); err != nil {
		t.Fatal(err)
	}
	SetSurfaces(surfaceInputBit)
	raw := "tel " + synMobile
	out, blocked := Apply(SurfaceToolResults, raw)
	if out != raw || blocked {
		t.Fatalf("面未启用应原样透传且不阻断: %q blocked=%v", out, blocked)
	}
}

// TestApplyGlobalOffPassthrough 全局关闭 → 透传（宿主显式关闭的既定语义）。
func TestApplyGlobalOffPassthrough(t *testing.T) {
	restore(t)
	SetEnabled(false)
	raw := "tel " + synMobile
	out, blocked := Apply(SurfaceInput, raw)
	if out != raw || blocked {
		t.Fatalf("全局关闭应透传: %q blocked=%v", out, blocked)
	}
}

// TestApplyValueSurfacesPropagate 结构化载荷同样受面与策略约束。
func TestApplyValueSurfacesPropagate(t *testing.T) {
	restore(t)
	payload := map[string]any{
		"id":    synID,
		"items": []string{synMobile, "harmless"},
		"n":     42, // 非字符串标量原样
	}
	out, blocked := ApplyValue(SurfaceToolResults, payload)
	if blocked {
		t.Fatal("mask 策略不应阻断")
	}
	m := out.(map[string]any)
	if strings.Contains(m["id"].(string), "110105") {
		t.Fatalf("map[string]any 未被处置: %v", m["id"])
	}
	items := m["items"].([]string)
	if strings.Contains(items[0], synMobile) {
		t.Fatalf("[]string 未被处置: %v", items[0])
	}
	if items[1] != "harmless" {
		t.Fatalf("无敏感内容不应被改写: %v", items[1])
	}
	if m["n"].(int) != 42 {
		t.Fatalf("非字符串标量应原样: %v", m["n"])
	}
}

// TestApplyValueMapStringString 该类型是已知结构盲区，本包须一并收口。
func TestApplyValueMapStringString(t *testing.T) {
	restore(t)
	out, _ := ApplyValue(SurfaceOutput, map[string]string{"contact": synMobile})
	if strings.Contains(out.(map[string]string)["contact"], synMobile) {
		t.Fatalf("map[string]string 未被处置: %v", out)
	}
}

// TestApplyValueBlockPropagatesFromNested 嵌套深处的阻断必须上浮——
// 只看顶层会漏掉深层载荷里的敏感值。
func TestApplyValueBlockPropagatesFromNested(t *testing.T) {
	restore(t)
	if err := SetStrategy(StrategyBlock); err != nil {
		t.Fatal(err)
	}
	deep := map[string]any{"a": []any{map[string]any{"b": synMobile}}}
	_, blocked := ApplyValue(SurfaceInput, deep)
	if !blocked {
		t.Fatal("深层命中的阻断必须上浮到顶层返回")
	}
}

// TestApplySpansOneStrategyPerCall 一次处置 = 一个策略。
//
// 处置形态与 blocked 必须自洽：block 与 mask 共用保长打码形态，redact 恒为占位，
// hash 恒为定长摘要。逐段读全局策略时，一次调用可能前半段按 mask、后半段按 redact，
// 而 blocked 又按最后一次读到的策略算——自洽关系当场崩掉，且崩掉的恰是 fail-closed
// 那个信号（文本已按非阻断形态处置，却回报不阻断）。
func TestApplySpansOneStrategyPerCall(t *testing.T) {
	restore(t)
	txt := "a " + synMobile + " b " + synID + " c"
	cases := []struct {
		name    string
		s       StrategyKind
		blocked bool
		// shape 判定这次调用产出的文本整体形态（不是单段形态）。
		shape func(t *testing.T, out string)
	}{
		{"mask", StrategyMask, false, func(t *testing.T, out string) {
			if len(out) != len(txt) {
				t.Fatalf("mask 应保长，实得 %d vs %d", len(out), len(txt))
			}
			if n := strings.Count(out, "****"); n != 2 { // 手机号与身份证各一段
				t.Fatalf("mask 下两个 span 都该保长打码，实得 %d 段：%q", n, out)
			}
		}},
		{"block 形态同 mask 但必须报阻断", StrategyBlock, true, func(t *testing.T, out string) {
			if n := strings.Count(out, "****"); n != 2 || len(out) != len(txt) {
				t.Fatalf("block 处置形态应同 mask（内核不代为阻断），实得 %q", out)
			}
		}},
		{"redact", StrategyRedact, false, func(t *testing.T, out string) {
			if n := strings.Count(out, redactedPlaceholder); n != 2 {
				t.Fatalf("redact 下两个 span 都该换占位，实得 %d 处：%q", n, out)
			}
			if strings.Contains(out, "****") {
				t.Fatalf("redact 下不得混入 mask 形态（策略中途被换过）：%q", out)
			}
		}},
		{"hash", StrategyHash, false, func(t *testing.T, out string) {
			if n := strings.Count(out, redactedPlaceholder); n != 0 || strings.Contains(out, "****") {
				t.Fatalf("hash 下不得混入其它策略形态：%q", out)
			}
			if len(out) != len("a ")+16+len(" b ")+16+len(" c") { // 每段定长 16 摘要
				t.Fatalf("hash 下两个 span 都该是定长摘要，实得 %q", out)
			}
		}},
	}
	for _, c := range cases {
		// 显式策略入口：策略是入参，一次调用内不可能被换。
		out, blocked := applySpansAs(txt, c.s)
		if blocked != c.blocked {
			t.Fatalf("%s：blocked 应为 %v，实得 %v（处置形态与阻断信号对不上）", c.name, c.blocked, blocked)
		}
		c.shape(t, out)

		// 全局入口：宿主设好策略后调用，两条路径必须给出同一结果。
		if err := SetStrategy(c.s); err != nil {
			t.Fatal(err)
		}
		got, gblocked := Apply(SurfaceOutput, txt)
		if got != out || gblocked != blocked {
			t.Fatalf("%s：Apply 应与 applySpansAs 同结果，实得 %q/%v vs %q/%v", c.name, got, gblocked, out, blocked)
		}
	}
}

// TestApplyValueKeepsNilContainers nil 容器原样保留。
//
// JSON 的 null 与空容器在审计链上是两种形状；把 null 改写成 [] / {} 不可逆，
// 事后无法分辨「没有」与「有一个但是空的」。断言必须同时看值与 nil-ness：
// 非 nil 的空容器 len 为 0，只断言 len 会让这个 bug 蒙混过关。
func TestApplyValueKeepsNilContainers(t *testing.T) {
	restore(t)
	var nilMapAny map[string]any
	var nilMapStr map[string]string
	var nilSliceAny []any
	var nilSliceStr []string

	cases := []struct {
		name  string
		nilIn any
	}{
		{"map[string]any", nilMapAny},
		{"map[string]string", nilMapStr},
		{"[]any", nilSliceAny},
		{"[]string", nilSliceStr},
	}
	for _, c := range cases {
		// 顶层直传与嵌套在载荷里两条路径都要保形（顶层走的是同四个 case 分支）。
		for _, in := range []any{c.nilIn, map[string]any{"field": c.nilIn}} {
			out, blocked := ApplyValue(SurfaceToolResults, in)
			if blocked {
				t.Fatalf("%s：nil 容器里没有任何检出，不该报阻断", c.name)
			}
			var got any = out
			if _, nested := in.(map[string]any); nested {
				got = out.(map[string]any)["field"]
			}
			if !reflect.ValueOf(got).IsNil() {
				t.Fatalf("%s：nil 容器被换成了非 nil 值（%#v）", c.name, got)
			}
			if reflect.TypeOf(got) != reflect.TypeOf(c.nilIn) {
				t.Fatalf("%s：动态类型应保持不变，实得 %v", c.name, reflect.TypeOf(got))
			}
		}
	}

	// 同一载荷里的真实值仍须被处置——保 nil 不得把下探整个关掉。
	out, _ := ApplyValue(SurfaceToolResults, map[string]any{
		"absent": nilMapStr,
		"real":   synMobile,
	})
	m := out.(map[string]any)
	if strings.Contains(m["real"].(string), synMobile) {
		t.Fatalf("非 nil 值应照常处置：%v", m["real"])
	}
	if !reflect.ValueOf(m["absent"]).IsNil() {
		t.Fatalf("同载荷内的 nil 容器仍须保形：%#v", m["absent"])
	}
}

// TestApplyValueNilStaysNullInJSON 把「保 nil」钉在它真正的目的上：编码后仍报 null。
func TestApplyValueNilStaysNullInJSON(t *testing.T) {
	restore(t)
	var absent map[string]string
	out, _ := ApplyValue(SurfaceInput, map[string]any{"absent": absent})
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"absent":{}`) || strings.Contains(string(b), `"absent":[]`) {
		t.Fatalf("null 被改写成了空容器，审计链形状已漂移：%s", b)
	}
	if !strings.Contains(string(b), `"absent":null`) {
		t.Fatalf("null 应原样保留，实得 %s", b)
	}
}
