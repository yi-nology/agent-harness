// 报告契约的容错用例：枚举同义词归一、合议宽容解码、渲染标签剥离。

package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

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
