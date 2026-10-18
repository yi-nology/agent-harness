package protocol

// 证据编号/引用完整性 + severity/impact_scopes + 受众分档渲染（批次一百三十七）。

import (
	"strings"
	"testing"
)

func TestAssignEvidenceIDsOnParse(t *testing.T) {
	r, err := ParseReport(`{"agent_name":"specialists/memory-analysis","domain":"故障诊断","conclusion":"c","confidence":"high","evidence":[{"tool":"free","snippet":"pid 123","interpretation":"i1"},{"tool":"ps","snippet":"503","interpretation":"i2"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Evidence[0].ID; got != "specialists/memory-analysis#E1" {
		t.Fatalf("证据编号挂号错误: %q", got)
	}
	if got := r.Evidence[1].ID; got != "specialists/memory-analysis#E2" {
		t.Fatalf("证据编号序号错误: %q", got)
	}
	// 已有编号不覆盖（LLM 自报与引擎挂号同规则时天然一致）。
	r2, err := ParseReport(`{"agent_name":"a","conclusion":"c","confidence":"low","evidence":[{"tool":"t","interpretation":"i","id":"custom#E9"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := r2.Evidence[0].ID; got != "custom#E9" {
		t.Fatalf("已有编号被覆盖: %q", got)
	}
}

func TestAssignEvidenceIDsEmptyAgent(t *testing.T) {
	r := &Report{Evidence: []Evidence{{Tool: "t", Interpretation: "i"}}}
	AssignEvidenceIDs(r)
	if got := r.Evidence[0].ID; got != "report#E1" {
		t.Fatalf("空 agent_name 兜底编号错误: %q", got)
	}
}

func TestCheckEvidenceRefs(t *testing.T) {
	r := &Report{
		Evidence: []Evidence{{Tool: "t", ID: "a#E1"}, {Tool: "t", ID: "b#E2"}},
		Verdicts: Verdicts{
			{Agent: "a", Stance: VerdictAdopt, EvidenceIDs: StringList{"a#E1", "ghost#E7", "ghost#E7", " "}},
			{Agent: "b", Stance: VerdictDispute},
		},
	}
	missing := CheckEvidenceRefs(r)
	if len(missing) != 1 || missing[0] != "ghost#E7" {
		t.Fatalf("缺失引用应去重且忽略空白: %v", missing)
	}
	if got := CheckEvidenceRefs(&Report{}); len(got) != 0 {
		t.Fatalf("无裁决应零缺失: %v", got)
	}
}

// Render 完整档：证据编号/裁决证据引用/严重度/受影响对象四类结构化面可见。
func TestRenderEvidenceCitationsAndSeverity(t *testing.T) {
	r := &Report{
		AgentName:    "workflow/fault-diagnosis",
		Conclusion:   "内存泄漏",
		Severity:     SeverityHigh,
		Impact:       "线上",
		ImpactScopes: StringList{"svc-a", "pod-b"},
		Evidence:     []Evidence{{Tool: "free", Interpretation: "可用内存骤降", ID: "specialists/memory-analysis#E1"}},
		Verdicts: Verdicts{{
			Agent: "specialists/memory-analysis", Stance: VerdictAdopt, Reason: "证据充分",
			EvidenceIDs: StringList{"specialists/memory-analysis#E1"},
		}},
		Confidence: ConfidenceMedium,
	}
	out := Render(r)
	for _, want := range []string{
		"（编号：specialists/memory-analysis#E1）",
		"- 严重度：high",
		"- 受影响对象：svc-a、pod-b",
		"（证据：specialists/memory-analysis#E1）",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("完整档缺 %q\n---\n%s", want, out)
		}
	}
}

// Render 摘要档：省证据明细/步骤/风险回退，保留结论/严重度/裁决 stance。
func TestRenderViewSummary(t *testing.T) {
	r := &Report{
		AgentName:  "workflow/fault-diagnosis",
		Conclusion: "内存泄漏",
		Severity:   SeverityHigh,
		Risk:       "重启抖动",
		Rollback:   "回滚配置",
		Recommendation: &Rec{Action: ActionKillAndRestart, Steps: []string{"systemctl restart a"},
			RequiresApproval: true},
		Evidence:   []Evidence{{Tool: "free", Interpretation: "i", ID: "a#E1"}, {Tool: "ps", Interpretation: "i2", ID: "a#E2"}},
		Verdicts:   Verdicts{{Agent: "a", Stance: VerdictAdopt, Reason: "r", EvidenceIDs: StringList{"a#E1"}}},
		Confidence: ConfidenceMedium,
	}
	out := RenderView(r, ViewSummary)
	for _, banned := range []string{"关键行", "风险与回退", "1. systemctl restart", "（证据：", "（r）"} {
		if strings.Contains(out, banned) {
			t.Fatalf("摘要档不应含 %q\n---\n%s", banned, out)
		}
	}
	for _, want := range []string{"**证据要点**：2 条", "- 严重度：high", "- [adopt] a", "动作：kill_and_restart"} {
		if !strings.Contains(out, want) {
			t.Fatalf("摘要档缺 %q\n---\n%s", want, out)
		}
	}
	// 完整档签名零漂移：同报告 Render 输出含步骤与风险段。
	full := Render(r)
	if !strings.Contains(full, "1. systemctl restart") || !strings.Contains(full, "风险与回退") {
		t.Fatalf("完整档行为漂移\n---\n%s", full)
	}
}

// severity 白名单 warn 级：未知值放行留痕，不拦整份报告。
func TestValidateSeverityWarn(t *testing.T) {
	r := &Report{AgentName: "a", Conclusion: "c", Confidence: ConfidenceHigh, Severity: "重大"}
	warns, verr := r.Validate()
	if verr != nil {
		t.Fatalf("severity 未知值应为 warn 非 critical: %v", verr)
	}
	if len(warns) == 0 {
		t.Fatalf("severity 未知值应有 warn 留痕")
	}
	r.Severity = SeverityCritical
	if _, verr = r.Validate(); verr != nil {
		t.Fatalf("合法 severity 不得报错: %v", verr)
	}
}

func TestMaxSeverity(t *testing.T) {
	if MaxSeverity("", SeverityLow) != SeverityLow {
		t.Fatal("空=未评估不拉低")
	}
	if MaxSeverity(SeverityMedium, SeverityCritical) != SeverityCritical {
		t.Fatal("取重者")
	}
	if MaxSeverity(SeverityHigh, SeverityMedium) != SeverityHigh {
		t.Fatal("取重者（反序）")
	}
	if MaxSeverity("乱写", SeverityInfo) != SeverityInfo {
		t.Fatal("未知值视为未评估")
	}
}
