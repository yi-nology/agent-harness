package budget

import (
	"testing"

	"github.com/oekit/pkg/errors"
)

// 压实计划的三条空操作守卫与正常路径。

// TestGuardTooFewItems 守卫一：有效条目数不足，连一次压实都撑不起来。
func TestGuardTooFewItems(t *testing.T) {
	for _, items := range []int{0, 1, -3, MinEffectiveItems - 1} { // 含空输入
		_, err := BuildPlan(PlanInput{Items: items, KeepTail: 0, ItemTokens: 10})
		if !errors.Is(err, ErrTooFewItems) {
			t.Fatalf("items=%d 应被 ErrTooFewItems 拒绝，实得 %v", items, err)
		}
		if err.Error() == ErrTooFewItems.Error() {
			t.Fatalf("拒绝应带明细（原因不是裸布尔），实得 %q", err.Error())
		}
	}
	// 门槛挂在保留尾部基线上：+2 不是拍脑袋的独立常量。
	if MinEffectiveItems != ForceKeepItems+MinSummarizeItems {
		t.Fatalf("门槛应由保留尾部基线派生，实得 %d", MinEffectiveItems)
	}
}

// TestGuardThresholdNotDefeatedByKeepTail1 防绕过回归：阈值取自保留尾部基线，
// 把 keepTail 调小拆不掉守卫。keepTail=1 且 items=4 时本实现会产出计划
// （cutoff=3 >= 最小摘要批量），而参考实现因 items=4 < FORCE_KEEP_MESSAGES+2=8
// 直接判定 nothing_to_compact——这是与参考实现真实存在的分歧点。
func TestGuardThresholdNotDefeatedByKeepTail1(t *testing.T) {
	if _, err := BuildPlan(PlanInput{Items: 4, KeepTail: 1, ItemTokens: 10}); !errors.Is(err, ErrTooFewItems) {
		t.Fatalf("keepTail=1 items=4 应被拒绝（阈值不随 keepTail 降低），实得 %v", err)
	}
}

// TestGuardCutoffTooLow 守卫二：截断下标 <= 0，保留尾部会吞掉整段会话。
func TestGuardCutoffTooLow(t *testing.T) {
	for _, c := range []struct{ items, keep int }{
		{MinEffectiveItems, MinEffectiveItems}, // 保留全部 → cutoff=0
		{10, 12},                               // 保留超过条目数 → cutoff<0
		{9, 9},                                 // 门槛之上、cutoff 恰为 0
	} {
		_, err := BuildPlan(PlanInput{Items: c.items, KeepTail: c.keep, ItemTokens: 10})
		if !errors.Is(err, ErrCutoffTooLow) {
			t.Fatalf("items=%d keep=%d 应被 ErrCutoffTooLow 拒绝，实得 %v", c.items, c.keep, err)
		}
	}
}

// TestGuardTooFewToSummarize 守卫三：待摘要条目数不足最小批量（拒绝「只摘要 1 条」）。
func TestGuardTooFewToSummarize(t *testing.T) {
	_, err := BuildPlan(PlanInput{Items: MinEffectiveItems, KeepTail: MinEffectiveItems - 1, ItemTokens: 10})
	if !errors.Is(err, ErrTooFewToSummarize) {
		t.Fatalf("只待摘要 1 条应被拒绝，实得 %v", err)
	}
	// 恰好等于最小批量则放行（守卫是「低于」，不是「不超过」）。
	if _, err := BuildPlan(PlanInput{Items: 10, KeepTail: 8, ItemTokens: 10}); err != nil {
		t.Fatalf("待摘要 2 条（等于下限）应放行，实得 %v", err)
	}
}

func TestPlanNormalPath(t *testing.T) {
	p, err := BuildPlan(PlanInput{Items: 10, KeepTail: 4, ItemTokens: 25})
	if err != nil {
		t.Fatalf("正常路径不该被拒，实得 %v", err)
	}
	if p.Cutoff != 6 || p.SummarizeItems != 6 || p.KeepTailItems != 4 {
		t.Fatalf("切分错误：%+v", p)
	}
	if p.GrossRemovedTokens != 150 { // 6 条 × 25
		t.Fatalf("毛移除量应为首段估算 150，实得 %d", p.GrossRemovedTokens)
	}
	if p.Items != 10 {
		t.Fatalf("计划应带回条目数以供宿主判过期，实得 %d", p.Items)
	}
}

func TestPlanNegativeKeepTailNormalized(t *testing.T) {
	p, err := BuildPlan(PlanInput{Items: 9, KeepTail: -1, ItemTokens: 2})
	if err != nil {
		t.Fatalf("负保留尾部按 0 视作，实得 %v", err)
	}
	if p.KeepTailItems != 0 || p.Cutoff != 9 {
		t.Fatalf("应退化为「全部进摘要」，实得 %+v", p)
	}
}

func TestPlanWithoutItemTokensSkipsEstimate(t *testing.T) {
	p, err := BuildPlan(PlanInput{Items: 8, KeepTail: 2})
	if err != nil {
		t.Fatalf("不该被拒，实得 %v", err)
	}
	if p.GrossRemovedTokens != 0 {
		t.Fatalf("无单条估算时不应编造移除量，实得 %d", p.GrossRemovedTokens)
	}
}

// TestPlanDeterministic 同一输入必得同一计划（编排可复现的基础）。
func TestPlanDeterministic(t *testing.T) {
	in := PlanInput{Items: 12, KeepTail: 5, ItemTokens: 7}
	first, err := BuildPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := BuildPlan(in)
		if err != nil || got != first {
			t.Fatalf("第 %d 次不一致：%+v / %v", i, got, err)
		}
	}
}

// TestPlanZeroPlanReturnedOnRefusal 被拒时不得返回半截计划。
func TestPlanZeroPlanReturnedOnRefusal(t *testing.T) {
	p, err := BuildPlan(PlanInput{Items: 1, KeepTail: 0})
	if !errors.Is(err, ErrTooFewItems) {
		t.Fatalf("应被拒，实得 %v", err)
	}
	if p != (Plan{}) {
		t.Fatalf("被拒时应返回零计划，实得 %+v", p)
	}
}
