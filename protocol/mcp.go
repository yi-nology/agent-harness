package protocol

import "encoding/json"

// envelopeTexts 提取 MCP CallToolResult 信封（{"content":[{"type":"text","text":...}]}）
// 的非空内层文本序列；非信封形态返回 nil。
func envelopeTexts(raw string) []string {
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil
	}
	texts := make([]string, 0, len(env.Content))
	for _, c := range env.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	return texts
}

// UnwrapMCPText 解 MCP 工具返回的信封取首个非空内层文本；非信封形态原样返回——
// 快照/结果进证据条目时，snippet 配额应留给有效数据而非包装层。
func UnwrapMCPText(raw string) string {
	if texts := envelopeTexts(raw); len(texts) > 0 {
		return texts[0]
	}
	return raw
}

// StructuredPayload 提取 MCP 工具返回的 structuredContent 主通道（MCP 2025-06-18，
// 批次一百零八）：agentkit 侧 invokableTool json.Marshal 整个 CallToolResult，故
// structuredContent 原文字节可从信封串直取（server 侧 RawStructuredContent 同字节
// 直通）。旧形态/纯文本工具无此字段，返回 false。
func StructuredPayload(raw string) (string, bool) {
	var env struct {
		Structured json.RawMessage `json:"structuredContent"`
	}
	// "null" 视为缺省（占位而非数据；下游直解零值谓词虽也能兜住，此处单源收口）。
	if err := json.Unmarshal([]byte(raw), &env); err != nil || len(env.Structured) == 0 || string(env.Structured) == "null" {
		return "", false
	}
	return string(env.Structured), true
}

// PayloadCandidates 程序化消费候选序：structuredContent 优先（规范主通道，形态由
// server 侧 outputSchema 对拍兜底），其余同 EnvelopeCandidates——调用方谓词不识别
// 的候选自然落空，缺 structuredContent 的旧 server 行为不变。envelopeFirst 语义
// 与 EnvelopeCandidates 一致。
func PayloadCandidates(raw string, envelopeFirst bool) []string {
	out := make([]string, 0, 4)
	if sp, ok := StructuredPayload(raw); ok {
		out = append(out, sp)
	}
	return append(out, EnvelopeCandidates(raw, envelopeFirst)...)
}

// EnvelopeCandidates 信封候选文本序列（批次二十八：credential unwrapMCPJSON / host
// parseProbeResult / executors parseShellResult 三处解析器共享信封骨架——各自的契约
// 谓词与候选顺序由调用方保留）：
//   - envelopeFirst=true：内层文本段在前、裸串兜底在后——目标是无判别谓词的 map 直解
//     （信封本身也是合法 map，必须先试内层防误命中）；
//   - envelopeFirst=false：裸串在前——调用方谓词能识别并拒绝信封形态。
//
// 批次一百零八起程序化消费方迁移 PayloadCandidates（structuredContent 优先）；
// 本函数保留为纯信封骨架（LLM 观测/文本路径仍用 UnwrapMCPText）。
func EnvelopeCandidates(raw string, envelopeFirst bool) []string {
	texts := envelopeTexts(raw)
	if envelopeFirst {
		return append(texts, raw)
	}
	return append([]string{raw}, texts...)
}
