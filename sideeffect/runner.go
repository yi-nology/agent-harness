package sideeffect

import (
	"context"
	"time"

	"github.com/oekit/pkg/errors"
)

// ErrInvalidAction 动作定义不完整（缺 Key 或 Resource）。
var ErrInvalidAction = errors.New("副作用动作定义不完整")

// ErrFenceStale 令牌已不是当前最新代（过期持有者的迟到写入）。
var ErrFenceStale = errors.New("栅栏令牌已过期")

// ErrNoStore 未装配存储。这是显式失败，不是静默成功——
// 「没接存储也返回 nil」会让调用方以为动作已施加。
var ErrNoStore = errors.New("未装配存储，无法执行副作用")

// ErrUnknownOutcome 存储侧返回了未定义的结论。
//
// 存在的原因：三态枚举里若多出一个「存储侧没告诉我到底怎么判」的取值，
// 调用方就只能在 default 分支里猜。把它显式化，调用方必须显式处理
// 「不知道」——这是本仓反复强调的：**没结论必须是可观测的状态，
// 不是让下游自己挑一个默认值**。
var ErrUnknownOutcome = errors.New("副作用闸返回未知结论")

// Runner 副作用闸的编排器：把「抢闸 → 执行 → 记账 → 释放」固化成一次调用。
//
// 存在的理由与 checkpoint.Committer 同源：四步顺序本身是机制，
// 每个宿主手抄一遍就会有一个抄漏的。
//
// 最容易抄漏的三处：
//   - **Complete 时忘了传 Fence** → 过期持有者的迟到结果能覆盖新结果；
//   - **执行失败后仍 Complete** → 一个没做成的动作被记成「做过」，
//     后续重试拿到的是空结果，而真相是「没做过」；
//   - **执行失败后不释放闸** → 失败的那次把资源占住直到 TTL，
//     后续重试全被挡成「别人占着」，动作永远做不成。
//
// 零值不可用，请用 NewRunner。
type Runner struct {
	store Store
	// ttl 本次持有的有效期。零值表示由 Store 按默认处理。
	ttl time.Duration
}

// NewRunner 组装编排器。store 为 nil 时 Execute 恒失败，不静默放行。
//
// ttl 是每次抢闸时申请的租约有效期。它是**业务判断**：宿主知道自己的动作
// 预计要多久，内核不知道。零值不表示「无限期」——那会让一道闸永远关着；
// 零值被判为非法（见 ErrInvalidAction）。
func NewRunner(store Store, ttl time.Duration) *Runner {
	return &Runner{store: store, ttl: ttl}
}

// Execute 执行一次副作用，保证「至多一次」。
//
// 流程：
//  1. 抢闸（Acquire）：一次原子判定，决定「该做 / 已做过 / 别人占着」。
//  2. 若已做过 → 返回 OutcomeReplayed 与既有结果，**一个字节都不执行**。
//  3. 若别人占着 → 返回 OutcomeContended（可重试的拒绝，不是失败）。
//  4. 否则执行动作，随后 Complete 记账，最后释放闸。
//
// 返回的 Outcome 就是三态本身——调用方不需要从「结果为空」去反推
// 「是别人占着还是我做过了」。压成一个 bool 会让这两种情况共用同一个
// false，调用方要么误把「没轮上我」当成「早已完成」，要么反过来。
//
// error 的含义：**只表示本次调用没能完成动作**（存储故障、动作失败、
// 记账失败）。OutcomeContended 不带 error——它是正常的三态之一，
// 宿主该做的是退避后重试，而不是把它当成失败放弃。
//
// **执行失败时不记账**（不 Complete）：动作没做成就是没做成，
// 下一个持有者必须能重新做它。这是「至多一次」与「至少一次」之间
// 本包选择的落点——宁可重做一次，也不把没做成的记成做成了。
//
// **执行失败时仍释放闸**：失败的那次已经把资源占住了，不释放的话
// 后续重试会被自己的残留闸挡成「别人占着」——动作从此再也做不成。
// 释放的是**自己这次**的闸（凭令牌），不会放掉别人的。
func (r *Runner) Execute(ctx context.Context, act Action, fn func(ctx context.Context) (Result, error)) (Outcome, Result, error) {
	if r == nil || r.store == nil {
		return OutcomeContended, Result{}, ErrNoStore
	}
	if act.Key == "" || act.Resource == "" {
		return OutcomeContended, Result{}, errors.Wrap(ErrInvalidAction, "副作用动作缺键或缺资源", "key", string(act.Key), "resource", act.Resource)
	}
	if r.ttl <= 0 {
		// 零 TTL 不是「无限期」，而是「没给有效期」——判为非法，
		// 因为它会让一道闸要么永远关着、要么立刻过期，两个都不是意图。
		return OutcomeContended, Result{}, errors.Wrap(ErrInvalidAction, "租约有效期必须为正", "ttl", r.ttl.String())
	}
	if fn == nil {
		return OutcomeContended, Result{}, errors.Wrap(ErrInvalidAction, "副作用动作缺执行体")
	}

	outcome, lease, prior, err := r.store.Acquire(ctx, act, r.ttl)
	if err != nil {
		return OutcomeContended, Result{}, err
	}

	switch outcome {
	case OutcomeReplayed:
		// 已完成：取回既有结果，一个字节都不执行。
		return OutcomeReplayed, prior, nil
	case OutcomeContended:
		// 资源正被他人持有且未过期：可重试的拒绝。
		// 刻意**不**返回 error——它是正常的三态之一，不是失败。
		return OutcomeContended, Result{}, nil
	case OutcomeApplied:
		// 继续：本次持有闸，去执行。
	default:
		// 存储侧返回了未定义的结论：显式失败，不猜。
		return OutcomeContended, Result{}, errors.Wrap(ErrUnknownOutcome, "存储侧返回了未知判定", "outcome", outcome.String())
	}

	res, execErr := fn(ctx)
	if execErr != nil {
		// 执行失败：**不记账**，让下一个持有者能重做。
		// 但**要释放闸**——否则这次失败把资源占住，后续重试全被挡成 contended。
		// 释放失败不掩盖原始错误：动作失败才是调用方要处理的那件事。
		_ = r.store.Release(ctx, act, lease)
		return OutcomeContended, Result{}, execErr
	}

	// 记账：带 Fence，存储侧校验令牌仍是最新代。
	recorded, err := r.store.Complete(ctx, act, lease, res)
	if err != nil {
		// 记账失败：动作**可能已经生效**，但没记上账。
		// 释放闸（若有）并如实报错——不假装成功，也不假装没做过。
		_ = r.store.Release(ctx, act, lease)
		return OutcomeContended, Result{}, err
	}
	return OutcomeApplied, recorded, nil
}

// Run 是 Execute 的便捷封装：只取结果，忽略三态。
//
// 用它就等于自愿放弃区分「我做完了」「别人做过了我拿回结果」
// 「正被别人占着」这三种情况——若调用方需要那个区分（审计、计费、
// 给用户看「已完成」而不是「正在做」），请用 Execute。
func (r *Runner) Run(ctx context.Context, act Action, fn func(ctx context.Context) (Result, error)) (Result, error) {
	outcome, res, err := r.Execute(ctx, act, fn)
	if err != nil {
		return Result{}, err
	}
	if outcome == OutcomeContended {
		return Result{}, errors.Wrap(errors.New("副作用资源正被他人持有"), "本次未执行动作", "key", string(act.Key), "resource", act.Resource)
	}
	return res, nil
}
