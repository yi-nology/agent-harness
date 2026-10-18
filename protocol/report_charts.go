// 报告图表附件契约（charts 字段）：类型定义与容错解码。
//
// 从 report_types.go 拆出——图表自成一块，与报告主干无交叉依赖；
// 拆分只为满足 max_file_lines，单文件读不动时该模块应重新分组。

package protocol

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/oekit/pkg/jsonrepair"
)

// ChartItem 饼图扇区；ChartPoint/ChartSeries 折线图数据点与系列。
// charts 是报告可选的可视化附件（type=pie 用 items，type=line 用 series），
// 数据必须来自真实采集——协议层只管忠实承载，不做语义校验。
type ChartItem struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type ChartPoint struct {
	T string  `json:"t,omitempty"`
	V float64 `json:"v"`
}

type ChartSeries struct {
	Label  string       `json:"label"`
	Points []ChartPoint `json:"points"`
}

// ChartNode 拓扑图节点；ChartEdge 有向边（type=topo 用：部署/架构/依赖关系可视化）。
type ChartNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status,omitempty"` // ok|warn|down|空（漂移词在解码期归一）
	Group  string `json:"group,omitempty"`  // 分层/分区名（接入层/应用层…）
}

type ChartEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Label  string `json:"label,omitempty"`
	Status string `json:"status,omitempty"` // ok|warn|down|空
}

// ChartEvent 时间线事件（type=timeline 用：故障演进/事件时序可视化）。
// Time 是展示字面（"14:02"、"Day1"），协议不解析时刻语义——数据必须来自
// 真实采集/日志时间戳，平台侧执行时间线另由前端从步骤簿记确定性派生。
type ChartEvent struct {
	Time   string `json:"time,omitempty"`
	Label  string `json:"label"`
	Status string `json:"status,omitempty"` // ok|warn|down|空（漂移词在解码期归一）
	Desc   string `json:"desc,omitempty"`
}

// 状态词白名单（实弹模型词形：healthy/down/error/正常/宕机…）。
var (
	chartStatusOK   = map[string]bool{"ok": true, "healthy": true, "health": true, "normal": true, "up": true, "running": true, "online": true, "active": true, "green": true, "pass": true, "good": true, "正常": true, "健康": true, "在线": true}
	chartStatusWarn = map[string]bool{"warn": true, "warning": true, "degraded": true, "pending": true, "yellow": true, "slow": true, "busy": true, "警告": true, "降级": true, "缓慢": true}
	chartStatusDown = map[string]bool{"down": true, "error": true, "critical": true, "crit": true, "failed": true, "fail": true, "crash": true, "crashed": true, "offline": true, "dead": true, "red": true, "unreachable": true, "stopped": true, "宕机": true, "故障": true, "异常": true, "失败": true, "停止": true, "离线": true}
)

// chartStatus 状态词归一为 ok|warn|down|空；未识别词保持空（前端按未知态渲染，不臆断）。
func chartStatus(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	switch {
	case t == "":
	case chartStatusOK[t]:
		return "ok"
	case chartStatusWarn[t]:
		return "warn"
	case chartStatusDown[t]:
		return "down"
	}
	return ""
}

// chartNodes 节点容错解码：id/label 互相回退（只写 name 时 id=label=name）、
// 状态词归一、group 键回退；无 id 且无 label 的垃圾元素丢弃。
func chartNodes(raw []map[string]any) []ChartNode {
	out := make([]ChartNode, 0, len(raw))
	for _, m := range raw {
		id := chartText(chartField(m, "id", "key", "name"))
		label := chartText(chartField(m, "label", "name", "title", "id"))
		if id == "" && label == "" {
			continue
		}
		if id == "" {
			id = label
		}
		if label == "" {
			label = id
		}
		out = append(out, ChartNode{
			ID:     id,
			Label:  label,
			Status: chartStatus(chartText(chartField(m, "status", "state", "health"))),
			Group:  chartText(chartField(m, "group", "layer", "zone", "tier", "cluster")),
		})
	}
	return out
}

// chartEvents 事件容错解码：label/desc 互相回退（全缺丢弃）、time/status 键回退
// （状态词同 chartStatus 归一）；上限 40 条防模型倾泻刷屏（宁缺毋滥同图表纪律）。
func chartEvents(raw []map[string]any) []ChartEvent {
	out := make([]ChartEvent, 0, len(raw))
	for _, m := range raw {
		label := chartText(chartField(m, "label", "name", "title", "event"))
		desc := chartText(chartField(m, "desc", "description", "detail", "note", "message"))
		if label == "" {
			label, desc = desc, ""
		}
		if label == "" {
			continue
		}
		out = append(out, ChartEvent{
			Time:   chartText(chartField(m, "time", "t", "at", "when", "timestamp")),
			Label:  label,
			Status: chartStatus(chartText(chartField(m, "status", "state", "health"))),
			Desc:   desc,
		})
		if len(out) >= 40 {
			break
		}
	}
	return out
}

// chartEdges 边容错解码：from/to 键回退（source/src、target/dst/dest）、端点缺失整条丢弃。
func chartEdges(raw []map[string]any) []ChartEdge {
	out := make([]ChartEdge, 0, len(raw))
	for _, m := range raw {
		from := chartText(chartField(m, "from", "source", "src"))
		to := chartText(chartField(m, "to", "target", "dst", "dest"))
		if from == "" || to == "" {
			continue
		}
		out = append(out, ChartEdge{
			From:   from,
			To:     to,
			Label:  chartText(chartField(m, "label", "name")),
			Status: chartStatus(chartText(chartField(m, "status", "state", "health"))),
		})
	}
	return out
}

// ChartSpec 单个图表定义。真实 LLM 的形态漂移（type 写成 donut/bar_chart、
// 数值写成 "62.4%"）在 UnmarshalJSON 内归一——畸形只损失该图表本身，
// 绝不影响整份报告解析（宽容留给格式，拦截留给白名单）。
type ChartSpec struct {
	Type   string        `json:"type"` // pie|line|bar|topo|timeline（空=无效 spec，调用方跳过）
	Title  string        `json:"title,omitempty"`
	Unit   string        `json:"unit,omitempty"`
	Items  []ChartItem   `json:"items,omitempty"`
	Series []ChartSeries `json:"series,omitempty"`
	Nodes  []ChartNode   `json:"nodes,omitempty"`  // type=topo 用
	Edges  []ChartEdge   `json:"edges,omitempty"`  // type=topo 用
	Events []ChartEvent  `json:"events,omitempty"` // type=timeline 用
}

// UnmarshalJSON 容错解码：type 漂移归一、数值字符串化容忍、无效数据点丢弃。
// 解析失败一律返回 nil 并置空 spec——图表是锦上添花，不能成为报告的故障点。
func (c *ChartSpec) UnmarshalJSON(data []byte) error {
	*c = ChartSpec{}
	var raw struct {
		Type   *string          `json:"type"`
		Title  *string          `json:"title"`
		Unit   *string          `json:"unit"`
		Items  []map[string]any `json:"items"`
		Series []map[string]any `json:"series"`
		Nodes  []map[string]any `json:"nodes"`
		Edges  []map[string]any `json:"edges"`
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if raw.Title != nil {
		c.Title = *raw.Title
	}
	if raw.Unit != nil {
		c.Unit = *raw.Unit
	}
	hasNodes, hasEdges := len(raw.Nodes) > 0, len(raw.Edges) > 0
	c.Type = normalizeChartType(raw.Type, len(raw.Items) > 0, len(raw.Series) > 0, hasNodes, hasEdges, len(raw.Events) > 0)
	switch c.Type {
	case "pie":
		for _, m := range raw.Items {
			v, ok := chartFloat(chartField(m, "value", "percent", "v"))
			if !ok {
				continue
			}
			c.Items = append(c.Items, ChartItem{Label: chartText(chartField(m, "label", "name")), Value: v})
		}
	case "line", "bar":
		for _, sm := range raw.Series {
			s := ChartSeries{Label: chartText(chartField(sm, "label", "name"))}
			points, _ := sm["points"].([]any)
			for _, p := range points {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				pv, ok := chartFloat(chartField(pm, "v", "value"))
				if !ok {
					continue
				}
				s.Points = append(s.Points, ChartPoint{T: chartText(chartField(pm, "t", "time", "label")), V: pv})
			}
			if len(s.Points) > 0 {
				c.Series = append(c.Series, s)
			}
		}
	case "topo":
		c.Nodes = chartNodes(raw.Nodes)
		c.Edges = chartEdges(raw.Edges)
	case "timeline":
		c.Events = chartEvents(raw.Events)
	}
	// bar+items 漂移（bar_chart 配 items 是真实 LLM 常见形态）：类目数据转单系列，数据不丢。
	if c.Type == "bar" && len(c.Series) == 0 {
		pts := make([]ChartPoint, 0, len(raw.Items))
		for _, m := range raw.Items {
			v, ok := chartFloat(chartField(m, "value", "percent", "v"))
			if !ok {
				continue
			}
			pts = append(pts, ChartPoint{T: chartText(chartField(m, "label", "name")), V: v})
		}
		if len(pts) > 0 {
			c.Series = append(c.Series, ChartSeries{Points: pts})
		}
	}
	return nil
}

// normalizeChartType 类型归一：pie/donut→pie、line/trend/timeseries→line、
// bar/bar_chart/column/histogram→bar、topology/architecture/deployment→topo、
// timeline/时间线/演进→timeline；缺失或未知时按数据形态推断（nodes+edges→topo、
// 有 events→timeline、有 items→pie、有 series→line、仅 nodes→topo），全无为空。
// 判定次序即正确性："timeline" 含子串 "line"，timeline 分支必须先于 line。
func normalizeChartType(t *string, hasItems, hasSeries, hasNodes, hasEdges, hasEvents bool) string {
	s := strings.ToLower(strings.TrimSpace(pointerText(t)))
	switch {
	case strings.Contains(s, "pie") || strings.Contains(s, "donut"):
		return "pie"
	case strings.Contains(s, "bar") || strings.Contains(s, "column") || strings.Contains(s, "histogram"):
		return "bar"
	case chartTypeTimeline(s):
		return "timeline"
	case strings.Contains(s, "line") || strings.Contains(s, "trend") ||
		strings.Contains(s, "timeseries") || strings.Contains(s, "time_series"):
		return "line"
	case chartTypeTopo(s):
		return "topo"
	case hasNodes && hasEdges:
		return "topo"
	case hasEvents:
		return "timeline"
	case hasItems:
		return "pie"
	case hasSeries:
		return "line"
	case hasNodes:
		return "topo"
	}
	return ""
}

// chartTypeTimeline 时间线别名：整词集合 + 含 timeline/时间线/时间轴 的包含匹配
// （chartTypeTopo 同构；"时序"不归此——timeseries 语义历史上归 line，勿抢）。
func chartTypeTimeline(s string) bool {
	switch s {
	case "timeline", "时间线", "时间轴", "演进", "故障演进", "大事记", "chronology", "milestones":
		return true
	}
	return strings.Contains(s, "timeline") || strings.Contains(s, "时间线") || strings.Contains(s, "时间轴")
}

// chartTypeTopo 拓扑类别名：整词别名集合 + 含 topo/拓扑 的包含匹配
// （"paragraph" 这类意外含 graph 的词不命中——graph 只整词收）。
func chartTypeTopo(s string) bool {
	switch s {
	case "topo", "topology", "graph", "arch", "architecture", "deployment", "deploy",
		"dependency", "dependencies", "flow", "flowchart", "tree",
		"拓扑", "拓扑图", "架构", "架构图", "部署", "部署图", "依赖":
		return true
	}
	return strings.Contains(s, "topo") || strings.Contains(s, "拓扑")
}

func pointerText(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// chartField 键名回退取值（模型常把 value 写在 percent/v 等键下）。
func chartField(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func chartText(v any) string {
	s, _ := jsonrepair.CoerceString(v)
	return s
}

// chartFloat 数值容错：数字直接收；字符串剥 % 与空白后 ParseFloat；
// 非有限值（NaN/Inf）视为无效。
func chartFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, isFiniteFloat(t)
	case string:
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "%"))
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return f, isFiniteFloat(f)
	}
	return 0, false
}

func isFiniteFloat(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}
