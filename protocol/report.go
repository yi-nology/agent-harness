package protocol

// 统一报告协议（PHASE1_PLAN §4.1）——类型即协议。
// Validate 的白名单语义与被替代的 kyaiops-filter.js 完全对齐：
// 执行类动作缺 requires_approval → 拦截；snippet 超长 → warn 放行（证据保真）；
// snippet 质量启发式判据与 check_consistency.py F 节同源。
// 本包只依赖标准库，保持引擎内零外部耦合。
//
// 按职责拆分：report_types.go 类型契约 · report_repair.go 宽容解析
// report_validate.go 白名单 · report_render.go Markdown 渲染。

import (
	"github.com/oekit/pkg/errors"
)

// SnippetMax evidence.snippet 长度上限；超长仅 warn 不截断（证据保真）。
const SnippetMax = 200

// recommendation.action 白名单（粗粒度枚举，hook/校验用）。
const (
	ActionKillAndRestart = "kill_and_restart"
	ActionTune           = "tune"
	ActionNone           = "none"
)

// 决策与置信枚举。
const (
	DecisionPending  = "pending_approval"
	DecisionApproved = "approved"
	DecisionRejected = "rejected"

	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// 严重度枚举（批次一百三十七，终报结构化补缺）：info|low|medium|high|critical。
// 与 confidence 相互独立——confidence 说「结论可信到什么程度」，severity 说「问题本身
// 有多重」。专家可不填（空=未评估，渲染与聚合按缺席处理）；聚合面取子报告最高值
// （只升不降），dispute 钳制与 confidence 同钳 ≤medium。
const (
	SeverityInfo     = "info"
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

var severityAll = map[string]bool{
	SeverityInfo: true, SeverityLow: true, SeverityMedium: true,
	SeverityHigh: true, SeverityCritical: true,
}

// SeverityRank 严重度序（越大越重；空=0 未评估）。聚合取 max、dispute 钳制都用它。
var SeverityRank = map[string]int{
	SeverityInfo: 1, SeverityLow: 2, SeverityMedium: 3, SeverityHigh: 4, SeverityCritical: 5,
}

// IsSeverityValid 严重度白名单判定（空合法=未评估）。
func IsSeverityValid(s string) bool { return s == "" || severityAll[s] }

// MaxSeverity 取两者较重者（未知值视为未评估，不参与比较）。
func MaxSeverity(a, b string) string {
	if SeverityRank[b] > SeverityRank[a] {
		return b
	}
	return a
}

var (
	// ErrNotReport 输出不是统一报告 JSON（调用方降级读 markdown 主体，不阻塞）。
	ErrNotReport = errors.New("protocol: 输出不是统一报告 JSON")
	// ErrViolation schema 白名单违规（必须拦截，违规项经 errors.Join 附带）。
	ErrViolation = errors.New("protocol: 报告未通过 schema 白名单校验")
)
