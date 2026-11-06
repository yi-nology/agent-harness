package pii

import (
	"strings"
	"testing"
)

// withCheck 按 GB 11643-1999 MOD 11-2 补校验位（测试构造用：语义校验各面可
// 独立反例，不被校验码误拒干扰）。
func withCheck(base17 string) string {
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(base17[i]-'0') * idWeights[i]
	}
	return base17 + string(idCheckChars[sum%11])
}

// redactIn / redactInValue 本文件的取形助记：以 input 面处置并断言「mask 缺省下
// 不得要求阻断」。
//
// 存在的理由：面无关门面（Redact / RedactValue）已于 2026-11-06 删除——它拿不到
// blocked 信号，宿主把策略配成 block 时无法中断，是个只能靠自觉的洞。本文件的
// 用例只关心**处置形态**，阻断信号由 policy_test.go / surface_*.go 专门断言；
// 这里显式写出面名，顺带把「每个出口都必须声明自己在哪一面」变成调用点可见的事实。
func redactIn(t *testing.T, s string) string {
	t.Helper()
	out, blocked := Apply(SurfaceInput, s)
	if blocked {
		t.Fatalf("mask 缺省策略下 input 面不应要求阻断: %q", s)
	}
	return out
}

func redactInValue(t *testing.T, v any) any {
	t.Helper()
	out, blocked := ApplyValue(SurfaceInput, v)
	if blocked {
		t.Fatalf("mask 缺省策略下 input 面不应要求阻断: %v", v)
	}
	return out
}

func TestRedactMobile(t *testing.T) {
	restore(t)
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"裸号", "联系人 13812345678 报告", "联系人 138****5678 报告"},
		{"+86 前缀", "+8613812345678", "+86138****5678"},
		{"0086 前缀", "008613812345678", "0086138****5678"},
		{"符号边界放行", "tel:13812345678;", "tel:138****5678;"},
		{"裸86不认", "8613812345678", "8613812345678"},
		{"12位不打", "138123456789", "138123456789"},
		{"字母前邻不打", "a13812345678", "a13812345678"},
		{"13位毫秒时间戳不打", "ts=1728643200000", "ts=1728643200000"},
		{"1[3-9]不匹配", "12345678901", "12345678901"},
	}
	for _, c := range cases {
		if got := redactIn(t, c.in); got != c.want {
			t.Errorf("%s: Apply(input, %q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestRedactResidentID(t *testing.T) {
	restore(t)
	valid := withCheck("11010519491231002") // = 11010519491231002X（经典测试号）
	if valid != "11010519491231002X" {
		t.Fatalf("构造基准失败: %s", valid)
	}
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"有效证号", "身份证 " + valid, "身份证 1101************2X"},
		{"小写x", strings.ToLower(valid), "1101************2x"},
		{"校验码错", "110105194912310021", "110105194912310021"},
		{"省码非白名单", withCheck("99010519491231002"), withCheck("99010519491231002")},
		{"生日非法0230", withCheck("11010519900230002"), withCheck("11010519900230002")},
		{"生日未来", withCheck("11010520991231002"), withCheck("11010520991231002")},
		{"顺序码全零", withCheck("11010519491231000"), withCheck("11010519491231000")},
		{"19位数字串", valid + "9", valid + "9"},
		{"随机18位数字", "123456789012345678", "123456789012345678"},
	}
	for _, c := range cases {
		if got := redactIn(t, c.in); got != c.want {
			t.Errorf("%s: Apply(input, %q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestRedactOverlapAndLength(t *testing.T) {
	restore(t)
	in := "手机13812345678与身份证11010519491231002X同句"
	got := redactIn(t, in)
	if strings.Contains(got, "13812345678") || strings.Contains(got, "11010519491231002X") {
		t.Fatalf("原文残留: %s", got)
	}
	if len(got) != len(in) {
		t.Fatalf("打码应保长度: %d != %d", len(got), len(in))
	}
	if redactIn(t, in) != got {
		t.Fatal("打码必须确定性")
	}
}

func TestRedactValueStructures(t *testing.T) {
	restore(t)
	in := map[string]any{
		"contact": "13812345678",
		// agentkit logredact 的值面下探不下探 map[string]string（结构盲区）——本包收口。
		"tags":  map[string]string{"owner": "13912345678"},
		"list":  []any{"id 11010519491231002X"},
		"strs":  []string{"13712345678"},
		"num":   42,
		"flag":  true,
		"clean": "无敏感",
	}
	out, ok := redactInValue(t, in).(map[string]any)
	if !ok {
		t.Fatal("应返回 map[string]any")
	}
	if out["contact"] != "138****5678" {
		t.Errorf("contact: %v", out["contact"])
	}
	tags, _ := out["tags"].(map[string]string)
	if tags["owner"] != "139****5678" {
		t.Errorf("map[string]string 盲区未收口: %v", out["tags"])
	}
	list, _ := out["list"].([]any)
	if list[0] != "id 1101************2X" {
		t.Errorf("[]any: %v", out["list"])
	}
	strs, _ := out["strs"].([]string)
	if strs[0] != "137****5678" {
		t.Errorf("[]string: %v", out["strs"])
	}
	if out["num"] != 42 || out["flag"] != true || out["clean"] != "无敏感" {
		t.Errorf("非目标值被改写: %v %v %v", out["num"], out["flag"], out["clean"])
	}
	// 入参不得被原地改写。
	if in["contact"] != "13812345678" {
		t.Fatal("ApplyValue 改写了入参")
	}
}

func TestEnabledSwitch(t *testing.T) {
	restore(t)
	SetEnabled(false)
	if redactIn(t, "13812345678") != "13812345678" {
		t.Fatal("关闭时应原样透传")
	}
	in := map[string]any{"a": "13812345678"}
	out := redactInValue(t, in)
	if m, _ := out.(map[string]any); m["a"] != "13812345678" {
		t.Fatal("关闭时 ApplyValue 应透传")
	}
	if Detect("13812345678") == nil {
		// Detect 是纯检测原语，不受开关影响（开关语义收敛在改写面）。
		t.Fatal("Detect 应始终报告命中")
	}
}
