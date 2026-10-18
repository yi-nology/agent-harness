package budget

import "testing"

// 窗口上限与压力档。

func TestMaxInputReservesOutput(t *testing.T) {
	// 窗口 8192、输出预留 1024、调用方自设 8000 → 取 7168。
	if got := MaxInput(8192, 8000, 1024); got != 7168 {
		t.Fatalf("应先扣输出预留再被自设上限收窄，实得 %d", got)
	}
}

func TestMaxInputTakesBaseWhenLower(t *testing.T) {
	if got := MaxInput(8192, 1024, 1024); got != 1024 {
		t.Fatalf("自设上限更低时以自设为准，实得 %d", got)
	}
}

// TestMaxInputFloorOfOne 地板只兜「扣成非正」这一种情形。
func TestMaxInputFloorOfOne(t *testing.T) {
	cases := []struct {
		name    string
		w, b, r int
	}{
		{"预留正好等于窗口", 1000, 800, 1000},
		{"预留超过窗口", 100, 800, 500},
		{"自设上限比窗口还宽也过不了地板", 100, 900, 1000},
		{"无自设上限且预留吃光窗口", 1000, 0, 1000},
	}
	for _, c := range cases {
		if got := MaxInput(c.w, c.b, c.r); got != MinInputTokens {
			t.Fatalf("%s：应落到地板 %d，实得 %d", c.name, MinInputTokens, got)
		}
	}
}

// TestMaxInputNoReserveKeepsCap 未要求预留时**原样返回**自设上限：既不扣减也不取地板。
// 旧实现在这里无条件相减并取 1，把「没要求预留」当成「预留吃光窗口」——两种现场
// 被压成一种，而前者的正确处置是拿回自己写的上限。
func TestMaxInputNoReserveKeepsCap(t *testing.T) {
	cases := []struct {
		name    string
		w, b, r int
		want    int
	}{
		{"不预留", 8192, 8000, 0, 8000},
		{"负预留按不预留", 8192, 8000, -1024, 8000},
		{"自设上限宽于窗口仍原样返回", 100, 800, 0, 800},
		{"全零参数返回零而非地板", 0, 0, 0, 0},
		{"负窗口视为未知", -5, 100, 0, 100},
	}
	for _, c := range cases {
		if got := MaxInput(c.w, c.b, c.r); got != c.want {
			t.Fatalf("%s：应原样返回 %d，实得 %d", c.name, c.want, got)
		}
	}
}

// TestMaxInputBaseWiderThanWindow 有预留时，自设上限宽于窗口仍按取小收窄。
func TestMaxInputBaseWiderThanWindow(t *testing.T) {
	// 窗口 100、预留 50 → 可用 50；自设 800 宽于窗口，取小为 50。
	if got := MaxInput(100, 800, 50); got != 50 {
		t.Fatalf("自设上限宽于窗口时应被扣减后的窗口收窄，实得 %d", got)
	}
}

// TestMaxInputWithoutBaseCap 无自设上限时完全由窗口与预留推导。
func TestMaxInputWithoutBaseCap(t *testing.T) {
	for _, b := range []int{0, -5} {
		if got := MaxInput(8192, b, 1024); got != 7168 {
			t.Fatalf("baseInput=%d 时应由窗口推导 7168，实得 %d", b, got)
		}
	}
}

// TestMaxInputWithHostCapOverrides 宿主显式配置的提示上限直接生效，不与窗口取小。
func TestMaxInputWithHostCapOverrides(t *testing.T) {
	cases := []struct {
		name                string
		w, b, r, host, want int
	}{
		{"显式上限比推导值小则压到显式值", 8192, 8000, 1024, 5000, 5000},
		{"显式上限比推导值大也不被收窄", 1000, 200, 100, 9000, 9000},
		{"完全没有窗口也能用显式上限", 0, 0, 0, 64, 64},
		{"无显式上限时退回推导", 8192, 8000, 1024, 0, 7168},
		{"负显式上限不算显式配置", 8192, 8000, 1024, -1, 7168},
	}
	for _, c := range cases {
		if got := MaxInputWithHostCap(c.w, c.b, c.r, c.host); got != c.want {
			t.Fatalf("%s：应得 %d，实得 %d", c.name, c.want, got)
		}
	}
}

func TestMaxInputExactlyZeroBecomesOne(t *testing.T) {
	// 预留正好等于窗口 → 余量 0，宁可给 1 也不给 0（0 必然截断，1 还剩一线）。
	if got := MaxInput(1000, 1000, 1000); got != 1 {
		t.Fatalf("余量为 0 时应取地板 1，实得 %d", got)
	}
}

func TestClassifyNormalTightCritical(t *testing.T) {
	cases := []struct {
		name       string
		input, cap int
		want       Pressure
	}{
		{"空输入", 0, 100, PressureNormal},
		{"刚过 tight 之前", 74, 100, PressureNormal},
		{"正好 tight", 75, 100, PressureTight},
		{"critical 之前", 89, 100, PressureTight},
		{"正好 critical", 90, 100, PressureCritical},
		{"越界", 250, 100, PressureCritical},
		{"负输入按 0", -1, 100, PressureNormal},
	}
	for _, c := range cases {
		if got := Classify(c.input, c.cap); got != c.want {
			t.Fatalf("%s：Classify(%d,%d) 应为 %v，实得 %v", c.name, c.input, c.cap, c.want, got)
		}
	}
}

func TestClassifyUnknownCapIsCritical(t *testing.T) {
	// 「不知道会不会溢出」与「不会溢出」在行为上相反。
	for _, cap := range []int{0, -1} {
		if got := Classify(1, cap); got != PressureCritical {
			t.Fatalf("cap=%d 时应判 critical（不得声称余量充足），实得 %v", cap, got)
		}
	}
}

func TestClassifyLargeValuesNoFloatDrift(t *testing.T) {
	// 用整数比值判定：0.9 边界不因浮点舍入掉档，且量级超出 int32 不溢出。
	const unit = 1 << 30
	cap := 10 * unit
	if got := Classify(9*unit, cap); got != PressureCritical {
		t.Fatalf("90%% 边界应判 critical，实得 %v", got)
	}
	if got := Classify(9*unit-1, cap); got != PressureTight {
		t.Fatalf("刚好低于 90%% 应判 tight，实得 %v", got)
	}
}

func TestPressureString(t *testing.T) {
	cases := map[Pressure]string{
		PressureNormal: "normal", PressureTight: "tight",
		PressureCritical: "critical", Pressure(99): "unknown",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Fatalf("Pressure(%d) 应为 %q，实得 %q", p, want, got)
		}
	}
}
