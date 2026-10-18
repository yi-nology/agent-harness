package toolfail

// 重发安全 / 模型指引的用例。夹具一律用合成名（srv-beta / item-01），不带领域味。
//
// 与 toolfail_test.go 分文件的原因不是分类，是门禁：单文件 400 行是硬阈值，
// 新增用例应当另起文件而不是把既有文件顶过线。

import (
	"strings"
	"testing"
	"time"
)

// TestRetryableAndResubmitAreIndependent 钉住本包最重要的一条判据：
// 「值得重试」与「可以原样重发」是两个独立维度，且存在**相反**的组合。
//
// 为什么这条必须钉死：只带 Retryable 的调用方在这两类失败面前必然误判——
// 对 transient 直接重发会在服务端已执行时制造重复副作用；对 rate_limited
// 不敢重发则会白白丢掉一次本可恢复的调用。两个方向的误判都源于
// 把两个维度压成了一个。
func TestRetryableAndResubmitAreIndependent(t *testing.T) {
	cases := []struct {
		class Class
		// 该类是否值得重试。
		wantRetryable bool
		// 该类缺省能否原样重发。
		wantSafe bool
		why      string
	}{
		{ClassTransient, true, false, "超时类：值得重试，但服务端可能已执行，重发会重复施加副作用"},
		{ClassRateLimited, true, true, "限流发生在受理前：既值得重试，重发又安全"},
		{ClassAuthentication, false, false, "凭证问题：两问皆否，重试是纯浪费"},
		{ClassNotFound, false, false, "目标不存在：重试不创造它"},
	}
	for _, c := range cases {
		p := PolicyOf(c.class)
		if p.Retryable != c.wantRetryable {
			t.Fatalf("类别 %q 的 Retryable 实得 %v，期望 %v（%s）", c.class, p.Retryable, c.wantRetryable, c.why)
		}
		if p.SafeToResubmit != c.wantSafe {
			t.Fatalf("类别 %q 的 SafeToResubmit 实得 %v，期望 %v（%s）", c.class, p.SafeToResubmit, c.wantSafe, c.why)
		}
	}

	// 反证：若两维度被压成一个，transient 与 rate_limited 至少有一个必然判错。
	// 这里显式断言「只按 Retryable 决策」与「按两维度决策」确实给出不同结论，
	// 否则说明这个维度没有承载任何信息。
	tr := New(ClassTransient, "timeout", "srv-beta 超时")
	rl := New(ClassRateLimited, "throttled", "srv-beta 限流")
	if tr.Retryable() != rl.Retryable() {
		t.Fatal("transient 与 rate_limited 都应可重试，差异必须来自重发维度而非可重试维度")
	}
	if tr.SafeToResubmit() == rl.SafeToResubmit() {
		t.Fatal("重发安全维度必须能区分 transient 与 rate_limited，否则该维度是空的")
	}
}

// TestOnlyClassThatCanProveUnreceivedDefaultsSafe 钉住保守缺省：
// 只有能证明「服务端未受理本次调用」的类别才允许缺省可重发。
//
// 这条是反向验证型用例：把任何一类的 SafeToResubmit 翻成 true 而不更新本用例，
// 门禁就会红。理由见 Policy.SafeToResubmit 的注释——超时的定义就是
// 「不知道对方执行到哪」，把它当成安全重发等于把一次调用放大成两次。
func TestOnlyClassThatCanProveUnreceivedDefaultsSafe(t *testing.T) {
	var safe []Class
	for _, c := range AllClasses() {
		if PolicyOf(c).SafeToResubmit {
			safe = append(safe, c)
		}
	}
	want := map[Class]bool{ClassRateLimited: true}
	if len(safe) != len(want) {
		t.Fatalf("缺省可重发的类别应恰为 %v，实得 %v", want, safe)
	}
	for _, c := range safe {
		if !want[c] {
			t.Fatalf("类别 %q 缺省可重发，但它无法证明本次调用未被受理——新增前请先论证", c)
		}
	}
}

// TestIdempotencyOverrideBeatsClassDefault 宿主的逐次表态覆盖类别缺省，两个方向都要成立。
func TestIdempotencyOverrideBeatsClassDefault(t *testing.T) {
	// 类别缺省不安全，宿主声明幂等 → 变安全。
	safe := New(ClassTransient, "timeout", "srv-beta 超时").Idempotent()
	if !safe.SafeToResubmit() {
		t.Fatal("宿主声明幂等后应判为可重发")
	}
	// 类别缺省安全，宿主声明有副作用 → 变不安全。
	unsafe := New(ClassRateLimited, "throttled", "srv-beta 限流").WithIdempotency(IdempotencyNo)
	if unsafe.SafeToResubmit() {
		t.Fatal("宿主声明有副作用后应判为不可重发，不能被类别缺省盖过")
	}
	// 未表态时回到类别缺省（限流仍安全）。
	unset := New(ClassRateLimited, "throttled", "srv-beta 限流")
	if !unset.SafeToResubmit() {
		t.Fatal("未表态时应取类别缺省")
	}
	// 显式表态 IdempotencyUnset 等同未表态。
	explicitUnset := New(ClassTransient, "timeout", "srv-beta 超时").WithIdempotency(IdempotencyUnset)
	if explicitUnset.SafeToResubmit() {
		t.Fatal("IdempotencyUnset 应等同于未表态，取类别缺省（transient 不可重发）")
	}
}

// TestIdempotencyString 机读名进日志，三态都要有稳定取值。
func TestIdempotencyString(t *testing.T) {
	want := map[Idempotency]string{
		IdempotencyUnset: "unspecified",
		IdempotencyYes:   "idempotent",
		IdempotencyNo:    "side_effecting",
	}
	for id, w := range want {
		if got := id.String(); got != w {
			t.Fatalf("幂等性 %d 的机读名实得 %q，期望 %q", int(id), got, w)
		}
	}
	// 越界取值不得静默变成某个已知态——那会把未知说成已知。
	if got := Idempotency(99).String(); got != "unspecified" {
		t.Fatalf("越界取值应回落 unspecified，实得 %q", got)
	}
}

// TestGuidanceTellsModelWhenResubmitIsUnsafe 指引必须把「可重试但不确定能否重发」
// 这一档说清楚，否则模型会当普通瞬时失败直接重发，副作用就落在那一步。
func TestGuidanceTellsModelWhenResubmitIsUnsafe(t *testing.T) {
	g := New(ClassTransient, "timeout", "srv-beta 超时").Guidance()
	if !strings.Contains(g, "确认上一次调用是否已经生效") {
		t.Fatalf("可重试但不可重发时，指引必须点破副作用风险，实得 %q", g)
	}

	// 重发安全的可重试失败不该带这句警告，否则是噪声。
	g2 := New(ClassRateLimited, "throttled", "srv-beta 限流").Guidance()
	if strings.Contains(g2, "确认上一次调用是否已经生效") {
		t.Fatalf("重发安全时不应再警告，实得 %q", g2)
	}
	if !strings.Contains(g2, "可以直接重试") {
		t.Fatalf("重发安全的可重试失败应明确说可以重试，实得 %q", g2)
	}

	// 不可重试的类别必须明说「不要重试」，否则模型会自行尝试。
	for _, c := range []Class{ClassPermanent, ClassAuthentication, ClassNotFound,
		ClassPermissionDenied, ClassInvalidArgument, ClassUnknown} {
		got := New(c, string(c), "srv-beta 失败").Guidance()
		if !strings.Contains(got, "不要重试") {
			t.Fatalf("类别 %q 不可重试，指引应含「不要重试」，实得 %q", c, got)
		}
	}
}

// TestGuidanceSurfacesDelayAndOperator 指引要把「等多久」与「要不要人」一并给出，
// 否则模型只拿到半截信息，仍然会做出错误的下一步。
func TestGuidanceSurfacesDelayAndOperator(t *testing.T) {
	limited := New(ClassRateLimited, "throttled", "srv-beta 限流").
		WithRetryAfter(30 * time.Second).Guidance()
	if !strings.Contains(limited, "30 秒") {
		t.Fatalf("限流类应把建议等待时长写进指引，实得 %q", limited)
	}

	auth := New(ClassAuthentication, "bad_cred", "srv-beta 凭证无效").Guidance()
	if !strings.Contains(auth, "需要人工介入") {
		t.Fatalf("需人工的类别应写明，实得 %q", auth)
	}

	// 未暴露等待时长的类别，不该凭空编一个等待值出来。
	noDelay := New(ClassRateLimited, "throttled", "srv-beta 限流").Guidance()
	if strings.Contains(noDelay, "建议等待") {
		t.Fatalf("无 RetryAfter 时不应编造等待时长，实得 %q", noDelay)
	}
}

// TestEveryClassHasRemediation 每个类别都必须有面向模型的指引。
//
// 为什么是契约：新增类别时若忘了写指引，模型拿到的就只有一句冷冰冰的码，
// 而「不写指引」正是判据表退化成散落 if 的起点——那时没人知道该给模型说什么。
func TestEveryClassHasRemediation(t *testing.T) {
	for _, c := range AllClasses() {
		r := PolicyOf(c).Remediation
		if strings.TrimSpace(r) == "" {
			t.Fatalf("类别 %q 缺模型指引：模型拿到失败后必须知道下一步做什么", c)
		}
		if len([]rune(r)) < 10 {
			t.Fatalf("类别 %q 的指引过短，多半是敷衍而非指引：%q", c, r)
		}
	}
	// 逐次覆盖优先于类别缺省。
	override := New(ClassNotFound, "nf", "item-01 不存在").WithRemediation("请改用 item-02 重试该标识。")
	if got := override.Remediation(); got != "请改用 item-02 重试该标识。" {
		t.Fatalf("逐次指引应覆盖类别缺省，实得 %q", got)
	}
	if got := New(ClassNotFound, "nf", "item-01 不存在").Remediation(); got == override.Remediation() {
		t.Fatal("未覆盖时必须取类别缺省，不能串到别处的实例值")
	}
}

// TestRemediationCarriesNoRetryVerdict 类别文案不得自带重试结论。
//
// 这条是文案层的反向验证：初版每个类别都自带「不要重试」「按提示时长等待」
// 这类机械结论，而 Guidance 又无条件追加一遍，于是渲染出来是
// 「……请确认标识是否正确。不要重试。」——同一句话说两遍；自报类更严重，
// 文案说「可以直接重试」而 Guidance 说「先确认上一次调用是否已经生效」，
// 同一段提示词里给出两个相反指令，模型无从取舍。
//
// 职责边界：Remediation 讲「这是什么情况、我该改什么」，
// Guidance 讲「能不能重试、重发安不安全、要不要人工、等多久」。
func TestRemediationCarriesNoRetryVerdict(t *testing.T) {
	// 这三个词是 Guidance 的机械判定用语；出现在 Remediation 里即职责越界。
	forbidden := []string{"重试", "等待", "人工介入"}
	for _, c := range AllClasses() {
		r := PolicyOf(c).Remediation
		for _, w := range forbidden {
			if strings.Contains(r, w) {
				t.Fatalf("类别 %q 的 Remediation 含有重试结论用语 %q：%q\n"+
					"该判断由 Guidance 统一追加，写两遍会重复，写歪了会自相矛盾",
					c, w, r)
			}
		}
	}
}

// TestGuidanceHasNoInternalContradiction Guidance 的两半（类别文案 + 机械结论）
// 拼起来不得出现相反指令。逐类渲染全文再断言，而不是只查某几个关键词。
func TestGuidanceHasNoInternalContradiction(t *testing.T) {
	// 「可以直接重试」与「先确认上一次调用是否已经生效」是相反指令，
	// 同时出现即说明某一层越界写了另一半的话。
	for _, c := range AllClasses() {
		g := New(c, string(c), "srv-beta 失败").Guidance()
		if strings.Contains(g, "可以直接重试") && strings.Contains(g, "是否已经生效") {
			t.Fatalf("类别 %q 的指引自相矛盾：%q", c, g)
		}
		// 不可重试的类别，机械结论不得缺席（模型必须被明确告知停手）。
		if !PolicyOf(c).Retryable && !strings.Contains(g, "不要重试") {
			t.Fatalf("类别 %q 不可重试，指引必须明说停手，实得 %q", c, g)
		}
		// 同理，可重试且重发安全的类别应明说可以直接重试。
		p := PolicyOf(c)
		if p.Retryable && p.SafeToResubmit && !strings.Contains(g, "可以直接重试") {
			t.Fatalf("类别 %q 可重试且重发安全，指引应明说可以直接重试，实得 %q", c, g)
		}
	}
}

// TestNewFailureNilSafety 指针被传进日志链路时不能 panic，也不能凭空变可重发。
func TestNewFailureNilSafety(t *testing.T) {
	var f *Failure
	if f.SafeToResubmit() {
		t.Fatal("nil 不得判为可重发")
	}
	if got := f.Remediation(); got != "" {
		t.Fatalf("nil 指引应为空串，实得 %q", got)
	}
	if got := f.Guidance(); got != "" {
		t.Fatalf("nil Guidance 应为空串，实得 %q", got)
	}
	if got := f.Error(); got != "<nil>" {
		t.Fatalf("nil Error 应为 <nil>，实得 %q", got)
	}
	// 链式装配在 nil 接收者上不得 panic。
	_ = f.WithIdempotency(IdempotencyYes).WithRemediation("x").WithRetryAfter(time.Second)
}
