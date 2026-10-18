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

// 编目声明风险档的合法区间（curated_risk）。此前只写在字段注释里而不校验，
// 99 或 -1 会原样放行，宿主拿它当「引擎基线下限」时得不到任何信号。
// 0 不参与判定：字段带 omitempty，0 与「未声明」不可区分。
const (
	RiskTierMin = 1
	RiskTierMax = 4
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

// severityRank 严重度序（越大越重；空/未知=0 未评估）。聚合取 max、dispute 钳制都用它。
//
// # 为什么不做成导出的 map
//
// 导出的 map 可被任何导入方就地改写，而 `protocol.SeverityRank["critical"] = 0`
// 会让 MaxSeverity 把最高危静默降级成「未评估」——**无编译错误、无运行错误、
// 无日志**，只在若干天后表现为「报告的严重度排序不对」，极难回溯到这一行。
//
// 这不是假想的风险：netguard/policy.go 对拦截名单守住了同一条（"默认名单刻意
// 不做成可写变量"），而本包此前是**全仓唯一的例外**——一次全仓扫描显示，除哨兵
// error 外，本仓导出变量中只有 SeverityRank 一个是可变容器。
//
// 读取一律走 SeverityRankOf；需要遍历的内部代码直接读本表。
var severityRank = map[string]int{
	SeverityInfo: 1, SeverityLow: 2, SeverityMedium: 3, SeverityHigh: 4, SeverityCritical: 5,
}

// SeverityRankOf 严重度序取值。未知值与空串一律返回 0（未评估）。
func SeverityRankOf(s string) int { return severityRank[s] }

// IsSeverityValid 严重度白名单判定（空合法=未评估）。
func IsSeverityValid(s string) bool { return s == "" || severityAll[s] }

// MaxSeverity 取两者较重者（未知值视为未评估，不参与比较）。
func MaxSeverity(a, b string) string {
	if SeverityRankOf(b) > SeverityRankOf(a) {
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
