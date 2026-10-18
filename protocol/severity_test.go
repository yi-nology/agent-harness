package protocol

import "testing"

// TestSeverityRankOrderIsMonotonic 严重度序必须严格递增，且 critical 是最高。
//
// 这条在导出可变 map 的形态下**测不出来**：宿主改一张表就能重排全序而测试照样绿。
// 收成不可变访问器之后，「序」不再是被外部共享的一份数据，而是本包的承诺，
// 故这里把它钉成用例。
func TestSeverityRankOrderIsMonotonic(t *testing.T) {
	order := []string{SeverityInfo, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}
	for i := 1; i < len(order); i++ {
		lo, hi := SeverityRankOf(order[i-1]), SeverityRankOf(order[i])
		if hi <= lo {
			t.Fatalf("%s(%d) 必须重于 %s(%d)", order[i], hi, order[i-1], lo)
		}
	}
}

// TestSeverityRankUnknownIsZero 未知值与空串一律 0（未评估）。
//
// 0 不是「最低」，是「不参与比较」：MaxSeverity 拿 0 和真实值比，恒判真实值更重，
// 于是未知值被当成最轻的那一档而非被丢弃——这是 MaxSeverity 的既有语义，
// 这里只钉住「未知不得被登记成某个真实档位」。
func TestSeverityRankUnknownIsZero(t *testing.T) {
	for _, s := range []string{"", "nope", "CRITICAL", "critical "} {
		if got := SeverityRankOf(s); got != 0 {
			t.Fatalf("未知/非法值 %q 应为 0（未评估），实得 %d", s, got)
		}
	}
}

// TestMaxSeverityIgnoresUnranked MaxSeverity 对未评估值不敏感。
func TestMaxSeverityIgnoresUnranked(t *testing.T) {
	if got := MaxSeverity("", SeverityHigh); got != SeverityHigh {
		t.Fatalf("空 + high 应得 high，实得 %q", got)
	}
	if got := MaxSeverity("nonsense", SeverityHigh); got != SeverityHigh {
		t.Fatalf("未知 + high 应得 high，实得 %q", got)
	}
	if got := MaxSeverity("", ""); got != "" {
		t.Fatalf("两个未评估应得空，实得 %q", got)
	}
}
