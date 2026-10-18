package budget

import (
	"context"
	"testing"

	"github.com/oekit/pkg/errors"
)

// 压实执行：归档 → 摘要 → 拼段。测试替身均为合成实现。

type fakeArchive struct {
	locator Locator
	err     error
	got     []Item
	calls   int
	log     *[]string
}

func (f *fakeArchive) Archive(_ context.Context, items []Item) (Locator, error) {
	f.calls++
	f.got = append([]Item(nil), items...)
	if f.log != nil {
		*f.log = append(*f.log, "archive")
	}
	return f.locator, f.err
}

type fakeSummarizer struct {
	summary Summary
	err     error
	got     []Item
	calls   int
	log     *[]string
}

func (f *fakeSummarizer) Summarize(_ context.Context, leading []Item) (Summary, error) {
	f.calls++
	f.got = append([]Item(nil), leading...)
	if f.log != nil {
		*f.log = append(*f.log, "summarize")
	}
	return f.summary, f.err
}

func items(n, tokens int, prefix string) []Item {
	out := make([]Item, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, Item{ID: prefix + "-" + itoa(i), Tokens: tokens})
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func mustPlan(t *testing.T, items, keep, per int) Plan {
	t.Helper()
	p, err := BuildPlan(PlanInput{Items: items, KeepTail: keep, ItemTokens: per})
	if err != nil {
		t.Fatalf("构造计划失败：%v", err)
	}
	return p
}

// mustApply 按计划构造与计划一致的入参并执行，测试里省掉重复的条数算术。
func mustApply(t *testing.T, p Plan, arc Archive, sum Summarizer) Result {
	t.Helper()
	res, err := Apply(context.Background(), p,
		items(p.SummarizeItems, 25, "item"), items(p.KeepTailItems, 25, "msg"), arc, sum)
	if err != nil {
		t.Fatalf("执行压实失败：%v", err)
	}
	return res
}

func TestApplyCompactsAndReportsDelta(t *testing.T) {
	p := mustPlan(t, 10, 4, 25)
	arc := &fakeArchive{locator: "arc-01"}
	sum := &fakeSummarizer{summary: Summary{Text: "摘要", Tokens: 40}}

	res, err := Apply(context.Background(), p, items(6, 25, "item"), items(4, 25, "msg"), arc, sum)
	if err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if len(res.Segment) != 5 { // 1 条摘要 + 4 条尾部
		t.Fatalf("压实后段长应为 5，实得 %d", len(res.Segment))
	}
	if res.Segment[0].ID != SummaryItemID || res.Segment[0].Tokens != 40 {
		t.Fatalf("首条应为摘要，实得 %+v", res.Segment[0])
	}
	for i, it := range items(4, 25, "msg") {
		if res.Segment[i+1] != it {
			t.Fatalf("尾部第 %d 条应逐字保留，实得 %+v", i, res.Segment[i+1])
		}
	}
	if res.Locator != "arc-01" || res.GrossRemovedTokens != 150 {
		t.Fatalf("应带回归档句柄与毛移除量：%+v", res)
	}
	if got := res.SnapshotDelta(); got != 40-150 {
		t.Fatalf("快照修正量应为摘要 40 - 毛移除 150，实得 %d", got)
	}
}

// TestNetRemovedTokensIsNetNotGross 净移除量 = max(0, 毛 - 摘要)，与参考实现同名
// removed_tokens 的口径一致。参考实现的 removed_tokens 就是这个净值；毛量必须走
// GrossRemovedTokens，按名字直接映射会凭空少算一份摘要的量。
func TestNetRemovedTokensIsNetNotGross(t *testing.T) {
	cases := []struct {
		name           string
		gross, summary int
		want           int
	}{
		{"摘要小于原文", 150, 40, 110},
		{"恰好相等", 100, 100, 0},
		{"摘要比原文还长时钳位到 0", 40, 150, 0},
		{"无摘要量", 150, 0, 150},
	}
	for _, c := range cases {
		if got := NetRemovedTokens(c.gross, c.summary); got != c.want {
			t.Fatalf("%s：NetRemovedTokens(%d,%d) 应为 %d，实得 %d", c.name, c.gross, c.summary, c.want, got)
		}
	}
}

// TestApplyArchivesBeforeSummarize 顺序是硬要求：不可逆操作之后的那一步最便宜。
func TestApplyArchivesBeforeSummarize(t *testing.T) {
	log := &[]string{}
	arc := &fakeArchive{locator: "arc-01", log: log}
	sum := &fakeSummarizer{summary: Summary{Tokens: 10}, log: log}

	if _, err := Apply(context.Background(), mustPlan(t, 10, 4, 10),
		items(6, 10, "item"), items(4, 10, "msg"), arc, sum); err != nil {
		t.Fatalf("不该失败：%v", err)
	}
	if len(*log) != 2 || (*log)[0] != "archive" || (*log)[1] != "summarize" {
		t.Fatalf("归档必须先于摘要（摘要失败要能重试），实得调用序 %v", *log)
	}
	// 归档拿到的应是进摘要的那一段，不是整段会话。
	if len(arc.got) != 6 || arc.got[0].ID != "item-1" {
		t.Fatalf("归档对象应为领先条目，实得 %+v", arc.got)
	}
}

func TestApplyPlanMismatchRefused(t *testing.T) {
	arc := &fakeArchive{locator: "arc-01"}
	sum := &fakeSummarizer{summary: Summary{Tokens: 10}}
	// 计划说 6 进摘要，调用方只给了 5 条 → 计划已过期。
	_, err := Apply(context.Background(), mustPlan(t, 10, 4, 25),
		items(5, 25, "item"), items(4, 25, "msg"), arc, sum)
	if !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("过期计划应被拒（宁可不压），实得 %v", err)
	}
	if arc.calls != 0 || sum.calls != 0 {
		t.Fatalf("被拒时不得有任何副作用，实得 archive=%d summarize=%d", arc.calls, sum.calls)
	}
}

func TestApplyNotWired(t *testing.T) {
	p := mustPlan(t, 10, 4, 10)
	if _, err := Apply(context.Background(), p, items(6, 10, "item"), items(4, 10, "msg"), nil, &fakeSummarizer{}); !errors.Is(err, ErrNotWired) {
		t.Fatalf("未装配应被拒，实得 %v", err)
	}
	if _, err := Apply(context.Background(), p, items(6, 10, "item"), items(4, 10, "msg"), &fakeArchive{}, nil); !errors.Is(err, ErrNotWired) {
		t.Fatalf("未装配应被拒，实得 %v", err)
	}
}

// TestApplyRefusesEmptyPlan 空计划压不出东西，却会产出一条摘要 + 一条「已压实」的
// 成功回执：上下文一字未动，宿主却以为已经处置过。这正是本包常量注释里写的
// 「报成功而什么都没做，比报错更糟」，所以它必须是错误而不是成功。
func TestApplyRefusesEmptyPlan(t *testing.T) {
	cases := []struct {
		name string
		plan Plan
	}{
		{"零值计划", Plan{}},
		{"声明了条目却一条都不摘要", Plan{Items: 10, Cutoff: 0, SummarizeItems: 0, KeepTailItems: 10}},
	}
	for _, c := range cases {
		arc := &fakeArchive{locator: "arc-01"}
		sum := &fakeSummarizer{summary: Summary{Text: "摘要", Tokens: 40}}
		res, err := Apply(context.Background(), c.plan, nil, nil, arc, sum)
		if !errors.Is(err, ErrEmptyPlan) {
			t.Fatalf("%s：应被 ErrEmptyPlan 拒绝，实得 %v", c.name, err)
		}
		if res.Segment != nil {
			t.Fatalf("%s：被拒时不得产出幽灵会话段，实得 %+v", c.name, res.Segment)
		}
		if arc.calls != 0 || sum.calls != 0 {
			t.Fatalf("%s：拒绝必须先于任何副作用，实得 archive=%d summarize=%d", c.name, arc.calls, sum.calls)
		}
	}
}

// TestApplyRefusesInconsistentPlan 计划各字段互不自洽时拒绝：Cutoff 真正驱动的是
// 「哪一段进摘要」，它与被实际使用的两个数字对不上时，这份计划不可复核。
func TestApplyRefusesInconsistentPlan(t *testing.T) {
	cases := []struct {
		name string
		plan Plan
	}{
		{"切分加起来不等于条目总数", Plan{Items: 10, Cutoff: 6, SummarizeItems: 6, KeepTailItems: 9}},
		{"截断下标与待摘要数不等", Plan{Items: 10, Cutoff: 4, SummarizeItems: 6, KeepTailItems: 4}},
	}
	for _, c := range cases {
		arc := &fakeArchive{locator: "arc-01"}
		sum := &fakeSummarizer{summary: Summary{Tokens: 40}}
		_, err := Apply(context.Background(), c.plan,
			items(c.plan.SummarizeItems, 25, "item"), items(c.plan.KeepTailItems, 25, "msg"), arc, sum)
		if !errors.Is(err, ErrPlanInconsistent) {
			t.Fatalf("%s：应被 ErrPlanInconsistent 拒绝，实得 %v", c.name, err)
		}
		if arc.calls != 0 || sum.calls != 0 {
			t.Fatalf("%s：拒绝必须先于任何副作用，实得 archive=%d summarize=%d", c.name, arc.calls, sum.calls)
		}
	}
}

// TestApplyStalePlanStillRefusedBeforeSideEffects 三条计划校验的顺序不变量：
// 过期计划仍然被拒，且拒绝发生在归档之前——不可逆操作一旦发生就撤不回来。
func TestApplyStalePlanStillRefusedBeforeSideEffects(t *testing.T) {
	arc := &fakeArchive{locator: "arc-01"}
	sum := &fakeSummarizer{summary: Summary{Tokens: 40}}
	// 计划说 6 进摘要、4 条尾部，调用方给了 8 条尾部 → 计划已过期。
	_, err := Apply(context.Background(), mustPlan(t, 10, 4, 25),
		items(6, 25, "item"), items(8, 25, "msg"), arc, sum)
	if !errors.Is(err, ErrPlanMismatch) {
		t.Fatalf("过期计划仍应被 ErrPlanMismatch 拒绝，实得 %v", err)
	}
	if arc.calls != 0 || sum.calls != 0 {
		t.Fatalf("被拒时不得有任何副作用，实得 archive=%d summarize=%d", arc.calls, sum.calls)
	}
}

// TestApplyArchiveFailureAborts 归档失败必须中止：原文没落地就不许丢。
func TestApplyArchiveFailureAborts(t *testing.T) {
	boom := errors.New("存储不可用")
	arc := &fakeArchive{err: boom}
	sum := &fakeSummarizer{summary: Summary{Tokens: 10}}
	_, err := Apply(context.Background(), mustPlan(t, 10, 4, 10),
		items(6, 10, "item"), items(4, 10, "msg"), arc, sum)
	if !errors.Is(err, boom) {
		t.Fatalf("应透出归档失败，实得 %v", err)
	}
	if sum.calls != 0 {
		t.Fatal("归档失败后不得继续摘要")
	}
}

// TestApplySummarizeFailureKeepsArchive 摘要失败时原文仍在，可直接重试。
func TestApplySummarizeFailureKeepsArchive(t *testing.T) {
	boom := errors.New("模型超时")
	arc := &fakeArchive{locator: "arc-07"}
	sum := &fakeSummarizer{err: boom}
	res, err := Apply(context.Background(), mustPlan(t, 10, 4, 10),
		items(6, 10, "item"), items(4, 10, "msg"), arc, sum)
	if !errors.Is(err, boom) {
		t.Fatalf("应透出摘要失败，实得 %v", err)
	}
	if res.Locator != "arc-07" {
		t.Fatalf("失败回执必须带回归档句柄供重试，实得 %q", res.Locator)
	}
	if len(res.Segment) != 0 {
		t.Fatalf("失败时不得产出半截会话段，实得 %+v", res.Segment)
	}
}

// TestApplySegmentNoAlias 结果切片不得与入参共享底层数组。
func TestApplySegmentNoAlias(t *testing.T) {
	tail := items(4, 10, "msg")
	res, err := Apply(context.Background(), mustPlan(t, 10, 4, 10),
		items(6, 10, "item"), tail, &fakeArchive{}, &fakeSummarizer{summary: Summary{Tokens: 10}})
	if err != nil {
		t.Fatal(err)
	}
	res.Segment[1].ID = "mutated"
	if tail[0].ID != "msg-1" {
		t.Fatalf("结果段与入参共享了底层数组：%v", tail)
	}
}
