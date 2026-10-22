package protocol

import "testing"

// 合成夹具：判定面契约的示例**不带任何领域词**（本仓规则：测试夹具用合成数据）。

// TestValidateVerdictAccepts 合法组合：四种置信语义各自该带什么。
func TestValidateVerdictAccepts(t *testing.T) {
	cases := []struct {
		name string
		v    Verdict
	}{
		{
			name: "判定面·分布集中度",
			v:    Verdict{Source: DecisionSourceClassifier, Kind: ConfidenceKindCalibrated, Value: "beta", Confidence: 0.62, Distribution: map[string]float64{"beta": 0.62, "alpha": 0.38}},
		},
		{
			name: "生成面·自评",
			v:    Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindSelfReported, Value: "alpha", Confidence: 0.9},
		},
		{
			name: "序数档",
			v:    Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindOrdinal, Value: "alpha", Ordinal: ConfidenceHigh},
		},
		{
			name: "规则命中",
			v:    Verdict{Source: DecisionSourceRule, Kind: ConfidenceKindRuleHit, Value: "alpha"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateVerdict(tc.v); err != nil {
				t.Fatalf("应通过校验: %v", err)
			}
		})
	}
}

// TestValidateVerdictRejects 校验面 fail-closed：来源/种类未知、值空、
// 置信字段与 kind 不匹配、置信越界，一律拒。
func TestValidateVerdictRejects(t *testing.T) {
	cases := []struct {
		name string
		v    Verdict
	}{
		{"来源未知", Verdict{Source: "mystery", Kind: ConfidenceKindCalibrated, Value: "x", Confidence: 0.5}},
		{"种类未知", Verdict{Source: DecisionSourceLLM, Kind: "vibes", Value: "x"}},
		{"值为空", Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindCalibrated, Confidence: 0.5}},
		{"序数档缺档位", Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindOrdinal, Value: "x"}},
		{"序数档档位非法", Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindOrdinal, Value: "x", Ordinal: "very-high"}},
		{"序数档带数值置信", Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindOrdinal, Value: "x", Ordinal: ConfidenceHigh, Confidence: 0.9}},
		{"数值置信带序数档", Verdict{Source: DecisionSourceClassifier, Kind: ConfidenceKindCalibrated, Value: "x", Confidence: 0.5, Ordinal: ConfidenceLow}},
		{"数值置信越界", Verdict{Source: DecisionSourceClassifier, Kind: ConfidenceKindCalibrated, Value: "x", Confidence: 1.4}},
		{"规则命中带置信", Verdict{Source: DecisionSourceRule, Kind: ConfidenceKindRuleHit, Value: "x", Confidence: 0.9}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateVerdict(tc.v); err == nil {
				t.Fatal("应拒绝")
			}
		})
	}
}

// TestVerdictComparable 跨 kind 的置信**不可比**（纪律的唯一判定面）：
// 序数档与分布集中度各自可比；自评与规则命中不可比；不同 kind 一律不可比。
func TestVerdictComparable(t *testing.T) {
	calA := Verdict{Source: DecisionSourceClassifier, Kind: ConfidenceKindCalibrated, Value: "a", Confidence: 0.7}
	calB := Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindCalibrated, Value: "a", Confidence: 0.7}
	ordA := Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindOrdinal, Value: "a", Ordinal: ConfidenceHigh}
	ordB := Verdict{Source: DecisionSourceRule, Kind: ConfidenceKindOrdinal, Value: "a", Ordinal: ConfidenceHigh}
	selfA := Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindSelfReported, Value: "a", Confidence: 0.9}
	selfB := Verdict{Source: DecisionSourceLLM, Kind: ConfidenceKindSelfReported, Value: "a", Confidence: 0.9}
	ruleA := Verdict{Source: DecisionSourceRule, Kind: ConfidenceKindRuleHit, Value: "a"}

	if !calA.Comparable(calB) {
		t.Error("同为分布集中度应可比")
	}
	if !ordA.Comparable(ordB) {
		t.Error("同为序数档应可比")
	}
	if selfA.Comparable(selfB) {
		t.Error("自评不可比（不同模型的 0.9 不是一回事）")
	}
	if ruleA.Comparable(ruleA) {
		t.Error("规则命中不比数值")
	}
	if calA.Comparable(ordA) {
		t.Error("不同 kind 不可比")
	}
}

// TestDecisionEventPayload 事件载荷是**扁平键**（跨宿主聚合的共同底座）。
func TestDecisionEventPayload(t *testing.T) {
	v := Verdict{
		Source:       DecisionSourceClassifier,
		Kind:         ConfidenceKindCalibrated,
		Value:        "beta",
		Confidence:   0.62,
		Distribution: map[string]float64{"beta": 0.62},
		Reason:       "分布集中",
	}
	p := DecisionEventPayload(v, false, "")
	if p["source"] != DecisionSourceClassifier {
		t.Errorf("source = %#v", p["source"])
	}
	if p["confidence_kind"] != ConfidenceKindCalibrated {
		t.Errorf("confidence_kind = %#v", p["confidence_kind"])
	}
	if p["value"] != "beta" {
		t.Errorf("value = %#v", p["value"])
	}
	if p["confidence"] != 0.62 {
		t.Errorf("confidence = %#v", p["confidence"])
	}
	if _, ok := p["escalated"]; ok {
		t.Error("未升级时不应带 escalated 键")
	}
	if _, ok := p["confidence_ordinal"]; ok {
		t.Error("数值置信不应带序数档键")
	}
	// 升级载荷带原因（原因词表）。
	p2 := DecisionEventPayload(v, true, EscalationLowConfidence)
	if p2["escalated"] != true {
		t.Errorf("升级载荷应带 escalated=true: %#v", p2)
	}
	if p2["escalation_reason"] != EscalationLowConfidence {
		t.Errorf("escalation_reason = %#v", p2["escalation_reason"])
	}
	if !IsEscalationReasonValid(EscalationLowConfidence) {
		t.Error("升级原因词表应含 low_confidence")
	}
	if IsEscalationReasonValid("whatever") {
		t.Error("未知升级原因不应在词表内")
	}
}

// TestDecisionSourceVocabulary 来源词表闭合（新增只追加、不改语义）。
func TestDecisionSourceVocabulary(t *testing.T) {
	for _, s := range []string{
		DecisionSourceRule, DecisionSourceLLM, DecisionSourceClassifier,
		DecisionSourceClarify, DecisionSourceFallback, DecisionSourceManual,
		DecisionSourceUnknown,
	} {
		if !IsDecisionSourceValid(s) {
			t.Errorf("来源 %q 应在词表内", s)
		}
	}
	if IsDecisionSourceValid("decision") {
		t.Error("「decision」在本仓已占用于处置/审批决策，不该出现在来源词表")
	}
}
