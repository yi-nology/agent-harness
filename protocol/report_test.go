package protocol

import (
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

// validReport 基准合规报告（各用例在其上覆写）。
// 夹具全用中性词面：契约示例带运维味等于把契约钉死在运维域上
// （harness-test-fixture-domain-vocabulary 门禁）。
func validReport() *Report {
	return &Report{
		AgentName:  "容量评估Agent",
		Domain:     "容量",
		Symptom:    "检测对象：node-a。检查该节点是否存在限流事件。",
		Conclusion: "node-a 近7天未检出限流事件，容量水位健康",
		Evidence: []Evidence{{
			Tool:           "ask-metrics_get_samples",
			Snippet:        "3 次 throttled @T1/T2/T3，target=svc-b",
			Interpretation: "近7天发生3次限流，受影响对象为 svc-b",
		}},
		Recommendation: &Rec{Action: ActionNone, RequiresApproval: false},
		Confidence:     ConfidenceHigh,
		Summary:        "容量水位健康，无需处理",
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
	for _, want := range []string{"## 容量评估Agent", "**结论**", "**证据要点**", "**方案与下一步**", "**风险与回退**", "**未决项与置信度**", "待审批", "kill -9 1234"} {
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
