package protocol

// LLM 输出宽容解析与结构修复。格式修复半边（栅栏剥离 → 散文抽 JSON → 单遍
// 字符串感知语法修复 → 标量归一骨架）自 ekit v0.34.0 起单源在 ekit/pkg/jsonrepair
// （agentkit 同名实现一并下沉）；本文件只持报告域的两个面：StringKeys/ListKeys
// 键集合，与 decision/scope/charts/布尔/证据语义归一 + recommendation 矛盾修复
// （经 Schema.OnMap 域内钩子接线，children 先归一、父层后裁决）。

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/oekit/pkg/jsonrepair"
)

// snippet 质量启发式：任一命中即视为含具体数据点（模式集与 kyaiops-filter.js / check_consistency.py 同源）。
var snippetQuality = []*regexp.Regexp{
	regexp.MustCompile(`\d`),                                // 数字（92%、503、pid 1234）
	regexp.MustCompile(`\d{4}[-/]`),                         // 时间戳前缀
	regexp.MustCompile(`\b[A-Z]{2,}\b`),                     // 全大写错误码（ENOMEM/OOM/SEGV）
	regexp.MustCompile(`(?i)error|err|fail|fault|oom|segv`), // 错误关键字
	regexp.MustCompile(`(?i)pid|proc|进程`),                   // 进程标识
	regexp.MustCompile(`[=<>≥≤]|@T\d|:\s*\d`),               // 结构化标记
}

// ParseReport 解析模型输出为报告：
//  1. trim；2) 若整体被 ```json 栅栏包裹则剥离；3) 快速判定形如报告；
//  4. 标量归一化（真实 LLM 会在字符串位输出 bool/数字——统一转文本）；5) JSON 反序列化。
//     任何一步失败返回 ErrNotReport——调用方按失败兜底降级读 markdown 主体，不阻塞聚合。
//
// maxReportInput 解析输入上限（实弹审计）：repair 通道对超大输入的多次整串处理是
// O(n·k)——2MB 已远超任何合法报告（max_output_tokens 上限约数百 KB），超限直接判非报告。
const maxReportInput = 2 << 20

// reportJSONSchema 报告解析的标量归一作用域（键集合为报告 schema 白名单面）。
var reportJSONSchema = &jsonrepair.Schema{
	StringKeys: stringKeys,
	ListKeys:   listKeys,
	OnMap:      normalizeReportMap,
}

func ParseReport(raw string) (*Report, error) {
	if len(raw) > maxReportInput {
		raw = raw[:maxReportInput]
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ErrNotReport
	}
	candidate := trimmed
	if m := fenceRe.FindStringSubmatch(trimmed); m != nil {
		candidate = strings.TrimSpace(m[1])
	}
	if !reportKeyRe.MatchString(candidate) {
		return nil, ErrNotReport
	}
	tryParse := func(candidate string) (Report, bool) {
		normalized, nerr := jsonrepair.Normalize(json.RawMessage(candidate), reportJSONSchema)
		if nerr == nil {
			candidate = normalized
		}
		var r Report
		if err := json.Unmarshal([]byte(candidate), &r); err != nil {
			return Report{}, false
		}
		return r, true
	}
	if r, ok := tryParse(candidate); ok {
		return withEvidenceIDs(&r), nil
	}
	// 修复通道：模型常见语法损坏（无效转义 \|、尾逗号、未闭合括号、全角结构标点）——
	// 修复后重试。修复产物整体降置信 low（markRepaired）：能救回结构但字段完整性已
	// 不可证（截断报告补括号后只剩 agent_name+confidence 的空壳曾以「解析成功」放行
	// ——实弹审计 Critical）。
	if repaired := jsonrepair.Repair(candidate); repaired != candidate {
		if r, ok := tryParse(repaired); ok {
			return markRepaired(withEvidenceIDs(&r)), nil
		}
	}
	// 提取通道：前缀散文/后缀散文包裹的 JSON 对象（"基于以上数据，输出统一报告：{...}供参考"）。
	if extracted := jsonrepair.ExtractObject(candidate); extracted != "" {
		if r, ok := tryParse(extracted); ok {
			return withEvidenceIDs(&r), nil
		}
		if r, ok := tryParse(jsonrepair.Repair(extracted)); ok {
			return markRepaired(withEvidenceIDs(&r)), nil
		}
	}
	return nil, ErrNotReport
}

// withEvidenceIDs 解析出口统一挂号（批次一百三十七）：三条成功路径（直解/修复/提取）
// 都过这里，证据编号自解析起即稳定，下游聚合/合并/合议引用不重排。
// 批次三百三十九起同点做脚手架净化（sanitizeScaffold）：落库/蒸馏/接力前数据面单点
// 收口 <system-reminder>/【历史对话】类 prompt 脚手架复述泄漏。
func withEvidenceIDs(r *Report) *Report {
	AssignEvidenceIDs(sanitizeScaffold(r))
	return r
}

// markRepaired 修复通道产物降级：置信钳 low（下游 verify/needs_review 语义承接）。
func markRepaired(r *Report) *Report {
	r.Confidence = ConfidenceLow
	return r
}

// stringKeys / listKeys：已知字符串键、字符串项列表键（标量容错归一化的作用范围）。
var (
	stringKeys = map[string]bool{
		"agent_name": true, "domain": true, "symptom": true, "conclusion": true, "impact": true,
		"request_type": true, "risk": true, "rollback": true, "summary": true,
		"redirect": true, "confidence": true, // scope/decision 有专属归一化分支
		"tool": true, "snippet": true, "interpretation": true, "action": true,
	}
	listKeys = map[string]bool{
		"steps": true, "rollback_steps": true, "needs_followup": true, "executed_steps": true,
	}
)

// normalizeReportMap Schema.OnMap 域内钩子：调用时该 map 的子节点已归一完毕
// （jsonrepair 递归序），域内键的语义收敛在此裁决。
func normalizeReportMap(m map[string]any) {
	for k, v := range m {
		switch k {
		case "decision":
			normalizeDecision(m, k, v)
		case "scope":
			normalizeScope(m, k, v)
		case "charts":
			normalizeCharts(m, k, v)
		case "requires_execution", "requires_approval":
			normalizeBoolField(m, k, v)
		case "curated_risk":
			normalizeRiskTier(m, k, v)
		case "evidence":
			normalizeEvidenceTools(m, k, v)
		}
	}
	normalizeNeedsFollowupScalar(m)
	fixRecommendationContradiction(m)
}

// normalizeNeedsFollowupScalar needs_followup 标量形态是布尔（无目标语义）→
// 空数组（jsonrepair ListKeys 通道产出 ["true"]/["false"] 单元素，在此收敛）。
func normalizeNeedsFollowupScalar(m map[string]any) {
	if arr, ok := m["needs_followup"].([]any); ok && len(arr) == 1 {
		if s, isStr := arr[0].(string); isStr && (s == "true" || s == "false") {
			m["needs_followup"] = []any{}
		}
	}
}

// normalizeDecision decision 归一：字符串 → 小写化；白名单（pending_approval/approved/
// rejected）保留；**其余任意值一律归空串（无决策）**。对象/数组形态（模型自造结构）→
// 删除键。2026-09-25 两轮 215 实弹（target_scope_mismatch_no_action/redirect）证明
// 逐词收录同义族是打地鼠——真模型持续自造诚实信号词（目标不匹配/转出/无判读对象…），
// 每个漏网词都让整份专项报告被拦、巡检聚合缺域。decision 本无治理语义（真正闸门在
// action+requires_approval，09-10 已裁定），未知值收敛为无决策零成本；Validate 的
// 白名单检查保留作直构报告后备防线。
func normalizeDecision(m map[string]any, k string, v any) {
	switch v.(type) {
	case map[string]any, []any:
		delete(m, k)
	default:
		if s, ok := jsonrepair.CoerceString(v); ok {
			sd := strings.ToLower(strings.TrimSpace(s))
			switch {
			case decisionAll[sd]:
				m[k] = sd
			case sd == "pending": // 常见缩写（实弹：模型输出 "pending"）→ 规范枚举
				m[k] = DecisionPending
			default:
				// 同义"无决策"词（hold/info/platform_mismatch…）与散文化表述、未知枚举形
				// ASCII 词同待遇：一律无决策（历史词族清单见 2026-09-09/10 实弹记录）。
				m[k] = ""
			}
			// 实弹 2026-09-10（macOS 只读检查）：模型把 pending_approval 当"待处理"用，
			// 而报告无审批语义（requires_approval 缺失）→ 撞 Validate 一致性红线，
			// 整份诚实的检查报告被拦。decision 本无治理语义（真正闸门在
			// action+requires_approval）：pending_approval 而无 requires_approval
			// 真值时归一为无决策，与自造枚举同待遇。Validate 的红线检查保留作
			// 直构报告的后备防线。
			if m[k] == DecisionPending && !rawRequiresApproval(m["recommendation"]) {
				m[k] = ""
			}
		}
	}
}

// normalizeScope scope 只认 in|out：枚举保留；其余任意形态（主机名/对象/数组）→ in
// （模型给出了检测对象）。
func normalizeScope(m map[string]any, k string, v any) {
	if s, ok := v.(string); ok && (s == "in" || s == "out") {
		m[k] = s
	} else {
		m[k] = "in"
	}
}

// normalizeCharts charts 非数组形态：单对象 → 包一层数组；字符串/数字等无结构数据 →
// 删除（图表可选，宁无勿炸——绝不能因附件形态让整份报告解析失败）。数组保持，元素级
// 漂移交给 ChartSpec.UnmarshalJSON。
func normalizeCharts(m map[string]any, k string, v any) {
	switch v.(type) {
	case []any:
	case map[string]any:
		m[k] = []any{v}
	default:
		delete(m, k)
	}
}

// normalizeBoolField 布尔字段：字符串 "true"/"false"、1/0 → bool（模型常见形态漂移）。
// normalizeBoolField 把字符串位收敛成真布尔，与 rawRequiresApproval 同一口径
// （EqualFold + TrimSpace）。
//
// 之前这里是精确匹配 "true"/"false"，于是 "TRUE" / "True" / " true " 三个常见
// 形态既没被归一化、又过不了严格 unmarshal 到 bool——**单个字符串的大小写就能
// 炸掉整份报告解析**，降级读 markdown 主体。同包的 rawRequiresApproval
// 早就用 EqualFold，两套口径不一致才是 bug 的根。
func normalizeBoolField(m map[string]any, k string, v any) {
	if s, ok := v.(string); ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			m[k] = true
		case "false":
			m[k] = false
		}
	} else if f, ok := v.(float64); ok {
		m[k] = f != 0
	}
}

// normalizeRiskTier 把 curated_risk 的字符串形态收敛成整数。
//
// CuratedRisk 是 Rec 里唯一没有容错解码的标量位：Rec 整体走严格 unmarshal，
// 而字符串位产出数字（"3"）时无任何归一化，直接把整份报告判成非报告——
// StringMap 的注释把这个失败模式列为最疼的一种，同包却漏了兄弟字段。
// 引擎基线语义（只升不降）不受影响：解析期只做类型收敛，不改数值。
func normalizeRiskTier(m map[string]any, k string, v any) {
	s, ok := v.(string)
	if !ok {
		return
	}
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		m[k] = n
	}
}

// normalizeEvidenceTools 模型常用 "type" 键承载工具名 → 回退映射到 tool（tool 优先）。
func normalizeEvidenceTools(m map[string]any, k string, v any) {
	if arr, ok := v.([]any); ok {
		for _, item := range arr {
			if em, ok := item.(map[string]any); ok {
				if _, hasTool := em["tool"]; !hasTool {
					if t, hasType := em["type"]; hasType {
						em["tool"] = t
					}
				}
			}
		}
	}
}

// fixRecommendationContradiction recommendation 特判：steps 非空而 action=none 是语义
// 矛盾——有可运行步骤就必须走审批（action 规整为 tune），杜绝"带步骤却免审"。同步
// requires_approval=true（实弹审计 Critical）：只改 action 不改审批位时，approval=false
// 的报告被 Validate 判「可执行但未要求审批」直接整份拒掉（"必拒"而非注释宣称的
// "走审批"），而 approval=true 的查看类步骤会被提审。
func fixRecommendationContradiction(sub map[string]any) {
	if _, hasSteps := sub["steps"]; hasSteps {
		if arr, isArr := sub["steps"].([]any); isArr && len(arr) > 0 {
			if a, hasAction := sub["action"]; !hasAction || a == "none" {
				sub["action"] = "tune"
				sub["requires_approval"] = true
			}
		}
	}
}
