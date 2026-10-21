package retryplan

import (
	"testing"
	"time"

	"github.com/oagentkit/guardx/toolfail"

	"github.com/oekit/pkg/errors"
)

// 合成数据：用中性词面构造失败，不带任何领域味。

// TestUnclassifiableIsNotRetryable 钉住：归不出类别的失败一律不重试。
//
// 这是「不可归类一律不可重试」的保守侧：判不出性质时，
// 赌它可重试 = 可能对永久失败死循环。
func TestUnclassifiableIsNotRetryable(t *testing.T) {
	d := Decide(errors.New("某种说不清的失败"), Options{})
	if d.Path != PathNone {
		t.Fatalf("归不出类别应判为不重试，实得 %v", d.Path)
	}
	if d.Retryable {
		t.Fatal("归不出类别不该判为可重试")
	}
}

// TestPermanentFailureIsNotRetryable 钉住：永久失败不重试。
func TestPermanentFailureIsNotRetryable(t *testing.T) {
	err := toolfail.New(toolfail.ClassPermanent, "code-1", "这类调用不支持")
	d := Decide(err, Options{})
	if d.Path != PathNone {
		t.Fatalf("永久失败应判为不重试，实得 %v", d.Path)
	}
	if d.Retryable {
		t.Fatal("永久失败不该判为可重试")
	}
}

// TestIdempotentFailureRetriesDirectly 钉住：明确幂等的失败 → 原样重发。
//
// 幂等性**逐次表态**优先于类别缺省——宿主明确说了「这次幂等」时，
// 就该按幂等处理，不必绕道幂等键。
func TestIdempotentFailureRetriesDirectly(t *testing.T) {
	err := toolfail.New(toolfail.ClassTransient, "code-2", "暂时不可用").Idempotent()
	d := Decide(err, Options{})
	if d.Path != PathDirect {
		t.Fatalf("明确幂等的瞬时失败应判为原样重发，实得 %v（%s）", d.Path, d.Reason)
	}
	if !d.Retryable {
		t.Fatal("瞬时失败应判为可重试")
	}
}

// TestNonIdempotentFailureNeedsReplay 钉住**最容易漏的一档**：
// 可重试但原样重发不安全 → 走幂等键重放。
//
// 误用形态：这一档被判成 PathDirect（原样重发）→
// 「先确认上一次是否已生效」这句提醒被丢掉，副作用落在这一步。
func TestNonIdempotentFailureNeedsReplay(t *testing.T) {
	err := toolfail.New(toolfail.ClassTransient, "code-3", "暂时不可用")
	d := Decide(err, Options{})
	if d.Path != PathReplayed {
		t.Fatalf("可重试但原样重发不安全的瞬时失败应判为幂等键重放，实得 %v（%s）", d.Path, d.Reason)
	}
	if !d.Retryable {
		t.Fatal("瞬时失败应判为可重试")
	}
	if d.Reason == "" {
		t.Fatal("决策应带理由")
	}
}

// TestWithoutIdempotencyBlocksRetry 钉住：可重试但无幂等键兜底 → **不重试**。
//
// 这是「宁可如实失败，不赌一次重复副作用」的落点。
// 误用形态：降级成 PathDirect → 原样重发 → 重复副作用。
func TestWithoutIdempotencyBlocksRetry(t *testing.T) {
	err := toolfail.New(toolfail.ClassTransient, "code-4", "暂时不可用")
	d := Decide(err, Options{WithoutIdempotency: true})
	if d.Path != PathNone {
		t.Fatalf("无幂等键兜底时应判为不重试，实得 %v（%s）", d.Path, d.Reason)
	}
	// 但「值得重试」这个事实不能被抹掉——宿主据此可以换路径（人工/改参）。
	if !d.Retryable {
		t.Fatal("仍应保留「值得重试」的判定（宿主可据此换处置路径）")
	}
}

// TestWithoutIdempotencyStillAllowsSafeDirect 钉住：无幂等键**不**一刀切禁掉重试。
//
// 明确幂等的失败原样重发是安全的，与有没有幂等键无关。
// 误用形态：把 WithoutIdempotency 当成「一律不重试」→
// 连明确幂等的失败也不重试，白白放弃合法重试。
func TestWithoutIdempotencyStillAllowsSafeDirect(t *testing.T) {
	err := toolfail.New(toolfail.ClassTransient, "code-5", "暂时不可用").Idempotent()
	d := Decide(err, Options{WithoutIdempotency: true})
	if d.Path != PathDirect {
		t.Fatalf("明确幂等的失败原样重发安全，无幂等键也不该禁掉，实得 %v（%s）", d.Path, d.Reason)
	}
}

// TestRateLimitedCarriesBackoff 钉住：限流类带退避与等待时长。
//
// 立刻重试限流只会立刻再被限流，把一次退避能吸收的拥塞放大成重试风暴。
func TestRateLimitedCarriesBackoff(t *testing.T) {
	err := toolfail.New(toolfail.ClassRateLimited, "code-6", "被限流").WithRetryAfter(3 * time.Second)
	d := Decide(err, Options{})
	if !d.Backoff {
		t.Fatal("限流类应建议退避")
	}
	if d.RetryAfter != 3*time.Second {
		t.Fatalf("应带出等待时长 3s，实得 %v", d.RetryAfter)
	}
}

// TestOperatorNeededCarriesFlag 钉住：需人工介入的失败要带出该标志。
func TestOperatorNeededCarriesFlag(t *testing.T) {
	err := toolfail.New(toolfail.ClassAuthentication, "code-7", "凭证失效")
	d := Decide(err, Options{})
	if !d.NeedsOperator {
		t.Fatal("鉴权类失败应标记需人工介入")
	}
}

// TestNilErrorIsNoop 钉住：没有失败时返回「不重试」，不编造结论。
func TestNilErrorIsNoop(t *testing.T) {
	d := Decide(nil, Options{})
	if d.Path != PathNone {
		t.Fatalf("无失败应判为不重试，实得 %v", d.Path)
	}
}

// TestPathString 钉住：三态的可读名（诊断与日志面，不参与判定）。
func TestPathString(t *testing.T) {
	cases := map[Path]string{
		PathNone:     "none",
		PathDirect:   "direct",
		PathReplayed: "replayed",
		Path(42):     "unknown",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Fatalf("Path(%d).String() = %q，应为 %q", int(p), got, want)
		}
	}
}
