package delta

// 合成夹具：推理/正文切分，不带任何领域味。

import (
	"strings"
	"testing"
)

// feedAll 把整段文本按固定宽度切块喂进去，模拟任意分片边界。
func feedAll(s *Splitter, text string, width int) (reasoning, content string) {
	var r, c strings.Builder
	for i := 0; i < len(text); i += width {
		end := i + width
		if end > len(text) {
			end = len(text)
		}
		rr, cc := s.Feed(text[i:end])
		r.WriteString(rr)
		c.WriteString(cc)
	}
	rr, cc := s.Flush()
	r.WriteString(rr)
	c.WriteString(cc)
	return r.String(), c.String()
}

// TestSplitterPlainTextPlainText 无标签时全文归正文，且逐字节无损。
func TestSplitterPlainTextPlainText(t *testing.T) {
	const text = "just a plain answer with no markers at all"
	r, c := feedAll(NewSplitter(), text, 3)
	if r != "" {
		t.Fatalf("不应有推理段: %q", r)
	}
	if c != text {
		t.Fatalf("正文应逐字保留:\n got %q\nwant %q", c, text)
	}
}

// TestSplitterTagSplitAcrossChunks 标签横跨分片：`<thi` + `nk>` 不得被切坏。
// 这是本文件存在的理由——字符一旦发出去就收不回来。
func TestSplitterTagSplitAcrossChunks(t *testing.T) {
	r, c := feedAll(NewSplitter(), "<think>deep</think>answer", 4)
	if r != "deep" {
		t.Fatalf("推理段应为 deep，实得 %q", r)
	}
	if c != "answer" {
		t.Fatalf("正文应为 answer，实得 %q", c)
	}
}

// TestSplitterWidthInvariance 无论按什么宽度切分，结论必须一致——
// 判定只依赖字节内容，不依赖分片边界。
func TestSplitterWidthInvariance(t *testing.T) {
	const text = "<think>reason here</think>middle<think>more</think>tail"
	var wantR, wantC string
	for _, w := range []int{1, 2, 3, 5, 7, 11, 64} {
		r, c := feedAll(NewSplitter(), text, w)
		if w == 1 {
			wantR, wantC = r, c
			continue
		}
		if r != wantR || c != wantC {
			t.Fatalf("宽度 %d 与基准不一致:\n got %q | %q\nwant %q | %q", w, r, c, wantR, wantC)
		}
	}
}

// TestSplitterPartialTagHeldBack 裸 `<` 不得被当成正文吐出，必须扣住等下一片。
func TestSplitterPartialTagHeldBack(t *testing.T) {
	s := NewSplitter()
	r, c := s.Feed("abc<")
	if r != "" || c != "abc" {
		t.Fatalf("`<` 应被扣住，只发 abc；实得 r=%q c=%q", r, c)
	}
	if s.Pending() != 1 {
		t.Fatalf("应扣住 1 字节，实得 %d", s.Pending())
	}
	r2, c2 := s.Feed("think>x")
	if r2 != "x" || c2 != "" {
		t.Fatalf("补齐后应转入推理段；实得 r=%q c=%q", r2, c2)
	}
}

// TestSplitterPendingNeverExceedsTag 扣住的上界是标签长度减一——
// 标签本体一旦完整必须被消费，否则会被永久扣住。
func TestSplitterPendingNeverExceedsTag(t *testing.T) {
	s := NewSplitter()
	if _, c := s.Feed(ThinkOpenTag); c != "" {
		t.Fatalf("完整标签不该被发出，实得 %q", c)
	}
	if s.Pending() != 0 {
		t.Fatalf("完整标签后不应有残留，实得 %d", s.Pending())
	}
	// 多个部分前缀叠加也不能超过上界
	s2 := NewSplitter()
	s2.Feed("<think><think><think><")
	if s2.Pending() > len(ThinkOpenTag)-1 {
		t.Fatalf("扣住长度 %d 超过上界 %d", s2.Pending(), len(ThinkOpenTag)-1)
	}
}

// TestSplitterFlushEmitsHeldTail 流末不 Flush 就丢流 = 永远丢掉尾巴。
// 扣住的尾巴按**当前段归属**吐出：此处从未进入推理段，故归正文。
func TestSplitterFlushEmitsHeldTail(t *testing.T) {
	s := NewSplitter()
	_, c := s.Feed("done<thi")
	if c != "done" {
		t.Fatalf("已确定部分应发出，实得 %q", c)
	}
	// 不再喂任何分片，直接 Flush
	r, c2 := s.Flush()
	if r != "" || c2 != "<thi" {
		t.Fatalf("Flush 应把尾巴按当前段（正文）吐出；实得 r=%q c=%q", r, c2)
	}
	if s.Pending() != 0 || s.InReasoning() {
		t.Fatalf("Flush 后应复位: pending=%d inReason=%v", s.Pending(), s.InReasoning())
	}
}

// TestSplitterUnterminatedStaysReasoning 未闭合的推理段：尾巴归推理段而非正文。
// 注意 Feed 的返回值必须累加——已确定部分在 Feed 就发出去了，Flush 只吐扣住的。
func TestSplitterUnterminatedStaysReasoning(t *testing.T) {
	s := NewSplitter()
	r1, c1 := s.Feed("<think>thinking forever<")
	if r1 != "thinking forever" || c1 != "" {
		t.Fatalf("Feed 应把推理段已确定部分发出；实得 r=%q c=%q", r1, c1)
	}
	if !s.InReasoning() {
		t.Fatal("应处于推理段")
	}
	r, c := s.Flush()
	if r != "<" || c != "" {
		t.Fatalf("未闭合尾巴应归推理段；实得 r=%q c=%q", r, c)
	}
}

// TestSplitterTailAttributedToCurrentMode 尾巴归属由 Flush 时的段决定：
// 内容模式下扣住的前缀，最终仍是内容（不是"看着像标签就升级成推理"）。
func TestSplitterTailAttributedToCurrentMode(t *testing.T) {
	s := NewSplitter()
	if _, c := s.Feed("plain<"); c != "plain" {
		t.Fatalf("已确定部分应发出，实得 %q", c)
	}
	if s.InReasoning() {
		t.Fatal("未遇到开标签时不应进入推理段")
	}
	r, c := s.Flush()
	if r != "" || c != "<" {
		t.Fatalf("正文模式下扣住的尾巴应仍归正文；实得 r=%q c=%q", r, c)
	}
}

// TestSplitterEmptyTagFallback 空标签回落缺省——空标签会让内容永远扣住不发。
func TestSplitterEmptyTagFallback(t *testing.T) {
	s := NewSplitterTags("", "")
	r, c := feedAll(s, "<think>a</think>b", 3)
	if r != "a" || c != "b" {
		t.Fatalf("空标签应回落缺省标签对；实得 r=%q c=%q", r, c)
	}
}

// TestSplitterCustomTags 自定义标签对可用。
func TestSplitterCustomTags(t *testing.T) {
	s := NewSplitterTags("<r>", "</r>")
	r, c := feedAll(s, "<r>hidden</r>shown", 3)
	if r != "hidden" || c != "shown" {
		t.Fatalf("自定义标签对应生效；实得 r=%q c=%q", r, c)
	}
}

// TestHoldBackLongestProperPrefix holdBack 取最长真前缀，不是第一个匹配。
func TestHoldBackLongestProperPrefix(t *testing.T) {
	if got := holdBack("abc<thi", ThinkOpenTag); got != 4 {
		t.Fatalf("应扣住 4 字节，实得 %d", got)
	}
	if got := holdBack("abc", ThinkOpenTag); got != 0 {
		t.Fatalf("无前缀关系应扣 0，实得 %d", got)
	}
	if got := holdBack("<", ThinkOpenTag); got != 1 {
		t.Fatalf("单字符前缀应扣 1，实得 %d", got)
	}
	if got := holdBack("<think>", ThinkOpenTag); got != 0 {
		t.Fatalf("完整标签不属真前缀，应扣 0，实得 %d", got)
	}
}
