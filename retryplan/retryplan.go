// Package retryplan 重试处置决策：把「该不该重试」与「重试会不会重复施加」
// 两问合成**一条可执行**的结论。
//
// # 它补的是哪一段
//
// 上游 `guardx/toolfail` 判的是**处置层语义**：这次失败该不该重试、能不能
// 原样重发、要不要人工。它对「超时类」给出的 Guidance 是：
//
//	「可以重试，但先确认上一次调用是否已经生效，避免重复施加副作用。」
//
// 这句话**只说清了要做什么，没说清怎么做到**。「确认上一次是否已生效」
// 需要一个机制——而那个机制本仓已经有了：`sideeffect` 的幂等键
// （同一 Key 重放 → 取回既有结果，而不是重复施加）。
//
// 两者此前是**并列的独立机制**，接线的活儿全推给宿主。后果不是「少个功能」，
// 而是**每个宿主各写一遍组合逻辑**，而组合逻辑里恰好藏着一个必须全局一致的判定：
//
//	「这次重试，到底是原样重发，还是走幂等键重放？」
//
// 这与 `guardset` 面对的是同一类问题（守卫怎么装配），故本包与它同族：
// **组合逻辑归内核，判定归各自机制**。
//
// # 三问与一条结论
//
// 本包把三个问题合成一条决策：
//
//	① 这次失败值得重试吗？          （toolfail 判）
//	② 原样重发安全吗？              （toolfail 判）
//	③ 若不安全，重试该走什么路径？  （sideeffect 判）
//
// 结论是 `Decision`，它同时告诉宿主**做什么**与**为什么**。
//
// # 关键取舍一：「可重试但不可原样重发」必须落到具体路径
//
// 这是最容易漏的一档。`toolfail` 把它标成「值得重试但不确定能不能重发」，
// 若接线只把它翻译成「重试」（不带路径），宿主面对超时类失败时
// 仍会原样重发——于是「先确认是否已生效」这句提醒被丢掉了，
// 副作用落在这一步。
//
// 故 `Decision` 在这一档给出的是 **`PathReplayed`**：
// **重试必须走幂等键**，由 `sideeffect` 保证「至多一次」。
// 没有幂等键可用时（`Options.WithoutIdempotency`），决策降级为
// `PathNone`（不重试）——而不是退化成原样重发。
//
// # 关键取舍二：没有幂等键就不许「猜着重试」
//
// 「值得重试但不确定能不能重发」+「没有幂等键」= **不能重试**。
// 此时正确的处置是如实失败交上层降级/人工，而不是赌一次。
// 赌的代价是一次重复副作用——正是本包存在的理由要防的那件事。
//
// # 关键取舍三：失败类别的判定归上游，本包不重新分类
//
// 本包**不重新判定**失败是什么类别（那是 `toolfail` 的事，判定口径必须
// 跨宿主一致）。它只把既有判定**组合**成决策。
//
// # 与 sideeffect 的关系
//
// 本包**不执行**重试，也不持有存储。它只产出「该走哪条路径」的决策；
// 执行是宿主的接缝：`PathReplayed` 对应 `sideeffect.Runner.Execute` 用
// **同一个幂等键**再跑一次。
//
// 本包对 `sideeffect` **零依赖**——它只说「该走幂等键路径」，
// 不关心幂等键怎么实现、存储怎么落。那是 `sideeffect` 的事。
// 两者是**契约上的衔接**，不是代码上的耦合。
package retryplan

import (
	"time"

	"github.com/oagentkit/guardx/toolfail"
)

// Path 重试路径。
type Path int

const (
	// PathNone 不重试。失败是终态，或重试没有意义/没有安全路径。
	PathNone Path = iota
	// PathDirect 原样重发。判定为「原样重发安全」（幂等或确认未受理）。
	PathDirect
	// PathReplayed 走幂等键重放。判定为「值得重试但原样重发不安全」，
	// 且幂等键可用——由幂等机制保证「至多一次」。
	PathReplayed
)

func (p Path) String() string {
	switch p {
	case PathNone:
		return "none"
	case PathDirect:
		return "direct"
	case PathReplayed:
		return "replayed"
	default:
		return "unknown"
	}
}

// Decision 一条重试处置决策。
type Decision struct {
	// Path 该走哪条路径（见 Path 的三个取值）。
	Path Path
	// Retryable 该失败本身是否值得重试（toolfail 的判定）。
	//
	// 与 Path 分开：PathNone + Retryable=true 是可能的
	// （值得重试，但没有安全的重试路径——见 §关键取舍二）。
	// 压成一个字段会把这两件事混成「不可重试」。
	Retryable bool
	// Backoff 是否建议退避后重试（限流类：立刻重试会放大拥塞）。
	Backoff bool
	// RetryAfter 服务端给的等待时长（0 = 无此信息）。
	RetryAfter time.Duration
	// NeedsOperator 是否需要人工介入（改凭证 / 放权限）。
	NeedsOperator bool
	// Reason 面向人的判定理由（宿主可原样进日志）。
	//
	// 与 `toolfail.Guidance()` 分开：Guidance 是给**模型**看的
	// （要可执行、不含现场描述），Reason 是给**人**看的
	// （要说清「为什么不能重试」这类结论）。
	Reason string
}

// Options 决策的输入条件。
type Options struct {
	// WithoutIdempotency 声明本次调用**没有**幂等键可用。
	//
	// 缺省（false）表示有幂等键可用——此时「可重试但不可原样重发」
	// 会被判为 PathReplayed。
	//
	// 置 true 时，该档降级为 PathNone：值得重试，但没有安全的重试路径
	// （见 §关键取舍二：宁可如实失败，不赌一次重复副作用）。
	WithoutIdempotency bool
}

// Decide 依据一次失败与当前条件，产出重试处置决策。
//
// err 为 nil 时返回 PathNone（没有失败可处置）。
//
// 判定完全基于 `toolfail` 的既有结论，本包不重新分类失败。
func Decide(err error, opts Options) Decision {
	if err == nil {
		return Decision{Path: PathNone, Reason: "无失败，无需处置"}
	}

	cls := toolfail.Classify(err)
	policy := toolfail.PolicyOf(cls)

	d := Decision{
		Retryable:     policy.Retryable,
		Backoff:       policy.Backoff,
		RetryAfter:    retryAfterOf(err),
		NeedsOperator: policy.NeedsOperator,
	}

	// 取逐次表态（宿主对该次调用的幂等性声明），它优先于类别缺省。
	f, ok := toolfail.As(err)
	safeToResubmit := policy.SafeToResubmit
	if ok {
		safeToResubmit = f.SafeToResubmit()
	}

	switch {
	case !policy.Retryable:
		// 不可重试：终态，换处置路径（改参数 / 找人工 / 收口）。
		d.Path = PathNone
		d.Reason = "该失败不可重试：" + string(cls)
	case safeToResubmit:
		// 可重试且原样重发安全：直接重发。
		d.Path = PathDirect
		d.Reason = "可重试，且原样重发安全"
	case opts.WithoutIdempotency:
		// 可重试但原样重发不安全，且没有幂等键兜底 → 不能重试。
		// 宁可如实失败交上层降级/人工，不赌一次重复副作用。
		d.Path = PathNone
		d.Reason = "值得重试，但原样重发不安全且无幂等键兜底，不能重试"
	default:
		// 可重试但原样重发不安全，且幂等键可用 → 走幂等键重放。
		d.Path = PathReplayed
		d.Reason = "值得重试，但原样重发不安全；须走幂等键重放以保证至多一次"
	}

	return d
}

// retryAfterOf 取服务端给的等待时长（若有）。
func retryAfterOf(err error) time.Duration {
	f, ok := toolfail.As(err)
	if !ok || f == nil {
		return 0
	}
	return f.RetryAfter
}
