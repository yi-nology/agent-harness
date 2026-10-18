// Package pending 反问/消歧挂起态的结构化载体。
//
// 多轮对话里「上一轮问了什么、这一轮的回答如何衔接」需要一个**唯一合法形态**的
// 持久化载体：列内容恒为 State 的 JSON（写入端 Encode 单源），读路径只认 JSON，
// 非法/损坏值一律降级为「无挂起」——挂起态是瞬态（等待用户答复），
// 跨版本存量行早已随会话终态或预算超时结算完毕，不需要保留历史协议兼容。
//
// 本包是**通用的多轮状态载体**，与「消歧/反问」的具体语义无关：
// Kind/Stage/Word 等字段的取值由宿主的决策链定义，本包只负责
// 「结构化 + 单一编码入口 + 宽容解码」。
package pending

import (
	"encoding/json"
	"strings"
)

// State 挂起态。列内容的唯一合法形态。
type State struct {
	Kind  string `json:"k"`            // 挂起类别（取值由宿主定义，如 disambig/term/done/target）
	Word  string `json:"w,omitempty"`  // 消歧关键词 / 被反问词条 / 抑制词条
	Stage string `json:"st,omitempty"` // 反问推进态（取值由宿主定义）
	Orig  string `json:"o,omitempty"`  // 被反问中断的原决策（消解后恢复用）
	Plan  string `json:"p,omitempty"`  // 决策点附带的方案意图标记
}

// Encode 编码为列值（唯一写入形态）。
func Encode(s State) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// Parse 解码列值：仅认 Encode 产物；非 JSON / 损坏 / 无 Kind 一律返回零值（无挂起）。
func Parse(v string) State {
	v = strings.TrimSpace(v)
	if v == "" {
		return State{}
	}
	var s State
	if err := json.Unmarshal([]byte(v), &s); err == nil && s.Kind != "" {
		return s
	}
	return State{}
}

// ColumnValue 落列前的归一：已经是 Encode 产物的原样透传；裸关键词（决策层的
// clarify 字段可能携带裸词）统一包成指定 Kind 的 JSON，**列内容恒 JSON**。
func ColumnValue(clarify, kind string) string {
	if clarify == "" || strings.HasPrefix(clarify, "{") {
		return clarify
	}
	return Encode(State{Kind: kind, Word: clarify})
}
