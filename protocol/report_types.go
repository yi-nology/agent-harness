package protocol

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/oekit/pkg/jsonrepair"
)

// 报告协议类型契约：LLM 输出的结构面（StringList/Evidence/Rec/Chart/Report）。

type StringList []string

func (s *StringList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		// 元素可能是对象/数字/布尔（模型常把步骤写成 {"step":1,"description":...}）——
		// 非字符串元素规整为文本（对象压缩为 JSON 文本，数据不丢）。
		var out []any
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return err
		}
		list := make([]string, 0, len(out))
		for _, item := range out {
			switch t := item.(type) {
			case string:
				list = append(list, t)
			case bool:
				list = append(list, fmt.Sprintf("%t", t))
			case float64:
				list = append(list, strconv.FormatFloat(t, 'f', -1, 64))
			default:
				b, _ := json.Marshal(t)
				list = append(list, string(b))
			}
		}
		*s = list
		return nil
	}
	var single string
	if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
		return err
	}
	if single == "" {
		*s = nil
		return nil
	}
	*s = []string{single}
	return nil
}

func (s StringList) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(s))
}

// Std 转标准 []string（strings.Join 等调用点用）。
func (s StringList) Std() []string { return []string(s) }

// Evidence 蒸馏后的单项证据：发现 + 关键行（snippet ≤200 且须含具体数据点）。
// 模型常把工具名写在 type/item/name/source 键下——解码时回退到 tool。
// ID 证据编号（批次一百三十七，结论可回溯面）：`<agent>#E<n>`，ParseReport 侧确定性
// 挂号（已有值不覆盖）；verdicts[].evidence_ids 引用它，聚合/合并只追加不重排，编号
// 全程稳定。
type Evidence struct {
	Tool           string `json:"tool"`
	Snippet        string `json:"snippet"`
	Interpretation string `json:"interpretation"`
	ID             string `json:"id,omitempty"`
}

func (e *Evidence) UnmarshalJSON(data []byte) error {
	var raw struct {
		Tool           *string `json:"tool"`
		Type           *string `json:"type"`
		Item           *string `json:"item"`
		Name           *string `json:"name"`
		Source         *string `json:"source"`
		Snippet        *string `json:"snippet"`
		Interpretation *string `json:"interpretation"`
		ID             *string `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, p := range []*string{raw.Tool, raw.Type, raw.Item, raw.Name, raw.Source} {
		if p != nil && *p != "" {
			e.Tool = *p
			break
		}
	}
	if raw.Snippet != nil {
		e.Snippet = *raw.Snippet
	}
	if raw.Interpretation != nil {
		e.Interpretation = *raw.Interpretation
	}
	if raw.ID != nil {
		e.ID = strings.TrimSpace(*raw.ID)
	}
	return nil
}

// StringMap 容错字符串参数表（change_params 专用）：编目参数声明 string_list 值
// 逗号分隔，但 LLM 偶发把数值/布尔形态写进值位——标准 unmarshal 对
// map[string]string 硬失败会炸掉整份报告解析（ErrNotReport 降级读 markdown 主体，
// 比单工具失败更疼；http_query per_page:20 同型实弹在案后对本协议面巡检补齐）。
// 数值/布尔 coerce 为文本，null/数组/对象丢键不炸；Marshal 形态与普通 map 一致。
type StringMap map[string]string

func (m *StringMap) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*m = nil
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(StringMap, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case float64:
			out[k] = strconv.FormatFloat(t, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(t)
		}
	}
	*m = out
	return nil
}

// Rec recommendation 子结构（处方）。批次九十四起支持编目形态：change_ref+
// change_params 引用已装包的变更块（<包>/changes/<slug>.yaml），平台渲染模板得
// 最终 steps——LLM 只给参数不给命令本体；自由 steps（未编目）仍合法，审批卡
// 标注「未编目」，站点可用 approval.curated_only 收紧为仅编目可执行。
type Rec struct {
	Action            string     `json:"action"` // kill_and_restart|tune|none
	Steps             StringList `json:"steps"`
	RequiresExecution bool       `json:"requires_execution"`
	RequiresApproval  bool       `json:"requires_approval"`
	ChangeRef         string     `json:"change_ref,omitempty"`    // 编目变更块 slug（可选）
	ChangeParams      StringMap  `json:"change_params,omitempty"` // 编目参数（string_list 值逗号分隔；容错解码见 StringMap）
	CuratedRisk       int        `json:"curated_risk,omitempty"`  // 编目声明风险档 1-4（引擎基线下限；LLM 评估只升不降）
}

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

// 合议三值裁决（verdict-judge 专属；spec 2026-09-15-verdict-judge-design §6）。
const (
	VerdictAdopt   = "adopt"
	VerdictDispute = "dispute"
	VerdictReject  = "reject"
)

var stanceAll = map[string]bool{VerdictAdopt: true, VerdictDispute: true, VerdictReject: true}

// VerdictItem 合议对单份专家报告的裁决。
type VerdictItem struct {
	Agent  string `json:"agent"`
	Stance string `json:"stance"` // adopt|dispute|reject
	Reason string `json:"reason,omitempty"`
	// EvidenceIDs 裁决依据的证据编号（批次一百三十七）：引用所依据报告的 evidence.id
	//（形如 specialists/memory-analysis#E2）——「引用证据说理」从 prompt 软要求升级为
	// 结构化引用通道；容忍单字符串/数组双形态（StringList 宽容解码），引用不存在的
	// 编号由引擎侧合并后校验（CheckEvidenceRefs），只在留痕面告警不拦截。
	EvidenceIDs StringList `json:"evidence_ids,omitempty"`
}

// Verdicts 宽容解码（仿 ChartSpec）：stance 非法或 agent 为空的条目丢弃，绝不影响整报解析。
type Verdicts []VerdictItem

func (v *Verdicts) UnmarshalJSON(data []byte) error {
	var raw []VerdictItem
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil // 形态不是数组等畸形：整体放弃，不炸报告
	}
	out := make(Verdicts, 0, len(raw))
	for _, it := range raw {
		if it.Agent == "" || !stanceAll[it.Stance] {
			continue
		}
		out = append(out, it)
	}
	*v = out
	return nil
}

// Followup 结构化接力指令（批次一百二十七）：谁（By）该做什么（What）、为什么（Why）。
// 与 NeedsFollowup 字符串列表双通道过渡——引擎侧新增接力语义走本类型（注入视图/模板
// 变量 {{followups}} 消费），旧字符串字段保留供存量前端渲染；LLM 输出协议不含本字段
// （防线段字段清单不收、repair 层不游走），由引擎侧（合议否决/链终止/聚合透传）填写。
// 前端跟进作栏后撤 needs_followup 字符串双写。
type Followup struct {
	By   string `json:"by,omitempty"`
	What string `json:"what"`
	Why  string `json:"why,omitempty"`
}

// StepMeta 环节执行元数据（批次一百二十七，引擎回填非 LLM 输出）：环节耗时/工具调用
// 次数/LLM 轮次——信封级质量信号，注入视图与聚合面可引用，与报告内容分离（对标 ZCode
// AgentCompletedOutput 的 usage/耗时字段）。报告落库随 meta 序列化；LLM 产出不经此字段。
type StepMeta struct {
	DurationMS int64 `json:"duration_ms,omitempty"`
	ToolCalls  int   `json:"tool_calls,omitempty"`
	Iterations int   `json:"iterations,omitempty"`
}

// ExploreGap 勘探缺口（批次二百零八 GE2 迭代勘探，spec
// 2026-10-07-promotion-channel-generalexec-design.md §7）：综合环节自判证据不足时输出
// 的续轮勘探缺口——aspect=方向名（进环节标签「勘探·<aspect>」），query=建议勘探问题。
// 仅通用轨评估流在 generalist.iterative_explore 开（缺省关）时消费；其余报告携带本
// 字段=无消费方零影响。
type ExploreGap struct {
	Aspect string `json:"aspect"`
	Query  string `json:"query,omitempty"`
}

// ReportProfile 报告形态档（批次二百一十七）：同底座分形态——protocol.Report 契约、
// 落库双写、流式分片与受众投影全轨共用一份，仅渲染大纲按档切换（单点在
// report_render.go）。diagnosis=诊断大纲（现役缺省；零值同义，存量报告零漂移）；
// answer=通用轨直答大纲（结论先行，不硬撑诊断专属面）。确定性由 flow 侧设置——
// LLM 输出协议不含本字段（防线段字段清单不收），与 Followups/Meta 同属引擎侧填写。
type ReportProfile string

const (
	ProfileDiagnosis ReportProfile = "diagnosis"
	ProfileAnswer    ReportProfile = "answer"
)

// Report 统一报告协议（所有专业/流程/治理层 agent 的最终消息）。
type Report struct {
	AgentName      string      `json:"agent_name"`
	Domain         string      `json:"domain"`
	Symptom        string      `json:"symptom"` // 首句须含「检测对象：」标注
	Conclusion     string      `json:"conclusion"`
	Evidence       []Evidence  `json:"evidence,omitempty"`
	Charts         []ChartSpec `json:"charts,omitempty"` // 可视化附件（占比→pie、时序→line、对比→bar、部署/依赖→topo、演进/时间线→timeline），畸形由 ChartSpec 容错
	Impact         string      `json:"impact,omitempty"`
	ImpactScopes   StringList  `json:"impact_scopes,omitempty"` // 受影响对象清单（批次一百三十七，结构化补缺：host/服务/pod 级；聚合去重合并）
	Severity       string      `json:"severity,omitempty"`      // 严重度 info|low|medium|high|critical（批次一百三十七；聚合取最高、dispute 钳 ≤medium；空=未评估）
	Recommendation *Rec        `json:"recommendation,omitempty"`
	RequestType    string      `json:"request_type,omitempty"` // 细粒度动作类别（风险分级用）
	Risk           string      `json:"risk,omitempty"`
	Rollback       string      `json:"rollback,omitempty"`
	RollbackSteps  StringList  `json:"rollback_steps,omitempty"`
	NeedsFollowup  StringList  `json:"needs_followup,omitempty"`
	Confidence     string      `json:"confidence"`          // high|medium|low
	Verdicts       Verdicts    `json:"verdicts,omitempty"`  // 合议裁决（workflow/verdict-judge 专属）
	Followups      []Followup  `json:"followups,omitempty"` // 结构化接力指令（批次一百二十七；引擎侧填写，非 LLM 输出字段）
	Meta           *StepMeta   `json:"meta,omitempty"`      // 环节执行元数据（批次一百二十七；引擎回填，非 LLM 输出字段）
	// ExploreGaps 勘探缺口（批次二百零八）：GE2 迭代勘探的综合环节输出通道——LLM 在
	// iterative_explore 开时按提示词输出，引擎按缺口发起受限续轮扇出。与 Followups 同属
	// 「特定流专属通道」，其余流出现即无消费方。
	ExploreGaps []ExploreGap `json:"explore_gaps,omitempty"`
	// Profile 报告形态档（批次二百一十七，引擎侧确定性填写，非 LLM 输出字段）：flow
	// 按轨设置，空=diagnosis 存量兼容。随 FinalReportJSON 落库，handler 受众投影
	// （view=summary 重渲染）因此自动按档分支，消费面零改动。
	Profile       ReportProfile `json:"profile,omitempty"`
	Summary       string        `json:"summary"`
	Scope         string        `json:"scope,omitempty"`    // in|out（kyaiops 扩展字段）
	Redirect      string        `json:"redirect,omitempty"` // scope=out 时的目标 slug，仅一层
	Decision      string        `json:"decision,omitempty"` // pending_approval|approved|rejected
	ExecutedSteps StringList    `json:"executed_steps,omitempty"`
	// AwaitingUser 待确认态专属标记（批次三百三十四，调度器 clarify 分支持位，非 LLM
	// 输出字段）：反问轮的回答不是诊断结论——渲染器以「待确认」替代「结论」标签，
	// 消除「要诊断哪个集群？」落 **结论** 体的生硬文案（可点选卡照常承担交互）。
	AwaitingUser bool `json:"awaiting_user,omitempty"`
	// Guidance 对话直答标记（引擎侧确定性填写，非 LLM 输出字段）：置位表示本条是
	// 调度引导/元问题/寒暄一类的对话回复而非诊断报告，markdown 走轻装视图
	// （不出 agent 标题与置信度）。内核不依赖任何命名约定判定该形态——判定依据
	// 由宿主通过本字段或 SetGuidanceAgentNames 注册，二选一。
	Guidance bool `json:"guidance,omitempty"`
}

var (
	fenceRe = regexp.MustCompile("(?s)^```(?:json)?\\s*(.*?)```\\s*$")
	// reportKeyRe 形如统一报告的快速判定（与 kyaiops-filter.js 同判据）。
	reportKeyRe = regexp.MustCompile(`"(agent_name|domain|conclusion|recommendation|confidence)"`)

	actionWhitelist = map[string]bool{ActionKillAndRestart: true, ActionTune: true, ActionNone: true}
	confidenceAll   = map[string]bool{ConfidenceHigh: true, ConfidenceMedium: true, ConfidenceLow: true}
	decisionAll     = map[string]bool{DecisionPending: true, DecisionApproved: true, DecisionRejected: true}
	scopeAll        = map[string]bool{"in": true, "out": true}

	// decisionNoneSynonyms 已删（2026-09-25）：normalizeDecision 对未知值一律归"无决策"，
	// 逐词收录是打地鼠（实弹：target_scope_mismatch_no_action/redirect 两轮各一新词形）。
	// 历史词族（hold/info/escalate/platform_mismatch/host_unreachable 等）自动被新语义覆盖。

	// slugRe executed_steps 须为纯 slug（禁 ✓/✗/(命中) 等符号标记——AGENTS.md 协议规则）。
	// 命名空间白名单含 imports（包专家，批次三百三十三 kylin-insight 实弹误报：合法
	// imports/kylin-insight-analyst 被标非 slug——包机制上线以来 imports 族一直误报）；
	// 段间允许嵌套（workflow/custom/<name> NL 建链）；ops-scheduler 为无命名空间单例。
	slugRe = regexp.MustCompile(`^((workflow|specialists|platform|compliance|imports)/[\w-]+(/[\w-]+)*|ops-scheduler)$`)
)

// IsConfidenceValid 合议触发门用（scheduler 层过滤无效报告）。
func IsConfidenceValid(c string) bool { return confidenceAll[c] }

// decisionProse 散文判据：小写化后仍含 [a-z0-9_-] 之外字符（空格/中文/标点）即非枚举词。
func decisionProse(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-'
	}) >= 0
}

// rawRequiresApproval 从归一化前的 recommendation 原始 map 判定 requires_approval 真值
// （bool / 字符串 "true" 双形态；结构缺失或不可判 → false）。decision 归一在字段游走期
// 执行，map 迭代序不定，兄弟字段必须自带解析、不依赖先后的归一结果。
func rawRequiresApproval(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	switch t := m["requires_approval"].(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	default:
		return false
	}
}
