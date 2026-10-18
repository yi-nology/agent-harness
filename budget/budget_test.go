package budget

import "testing"

// 分段计量与展示口径。合成分段键：seg-alpha / seg-beta。

func TestMeterZeroValueUsable(t *testing.T) {
	var m Meter // 零值必须可用：调用方不该为了计量先 New 一个东西
	m.Add(SegmentTools, 100)
	if got := m.Tokens(SegmentTools); got != 100 {
		t.Fatalf("零值 Meter 应可累加，实得 %d", got)
	}
}

func TestMeterPerSegmentAndTotal(t *testing.T) {
	var m Meter
	m.Add(SegmentSystem, 10)
	m.Add(SegmentTools, 20)
	m.Add(SegmentConversation, 70)
	if got := m.Total(); got != 100 {
		t.Fatalf("总量应等于各段求和，实得 %d", got)
	}
	for seg, want := range map[Segment]int{
		SegmentSystem: 10, SegmentTools: 20, SegmentConversation: 70,
		SegmentMCP: 0, // 未计量过
	} {
		if got := m.Tokens(seg); got != want {
			t.Fatalf("段 %q 应为 %d，实得 %d", seg, want, got)
		}
	}
}

func TestMeterNegativeTokensCountedAsZero(t *testing.T) {
	var m Meter
	m.Add(SegmentConversation, 50)
	m.Add(SegmentConversation, -30) // 估算侧 bug：不得让负量抹掉已计的量
	if got := m.Tokens(SegmentConversation); got != 50 {
		t.Fatalf("负增量应按 0 计入，实得 %d", got)
	}
}

// TestShrinkSubtractsFromSegment 压实后就地减账：没有模型调用，就没有新报数可依。
func TestShrinkSubtractsFromSegment(t *testing.T) {
	var m Meter
	m.Add(SegmentConversation, 1000)
	m.Add(SegmentSystem, 200)
	if got := m.Shrink(SegmentConversation, 400); got != 400 {
		t.Fatalf("应返回实际扣减量 400，实得 %d", got)
	}
	if got := m.Tokens(SegmentConversation); got != 600 {
		t.Fatalf("会话段应减到 600，实得 %d", got)
	}
	if got := m.Total(); got != 800 {
		t.Fatalf("总量应同步减到 800，实得 %d", got)
	}
}

// TestShrinkClampsToSegment 实有量不足时按段钳位，返回值可分辨「账上就这么多」。
func TestShrinkClampsToSegment(t *testing.T) {
	var m Meter
	m.Add(SegmentConversation, 300)
	m.Add(SegmentSystem, 200)
	if got := m.Shrink(SegmentConversation, 999); got != 300 {
		t.Fatalf("应返回实有量 300（钳位可观测），实得 %d", got)
	}
	// 钳位不得外溢到别的段：会话段归零，系统提示段照旧。
	if got := m.Tokens(SegmentConversation); got != 0 {
		t.Fatalf("会话段应归零，实得 %d", got)
	}
	if got := m.Tokens(SegmentSystem); got != 200 {
		t.Fatalf("别段不得被连带抹掉，实得 %d", got)
	}
	if got := m.Total(); got != 200 {
		t.Fatalf("总量应为 200，实得 %d", got)
	}
}

// TestShrinkNonPositiveOrEmptyIsNoOp 非正入参与空段都是空操作，且不制造负值。
func TestShrinkNonPositiveOrEmptyIsNoOp(t *testing.T) {
	var m Meter // 零值 Meter：map 为 nil 时也不得 panic
	m.Add(SegmentConversation, 50)
	for _, n := range []int{0, -5} {
		if got := m.Shrink(SegmentConversation, n); got != 0 {
			t.Fatalf("入参 %d 应为空操作并返回 0，实得 %d", n, got)
		}
	}
	if got := m.Shrink("seg-absent", 100); got != 0 {
		t.Fatalf("未计量过的段无可扣，实得 %d", got)
	}
	if got := m.Tokens(SegmentConversation); got != 50 {
		t.Fatalf("空操作不得改变账面，实得 %d", got)
	}
}

// TestShrinkAfterCompactionClosesTheLoop 端到端闭环：计划 → 压实 → 按净额减账。
// 账面必须回到「摘要 + 逐字保留的尾部」，而不是停在压实前的旧值。
func TestShrinkAfterCompactionClosesTheLoop(t *testing.T) {
	var m Meter
	m.Add(SegmentConversation, 10*25) // 10 条 × 25
	p := mustPlan(t, 10, 4, 25)
	res := mustApply(t, p, &fakeArchive{}, &fakeSummarizer{summary: Summary{Tokens: 40}})

	net := NetRemovedTokens(res.GrossRemovedTokens, res.Summary.Tokens)
	if net != 110 {
		t.Fatalf("净移除量应为 150-40=110，实得 %d", net)
	}
	if got := m.Shrink(SegmentConversation, net); got != net {
		t.Fatalf("应扣满净移除量，实得 %d", got)
	}
	// 残留应为 1 条摘要 + 4 条逐字保留：40 + 4*25。
	if got := m.Total(); got != 140 {
		t.Fatalf("压实后总量应为 40+100=140，实得 %d", got)
	}
	// 快照展示口径跟着走，越界随之解除。
	rep := m.Snapshot(200)
	if rep.Display().Overflow() {
		t.Fatal("减账后不应再判越界")
	}
}

func TestSegmentsCanonicalThenHostSorted(t *testing.T) {
	var m Meter
	m.Add("seg-beta", 3)
	m.Add(SegmentTools, 2)
	m.Add("seg-alpha", 1)

	got := m.Segments()
	// 规范分段固定在前（顺序稳定才能 diff），宿主分段按名字排在后。
	want := []Segment{SegmentSystem, SegmentTools, SegmentRules,
		SegmentSkills, SegmentMCP, SegmentConversation, "seg-alpha", "seg-beta"}
	if len(got) != len(want) {
		t.Fatalf("应列出 6 规范段 + 2 宿主段，实得 %d 段", len(got))
	}
	for i, s := range got {
		if s.Segment != want[i] {
			t.Fatalf("第 %d 段应为 %q，实得 %q", i, want[i], s.Segment)
		}
	}
	if got[6].Tokens != 1 || got[7].Tokens != 3 {
		t.Fatalf("宿主分段量应各归各位，实得 %+v", got[6:])
	}
}

// TestSegmentsUnmeasuredReturnsCanonicalZeroes 未计量过时返回规范分段的零值列表，
// 不是空切片：调用方遍历不必判空，且「存在但量为 0」是比「不存在」更诚实的陈述。
// （原用例注释写的是「返回空切片而非 nil」，与实现不符，实现为准。）
func TestSegmentsUnmeasuredReturnsCanonicalZeroes(t *testing.T) {
	var m Meter
	got := m.Segments()
	if got == nil {
		t.Fatal("零值 Meter 的 Segments 应恒非 nil（调用方遍历不该判空）")
	}
	if len(got) != len(canonicalSegments) {
		t.Fatalf("未计量时应逐条列出 %d 个规范分段，实得 %d 条", len(canonicalSegments), len(got))
	}
	for i, u := range got {
		if u.Segment != canonicalSegments[i] || u.Tokens != 0 {
			t.Fatalf("第 %d 条应为 %q 的零值，实得 %+v", i, canonicalSegments[i], u)
		}
	}
}

func TestMeterResetKeepsZero(t *testing.T) {
	var m Meter
	m.Add("seg-alpha", 5)
	m.Reset()
	if got := m.Tokens("seg-alpha"); got != 0 {
		t.Fatalf("Reset 后应为 0，实得 %d", got)
	}
	if got := m.Total(); got != 0 {
		t.Fatalf("Reset 后总量应为 0，实得 %d", got)
	}
}

// TestSnapshotInputIsSegmentSum 报告的 Input 必须等于分段求和。
func TestSnapshotInputIsSegmentSum(t *testing.T) {
	var m Meter
	m.Add(SegmentSystem, 10)
	m.Add(SegmentTools, 20)
	m.Add("seg-alpha", 30)
	m.Add("seg-beta", 5)
	rep := m.Snapshot(100)
	if rep.Input != 65 {
		t.Fatalf("Input 应等于分段求和 65，实得 %d", rep.Input)
	}
	if len(rep.Segments) != 8 { // 6 规范分段 + 2 个宿主分段
		t.Fatalf("分段条数应为 8，实得 %d：%+v", len(rep.Segments), rep.Segments)
	}
	// 报告的分段顺序同样稳定：宿主分段按名字排，不按写入顺序。
	if rep.Segments[6].Segment != "seg-alpha" || rep.Segments[7].Segment != "seg-beta" {
		t.Fatalf("宿主分段应按名字排序：%+v", rep.Segments[6:])
	}
}

func TestDisplayClampsButKeepsInput(t *testing.T) {
	d := Display{Input: 250, Cap: 200}
	if got := d.Used(); got != 200 {
		t.Fatalf("展示值应钳位到 200，实得 %d", got)
	}
	if d.Input != 250 {
		t.Fatalf("未钳位 input 必须保留（换 cap 后要能重渲染），实得 %d", d.Input)
	}
	if !d.Overflow() {
		t.Fatal("越过上限应报 overflow")
	}
	// 换 cap 后重渲染：同一份 Input 换个上限就该出另一个 Used。
	if got := (Display{Input: 250, Cap: 300}).Used(); got != 250 {
		t.Fatalf("cap 放宽后展示值应为 250，实得 %d", got)
	}
}

func TestDisplayAtCapExactly(t *testing.T) {
	d := Display{Input: 200, Cap: 200}
	if got := d.Used(); got != 200 || d.Overflow() {
		t.Fatalf("正好等于上限不算越界：used=%d overflow=%v", got, d.Overflow())
	}
}

func TestDisplayUnknownCap(t *testing.T) {
	d := Display{Input: 42, Cap: 0}
	if got := d.Used(); got != 42 {
		t.Fatalf("未设上限应原样展示，实得 %d", got)
	}
	if d.Overflow() {
		t.Fatal("未设上限不应判越界")
	}
}

// TestProviderTotalNeverRetroFits provider 报数只进展示口径，不回填分段估算。
func TestProviderTotalNeverRetroFits(t *testing.T) {
	var m Meter
	m.Add(SegmentSystem, 10)
	m.Add(SegmentTools, 20)
	rep := m.Snapshot(100)
	rep.Provider = 45 // provider 报 45，估计只有 30

	if rep.Input != 30 {
		t.Fatalf("provider 报数不得回填 Input（tokenizer 误差不能伪装成分段精确度），实得 %d", rep.Input)
	}
	if got := rep.Drift(); got != 15 {
		t.Fatalf("Drift 应为 45-30=15，实得 %d", got)
	}
	if got := rep.Display().Used(); got != 30 {
		t.Fatalf("分段展示口径应仍为 30，实得 %d", got)
	}
	if got := rep.ProviderDisplay().Used(); got != 45 {
		t.Fatalf("provider 展示口径应为 45，实得 %d", got)
	}
}

func TestProviderDisplayFallsBack(t *testing.T) {
	var m Meter
	m.Add(SegmentSystem, 8)
	rep := m.Snapshot(0)
	rep.Provider = 0 // 本次无 provider 报数
	if got := rep.ProviderDisplay().Used(); got != 8 {
		t.Fatalf("无报数时应退回分段口径 8（不凭空显示 0），实得 %d", got)
	}
	if got := rep.Drift(); got != 0 {
		t.Fatalf("无报数时 Drift 应为 0，实得 %d", got)
	}
}
