package protocol

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
)

// 报告 → Markdown 渲染（控制台/最终报告同一出口）。

// stepOrdinalRe 模型自带的步骤编号/列表前缀（"1. "、"2、"、"（3）"、"- "）——模型不总守
// 协议，Render 侧统一补号，不剥离会叠加成 "1. 1. xxx" 双编号。条件剥离：无前缀的步骤
// 原样保留，既有输出零漂移。
var stepOrdinalRe = regexp.MustCompile(`^(?:\d{1,3}[.、)）]|[（(]\d{1,3}[)）]|[-*+•])\s*`)

func stripStepOrdinal(s string) string {
	return strings.TrimSpace(stepOrdinalRe.ReplaceAllString(strings.TrimSpace(s), ""))
}

// ReportView 渲染受众档位（批次一百三十七）：full=完整档（历史行为）；summary=摘要档
// （管理层/通知面——留结论/影响/方案动作/裁决 stance，省证据明细/步骤/回退）。
// 渲染层不做受众推断，调用方（API/通知）显式选档。
type ReportView string

const (
	ViewFull    ReportView = "full"
	ViewSummary ReportView = "summary"
)

// Render 用户可见 Markdown（完整档；历史唯一出口，签名不变零漂移）。
// 大纲：标题→现象→结论→证据要点→影响范围→方案与下一步→风险与回退→未决项与置信度
// →合议裁决→小结。
func Render(r *Report) string { return RenderView(r, ViewFull) }

// RenderOptions 渲染可配置项：宿主注入其品牌/文案面，内核不内置任何宿主文案。
//
// 设计纪律：报告标题这类**展示文案属于宿主**——内核硬编码兜底标题等于把某个宿主
// 的品牌写进内核，换宿主就是一处需要改内核的债。故留空即用中性兜底。
type RenderOptions struct {
	// DefaultTitle AgentName 为空时的标题兜底。留空 = 用中性兜底「诊断报告」。
	DefaultTitle string
}

// 诊断大纲的中性兜底标题（宿主可用 RenderOptions.DefaultTitle 覆盖）。
const defaultReportTitle = "诊断报告"

// WithTitle 返回带指定兜底标题的副本（便捷构造）。
func (o RenderOptions) WithTitle(title string) RenderOptions {
	o.DefaultTitle = title
	return o
}

func (o RenderOptions) fallbackTitle() string {
	if o.DefaultTitle != "" {
		return o.DefaultTitle
	}
	return defaultReportTitle
}

// RenderView 按受众档位渲染（未注入宿主配置项）。full 档在历史大纲上增量补四类结构化
// 面：证据编号（可回溯）、裁决证据引用、severity、impact_scopes——缺省字段不渲染，
// 存量报告逐字节兼容。形态档分支：answer=通用轨直答大纲，其余（含零值）一律诊断大纲。
//
// 签名与语义保持不变：宿主既有调用零改动。需要宿主文案的走 RenderViewWith。
func RenderView(r *Report, view ReportView) string {
	return RenderViewWith(r, view, RenderOptions{})
}

// RenderViewWith 按受众档位渲染并注入宿主配置项。
func RenderViewWith(r *Report, view ReportView, opt RenderOptions) string {
	if r.Profile == ProfileAnswer {
		return renderAnswerView(r, view)
	}
	summary := view == ViewSummary
	var b strings.Builder
	title := r.AgentName
	if title == "" {
		title = opt.fallbackTitle()
	}
	fmt.Fprintf(&b, "## %s\n\n", title)
	if r.Symptom != "" {
		fmt.Fprintf(&b, "**现象**：%s\n\n", r.Symptom)
	}

	fmt.Fprintf(&b, conclusionLine(r), r.Conclusion)

	writeEvidenceBlock(&b, r, summary)

	if r.Impact != "" {
		fmt.Fprintf(&b, "**影响范围**：%s\n\n", r.Impact)
	}
	if len(r.ImpactScopes) > 0 {
		fmt.Fprintf(&b, "- 受影响对象：%s\n\n", strings.Join(r.ImpactScopes.Std(), "、"))
	}

	if rec := r.Recommendation; rec != nil && rec.Action != ActionNone {
		fmt.Fprintf(&b, "**方案与下一步**（动作：%s）\n\n", rec.Action)
		if !summary {
			for i, step := range rec.Steps {
				fmt.Fprintf(&b, "%d. %s\n", i+1, stripStepOrdinal(step))
			}
			b.WriteString("\n")
		}
		switch r.Decision {
		case DecisionPending:
			b.WriteString("⏸ 该方案待审批确认后执行。\n\n")
		case DecisionApproved:
			b.WriteString("✅ 该方案已审批通过并进入执行。\n\n")
		case DecisionRejected:
			b.WriteString("❌ 该方案已驳回，需修订后重提。\n\n")
		}
	}

	if !summary && (r.Risk != "" || r.Rollback != "") {
		b.WriteString("**风险与回退**：\n\n")
		if r.Risk != "" {
			fmt.Fprintf(&b, "- 风险：%s\n", r.Risk)
		}
		if r.Rollback != "" {
			fmt.Fprintf(&b, "- 回退：%s\n", r.Rollback)
		}
		b.WriteString("\n")
	}

	b.WriteString("**未决项与置信度**：\n\n")
	if len(r.NeedsFollowup) > 0 {
		if summary {
			fmt.Fprintf(&b, "- 待追加域：%d 项\n", len(r.NeedsFollowup))
		} else {
			fmt.Fprintf(&b, "- 待追加域：%s\n", strings.Join(r.NeedsFollowup.Std(), "、"))
		}
	}
	if r.Severity != "" {
		fmt.Fprintf(&b, "- 严重度：%s\n", r.Severity)
	}
	fmt.Fprintf(&b, "- 置信度：%s\n", r.Confidence)
	if len(r.Verdicts) > 0 {
		b.WriteString("**合议裁决**：\n\n")
		for _, v := range r.Verdicts {
			fmt.Fprintf(&b, "- [%s] %s", v.Stance, v.Agent)
			if !summary {
				if v.Reason != "" {
					fmt.Fprintf(&b, "（%s）", v.Reason)
				}
				if len(v.EvidenceIDs) > 0 {
					fmt.Fprintf(&b, "（证据：%s）", strings.Join(v.EvidenceIDs.Std(), "、"))
				}
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if r.Summary != "" {
		fmt.Fprintf(&b, "- 小结：%s\n", r.Summary)
	}
	return b.String()
}

// writeEvidenceBlock 证据要点段（诊断/直答两形态共用；summary 档只报条数）。标题字面
// 两形态一致——前端证据折叠（splitEvidenceSection）按「证据要点」标题匹配，改字面
// 即破坏折叠，勿单边改。
func writeEvidenceBlock(b *strings.Builder, r *Report, summary bool) {
	if len(r.Evidence) == 0 {
		return
	}
	if summary {
		fmt.Fprintf(b, "**证据要点**：%d 条（完整档含明细）\n\n", len(r.Evidence))
		return
	}
	b.WriteString("**证据要点**：\n\n")
	for _, ev := range r.Evidence {
		fmt.Fprintf(b, "- [%s] %s", ev.Tool, ev.Interpretation)
		if ev.Snippet != "" {
			fmt.Fprintf(b, "（关键行：%s）", flattenSnippet(ev.Snippet))
		}
		if ev.ID != "" {
			fmt.Fprintf(b, "（编号：%s）", ev.ID)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// renderAnswerView 直答大纲（批次二百一十七）：通用轨回答不是诊断——结论先行，不渲染
// 诊断专属面（现象占位/影响范围/风险回退/合议/severity），尾注只列非空项（不硬撑
// 「未决项与置信度」空壳段）。通用顾问 prompt 钉死 action=none，诊断档按动作门渲染
// steps 的规矩在此不适用：有 steps 就按「建议与下一步」出。needs_followup 在评估口径
// 里承接的是拍板点清单（assessSynthesisInstruction ④），正名「拍板点」。交接升格
// （generalistHandoff）产物不带 answer 档、走诊断大纲——换轨即换格式，审批语义段
// 由诊断档承接，故此处不渲染 decision 标记。
// conclusionLine 结论行格式（批次三百三十四）：待用户确认的反问轮「回答」不是诊断
// 结论——AwaitingUser 持位走「待确认」标签（待确认态专属呈现），其余保持「结论」体。
func conclusionLine(r *Report) string {
	if r.AwaitingUser {
		return "**待确认**：%s\n\n"
	}
	return "**结论**：%s\n\n"
}

// 引导声位判定：宿主声明「这条是对话直答，不是诊断报告」，渲染走轻装视图
// （不出 agent 标题、置信度与内部小结——小结留在 JSON 供机器消费，markdown 只面向用户）。
//
// 两个声明通道，优先显式置位：
//  1. 结构化：Report.Guidance = true（推荐——不依赖任何命名约定）；
//  2. 命名注册：宿主调 SetGuidanceAgentNames(...) 注册自己的引导声位名
//     （迁移面：宿主既有代码按名字判定时用这条，登记一次即可）。
//
// 设计纪律：本包**不认识任何具体 agent 名**。早期版本硬编码了一个中文哨兵名，
// 那等于把宿主的命名约定焊进内核——宿主一改名就静默失效，且换个宿主就直接不适用。
// 判定语义属于内核，判定依据属于宿主。
func (r *Report) isGuidance() bool {
	if r.Guidance {
		return true
	}
	names := guidanceNames.Load()
	if names == nil {
		return false
	}
	_, ok := (*names)[r.AgentName]
	return ok
}

// guidanceNames 宿主注册的引导声位名集合（并发安全，热更生效）。
var guidanceNames atomic.Pointer[map[string]struct{}]

// SetGuidanceAgentNames 注册/清空引导声位名集合（传空/不传即清空）。
// 宿主在装配期调用一次即可；重复调用覆盖而非累加。
func SetGuidanceAgentNames(names ...string) {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		if n != "" {
			m[n] = struct{}{}
		}
	}
	guidanceNames.Store(&m)
}

// GuidanceAgentRegistered 报告该 agent 名是否已被宿主登记为引导声位（诊断用）。
func GuidanceAgentRegistered(agentName string) bool {
	names := guidanceNames.Load()
	if names == nil {
		return false
	}
	_, ok := (*names)[agentName]
	return ok
}

func renderAnswerView(r *Report, view ReportView) string {
	summary := view == ViewSummary
	guidance := r.isGuidance()
	var b strings.Builder
	if !guidance {
		title := r.AgentName
		if title == "" {
			title = "回答报告"
		}
		fmt.Fprintf(&b, "## %s\n\n", title)
	}
	fmt.Fprintf(&b, conclusionLine(r), r.Conclusion)

	writeEvidenceBlock(&b, r, summary)

	if rec := r.Recommendation; rec != nil && len(rec.Steps) > 0 {
		if summary {
			fmt.Fprintf(&b, "**建议与下一步**：%d 步（完整档含明细）\n\n", len(rec.Steps))
		} else {
			b.WriteString("**建议与下一步**：\n\n")
			for i, step := range rec.Steps {
				fmt.Fprintf(&b, "%d. %s\n", i+1, stripStepOrdinal(step))
			}
			b.WriteString("\n")
		}
	}

	if len(r.NeedsFollowup) > 0 {
		b.WriteString("**拍板点**：\n\n")
		for _, f := range r.NeedsFollowup {
			fmt.Fprintf(&b, "- %s\n", f)
		}
		b.WriteString("\n")
	}

	if r.Confidence != "" && !guidance {
		fmt.Fprintf(&b, "**置信度**：%s\n\n", r.Confidence)
	}
	if r.Summary != "" && !guidance {
		fmt.Fprintf(&b, "**小结**：%s\n", r.Summary)
	}
	return b.String()
}
