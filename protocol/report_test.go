package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

// validReport 基准合规报告（各用例在其上覆写）。
func validReport() *Report {
	return &Report{
		AgentName:  "内存分析Agent",
		Domain:     "内存",
		Symptom:    "检测对象：localhost（本机）。检查本机是否存在OOM事件。",
		Conclusion: "本机近7天未检出OOM Killer事件，内存状态健康",
		Evidence: []Evidence{{
			Tool:           "ask-ops_get_logs",
			Snippet:        "3 次 OOM kill @T1/T2/T3，victim=java pid1234 RSS=8G",
			Interpretation: "近7天发生3次OOM kill，受害者为java进程",
		}},
		Recommendation: &Rec{Action: ActionNone, RequiresApproval: false},
		Confidence:     ConfidenceHigh,
		Summary:        "内存健康，无需处置",
		Scope:          "in",
	}
}

func mustParse(t *testing.T, raw string) *Report {
	t.Helper()
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("ParseReport 意外失败: %v", err)
	}
	return r
}

// T1 合规报告 → 无 critical 无 warn。
func TestValidateCompliant(t *testing.T) {
	warns, err := validReport().Validate()
	if err != nil {
		t.Fatalf("合规报告被判违规: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("合规报告不应有 warn: %v", warns)
	}
}

// T2 执行类动作 requires_approval=false → critical（治理铁律，对齐 filter.js）。
func TestExecClassRequiresApproval(t *testing.T) {
	r := validReport()
	r.Recommendation = &Rec{Action: ActionKillAndRestart, Steps: []string{"kill -9 1234"}, RequiresApproval: false}
	_, err := r.Validate()
	if !errors.Is(err, ErrViolation) || !strings.Contains(err.Error(), "requires_approval") {
		t.Fatalf("期望 requires_approval 违规, got: %v", err)
	}
}

// T3 requires_execution=true 但 requires_approval=false → critical。
func TestRequiresExecutionImpliesApproval(t *testing.T) {
	r := validReport()
	r.Recommendation = &Rec{Action: ActionNone, RequiresExecution: true, RequiresApproval: false}
	if _, err := r.Validate(); !errors.Is(err, ErrViolation) {
		t.Fatalf("期望违规, got: %v", err)
	}
}

// T4 action 非白名单（细粒度 request_type 不能进 action 位）→ critical。
func TestActionNotInWhitelist(t *testing.T) {
	r := validReport()
	r.Recommendation = &Rec{Action: "restart", RequiresApproval: true}
	_, err := r.Validate()
	if !errors.Is(err, ErrViolation) || !strings.Contains(err.Error(), "action 不在白名单") {
		t.Fatalf("期望 action 白名单违规, got: %v", err)
	}
}

// T5/T6 confidence 非法/缺失 → critical。
func TestConfidenceRules(t *testing.T) {
	r := validReport()
	r.Confidence = "sure"
	if _, err := r.Validate(); !errors.Is(err, ErrViolation) {
		t.Fatalf("期望 confidence 白名单违规, got: %v", err)
	}
	r2 := validReport()
	r2.Confidence = ""
	if _, err := r2.Validate(); !errors.Is(err, ErrViolation) || !strings.Contains(err.Error(), "缺失") {
		t.Fatalf("期望 confidence 缺失违规, got: %v", err)
	}
}

// T7 decision 非法枚举 → critical。
func TestDecisionWhitelist(t *testing.T) {
	r := validReport()
	r.Decision = "approved_by_llm"
	if _, err := r.Validate(); !errors.Is(err, ErrViolation) {
		t.Fatalf("期望 decision 白名单违规, got: %v", err)
	}
}

// T8/T9 decision=pending_approval 与 requires_approval 的一致性。
func TestPendingDecisionConsistency(t *testing.T) {
	ok := validReport()
	ok.Decision = DecisionPending
	ok.Recommendation = &Rec{Action: ActionTune, RequiresApproval: true}
	if _, err := ok.Validate(); err != nil {
		t.Fatalf("pending+requires_approval=true 应通过: %v", err)
	}

	bad := validReport()
	bad.Decision = DecisionPending
	bad.Recommendation = &Rec{Action: ActionTune, RequiresApproval: false}
	if _, err := bad.Validate(); !errors.Is(err, ErrViolation) {
		t.Fatalf("pending+requires_approval=false 应违规: %v", err)
	}
}

// T10/T11 scope/redirect 配对。
func TestScopeRedirect(t *testing.T) {
	bad := validReport()
	bad.Scope = "out"
	if _, err := bad.Validate(); !errors.Is(err, ErrViolation) || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("期望 scope=out 缺 redirect 违规, got: %v", err)
	}
	good := validReport()
	good.Scope = "out"
	good.Redirect = "specialists/io-analysis"
	if _, err := good.Validate(); err != nil {
		t.Fatalf("scope=out+redirect 应通过: %v", err)
	}
	bad2 := validReport()
	bad2.Scope = "external"
	if _, err := bad2.Validate(); !errors.Is(err, ErrViolation) {
		t.Fatalf("期望 scope 白名单违规, got: %v", err)
	}
}

// T12 snippet 超长（含数据点）→ 恰 1 条 warn（超长），放行不截断（证据保真）。
func TestSnippetOverLong(t *testing.T) {
	r := validReport()
	dataPoint := strings.Repeat("x", 250) + " err" // 长且含错误关键字→仅超长 warn
	r.Evidence[0].Snippet = dataPoint
	warns, err := r.Validate()
	if err != nil {
		t.Fatalf("超长 snippet 应放行: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "超长") {
		t.Fatalf("期望恰 1 条超长 warn, got: %v", warns)
	}
}

// T13 snippet 超长且无数据点 → 2 条 warn（超长 + 缺数据点），对齐 T3 基线复现。
func TestSnippetOverLongAndLowQuality(t *testing.T) {
	r := validReport()
	r.Evidence[0].Snippet = strings.Repeat("x", 250)
	warns, err := r.Validate()
	if err != nil {
		t.Fatalf("不应拦截: %v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("期望 2 条 warn（超长+缺数据点）, got: %v", warns)
	}
}

// T14 纯描述性短 snippet（≤20 字符）→ 不判质量，无 warn。
func TestSnippetShortNoPenalty(t *testing.T) {
	r := validReport()
	r.Evidence[0].Snippet = "系统有内存问题"
	warns, err := r.Validate()
	if err != nil || len(warns) != 0 {
		t.Fatalf("短 snippet 不判质量, got warns=%v err=%v", warns, err)
	}
}

// T15 描述性长 snippet（>20 无任何数据点特征）→ 缺数据点 warn。
func TestSnippetLongLowQuality(t *testing.T) {
	r := validReport()
	r.Evidence[0].Snippet = "系统存在比较明显的内存不足问题需要尽快处理以免影响业务运行"
	warns, err := r.Validate()
	if err != nil {
		t.Fatalf("仅 warn 不拦截: %v", err)
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "缺少具体数据点") {
			found = true
		}
	}
	if !found {
		t.Fatalf("期望缺数据点 warn, got: %v", warns)
	}
}

// T16 ```json 栅栏整体包裹 → ParseReport 剥离成功。
func TestParseFencedJSON(t *testing.T) {
	raw := "```json\n{\"agent_name\":\"X\",\"confidence\":\"high\"}\n```"
	r := mustParse(t, raw)
	if r.AgentName != "X" {
		t.Fatalf("字段丢失: %+v", r)
	}
}

// T17 散文包裹的报告 → 提取通道救回（mimo 实弹真实形态：前缀散文 + JSON）。
// 降级路径（ErrNotReport）仍由 TestParsePlainText 覆盖（无大括号的纯文本）。
func TestParseProseWrappedRescued(t *testing.T) {
	raw := "以下是诊断结果：\n{\"agent_name\":\"X\",\"confidence\":\"high\"}\n以上供参考"
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("散文包裹报告应被提取救回: %v", err)
	}
	if r.AgentName != "X" {
		t.Fatalf("字段丢失: %+v", r)
	}
}

// T18 非法 JSON（形如报告但解析失败）→ ErrNotReport。
func TestParseInvalidJSON(t *testing.T) {
	raw := "{\"agent_name\":\"X\",\"confidence\":}"
	if _, err := ParseReport(raw); !errors.Is(err, ErrNotReport) {
		t.Fatalf("期望 ErrNotReport, got: %v", err)
	}
}

// T19 普通文本（无协议关键字段）→ ErrNotReport（脚本/命令类输出不受影响）。
func TestParsePlainText(t *testing.T) {
	if _, err := ParseReport("#!/bin/bash\necho hello"); !errors.Is(err, ErrNotReport) {
		t.Fatalf("期望 ErrNotReport, got: %v", err)
	}
}

// T20 executed_steps 符号标记 → warn（禁 ✓/✗，AGENTS 协议规则）。
func TestExecutedStepsSymbolWarn(t *testing.T) {
	r := validReport()
	r.ExecutedSteps = []string{"workflow/fault-diagnosis ✓"}
	warns, err := r.Validate()
	if err != nil {
		t.Fatalf("符号标记仅 warn: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "非 slug") {
		t.Fatalf("期望非 slug warn, got: %v", warns)
	}
}

// T21 只读报告（recommendation 缺省）→ 通过（只读 agent 不填 action）。
func TestReadOnlyNoRecommendation(t *testing.T) {
	r := validReport()
	r.Recommendation = nil
	if _, err := r.Validate(); err != nil {
		t.Fatalf("只读报告应通过: %v", err)
	}
}

// T22 Render：含大纲关键节、不含原始 JSON dump（AGENTS「禁止向用户输出原始 JSON」）。
func TestRenderMarkdown(t *testing.T) {
	r := validReport()
	r.Recommendation = &Rec{Action: ActionKillAndRestart, Steps: []string{"kill -9 1234", "restart java"}, RequiresApproval: true}
	r.Decision = DecisionPending
	r.Risk = "kill 可能导致短时业务中断"
	r.Rollback = "重启同名服务恢复"
	r.NeedsFollowup = []string{"specialists/io-analysis"}
	out := Render(r)
	for _, want := range []string{"## 内存分析Agent", "**结论**", "**证据要点**", "**方案与下一步**", "**风险与回退**", "**未决项与置信度**", "待审批", "kill -9 1234"} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染缺关键内容 %q", want)
		}
	}
	if strings.Contains(out, `"agent_name"`) || strings.Contains(out, "{\"") {
		t.Errorf("渲染泄漏原始 JSON:\n%s", out)
	}
}

// T25 Render：证据 snippet 含换行（模型回填多行 JSON 快照）时拍平为单行——
// 列表项被换行打断会破坏 markdown 渲染且形似原始 JSON dump（实弹 2026-09-09）。
func TestRenderEvidenceSnippetSingleLine(t *testing.T) {
	r := validReport()
	r.Evidence = []Evidence{{
		Tool:           "ask-ops",
		Interpretation: "处置前性能快照（对比参照）",
		Snippet:        "before {\n  \"loadavg\": \"6.17 7.79\",\n  \"io_stat\": \"\"\n}",
	}}
	out := Render(r)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "（关键行：") && !strings.HasSuffix(line, "）") {
			t.Errorf("证据列表行被 snippet 换行打断（应单行收尾于右括号）:\n%s", line)
		}
	}
	want := `before { "loadavg": "6.17 7.79", "io_stat": "" }`
	if !strings.Contains(out, want) {
		t.Errorf("snippet 应拍平为单行空格分隔 %q:\n%s", want, out)
	}
}

// T23 容错解码：真实 LLM 常把数组字段写成字符串（mimo 实弹踩坑）——StringList 兼容。
func TestStringListTolerance(t *testing.T) {
	raw := `{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
		`"rollback_steps":"重启服务","needs_followup":[],"executed_steps":"",` +
		`"recommendation":{"action":"tune","steps":"sysctl -w vm.swappiness=10","requires_approval":true}}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("字符串形态的列表字段应容错解码: %v", err)
	}
	if len(r.RollbackSteps) != 1 || r.RollbackSteps[0] != "重启服务" {
		t.Fatalf("rollback_steps 字符串应转单元素数组: %v", r.RollbackSteps)
	}
	if len(r.Recommendation.Steps) != 1 {
		t.Fatalf("steps 字符串应转单元素数组: %v", r.Recommendation.Steps)
	}
	if len(r.ExecutedSteps) != 0 {
		t.Fatalf("executed_steps 空串应为空列表: %v", r.ExecutedSteps)
	}
	if _, err := r.Validate(); err != nil {
		t.Fatalf("容错解码后应通过校验: %v", err)
	}
}

// T24 标量容错：字符串位的 bool/数字统一转文本（mimo 实弹踩坑：bool 落字符串字段）。
func TestScalarCoercion(t *testing.T) {
	raw := `{"agent_name":"X","confidence":true,"conclusion":"c","summary":"s","scope":1,` +
		`"executed_steps":[true,"workflow/fault-diagnosis"],` +
		`"recommendation":{"action":"none","requires_approval":false}}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("标量应容错转文本: %v", err)
	}
	if r.Confidence != "true" {
		t.Fatalf("标量未归一化: %q", r.Confidence)
	}
	// scope 非枚举文本（数字/主机名）→ 规整为 in（有明确检测对象语义）。
	if r.Scope != "in" {
		t.Fatalf("scope 应规整为 in: %q", r.Scope)
	}
	if len(r.ExecutedSteps) != 2 || r.ExecutedSteps[0] != "true" {
		t.Fatalf("列表内标量应转文本: %v", r.ExecutedSteps)
	}
}

// T25 JSON 修复通道：无效转义（\|）与尾逗号——mimo 实弹踩坑。
func TestRepairChannel(t *testing.T) {
	raw := "{\"agent_name\":\"X\",\"confidence\":\"high\",\"conclusion\":\"c\",\"summary\":\"s\"," +
		"\"conclusion\":\"ok\\|done\",}" // 重复键 + 无效转义 \| + 尾逗号
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("修复通道应救回: %v", err)
	}
	if !strings.Contains(r.Conclusion, "done") {
		t.Fatalf("结论丢失: %q", r.Conclusion)
	}
}

// T26 提取通道：散文前缀（含 ```json 围栏）与截断括号——mimo 实弹踩坑。
func TestExtractFromProseAndTruncated(t *testing.T) {
	// 前缀散文 + 围栏 + 后缀散文。
	prose := "数据基本全部因非 root 权限无法获取。基于仅有的负载数据，输出统一报告：\n" +
		"```json\n{\"agent_name\":\"X\",\"confidence\":\"low\",\"conclusion\":\"c\",\"summary\":\"s\"}\n```\n以上。"
	r, err := ParseReport(prose)
	if err != nil {
		t.Fatalf("散文包裹应提取成功: %v", err)
	}
	if r.AgentName != "X" {
		t.Fatalf("字段丢失: %+v", r)
	}
	// 截断：括号未闭合 + 尾随全角括号。
	truncated := "{\n  \"agent_name\": \"healer\",\n  \"confidence\": \"high\",\n  \"conclusion\": \"x\",\n  \"summary\": \"s\",\n  \"recommendation\": {\n    \"action\": \"none\"\n  }）"
	r2, err := ParseReport(truncated)
	if err != nil {
		t.Fatalf("截断 JSON 应经提取+修复救回: %v", err)
	}
	if r2.Conclusion != "x" {
		t.Fatalf("字段异常: %+v", r2)
	}
}

// ── charts 可视化附件：解析、容错归一、畸形降级 ──

// T-charts-1 正常 pie+line 完整解析。
func TestChartsParse(t *testing.T) {
	raw := `{"agent_name":"IO分析Agent","confidence":"high","conclusion":"c","summary":"s",
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

// T27 conclusion 数组形态（mimo 实弹 2026-09-09）：结构化条目数组 → 序号拼接文本，
// 不因类型错让整份报告 ErrNotReport。
func TestConclusionListTolerance(t *testing.T) {
	raw := `{"agent_name":"X","domain":"system","confidence":"high","summary":"s",` +
		`"symptom":"检测对象：H。",` +
		`"conclusion":[{"id":"RC-1","title":"stress-load 产生 CPU 压力","status":"confirmed",` +
		`"detail":"三个子进程各占 ~98% CPU"},{"id":"RC-2","title":"metrics-agent 203/EXEC",` +
		`"detail":"可执行文件缺失导致循环重启"}]}`
	r, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("结论数组应归一为文本: %v", err)
	}
	for _, want := range []string{"1. stress-load 产生 CPU 压力：三个子进程各占 ~98% CPU", "2. metrics-agent 203/EXEC"} {
		if !strings.Contains(r.Conclusion, want) {
			t.Fatalf("结论条目丢失 %q: %q", want, r.Conclusion)
		}
	}
	// 字符串项数组同样拼接。
	r2 := mustParse(t, `{"agent_name":"X","confidence":"medium","conclusion":["甲","乙"],"summary":"s"}`)
	if r2.Conclusion != "1. 甲；2. 乙" {
		t.Fatalf("标量数组应序号拼接: %q", r2.Conclusion)
	}
}

// T28 decision="info"（实弹 2026-09-09：巡检 agent 用 info 表达"仅告知"）→ 归一空串放行。
func TestDecisionInfoSynonym(t *testing.T) {
	r, err := ParseReport(`{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
		`"decision":"info","recommendation":{"action":"none","requires_approval":false}}`)
	if err != nil {
		t.Fatalf("info 应归一为无决策: %v", err)
	}
	if _, verr := r.Validate(); verr != nil {
		t.Fatalf("归一后应通过白名单校验: %v", verr)
	}
	if r.Decision != "" {
		t.Fatalf("decision 应为空串: %q", r.Decision)
	}
}

// T29 decision 环境受限自造枚举（实弹 2026-09-09 复盘：专家在目标机不可达/平台不匹配时
// 自述无法给出决策，曾被全量拦截成 5 连违约）→ 归一空串放行；散文化取值（中文句子）同归；
// 枚举空间的未知 ASCII 词仍拦截（T7 语义不变）。
func TestDecisionEnvironmentSynonyms(t *testing.T) {
	for _, d := range []string{"host_unreachable", "platform_mismatch_stop", "degraded",
		"awaiting_input", "clarification_needed", "采集受限，需人工检查"} {
		r, err := ParseReport(`{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
			`"decision":"` + d + `","recommendation":{"action":"none","requires_approval":false}}`)
		if err != nil {
			t.Fatalf("decision=%q 应归一为无决策: %v", d, err)
		}
		if r.Decision != "" {
			t.Fatalf("decision=%q 应为空串: %q", d, r.Decision)
		}
		if _, verr := r.Validate(); verr != nil {
			t.Fatalf("decision=%q 归一后应通过校验: %v", d, verr)
		}
	}
}

// T29 decision="escalate"（实弹 2026-09-10：平台无工具可用时模型表达"转人工"）→ 归一空串。
func TestDecisionEscalateSynonym(t *testing.T) {
	r, err := ParseReport(`{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
		`"decision":"escalate","recommendation":{"action":"none","requires_approval":false}}`)
	if err != nil {
		t.Fatalf("escalate 应归一为无决策: %v", err)
	}
	if _, verr := r.Validate(); verr != nil {
		t.Fatalf("归一后应通过白名单校验: %v", verr)
	}
}

// T31 未知枚举形 decision 一律归"无决策"（2026-09-25 215 两轮实弹结构收敛）：
// target_scope_mismatch_no_action（安全专家报 Alpine 容器无判读对象）与 redirect
// （k8s 专家表达本域转出）是连续两轮各出现一枚的新词形——逐词收录同义族是打地鼠，
// 真模型持续自造诚实信号词，每个漏网词都让整份专项报告被拦、巡检聚合缺域。
// decision 无治理语义（真正闸门在 action+requires_approval），未知值归空零成本。
func TestDecisionUnknownEnumNormalized(t *testing.T) {
	for _, d := range []string{"target_scope_mismatch_no_action", "redirect", "target_scope_mismatch"} {
		r, err := ParseReport(`{"agent_name":"X","confidence":"high","conclusion":"c","summary":"s",` +
			`"decision":"` + d + `","recommendation":{"action":"none","requires_approval":false}}`)
		if err != nil {
			t.Fatalf("decision=%q 应归一为无决策: %v", d, err)
		}
		if r.Decision != "" {
			t.Fatalf("decision=%q 应为空串: %q", d, r.Decision)
		}
		if _, verr := r.Validate(); verr != nil {
			t.Fatalf("decision=%q 归一后应通过校验: %v", d, verr)
		}
	}
}

// T30 合议 verdicts 宽容解码：非法 stance/缺 agent 条目解码期丢弃、报告不炸；
// 合法枚举兜底——手工构造的非法 stance 必须被 Validate 拦截。
func TestVerdictsTolerantDecode(t *testing.T) {
	raw := `{"agent_name":"workflow/verdict-judge","confidence":"medium",
		"verdicts":[{"agent":"specialists/memory-analysis","stance":"adopt","reason":"证据自洽"},
		{"agent":"x","stance":"maybe"},{"agent":"y"},{"stance":"reject"},
		{"agent":"specialists/io-analysis","stance":"reject","reason":"与证据冲突"}]}`
	var r Report
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("宽容解码不得报错: %v", err)
	}
	if len(r.Verdicts) != 2 {
		t.Fatalf("非法 stance/缺 agent 条目应丢弃, got %d: %+v", len(r.Verdicts), r.Verdicts)
	}
	if r.Verdicts[0].Stance != VerdictAdopt || r.Verdicts[1].Stance != VerdictReject {
		t.Fatalf("stance 解析错误: %+v", r.Verdicts)
	}
	// 合法枚举兜底：手工构造的非法 stance 必须被 Validate 拦截
	bad := Report{AgentName: "a", Confidence: ConfidenceHigh,
		Verdicts: Verdicts{{Agent: "x", Stance: "maybe"}}}
	// 注：实际签名为 (*Report).Validate() ([]string, error)，按 report_validate.go 现状对齐调用形态。
	if _, err := bad.Validate(); err == nil {
		t.Fatal("手工构造的非法 stance 必须被 Validate 拦截")
	}
}

// T23 Render 剥离模型自带步骤编号/列表前缀（展示层双编号修复）：无前缀步骤原样保留。
func TestRenderStripsModelStepOrdinals(t *testing.T) {
	r := validReport()
	r.Recommendation = &Rec{
		Action: ActionTune,
		Steps:  []string{"1. 检查磁盘水位", "2、清理过期日志", "（3）重启服务", "- 观察指标", "kill -9 1234"},
	}
	out := Render(r)
	for _, want := range []string{"1. 检查磁盘水位", "2. 清理过期日志", "3. 重启服务", "4. 观察指标", "5. kill -9 1234"} {
		if !strings.Contains(out, want) {
			t.Errorf("渲染缺剥离后步骤 %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"1. 1.", "2. 2、", "3. （3）", "4. - 观察指标"} {
		if strings.Contains(out, bad) {
			t.Errorf("双编号残留 %q:\n%s", bad, out)
		}
	}
}

// T20b executed_steps 合法 slug 命名空间（批次三百三十三）：imports/ 包专家、
// workflow/custom 嵌套段、ops-scheduler 裸单例全部零 warn（kylin-insight 实弹：
// 合法 imports/kylin-insight-analyst 曾被旧正则误报非 slug 项）；符号标记与未知
// 命名空间照旧告警（T20 语义不退化）。
func TestExecutedStepsLegalNamespaces(t *testing.T) {
	r := validReport()
	r.ExecutedSteps = []string{"imports/kylin-insight-analyst", "workflow/custom/bq215-exec-probe", "ops-scheduler", "workflow/fault-diagnosis"}
	warns, err := r.Validate()
	if err != nil {
		t.Fatalf("合法 slug 不应 error: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("合法 slug 命名空间应零 warn, got: %v", warns)
	}
	r.ExecutedSteps = []string{"imports/kylin-insight-analyst ✓", "bogus/thing"}
	warns, err = r.Validate()
	if err != nil {
		t.Fatalf("符号标记仅 warn: %v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("符号标记与未知命名空间应各 1 warn, got: %v", warns)
	}
}

// TestRenderAwaitingUserLabel 待确认态专属呈现（批次三百三十四）：AwaitingUser 持位
// 渲染「待确认」标签、不再冒充「结论」体（反问轮实弹：「要诊断哪个集群？」落
// **结论** 观感生硬）；缺省报告结论文案零变化。
func TestRenderAwaitingUserLabel(t *testing.T) {
	r := validReport()
	r.Conclusion = "要诊断哪个集群？回复序号或集群名"
	r.AwaitingUser = true
	md := RenderView(r, ViewFull)
	if !strings.Contains(md, "**待确认**：要诊断哪个集群？") {
		t.Fatalf("待确认标签缺失:\n%s", md[:min(len(md), 200)])
	}
	if strings.Contains(md, "**结论**") {
		t.Fatal("待确认态不应出现结论体")
	}
	r.AwaitingUser = false
	if md2 := RenderView(r, ViewFull); !strings.Contains(md2, "**结论**：要诊断哪个集群？") {
		t.Fatal("缺省应保持结论体")
	}
	// answer 档同语义（引导声位轻装路径）
	r.Profile = ProfileAnswer
	r.AwaitingUser = true
	if md3 := RenderView(r, ViewFull); !strings.Contains(md3, "**待确认**：") || strings.Contains(md3, "**结论**") {
		t.Fatalf("answer 档待确认标签缺失:\n%s", md3[:min(len(md3), 200)])
	}
}
