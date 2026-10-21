package sideeffect

import (
	"context"
	"time"

	"github.com/oekit/pkg/errors"
)

// ErrInvalidAction 动作定义不完整（缺 Key 或 Resource）。
var ErrInvalidAction = errors.New("副作用动作定义不完整")

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

// ErrLeaseLost 续期时发现闸已易主（本次持有资格已失效）。
//
// 与 ErrFenceStale 分开是为了让调用方能区分两件事：
//   - ErrFenceStale：**你刚才的写入**被拒（迟到写）；
//   - ErrLeaseLost：**你不再持有**这道闸（该停止动作了）。
//
// 两者都指向「本次动作不该继续」，但一个发生在事后（写被拒），
// 一个发生在事前（还能悬崖勒马）。事前那个更值钱——它让宿主
// 有机会**在施加副作用之前**收手。
var ErrLeaseLost = errors.New("租约持有资格已失效")

// Runner 副作用闸的编排器：把「抢闸 → 检查 → 执行 → 记账 → 释放」固化成一次调用。
//
// 存在的理由与 checkpoint.Committer 同源：步骤顺序本身是机制，
// 每个宿主手抄一遍就会有一个抄漏的。
//
// 最容易抄漏的四处：
//   - **Complete 时忘了传 Fence** → 过期持有者的迟到结果能覆盖新结果；
//   - **执行失败后仍 Complete** → 一个没做成的动作被记成「做过」，
//     后续重试拿到的是空结果，而真相是「没做过」；
//   - **执行失败后不释放闸** → 失败的那次把资源占住直到 TTL，
//     后续重试全被挡成「别人占着」，动作永远做不成；
//   - **施加副作用前不检查持有资格** → 闸已易主时仍施加，与新持有者重复。
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

// Effect 动作的执行体。
//
// 拆成「准备」与「施加」两段，是为了在**两者之间**插入持有资格检查：
// 准备阶段可能很慢（调外部接口、算结果），而闸的有效期可能在这段时间里
// 到期被别人接管。此时若不加检查就进入施加阶段，就是**两份闸同时开着**
// ——本次动作与新持有者做同一件事，副作用施加两次。
//
// 两段各自可失败：
//   - prepare 失败 → 动作没开始，**未施加任何副作用**（可安全重试）；
//   - apply 失败 → 副作用**可能已部分生效**（不可假定「没发生」）。
//
// apply 返回的 Result 只在 apply 成功时才有意义。
type Effect struct {
	// Prepare 准备阶段：算结果、构造请求，但**不施加副作用**。
	// 可为 nil（不需要准备阶段）。
	Prepare func(ctx context.Context) (any, error)
	// Apply 施加阶段：真正对外部世界动手。prepared 是 Prepare 的产出。
	Apply func(ctx context.Context, prepared any) (Result, error)
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
//
// Execute 是 ExecuteWith 的单段特例（无准备阶段、无资格检查）。
// **动作耗时可能超过 TTL 时，请改用 ExecuteWith**——见那里关于
// 「危险窗口」的说明。
func (r *Runner) Execute(ctx context.Context, act Action, fn func(ctx context.Context) (Result, error)) (Outcome, Result, error) {
	return r.ExecuteWith(ctx, act, Effect{Apply: func(ctx context.Context, _ any) (Result, error) {
		return fn(ctx)
	}})
}

// ExecuteWith 执行一次带「准备 / 施加」两段与**持有资格检查**的副作用。
//
// # 为什么要在施加前检查
//
// 单段执行有一个**危险窗口**：从抢闸到施加副作用之间，如果准备耗时超过
// TTL，闸就过期并被别人接管。此时本次调用若继续施加，就是
// **两份闸同时开着**——本次动作与新持有者做同一件事，副作用施加两次。
//
// 而这个窗口**无法靠 Complete 时的令牌校验堵住**：Complete 被拒只说明
// 「你没记上账」，**副作用早已施加**。事后补救不了已发生的事。
//
// 所以检查必须落在**施加之前**：那时若发现闸已易主，本次动作还能
// 干净地放弃（未施加任何副作用），下一个持有者可以重做。
//
// # 检查做了什么
//
// 调用 `Store.Renew`：它同时做「延长有效期」与「校验令牌仍是最新代」。
// 只做前者的续期是**假续期**——闸已易主时延长的其实是别人的闸。
//
// 检查失败（ErrLeaseLost）时：**不施加任何副作用**，释放闸（如有），
// 返回 OutcomeContended + ErrLeaseLost。这是可重试的拒绝：
// 动作没做，闸已易主，宿主应当退避后**重试整个动作**
// （新一轮会重新抢闸，且幂等键保证若上次已施加则取回既有结果）。
//
// # 两段的失败语义不同
//
//   - Prepare 失败 → **未施加任何副作用**，可安全重试；
//   - Apply 失败 → 副作用**可能已部分生效**，不可假定「没发生」。
//     此时本包**不记账**（动作没记成做过），但也不会假装它没发生——
//     错误原样透出，由宿主按自己的语义决定怎么办。
func (r *Runner) ExecuteWith(ctx context.Context, act Action, eff Effect) (Outcome, Result, error) {
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
	if eff.Apply == nil {
		return OutcomeContended, Result{}, errors.Wrap(ErrInvalidAction, "副作用动作缺施加阶段")
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

	// 准备阶段：可失败，且失败时**未施加任何副作用**。
	var prepared any
	if eff.Prepare != nil {
		prepared, err = eff.Prepare(ctx)
		if err != nil {
			// 准备失败：动作没开始，释放闸（不记账——没做过的不能记成做过）。
			_ = r.store.Release(ctx, act, lease)
			return OutcomeContended, Result{}, err
		}
	}

	// 施加前的持有资格检查：这是 ExecuteWith 存在的核心理由。
	// 准备阶段可能耗时超过 TTL，闸可能已易主——此刻收手还来得及。
	renewed, err := r.store.Renew(ctx, act, lease, r.ttl)
	if err != nil {
		// 闸已易主（或续期失败）：**不施加任何副作用**，干净放弃。
		// 释放闸——但那多半已经不是自己的闸了（故 Release 可能被拒，
		// 那是正确的：不能放掉新持有者的闸）。
		_ = r.store.Release(ctx, act, lease)
		if errors.Is(err, ErrFenceStale) {
			return OutcomeContended, Result{}, errors.Wrap(ErrLeaseLost, "施加前检查发现闸已易主，本次动作放弃", "resource", act.Resource)
		}
		return OutcomeContended, Result{}, err
	}
	lease = renewed

	res, execErr := eff.Apply(ctx, prepared)
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
