package protocol

// 报告双形态档（批次二百一十七）：answer 档直答大纲 + 零值存量兼容 + JSON 往返保真。
// 诊断大纲本体（现象→结论→证据→…）由 report_test.go/citation 侧既有用例钉住，此处
// 只钉形态分支。

import (
	"encoding/json"
	"strings"
	"testing"
)

func answerFixture() *Report {
	return &Report{
		AgentName:  "platform/generalist",
		Domain:     "通用评估",
		Profile:    ProfileAnswer,
		Conclusion: "可行，建议分三步走",
		Evidence: []Evidence{
			{Tool: "search_knowledge", Interpretation: "站内已有同类案例", Snippet: "2026-09 案例", ID: "EV-1"},
			{Tool: "web_search", Interpretation: "业界主流做法一致"},
		},
		Recommendation: &Rec{Action: ActionNone, Steps: StringList{"1. 先备份", "2. 灰度切换", "3. 全量放量"}},
		NeedsFollowup:  StringList{"预算归属需拍板", "切换窗口需相关方确认"},
		Confidence:     ConfidenceMedium,
		Summary:        "整体可行，注意灰度节奏",
	}
}

func TestRenderAnswerProfileFull(t *testing.T) {
	out := RenderView(answerFixture(), ViewFull)
	if !strings.HasPrefix(out, "## platform/generalist\n\n") {
		t.Fatalf("标题应在首行: %q", out)
	}
	// 结论先行：结论段位置早于证据段。
	if i, j := strings.Index(out, "**结论**："), strings.Index(out, "**证据要点**"); i < 0 || j < 0 || i > j {
		t.Fatalf("直答大纲应结论先行（结论@%d 证据@%d）", i, j)
	}
	if !strings.Contains(out, "**证据要点**：\n\n- [search_knowledge] 站内已有同类案例（关键行：2026-09 案例）（编号：EV-1）") {
		t.Fatalf("证据明细应保留（编号/关键行/工具）: %q", out)
	}
	// action=none 下的 steps 按「建议与下一步」出（诊断档的动作门在此不适用）。
	if !strings.Contains(out, "**建议与下一步**：\n\n1. 先备份\n2. 灰度切换\n3. 全量放量") {
		t.Fatalf("steps 应按建议面渲染: %q", out)
	}
	if !strings.Contains(out, "**拍板点**：\n\n- 预算归属需拍板\n- 切换窗口需相关方确认") {
		t.Fatalf("needs_followup 应正名拍板点: %q", out)
	}
	if !strings.Contains(out, "**置信度**：medium") || !strings.Contains(out, "**小结**：整体可行") {
		t.Fatalf("尾注应渲染非空置信度/小结: %q", out)
	}
	// 诊断专属面不出现（含空壳段「未决项与置信度」）。
	for _, banned := range []string{"**现象**", "**影响范围**", "**风险与回退**", "未决项与置信度", "方案与下一步"} {
		if strings.Contains(out, banned) {
			t.Fatalf("直答大纲不应出现诊断段 %q: %q", banned, out)
		}
	}
}

func TestRenderAnswerProfileSparse(t *testing.T) {
	// 纯直答：无证据/无步骤/无拍板点/无小结，只有结论+置信度——不应有空壳段。
	out := RenderView(&Report{AgentName: "通用顾问", Profile: ProfileAnswer,
		Conclusion: "kubectl logs <pod> 即可", Confidence: ConfidenceHigh}, ViewFull)
	want := "## 通用顾问\n\n**结论**：kubectl logs <pod> 即可\n\n**置信度**：high\n\n"
	if out != want {
		t.Fatalf("稀疏直答应极简收口:\n got %q\nwant %q", out, want)
	}
}

func TestRenderAnswerProfileSummary(t *testing.T) {
	out := RenderView(answerFixture(), ViewSummary)
	if !strings.Contains(out, "**证据要点**：2 条（完整档含明细）") {
		t.Fatalf("summary 档证据应只报条数: %q", out)
	}
	if !strings.Contains(out, "**建议与下一步**：3 步（完整档含明细）") {
		t.Fatalf("summary 档步骤应只报步数: %q", out)
	}
	// 拍板点是管理层/通知面的决策核心，summary 档全量保留。
	if !strings.Contains(out, "- 切换窗口需相关方确认") {
		t.Fatalf("summary 档拍板点应全量: %q", out)
	}
	if strings.Contains(out, "1. 先备份") {
		t.Fatalf("summary 档不应含步骤明细: %q", out)
	}
}

// TestRenderProfileZeroValueIsDiagnosis 零值/显式 diagnosis 走诊断大纲且逐字节一致——
// 存量报告（无 profile 字段的历史 FinalReportJSON）零漂移。
func TestRenderProfileZeroValueIsDiagnosis(t *testing.T) {
	r := &Report{
		AgentName: "specialists/x", Symptom: "检测对象：localhost。CPU 高。",
		Conclusion: "进程占满 CPU", Confidence: ConfidenceHigh, Severity: "high",
	}
	zero := RenderView(r, ViewFull)
	r.Profile = ProfileDiagnosis
	named := RenderView(r, ViewFull)
	if zero != named {
		t.Fatalf("零值与显式 diagnosis 应逐字节一致")
	}
	for _, want := range []string{"**现象**：", "**未决项与置信度**：", "- 置信度：high"} {
		if !strings.Contains(zero, want) {
			t.Fatalf("诊断大纲段缺失 %q: %q", want, zero)
		}
	}
}

// TestProfileRoundTrip 形态档随 FinalReportJSON 落库再经 ParseReport 读回必须保真——
// handler 受众投影（views.go applyReportView）靠它自动按档重渲染。
func TestProfileRoundTrip(t *testing.T) {
	b, err := json.Marshal(answerFixture())
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseReport(string(b))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if r.Profile != ProfileAnswer {
		t.Fatalf("profile 应保真 round-trip: %q", r.Profile)
	}
	// 存量 JSON（无 profile 键）读回零值=diagnosis 语义。
	r2, err := ParseReport(`{"agent_name":"a","conclusion":"c","confidence":"high"}`)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Profile != "" {
		t.Fatalf("存量无 profile 键应读回零值: %q", r2.Profile)
	}
}

// TestRenderGuidanceVoiceLight 引导声位轻装：调度引导/元问题/寒暄直答是对话回复
// 不是报告——markdown 不出 agent 标题/置信度/内部小结（实弹 sess-1010-akyekwwv：
// 问候收到「运维任务调度Agent」H2+「置信度 medium」机械报告）；非引导声位 answer
// 档标题不受影响（TestRenderAnswerProfileFull 钉住）。
//
// 夹具用合成引导名——引导声位的判定依据属于宿主，内核测试不绑定任何具体命名。
func TestRenderGuidanceVoiceLight(t *testing.T) {
	r := &Report{
		AgentName:  "synthetic-guide",
		Domain:     "调度",
		Profile:    ProfileAnswer,
		Symptom:    "检测对象：无（寒暄客套，不涉及检测对象）",
		Conclusion: "你好，我是测试用引导声位。",
		Confidence: ConfidenceMedium,
		Summary:    "自答：寒暄客套直答",
		Scope:      "in",
		Guidance:   true, // 通道一：结构化置位（推荐路径）
	}
	out := RenderView(r, ViewFull)
	if !strings.HasPrefix(out, "**结论**：") {
		t.Fatalf("引导声位首行即结论（无 agent H2）: %q", out)
	}
	for _, ban := range []string{"## ", "**置信度**", "**小结**", "P5 自答"} {
		if strings.Contains(out, ban) {
			t.Fatalf("引导声位不应含 %q: %q", ban, out)
		}
	}
	// 摘要档同契约。
	if s := RenderView(r, ViewSummary); !strings.HasPrefix(s, "**结论**：") || strings.Contains(s, "**置信度**") {
		t.Fatalf("摘要档同轻装: %q", s)
	}
}
