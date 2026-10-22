package protocol

import (
	"github.com/oekit/pkg/errors"
)

// 判定面契约：判定**来源**词表 + 置信**语义种类** + 判定结果信封 + 升级/回落原因词表。
//
// # 为什么属本包
//
// 判定语义一旦各宿主各判一套，看板与门禁就分叉（与本仓 toolfail 的失败分类、
// sandbox 的隔离档位同因：**口径分叉 = 治理失效**）。它与「谁在用它」无关，
// 是机制面，故下沉本包。
//
// # 三条纪律
//
//  1. **来源与置信语义分列**：Source 说「谁做的判定」，Kind 说「这个置信怎么来的」——
//     两者正交。混用会让「模型自评 0.9」与「分布集中度 0.9」被当成同一种东西，
//     而它们的可比性完全不同（见 Comparable）。
//  2. **跨 kind 的置信不可比**：判定面（分布集中度）可跨模型比较；生成面（自评）
//     不可跨模型比较；序数档（high|medium|low）是第三类；规则命中压根不是概率。
//     Verdict.Comparable 是这条纪律的**判定面**，别在别处再写一份。
//  3. **枚举只增不改**：本包的枚举一旦发布就是契约面（宿主按它分流），新增值只
//     追加、不改语义、不删。未知值按 unknown 处理（fail-soft 展示），
//     但**校验面 fail-closed**（ValidateVerdict 拒未知来源/种类）。
//
// # 与「报告置信」的关系
//
// 报告协议里的 Confidence（high|medium|low）是**序数档**，对应
// ConfidenceKindOrdinal；它与判定面的数值置信（0-1 的分布集中度）不是同一个量，
// 不可互换。本包两者都留，各自带 kind 标注。

// 判定来源词表。
const (
	// DecisionSourceRule 确定性规则（词面/规则面）。
	DecisionSourceRule = "rule"
	// DecisionSourceLLM 生成面（自回归模型）。
	DecisionSourceLLM = "llm"
	// DecisionSourceClassifier 判定面（非自回归判定模型：一次前向 pass 出有限标签
	// + 概率分布 + 校准置信度）。
	//
	// 刻意**不叫 decision**：本仓的 decision 一词已占用于「处置/审批决策」，
	// 同一仓里一个词两义是最贵的漂移源。
	DecisionSourceClassifier = "classifier"
	// DecisionSourceClarify 反问衔接（由澄清产生）。
	DecisionSourceClarify = "clarify"
	// DecisionSourceFallback 兜底（未命中任何判定面时的默认路径）。
	DecisionSourceFallback = "fallback"
	// DecisionSourceManual 人工判定。
	DecisionSourceManual = "manual"
	// DecisionSourceUnknown 来源不可知（解析失败/上游未标注）。
	DecisionSourceUnknown = "unknown"
)

// 置信语义种类词表（**跨种类不可比**）。
const (
	// ConfidenceKindOrdinal 序数档（high|medium|low）：档位语义，非概率。
	ConfidenceKindOrdinal = "ordinal"
	// ConfidenceKindCalibrated 分布集中度（0-1）：由概率分布算出，**可跨模型比较**
	// （同一口径下才有意义——口径随候选集规模变化，跨候选集仍不可移植）。
	ConfidenceKindCalibrated = "calibrated_distribution"
	// ConfidenceKindSelfReported 模型自评（0-1）：**不可跨模型比较**。
	ConfidenceKindSelfReported = "self_reported"
	// ConfidenceKindRuleHit 规则命中：命中即确定，**不是概率**。
	ConfidenceKindRuleHit = "rule_hit"
)

// 升级/回落原因词表（判定面 → 生成面 / 人工 的移交原因）。
const (
	// EscalationLowConfidence 置信未达门槛。
	EscalationLowConfidence = "low_confidence"
	// EscalationError 判定面调用报错。
	EscalationError = "error"
	// EscalationTimeout 判定面调用超时。
	EscalationTimeout = "timeout"
	// EscalationUnavailable 判定面不可用（未配置/端点不可达）。
	EscalationUnavailable = "unavailable"
	// EscalationUnknown 原因不可知。
	EscalationUnknown = "unknown"
)

// decisionSources 来源词表集合（校验面用）。
var decisionSources = map[string]bool{
	DecisionSourceRule:       true,
	DecisionSourceLLM:        true,
	DecisionSourceClassifier: true,
	DecisionSourceClarify:    true,
	DecisionSourceFallback:   true,
	DecisionSourceManual:     true,
	DecisionSourceUnknown:    true,
}

// confidenceKinds 置信语义种类集合（校验面用）。
var confidenceKinds = map[string]bool{
	ConfidenceKindOrdinal:      true,
	ConfidenceKindCalibrated:   true,
	ConfidenceKindSelfReported: true,
	ConfidenceKindRuleHit:      true,
}

// escalationReasons 升级/回落原因集合（校验面用）。
var escalationReasons = map[string]bool{
	EscalationLowConfidence: true,
	EscalationError:         true,
	EscalationTimeout:       true,
	EscalationUnavailable:   true,
	EscalationUnknown:       true,
}

// IsDecisionSourceValid 来源是否在词表内。
func IsDecisionSourceValid(s string) bool { return decisionSources[s] }

// IsConfidenceKindValid 置信语义种类是否在词表内。
func IsConfidenceKindValid(k string) bool { return confidenceKinds[k] }

// IsEscalationReasonValid 升级/回落原因是否在词表内。
func IsEscalationReasonValid(r string) bool { return escalationReasons[r] }

// Verdict 判定结果信封：来源 + 置信语义 + 判定值。
//
// **置信字段按 kind 二选一**（别都填、也别都不填）：
//
//	kind=calibrated_distribution → Confidence（0-1）有意义，Ordinal 不填
//	kind=ordinal                → Ordinal（high|medium|low）有意义，Confidence 不填
//	kind=self_reported          → Confidence（0-1）有意义但**不可跨模型比较**
//	kind=rule_hit               → 两者都不填（命中即确定，不是概率）
//
// Distribution 是判定面的概率分布（候选 → 概率）：它让「为什么是这个结论」可复算，
// 也是跨来源核对的唯一共同底座（两边的分布可比，自评不可比）。
type Verdict struct {
	// Source 判定来源（DecisionSource 词表）。
	Source string `json:"source"`
	// Kind 置信语义种类（ConfidenceKind 词表）。
	Kind string `json:"confidence_kind"`
	// Value 判定结果（标签；语义由来源决定）。
	Value string `json:"value"`
	// Confidence 数值置信（0-1）。仅 kind=calibrated_distribution / self_reported 有意义。
	Confidence float64 `json:"confidence,omitempty"`
	// Ordinal 序数置信档（high|medium|low）。仅 kind=ordinal 有意义。
	Ordinal string `json:"confidence_ordinal,omitempty"`
	// Distribution 概率分布（候选 → 概率）；判定面给，生成面可空。
	Distribution map[string]float64 `json:"distribution,omitempty"`
	// Reason 一句话依据（可空）。
	Reason string `json:"reason,omitempty"`
}

// ValidateVerdict 校验判定信封（fail-closed）：来源/种类必须在词表内；置信字段
// 必须**与 kind 匹配**（数值置信 ∈ [0,1]；序数档 ∈ high|medium|low）；
// kind=rule_hit 时两个置信字段都不得填（规则命中不是概率）。
//
// 未知来源/种类一律拒（不是落 unknown）——信封是契约面，校验面必须闭合。
func ValidateVerdict(v Verdict) error {
	if !IsDecisionSourceValid(v.Source) {
		return errors.New("protocol: 判定来源非法: " + v.Source)
	}
	if !IsConfidenceKindValid(v.Kind) {
		return errors.New("protocol: 置信语义种类非法: " + v.Kind)
	}
	if v.Value == "" {
		return errors.New("protocol: 判定值为空")
	}
	switch v.Kind {
	case ConfidenceKindOrdinal:
		if v.Ordinal == "" {
			return errors.New("protocol: 序数档缺置信档")
		}
		if !IsConfidenceValid(v.Ordinal) {
			return errors.New("protocol: 序数置信档非法: " + v.Ordinal)
		}
		if v.Confidence != 0 {
			return errors.New("protocol: 序数档不应带数值置信")
		}
	case ConfidenceKindCalibrated, ConfidenceKindSelfReported:
		// 数值置信允许为 0（完全不确定）⇒ 只校验范围，不强制非零。
		if v.Confidence < 0 || v.Confidence > 1 {
			return errors.New("protocol: 数值置信越界")
		}
		if v.Ordinal != "" {
			return errors.New("protocol: 数值置信不应带序数档")
		}
	case ConfidenceKindRuleHit:
		if v.Confidence != 0 || v.Ordinal != "" {
			return errors.New("protocol: 规则命中不应带置信值")
		}
	}
	return nil
}

// Comparable 两个判定的置信**是否可比**。
//
// 规则：同 kind，且该 kind 的语义允许跨来源比较——
//   - ordinal：同一套档位词表 ⇒ 可比；
//   - calibrated_distribution：同一口径下可比（**跨候选集仍不可移植**：口径随
//     候选集规模变化）；
//   - self_reported：模型自评 ⇒ **不可比**（不同模型的 0.9 不是一回事）；
//   - rule_hit：命中即确定 ⇒ 不比数值。
//
// 这条判定面是「置信不可跨 kind 混用」纪律的唯一实现点——别在别处再写一份。
func (v Verdict) Comparable(o Verdict) bool {
	if v.Kind != o.Kind {
		return false
	}
	switch v.Kind {
	case ConfidenceKindOrdinal, ConfidenceKindCalibrated:
		return true
	default:
		return false
	}
}

// DecisionEventPayload 判定面事件载荷（decision_answered / decision_escalated 共用）。
//
// 载荷是**扁平**的：宿主落库与看板聚合都按这几个键取，不解析嵌套信封（嵌套的
// 键名各宿主各写各的必然漂移——扁平键是跨仓聚合的最低共同底座）。
//
// escalated=true 时带 reason（升级/回落原因，EscalationReason 词表）。
func DecisionEventPayload(v Verdict, escalated bool, reason string) map[string]any {
	payload := map[string]any{
		"source":          v.Source,
		"confidence_kind": v.Kind,
		"value":           v.Value,
	}
	if v.Confidence != 0 {
		payload["confidence"] = v.Confidence
	}
	if v.Ordinal != "" {
		payload["confidence_ordinal"] = v.Ordinal
	}
	if len(v.Distribution) > 0 {
		payload["distribution"] = v.Distribution
	}
	if v.Reason != "" {
		payload["reason"] = v.Reason
	}
	if escalated {
		payload["escalated"] = true
		if reason != "" {
			payload["escalation_reason"] = reason
		}
	}
	return payload
}
