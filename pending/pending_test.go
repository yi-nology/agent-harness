// 挂起态结构化单测（批次一百一十八 E 项）：列内容唯一合法形态=State JSON——
// 四态解析与往返恒等；三代格式读路径（前缀协议/裸关键词）已随 2026-10-07 兼容清扫
// 退役，非 JSON 值一律按无挂起处理。
package pending

import "testing"

func TestParsePendingStateJSONOnly(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want State
	}{
		{"空值", "", State{}},
		{"JSON·term", `{"k":"term","w":"负载","o":"workflow/fault-diagnosis","p":"1"}`,
			State{Kind: "term", Word: "负载", Orig: "workflow/fault-diagnosis", Plan: "1"}},
		{"JSON·done", `{"k":"done","w":"负载"}`, State{Kind: "done", Word: "负载"}},
		{"JSON·target", `{"k":"target","st":"cluster","o":"specialists/k8s-health","p":"0"}`,
			State{Kind: "target", Stage: "cluster", Orig: "specialists/k8s-health", Plan: "0"}},
		{"JSON·disambig", `{"k":"disambig","w":"审计"}`, State{Kind: "disambig", Word: "审计"}},
		// 退役格式（前缀协议/裸关键词/损坏 JSON）：一律按无挂起，不再懒解析。
		{"退役·term前缀", "@term:负载|workflow/fault-diagnosis|0", State{}},
		{"退役·done前缀", "@done:负载", State{}},
		{"退役·target前缀", "@target:host|workflow/fault-diagnosis|1", State{}},
		{"退役·裸关键词", "审计", State{}},
		{"损坏JSON", `{"k":`, State{}},
	}
	for _, c := range cases {
		if got := Parse(c.in); got != c.want {
			t.Errorf("%s: Parse(%q) = %+v, 期望 %+v", c.name, c.in, got, c.want)
		}
	}
}

// TestPendingRoundTrip 编码→解析恒等（列内容唯一合法形态=State JSON）。
func TestPendingRoundTrip(t *testing.T) {
	for _, ps := range []State{
		{Kind: "term", Word: "负载", Orig: "workflow/fault-diagnosis", Plan: "1"},
		{Kind: "done", Word: "磁盘满"},
		{Kind: "target", Stage: "ask", Orig: "specialists/k8s-health", Plan: "0"},
		{Kind: "disambig", Word: "审计"},
	} {
		if got := Parse(Encode(ps)); got != ps {
			t.Errorf("往返不等: %+v → %q → %+v", ps, Encode(ps), got)
		}
	}
}
