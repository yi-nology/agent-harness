// resubmit.go —— 重发安全（幂等性）与面向模型的处置指引。
//
// 与 toolfail.go 分文件的原因不是分类，是**行数阈值**：新增这两维之后
// toolfail.go 会顶破 400 行的结构门禁。拆分只为满足机械阈值——
// 但拆分位置本身是按关注点切的（判定维度 vs 类别表），不是按行数硬切。
//
// 本文件回答两个此前无人回答的问题：
//
//	① 原样重发这次调用安不安全？          → SafeToResubmit
//	② 模型拿到这个失败后下一步该干什么？    → Guidance
//
// 两者都不是新功能，是把已有信息补齐成**可判定**的形态：重试决策此前只看
// 「有没有收益」，模型侧此前只能看到一句冷冰冰的失败文案。

package toolfail

import (
	"strconv"
	"strings"
	"time"
)

// Idempotency 幂等性声明：这次调用**原样重发**会不会造成重复副作用。
//
// 为什么要三态而不是 bool：宿主对「我这个工具幂等吗」的确是有知识的（只读查询
// 幂等、创建类不幂等），但**大多数失败它并没有专门想过**。用 bool 的话，"没想过"
// 与 "确实不幂等" 会被压成同一个 false，缺省口径就被静默改成了「一律不安全」；
// 用三态则缺省仍是「按类别默认」，只有显式表态才覆盖。
type Idempotency int

const (
	// IdempotencyUnset 宿主未表态（缺省）：按 PolicyOf(Class).SafeToResubmit 取类别默认。
	IdempotencyUnset Idempotency = iota

	// IdempotencyYes 明确幂等 / 本次调用未被受理：原样重发安全。
	IdempotencyYes

	// IdempotencyNo 明确有副作用 / 本次调用可能已生效：原样重发不安全。
	IdempotencyNo
)

// String 机读名（进日志与事件载荷）。
func (i Idempotency) String() string {
	switch i {
	case IdempotencyYes:
		return "idempotent"
	case IdempotencyNo:
		return "side_effecting"
	default:
		return "unspecified"
	}
}

// WithIdempotency 声明本次调用的幂等性（覆盖类别缺省），返回自身以便链式装配。
func (f *Failure) WithIdempotency(id Idempotency) *Failure {
	if f != nil {
		f.Idempotency = id
	}
	return f
}

// Idempotent 便捷构造：声明原样重发安全。
func (f *Failure) Idempotent() *Failure { return f.WithIdempotency(IdempotencyYes) }

// WithRemediation 附上该次失败专有的模型指引（覆盖类别缺省），返回自身便于链式装配。
func (f *Failure) WithRemediation(r string) *Failure {
	if f != nil {
		f.RemediationOverride = r
	}
	return f
}

// SafeToResubmit 原样重发这次调用是否安全（不会造成重复副作用）。
//
// 判定顺序：宿主逐次表态优先，未表态才取类别缺省。
//
// 与 Retryable 分开是因为两者可以相反，且相反时最危险：超时值得重试，
// 但服务端可能已经执行过，原样重发就会把一次调用变成两次副作用。
// 只看 Retryable 的调用方必然在这两种误判中选一个。
func (f *Failure) SafeToResubmit() bool {
	if f == nil {
		return false
	}
	switch f.Idempotency {
	case IdempotencyYes:
		return true
	case IdempotencyNo:
		return false
	default:
		return PolicyOf(f.Class).SafeToResubmit
	}
}

// Remediation 该次失败面向模型的处置指引：优先取 Failure 自带，否则取类别缺省。
//
// 单次覆盖存在的理由：同一类别里不同工具的下一步往往不同
// （同一个 invalid_argument，改哪个字段取决于是哪个参数非法）。
func (f *Failure) Remediation() string {
	if f == nil {
		return ""
	}
	if f.RemediationOverride != "" {
		return f.RemediationOverride
	}
	return PolicyOf(f.Class).Remediation
}

// Guidance 把分类结论渲染成一行面向模型的指引：处置指引 + 重试/重发/人工三维结论。
//
// 它是 Remediation 的**确定消费方**：判据建了没人读等于没建。本函数把
// 「发生了什么」(Class/Message) 转成「你下一步该干什么」，是模型唯一需要的形态。
//
// 为什么编排层不自己拼：拼法一旦散落到各调用点，同一类失败会在不同环节给模型
// 不同指令，而模型对指令措辞的敏感度远高于人对它的敏感度——措辞漂移会直接变成
// 行为漂移。
func (f *Failure) Guidance() string {
	if f == nil {
		return ""
	}
	p := PolicyOf(f.Class)
	var b strings.Builder
	b.WriteString(f.Remediation())

	switch {
	case p.Retryable && f.SafeToResubmit():
		b.WriteString("可以直接重试。")
	case p.Retryable:
		// 「值得重试但不确定能不能重发」是最需要说清楚的一档：不说，模型会
		// 把它当普通瞬时失败直接重发，副作用就落在这一步。
		b.WriteString("可以重试，但先确认上一次调用是否已经生效，避免重复施加副作用。")
	default:
		b.WriteString("不要重试。")
	}

	if p.SurfaceDelay && f.RetryAfter > 0 {
		b.WriteString("建议等待 ")
		b.WriteString(strconv.FormatInt(int64(f.RetryAfter/time.Second), 10))
		b.WriteString(" 秒。")
	}
	if p.NeedsOperator {
		b.WriteString("需要人工介入后才能继续。")
	}
	return b.String()
}
