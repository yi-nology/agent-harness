package delta

import "strings"

// 跨分片的推理段/正文段标签分流。
//
// ## 要解决的问题
//
// 深度思考模型的正文里常混着推理段标签（`<think>…</think>`）。分片是任意切分的，
// 一个标签完全可能横跨两个 chunk：第一个 chunk 以 `<thi` 结尾，第二个以 `nk>` 开头。
// 若朴素地按「当前 chunk 内找标签」切分，会把 `<thi` 当正文吐出去、把 `nk>` 当推理吐出去——
// 两段都被污染，且**不可逆**：字符已经发出去了，收不回来。
//
// 正确做法是：**尾随部分只要还有可能长成标签，就先扣住不发**，等下一个 chunk
// 到了再判定。本文件就是那个「安全发射窗口」。
//
// ## 与本包既有机制的关系
//
// Pacer 管的是**发布节流**（窗口/帽/seq/权威优先），Splitter 管的是**内容切分**。
// 两者正交且配套：Splitter 的输出直接喂 Pacer.Offer，分片才不会被标签歧义污染。

const (
	// ThinkOpenTag 推理段起始标签（缺省）。
	ThinkOpenTag = "<think>"
	// ThinkCloseTag 推理段结束标签（缺省）。
	ThinkCloseTag = "</think>"
)

// Splitter 跨分片标签分流器。零值不可用，请用 NewSplitter。
//
// 单 goroutine 契约：与 Pacer 同约束（调用方串行驱动），故不加锁。
type Splitter struct {
	openTag  string
	closeTag string
	pending  string // 扣住待判的尾巴
	inReason bool
}

// NewSplitter 构造分流器（缺省 <think>/</think> 标签对）。
func NewSplitter() *Splitter {
	return NewSplitterTags(ThinkOpenTag, ThinkCloseTag)
}

// NewSplitterTags 构造分流器并指定标签对（宿主用别的推理标记时用）。
// 任一标签为空则回落缺省值——空标签会让「后缀是标签前缀」恒真，
// 结果是内容永远扣住不发，是最难查的一类挂起。
func NewSplitterTags(openTag, closeTag string) *Splitter {
	if openTag == "" {
		openTag = ThinkOpenTag
	}
	if closeTag == "" {
		closeTag = ThinkCloseTag
	}
	return &Splitter{openTag: openTag, closeTag: closeTag}
}

// InReasoning 当前是否处于推理段（供调用方决定归属，扣住的尾巴也在段内）。
func (s *Splitter) InReasoning() bool { return s.inReason }

// Pending 当前扣住的待判尾巴长度（诊断用：持续增长说明标签永不成形）。
func (s *Splitter) Pending() int { return len(s.pending) }

// Feed 吃进一个分片，返回本次**可安全发出**的推理段与正文段。
//
// 返回值可能为空——那不是丢内容，是扣在 pending 里等下一个分片。
// 扣住的上界是标签长度减一（标签本体一旦完整就会被消费，不会被扣）。
func (s *Splitter) Feed(chunk string) (reasoning, content string) {
	var rb, cb strings.Builder
	s.pending += chunk
	for len(s.pending) > 0 {
		tag := s.openTag
		if s.inReason {
			tag = s.closeTag
		}
		if i := strings.Index(s.pending, tag); i >= 0 {
			s.emit(&rb, &cb, s.pending[:i])
			s.pending = s.pending[i+len(tag):]
			s.inReason = !s.inReason
			continue
		}
		// 本轮无完整标签：把「仍可能长成标签」的最长后缀扣住。
		k := holdBack(s.pending, tag)
		s.emit(&rb, &cb, s.pending[:len(s.pending)-k])
		s.pending = s.pending[len(s.pending)-k:]
		break
	}
	return rb.String(), cb.String()
}

// Flush 流末冲刷：把扣住的尾巴按当前段归属一次性发出，并复位状态。
// 不 Flush 就丢流，等于把最后几个字符永远扣在手里。
func (s *Splitter) Flush() (reasoning, content string) {
	var rb, cb strings.Builder
	s.emit(&rb, &cb, s.pending)
	s.pending = ""
	s.inReason = false
	return rb.String(), cb.String()
}

// emit 按当前段归属追加文本。
func (s *Splitter) emit(rb, cb *strings.Builder, text string) {
	if text == "" {
		return
	}
	if s.inReason {
		rb.WriteString(text)
		return
	}
	cb.WriteString(text)
}

// holdBack 返回 buffer 末尾应扣住的字节数：最长的、恰好是 tag 真前缀的后缀长度。
//
// 只算真前缀（长度 < len(tag)）——完整标签会被 Index 消费掉，
// 算进去会导致标签本体被永久扣住。
func holdBack(buffer, tag string) int {
	max := len(tag) - 1
	if max > len(buffer) {
		max = len(buffer)
	}
	for k := max; k >= 1; k-- {
		if buffer[len(buffer)-k:] == tag[:k] {
			return k
		}
	}
	return 0
}
