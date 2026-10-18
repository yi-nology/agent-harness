// charts 可视化附件用例：解析、容错归一、畸形降级。

package protocol

import (
	"fmt"
	"strings"
	"testing"
)

// ── charts 可视化附件：解析、容错归一、畸形降级 ──

// T-charts-1 正常 pie+line 完整解析。
func TestChartsParse(t *testing.T) {
	raw := `{"agent_name":"吞吐评估Agent","confidence":"high","conclusion":"c","summary":"s",
		"charts":[
			{"type":"pie","title":"磁盘用量分布","unit":"%","items":[{"label":"/ (sda1)","value":78.2},{"label":"/data","value":45.6}]},
			{"type":"line","title":"eth0 流量","unit":"MB/s","series":[
				{"label":"rx","points":[{"t":"14:00","v":12.4},{"t":"14:05","v":18.9}]},
				{"label":"tx","points":[{"t":"14:00","v":3.1},{"t":"14:05","v":2.7}]}
			]}
		]}`
	r := mustParse(t, raw)
	if len(r.Charts) != 2 {
		t.Fatalf("应解析出 2 个图表: %+v", r.Charts)
	}
	pie, line := r.Charts[0], r.Charts[1]
	if pie.Type != "pie" || len(pie.Items) != 2 || pie.Items[0].Value != 78.2 {
		t.Fatalf("饼图解析异常: %+v", pie)
	}
	if line.Type != "line" || len(line.Series) != 2 || len(line.Series[0].Points) != 2 || line.Series[0].Points[1].V != 18.9 {
		t.Fatalf("折线图解析异常: %+v", line)
	}
}

// T-charts-2 形态漂移：type 写成 donut/line_chart、数值写成 "62.4%"、单对象 charts。
func TestChartsTolerance(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",
		"charts":{"type":"donut","items":[{"label":"used","value":"62.4%"},{"label":"free","value":"37.6"}]}}`
	r := mustParse(t, raw)
	if len(r.Charts) != 1 {
		t.Fatalf("单对象 charts 应包一层数组: %+v", r.Charts)
	}
	c := r.Charts[0]
	if c.Type != "pie" {
		t.Fatalf("donut 应归一为 pie: %q", c.Type)
	}
	if len(c.Items) != 2 || c.Items[0].Value != 62.4 {
		t.Fatalf("字符串数值应容错解析: %+v", c.Items)
	}

	raw2 := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",
		"charts":[{"type":"line_chart","series":[{"label":"rx","points":[{"time":"14:00","v":"12.4"},{"t":"14:05","value":18.9}]}]}]}`
	r2 := mustParse(t, raw2)
	ln := r2.Charts[0]
	if ln.Type != "line" || len(ln.Series) != 1 || len(ln.Series[0].Points) != 2 {
		t.Fatalf("line_chart/time/value 漂移应归一: %+v", ln)
	}
	if ln.Series[0].Points[0].T != "14:00" || ln.Series[0].Points[0].V != 12.4 {
		t.Fatalf("时间键回退与字符串数值异常: %+v", ln.Series[0].Points)
	}
}

// T-charts-3 畸形 charts：字符串/坏元素只损失图表，绝不影响整份报告解析；
// 缺 type 时按数据形态推断；无效点丢弃后空 spec 保位不炸。
func TestChartsMalformedDegrade(t *testing.T) {
	// charts 是字符串：报告仍解析成功，无图表。
	r := mustParse(t, `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s","charts":"磁盘 78%"}`)
	if len(r.Charts) != 0 {
		t.Fatalf("字符串 charts 不应产出图表: %+v", r.Charts)
	}
	// 元素畸形（items 是字符串、value 非数）：spec 置空/丢点，报告不炸。
	r2 := mustParse(t, `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",
		"charts":[{"items":"not-an-array"},{"type":"bar_chart","items":[{"label":"a","value":"abc"},{"label":"b","value":5}]},{"type":"","series":[{"label":"x","points":[{"v":"nan"},{"v":1},{"v":2}]}]}]}`)
	if len(r2.Charts) != 3 {
		t.Fatalf("畸形 spec 应保位: %+v", r2.Charts)
	}
	if r2.Charts[0].Type != "" {
		t.Fatalf("items 畸形应置空: %+v", r2.Charts[0])
	}
	// bar_chart+items 漂移：type 归一为 bar，items 数据转单系列；value="abc" 的点丢弃，b=5 保留。
	if r2.Charts[1].Type != "bar" || len(r2.Charts[1].Series) != 1 ||
		len(r2.Charts[1].Series[0].Points) != 1 || r2.Charts[1].Series[0].Points[0].V != 5 {
		t.Fatalf("bar_chart 应归一并把 items 转单系列: %+v", r2.Charts[1])
	}
	// v="nan" 丢弃,剩余 2 点;缺 type 有 series → 推断 line。
	if r2.Charts[2].Type != "line" || len(r2.Charts[2].Series) != 1 || len(r2.Charts[2].Series[0].Points) != 2 {
		t.Fatalf("nan 点应丢弃且缺 type 按 series 推断: %+v", r2.Charts[2])
	}
	// 无 charts 字段。
	r3 := mustParse(t, `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s"}`)
	if r3.Charts != nil {
		t.Fatalf("无 charts 应为 nil: %+v", r3.Charts)
	}
}

// T-charts-4 bar 柱状图：正常解析、别名归一（column/histogram）、bar+items 转单系列。
func TestChartsBar(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",
		"charts":[
			{"type":"bar","title":"TOP 进程 CPU","unit":"%","series":[{"label":"cpu%","points":[{"t":"mysqld","v":132.5},{"t":"node","v":88.2}]}]},
			{"type":"column","title":"各目录空间","series":[{"label":"GB","points":[{"t":"/var","v":21.4}]}]},
			{"type":"bar_chart","title":"连接数","items":[{"label":"estab","value":42},{"label":"listen","value":3}]}
		]}`
	r := mustParse(t, raw)
	if len(r.Charts) != 3 {
		t.Fatalf("应解析出 3 个图表: %+v", r.Charts)
	}
	b := r.Charts[0]
	if b.Type != "bar" || len(b.Series) != 1 || len(b.Series[0].Points) != 2 || b.Series[0].Points[0].T != "mysqld" {
		t.Fatalf("bar 解析异常: %+v", b)
	}
	if r.Charts[1].Type != "bar" {
		t.Fatalf("column 别名应归一为 bar: %q", r.Charts[1].Type)
	}
	conv := r.Charts[2]
	if conv.Type != "bar" || len(conv.Series) != 1 || len(conv.Series[0].Points) != 2 || conv.Series[0].Points[1].V != 3 {
		t.Fatalf("bar_chart+items 应转单系列: %+v", conv)
	}
}

// T-charts-5 topo 拓扑图：正常解析、type/键名漂移（deployment、source/target）、
// 状态词归一（healthy→ok、error→down、警告→warn）、id/label 互回退、空端点整条丢弃、
// 缺 type 按 nodes+edges 推断；未知端点（ghost）协议层保留，剔除交前端渲染层。
func TestChartsTopo(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",
		"charts":[
			{"type":"topo","title":"服务部署拓扑","nodes":[
				{"id":"nginx","label":"Nginx","status":"ok","group":"接入层"},
				{"id":"app","label":"app-01","status":"down","group":"应用层"},
				{"id":"db","name":"MySQL","status":"healthy","group":"数据层"}
			],"edges":[
				{"from":"nginx","to":"app","label":"反代"},
				{"source":"app","target":"db","status":"error"},
				{"from":"app","to":"ghost"},
				{"from":"","to":"db"}
			]},
			{"type":"deployment","nodes":[{"label":"cache","state":"警告"}]},
			{"nodes":[{"id":"a"},{"id":"b"}],"edges":[{"from":"a","to":"b"}]}
		]}`
	r := mustParse(t, raw)
	if len(r.Charts) != 3 {
		t.Fatalf("应解析出 3 个图表: %+v", r.Charts)
	}
	topo := r.Charts[0]
	if topo.Type != "topo" || len(topo.Nodes) != 3 || len(topo.Edges) != 3 {
		t.Fatalf("topo 解析异常: %+v", topo)
	}
	if topo.Nodes[2].ID != "db" || topo.Nodes[2].Label != "MySQL" || topo.Nodes[2].Status != "ok" {
		t.Fatalf("label 键回退与 healthy 归一异常: %+v", topo.Nodes[2])
	}
	if topo.Edges[0].Label != "反代" || topo.Edges[1].Status != "down" {
		t.Fatalf("边键回退与 error 归一异常: %+v", topo.Edges)
	}
	// deployment 别名归一；state=警告 → warn；id 缺省回退 label。
	dep := r.Charts[1]
	if dep.Type != "topo" || len(dep.Nodes) != 1 || dep.Nodes[0].ID != "cache" || dep.Nodes[0].Status != "warn" {
		t.Fatalf("deployment/state=警告 漂移应归一: %+v", dep)
	}
	// 缺 type：nodes+edges 推断 topo。
	inferred := r.Charts[2]
	if inferred.Type != "topo" || len(inferred.Nodes) != 2 || len(inferred.Edges) != 1 {
		t.Fatalf("缺 type 应按 nodes+edges 推断: %+v", inferred)
	}
}

// T-charts-6 timeline 时间线：正常解析、别名归一（"timeline" 含子串 "line"，
// 判定必须先于 line）、键名回退（name/at/state/detail）、状态词归一、
// label+desc 全缺事件丢弃、缺 type 按 events 推断、"时序/timeseries" 仍归 line 不被抢、
// 事件数上限截断。
func TestChartsTimeline(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",
		"charts":[
			{"type":"timeline","title":"故障演进","events":[
				{"time":"14:02","label":"网关 5xx 突增","status":"down","desc":"错误率 0.1%→7%"},
				{"at":"14:05","name":"上游超时","state":"警告"},
				{"time":"14:20","label":"恢复","status":"healthy","detail":"滚动重启完成"}
			]},
			{"type":"时间线","events":[{"label":"a"}]},
			{"events":[{"time":"1","desc":"仅 desc 回退成 label"},{"time":"2"}]},
			{"type":"timeseries","series":[{"label":"x","points":[{"v":1}]}]}
		]}`
	r := mustParse(t, raw)
	if len(r.Charts) != 4 {
		t.Fatalf("应解析出 4 个图表: %+v", r.Charts)
	}
	tl := r.Charts[0]
	if tl.Type != "timeline" || len(tl.Events) != 3 {
		t.Fatalf("timeline 解析异常: %+v", tl)
	}
	if tl.Events[0].Time != "14:02" || tl.Events[0].Status != "down" || tl.Events[0].Desc != "错误率 0.1%→7%" {
		t.Fatalf("事件字段解析异常: %+v", tl.Events[0])
	}
	if tl.Events[1].Label != "上游超时" || tl.Events[1].Time != "14:05" || tl.Events[1].Status != "warn" {
		t.Fatalf("name/at/state 键回退与状态归一异常: %+v", tl.Events[1])
	}
	if tl.Events[2].Status != "ok" || tl.Events[2].Desc != "滚动重启完成" {
		t.Fatalf("healthy 归一与 detail 回退异常: %+v", tl.Events[2])
	}
	// 中文别名归一。
	if r.Charts[1].Type != "timeline" {
		t.Fatalf("「时间线」应归一为 timeline: %q", r.Charts[1].Type)
	}
	// 缺 type 按 events 推断；label+desc 全缺事件丢弃，desc 回退成 label。
	inferred := r.Charts[2]
	if inferred.Type != "timeline" || len(inferred.Events) != 1 || inferred.Events[0].Label != "仅 desc 回退成 label" {
		t.Fatalf("缺 type 推断与 label/desc 回退异常: %+v", inferred)
	}
	// timeseries 历史上归 line，timeline 分支不得抢。
	if r.Charts[3].Type != "line" {
		t.Fatalf("timeseries 应仍归 line: %q", r.Charts[3].Type)
	}

	// 事件数上限：45 条截到 40（宁缺毋滥防刷屏）。
	var sb strings.Builder
	sb.WriteString(`{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s","charts":[{"type":"timeline","events":[`)
	for i := 0; i < 45; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"label":"ev%d"}`, i)
	}
	sb.WriteString(`]}]}`)
	rc := mustParse(t, sb.String())
	if len(rc.Charts[0].Events) != 40 {
		t.Fatalf("事件数应截断到 40: %d", len(rc.Charts[0].Events))
	}
}
