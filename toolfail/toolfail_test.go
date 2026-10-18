package toolfail

// 夹具一律用合成名（srv-beta / item-01），不带任何领域味。

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// TestPolicyTableContract 策略表契约测试。
//
// 为什么要有这条：策略表是「调不调重试」的唯一判据，改它等于改编排语义。
// 逐条写死期望值，是为了让任何静默翻转（比如把 ClassAuthentication 的
// Retryable 改成 true）在测试里立刻变红，而不是等到线上重试风暴才发现。
func TestPolicyTableContract(t *testing.T) {
	want := map[Class]Policy{
		ClassTransient:        {Class: ClassTransient, Retryable: true, Backoff: true},
		ClassPermanent:        {Class: ClassPermanent, Retryable: false},
		ClassAuthentication:   {Class: ClassAuthentication, Retryable: false, NeedsOperator: true},
		ClassNotFound:         {Class: ClassNotFound, Retryable: false},
		ClassPermissionDenied: {Class: ClassPermissionDenied, Retryable: false, NeedsOperator: true},
		ClassInvalidArgument:  {Class: ClassInvalidArgument, Retryable: false},
		ClassRateLimited:      {Class: ClassRateLimited, Retryable: true, Backoff: true, SurfaceDelay: true, SafeToResubmit: true},
		ClassSelfReported:     {Class: ClassSelfReported, Retryable: true, Backoff: true},
		ClassUnknown:          {Class: ClassUnknown, Retryable: false},
	}
	for _, c := range AllClasses() {
		got := PolicyOf(c)
		w, ok := want[c]
		if !ok {
			t.Fatalf("类别 %q 出现在 AllClasses 但未登记策略表", c)
		}
		// Remediation 逐类比对会把整段中文文案钉进期望值，文案一改就红——
		// 它由 TestEveryClassHasRemediation 单独判定非空即可，无需逐字比对。
		got.Remediation, w.Remediation = "", ""
		if got != w {
			t.Fatalf("类别 %q 策略漂移:\n 实得 %+v\n 期望 %+v", c, got, w)
		}
	}
	if len(want) != len(AllClasses()) {
		t.Fatalf("策略表与全类别集数量不符: %d vs %d", len(want), len(AllClasses()))
	}
}

// TestRetryableSetIsExactlyThree 可重试集合的收敛面：瞬时故障、限流，外加**错误自报**一类。
// 「哪些值得重试」是最容易被随手扩大的判据，钉死它比钉死每条实现更重要。
//
// 2026-10-18 起是三个而不是两个：ClassSelfReported 由 Classify 的自报判据选出，
// 它的加入是缺陷修复的结果（可恢复的失败此前被归成不可重试），不是有人放宽了判据。
// 注意 ClassUnknown 仍在集合外——「没人表态」永远不等于「可以重试」。
func TestRetryableSetIsExactlyThree(t *testing.T) {
	var retryable []Class
	for _, c := range AllClasses() {
		if PolicyOf(c).Retryable {
			retryable = append(retryable, c)
		}
	}
	want := []Class{ClassTransient, ClassRateLimited, ClassSelfReported}
	if !reflect.DeepEqual(retryable, want) {
		t.Fatalf("可重试集合应恰为 %v, 实得 %v", want, retryable)
	}
}

// TestOperatorInterventionSet 需要人工介入的恰是「机器自己无能为力」两类。
func TestOperatorInterventionSet(t *testing.T) {
	var ops []Class
	for _, c := range AllClasses() {
		if PolicyOf(c).NeedsOperator {
			ops = append(ops, c)
		}
	}
	if len(ops) != 2 || ops[0] != ClassAuthentication || ops[1] != ClassPermissionDenied {
		t.Fatalf("需人工介入集合应恰为 {authentication, permission_denied}, 实得 %v", ops)
	}
}

// TestUnclassifiableIsNotRetryable 无法归类的错误**不得**默认成可重试。
// 这是全包最保守的一条判定：误判为可重试会把一次失败放大成 N 次。
func TestUnclassifiableIsNotRetryable(t *testing.T) {
	if got := Classify(errors.New("srv-beta 返回了一段看不懂的输出")); got != ClassUnknown {
		t.Fatalf("无法归类应得 ClassUnknown, 实得 %q", got)
	}
	if Retryable(errors.New("看不懂")) {
		t.Fatalf("无法归类的错误绝不可判为可重试")
	}
	if PolicyOf(ClassUnknown).Retryable {
		t.Fatalf("ClassUnknown 策略不可重试")
	}
	// 零值与空类同样走保守侧。
	if Classify(nil) != ClassUnknown || PolicyOf("").Retryable || Retryable(nil) {
		t.Fatalf("nil/零值应走保守侧")
	}
	if PolicyOf("never-registered").Class != ClassUnknown {
		t.Fatalf("未登记类别应回落 ClassUnknown, 实得 %q", PolicyOf("never-registered").Class)
	}
}

// TestClassifyBySentinel 哨兵集合逐个可归类。
func TestClassifyBySentinel(t *testing.T) {
	cases := []struct {
		err   error
		class Class
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
		if got := Classify(c.err); got != c.class {
			t.Fatalf("哨兵归类错: %v → %q，期望 %q", c.err, got, c.class)
		}
	}
}

// TestClassifyThroughWrapping 包装后仍可归类（errors.Is 穿透）。
func TestClassifyThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("调用 item-01 失败: %w", ErrRateLimited)
	if Classify(wrapped) != ClassRateLimited {
		t.Fatalf("穿透包装后归类错: %q", Classify(wrapped))
	}
	if !Retryable(wrapped) {
		t.Fatalf("限流经包装后仍应可重试")
	}
}

// TestClassifyPrefersFailureOverSentinel *Failure 优先：它携带完整判定信息。
func TestClassifyPrefersFailureOverSentinel(t *testing.T) {
	f := New(ClassRateLimited, "", "srv-beta 限流").WithCause(ErrRateLimited)
	wrapped := fmt.Errorf("第 2 次尝试: %w", f)
	if Classify(wrapped) != ClassRateLimited {
		t.Fatalf("应取 *Failure 的类别, 实得 %q", Classify(wrapped))
	}
	got, ok := As(wrapped)
	if !ok || got.Code != string(ClassRateLimited) || got.RetryAfter != 0 {
		t.Fatalf("As 应取出完整结构化失败: %+v ok=%v", got, ok)
	}
	if !errors.Is(wrapped, ErrRateLimited) {
		t.Fatalf("Unwrap 链应保住根因判定")
	}
}

// TestClassifyAsFallback 工具侧自有错误体系：显式归类一次，全链路可捡。
func TestClassifyAsFallback(t *testing.T) {
	own := errors.New("srv-beta: item-01 不存在")
	if Classify(own) != ClassUnknown {
		t.Fatalf("自有错误在未归类前应是 unknown")
	}
	f := ClassifyAs(ClassNotFound, own)
	if f == nil || f.Class != ClassNotFound {
		t.Fatalf("ClassifyAs 应产出指定类别: %+v", f)
	}
	if f.Code != string(ClassNotFound) {
		t.Fatalf("未给码时应取类别值, 实得 %q", f.Code)
	}
	if Classify(f) != ClassNotFound || !errors.Is(f, own) {
		t.Fatalf("归类后链路应可识别且保住根因")
	}
	if ClassifyAs(ClassTransient, nil) != nil {
		t.Fatalf("无失败时 ClassifyAs 应返回 nil")
	}
}

// TestFailureErrorFormat 错误文案须带类别与码（聚合统计按码而非文案）。
func TestFailureErrorFormat(t *testing.T) {
	f := New(ClassRateLimited, "rate_limited", "srv-beta 限流").WithRetryAfter(3 * time.Second)
	got := f.Error()
	for _, want := range []string{"rate_limited", "srv-beta 限流", "retry_after=3s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("错误文案缺少 %q: %s", want, got)
		}
	}
	if !f.Retryable() {
		t.Fatalf("限流应可重试")
	}
	if !PolicyOf(f.Class).SurfaceDelay {
		t.Fatalf("限流类应暴露延迟提示")
	}
	if New(ClassPermanent, "", "不支持的能力").Retryable() {
		t.Fatalf("永久失败不可重试")
	}
}

// TestFailureNilSafety 零值/空字段安全：nil 接收者不得 panic——
// 错误常被无防御地塞进日志链路，panic 会杀掉整个进程。
func TestFailureNilSafety(t *testing.T) {
	var nilF *Failure
	if nilF.Error() != "<nil>" || nilF.Unwrap() != nil || nilF.Retryable() {
		t.Fatalf("nil *Failure 访问应安全且不可重试")
	}
	if nilF.WithCause(errors.New("x")) != nil || nilF.WithRetryAfter(time.Second) != nil {
		t.Fatalf("nil 链式装配应返回 nil 而非 panic")
	}
	bare := New("", "", "")
	if bare.Class != ClassUnknown || bare.Code != string(ClassUnknown) {
		t.Fatalf("空类别/空码应回落 unknown, 实得 %+v", bare)
	}
	if _, ok := As(errors.New("item-01 失败")); ok {
		t.Fatalf("无 *Failure 时 As 应返回 false")
	}
	if _, ok := As(nil); ok {
		t.Fatalf("nil 上 As 应返回 false")
	}
	// 链上挂着 nil *Failure（非 nil error 接口）时不得当成有效失败——
	// errors.As 会命中该节点，此处必须显式挡掉，否则上层会读空字段。
	var typedNil error = nilF
	if got, ok := As(typedNil); ok || got != nil {
		t.Fatalf("链上 nil *Failure 不应判为有效失败: %+v ok=%v", got, ok)
	}
	if Classify(typedNil) != ClassUnknown || Retryable(typedNil) {
		t.Fatalf("链上 nil *Failure 应走保守侧")
	}
}

// TestFailureChainable 链式装配。
func TestFailureChainable(t *testing.T) {
	cause := errors.New("底层原因")
	f := New(ClassTransient, "read_timeout", "srv-beta 读超时").WithRetryAfter(500 * time.Millisecond).WithCause(cause)
	if f.RetryAfter != 500*time.Millisecond || !errors.Is(f, cause) {
		t.Fatalf("链式装配结果不符: %+v", f)
	}
}

// ── 自报判据（RetryableReporter）──────────────────────────────────────────
//
// 这一组考的是「错误自己答的话，本包认不认」。它对应一个真实缺陷：可恢复的失败
// （典型是执行门禁的「可重试 + 已顺手激活」）此前在本包一律落到 unknown，
// 通用退避循环于是把该重试的一次失败当成永久失败。

// selfReportedErr 合成的自报错误：只多一个 Retryable() 方法，不 import 本包。
//
// 用合成类型而不是引用某个真实错误：这条判据考的是「方法集被不被认」，
// 夹具一旦带上具体工具味，契约就被钉死在那个实现上。
type selfReportedErr struct {
	msg   string
	retry bool
}

func (e selfReportedErr) Error() string   { return e.msg }
func (e selfReportedErr) Retryable() bool { return e.retry }

// selfReportedWithCause 自报 + 包着哨兵：用来钉死自报与哨兵的先后。
type selfReportedWithCause struct {
	retry bool
	cause error
}

func (e selfReportedWithCause) Error() string   { return "外层自报错误" }
func (e selfReportedWithCause) Unwrap() error   { return e.cause }
func (e selfReportedWithCause) Retryable() bool { return e.retry }

// TestClassifyAcceptsSelfReportedRetryable 自报「可重试」→ ClassSelfReported 且可重试。
func TestClassifyAcceptsSelfReportedRetryable(t *testing.T) {
	err := error(selfReportedErr{msg: "item-01 的条目尚未就绪", retry: true})
	if got := Classify(err); got != ClassSelfReported {
		t.Fatalf("自报可重试应得 ClassSelfReported, 实得 %q", got)
	}
	if !Retryable(err) {
		t.Fatal("自报可重试的错误经本包判定仍须可重试")
	}
	// 包装后必须照样认：调用点几乎都会用 %w 加一层上下文，只认最外层
	// 等于这道判据对真实链路失效。
	wrapped := fmt.Errorf("第 2 次尝试: %w", err)
	if got := Classify(wrapped); got != ClassSelfReported || !Retryable(wrapped) {
		t.Fatalf("包装后自报判据应仍生效, 实得 %q", Classify(wrapped))
	}
	// 自报类必须可重试：判据选中它就是为了那个结论，查不到就等于白选。
	if !PolicyOf(ClassSelfReported).Retryable {
		t.Fatal("ClassSelfReported 的策略必须是可重试")
	}
}

// TestClassifyRejectsSelfReportedPermanent 自报「不可重试」→ 不给类别，且不可重试。
//
// 为什么给 ClassUnknown 而不是「不可重试」这一位就够：自报只答了「原样重试有没有用」，
// 没答「这属于哪一类」。硬凑一个类别（永久/未找到/权限……）等于拿一位信息编出一个类别，
// 分类反而不可信；而 ClassUnknown 的策略恰好就是不可重试，与自报同侧，一致。
// 想连类别一起表达，走 ClassifyAs 显式归类。
func TestClassifyRejectsSelfReportedPermanent(t *testing.T) {
	err := error(selfReportedErr{msg: "item-01 不在目录内", retry: false})
	if got := Classify(err); got != ClassUnknown {
		t.Fatalf("自报不可重试应得 ClassUnknown, 实得 %q", got)
	}
	if Retryable(err) {
		t.Fatal("自报不可重试的错误经本包判定必须仍不可重试")
	}
	if got := Classify(fmt.Errorf("第 2 次尝试: %w", err)); got != ClassUnknown {
		t.Fatalf("包装后自报不可重试应仍是 unknown, 实得 %q", got)
	}
	// 自报优先于哨兵：若让哨兵把它翻成可重试，本包就亲口否定了错误自己的报告，
	// 两个入口（错误自身 / Retryable(err)）会对同一个错误给出相反答案。
	if got := Classify(selfReportedWithCause{retry: false, cause: ErrRateLimited}); got != ClassUnknown {
		t.Fatalf("自报不可重试不得被哨兵翻成可重试类, 实得 %q", got)
	}
	if got := Classify(selfReportedWithCause{retry: true, cause: ErrPermanent}); got != ClassSelfReported {
		t.Fatalf("自报可重试应压过哨兵, 实得 %q", got)
	}
}

// TestClassifyPrefersExplicitFailureOverSelfReport 显式 *Failure 压过自报：
// 显式分类更具体（类别 + 码 + 等待时长齐备），自报只给一位。
func TestClassifyPrefersExplicitFailureOverSelfReport(t *testing.T) {
	inner := selfReportedErr{msg: "内层自报可重试", retry: true}
	f := New(ClassPermanent, "", "item-01 不在目录内").WithCause(inner)
	wrapped := fmt.Errorf("第 1 次尝试: %w", f)

	if got := Classify(wrapped); got != ClassPermanent {
		t.Fatalf("显式 *Failure 应压过内层自报, 实得 %q", got)
	}
	if Retryable(wrapped) {
		t.Fatal("显式判为永久失败时，不得因内层自报翻成可重试")
	}
}

// TestClassifySentinelAndUnknownUnchanged 自报判据不得改动既有的哨兵/未知两条路径。
func TestClassifySentinelAndUnknownUnchanged(t *testing.T) {
	// 不实现自报接口的错误仍旧走哨兵。
	if got := Classify(ErrRateLimited); got != ClassRateLimited || !Retryable(ErrRateLimited) {
		t.Fatalf("哨兵路径应不受影响, 实得 %q", got)
	}
	if got := Classify(fmt.Errorf("调用 item-01 失败: %w", ErrNotFound)); got != ClassNotFound {
		t.Fatalf("哨兵穿透应不受影响, 实得 %q", got)
	}
	// 什么都没表态的错误仍旧归 unknown 且不可重试（fail-closed 缺省不动摇）。
	bare := errors.New("看不懂的一段输出")
	if got := Classify(bare); got != ClassUnknown || Retryable(bare) {
		t.Fatalf("无人表态的错误应仍是 unknown 且不可重试, 实得 %q", got)
	}
	if Classify(nil) != ClassUnknown || Retryable(nil) {
		t.Fatal("nil 应仍走保守侧")
	}
}
