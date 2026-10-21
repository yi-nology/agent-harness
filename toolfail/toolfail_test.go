package toolfail

import (
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// 本文件只验证**门面契约**——本包转成上游转发后，宿主的调用面必须逐字不变。
//
// 实现层的行为测试归上游（github.com/oagentkit/guardx/toolfail），不在本仓重复一份：
// 同一机制的两份测试会各自漂移，漂移时没人知道该信哪份。
//
// 这里钉的是**接口面**的三件事：
//  1. 类型同一性——别名不是「长得像」，编译期就是同一个类型（errors.As 能捡到）；
//  2. 导出符号齐全——宿主用到的每个符号都在；
//  3. 调用形态不变——参数与返回值照旧。

// TestTypeIdentity 类型别名是编译期同一类型。
//
// 这条不是形式主义：宿主（bianque 的 internal/engine/scheduler/experts/dispatch.go）
// 用 errors.As / errors.Is 判失败类别，若本仓的类型与上游的不是同一个，
// 断言会静默失效——链上明明有 *Failure，As 却说没有。
func TestTypeIdentity(t *testing.T) {
	f := New(ClassTransient, "t", "瞬时")
	// 走 errors.As：只有类型同一时才能捡到。
	var got *Failure
	if !errors.As(error(f), &got) {
		t.Fatal("errors.As 应能捡到 *Failure——类型别名必须与上游同一类型")
	}
	if got.Class != ClassTransient {
		t.Errorf("类别=%s，应为 transient", got.Class)
	}
}

// TestFailureImplementsError Failure 仍是 error。
func TestFailureImplementsError(t *testing.T) {
	var err error = New(ClassPermanent, "p", "不支持")
	if err.Error() == "" {
		t.Error("错误串不应为空")
	}
}

// TestClassifyRoundTrip 分类入口的调用形态不变。
func TestClassifyRoundTrip(t *testing.T) {
	if got := Classify(ErrRateLimited); got != ClassRateLimited {
		t.Errorf("Classify(哨兵)=%s，应为 rate_limited", got)
	}
	if got := Classify(errors.New("未知")); got != ClassUnknown {
		t.Errorf("Classify(未知)=%s，应为 unknown", got)
	}
	if got := Classify(nil); got != ClassUnknown {
		t.Errorf("Classify(nil)=%s，应为 unknown", got)
	}
}

// TestClassifyAsKeepsCause ClassifyAs 保留根因。
func TestClassifyAsKeepsCause(t *testing.T) {
	cause := errors.New("根因")
	f := ClassifyAs(ClassInvalidArgument, cause)
	if f == nil {
		t.Fatal("ClassifyAs 应返回非 nil")
	}
	if f.Class != ClassInvalidArgument {
		t.Errorf("类别=%s，应为 invalid_argument", f.Class)
	}
	if !errors.Is(f, cause) {
		t.Error("根因应保留（errors.Is 可穿透）")
	}
}

// TestAsPicksFromChain As 能从包装链里捡出 Failure。
func TestAsPicksFromChain(t *testing.T) {
	wrapped := errors.Wrap(New(ClassPermissionDenied, "pd", "没权限"), "外层")
	got, ok := As(wrapped)
	if !ok || got == nil {
		t.Fatal("应能从错误链取出 *Failure")
	}
	if got.Class != ClassPermissionDenied {
		t.Errorf("类别=%s，应为 permission_denied", got.Class)
	}
}

// TestRetryableEntryPoint Retryable 入口的调用形态不变。
func TestRetryableEntryPoint(t *testing.T) {
	if !Retryable(ErrTransient) {
		t.Error("transient 应可重试")
	}
	if Retryable(ErrPermanent) {
		t.Error("permanent 不应可重试")
	}
	if Retryable(errors.New("未知")) {
		t.Error("未知错误不应可重试（保守缺省）")
	}
}

// TestPolicyOfQueryable 策略表可查。
func TestPolicyOfQueryable(t *testing.T) {
	p := PolicyOf(ClassRateLimited)
	if !p.Retryable {
		t.Error("rate_limited 应可重试")
	}
	if !p.SafeToResubmit {
		t.Error("rate_limited 应可断言重发安全")
	}
	// 未知类别按 unknown 处理（不可重试）。
	if PolicyOf(Class("no_such_class")).Retryable {
		t.Error("未知类别不应可重试")
	}
}

// TestAllClassesExported 全类别集合可导出。
func TestAllClassesExported(t *testing.T) {
	classes := AllClasses()
	if len(classes) == 0 {
		t.Fatal("类别集合不应为空")
	}
	found := map[Class]bool{}
	for _, c := range classes {
		found[c] = true
	}
	for _, want := range []Class{
		ClassTransient, ClassPermanent, ClassAuthentication, ClassNotFound,
		ClassPermissionDenied, ClassInvalidArgument, ClassRateLimited,
		ClassSelfReported, ClassUnknown,
	} {
		if !found[want] {
			t.Errorf("类别 %s 应在 AllClasses 里", want)
		}
	}
}

// TestFailureMethodsPresent Failure 的方法随类型别名一同转发。
//
// 这些方法是宿主真正在用的（WithIdempotency / Guidance 等），别名不带过来就会
// 编译不过——门面的「调用面不变」承诺就落空了。
func TestFailureMethodsPresent(t *testing.T) {
	f := New(ClassTransient, "t", "瞬时").
		WithRetryAfter(3 * time.Second).
		WithIdempotency(IdempotencyYes).
		WithRemediation("等会儿再试。").
		WithCause(errors.New("根因"))

	if f.RetryAfter != 3*time.Second {
		t.Errorf("RetryAfter=%v，应为 3s", f.RetryAfter)
	}
	if !f.SafeToResubmit() {
		t.Error("明确幂等后应可重发")
	}
	if got := f.Remediation(); got != "等会儿再试。" {
		t.Errorf("Remediation=%q，应为覆盖值", got)
	}
	if f.Guidance() == "" {
		t.Error("Guidance 不应为空")
	}
	if !f.Retryable() {
		t.Error("transient 应可重试")
	}
}

// TestIdempotencyConstants 三态常量可导出。
func TestIdempotencyConstants(t *testing.T) {
	f := New(ClassTransient, "t", "瞬时")
	if f.Idempotency != IdempotencyUnset {
		t.Error("默认应为未表态")
	}
	if IdempotencyYes.String() != "idempotent" {
		t.Errorf("IdempotencyYes=%q，应为 idempotent", IdempotencyYes.String())
	}
}

// TestSentinelsExported 哨兵可导出且可被 Classify 认出。
func TestSentinelsExported(t *testing.T) {
	cases := []struct {
		err  error
		want Class
	}{
		{ErrTransient, ClassTransient},
		{ErrPermanent, ClassPermanent},
		{ErrAuthentication, ClassAuthentication},
		{ErrNotFound, ClassNotFound},
		{ErrPermissionDenied, ClassPermissionDenied},
		{ErrInvalidArgument, ClassInvalidArgument},
		{ErrRateLimited, ClassRateLimited},
	}
	for _, c := range cases {
		if got := Classify(c.err); got != c.want {
			t.Errorf("Classify(%v)=%s，应为 %s", c.err, got, c.want)
		}
	}
}

// TestRetryableReporterInterface 自报接口可导出（宿主工具侧靠它表态）。
func TestRetryableReporterInterface(t *testing.T) {
	var r RetryableReporter = selfReporter{retryable: true}
	if !r.Retryable() {
		t.Error("自报应可重试")
	}
	// 自报路径的分类：可重试 → self_reported。
	if got := Classify(error(selfReporter{retryable: true})); got != ClassSelfReported {
		t.Errorf("自报错误→%s，应为 self_reported", got)
	}
}

// selfReporter 自报可重试的错误。
type selfReporter struct{ retryable bool }

func (s selfReporter) Error() string   { return "自报错误" }
func (s selfReporter) Retryable() bool { return s.retryable }
