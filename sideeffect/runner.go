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
// 存在的原因：判据枚举里若多出一个「存储侧没告诉我到底怎么判」的取值，
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
// 最容易抄漏的六处：
//   - **Complete 时忘了传 Fence** → 过期持有者的迟到结果能覆盖新结果；
//   - **执行失败后仍 Complete** → 一个没做成的动作被记成「做过」，
//     后续重试拿到的是空结果，而真相是「没做过」；
//   - **执行失败后不释放闸** → 失败的那次把资源占住直到 TTL，
//     后续重试全被挡成「别人占着」，动作永远做不成；
//   - **施加副作用前不检查持有资格** → 闸已易主时仍施加，与新持有者重复；
//   - **执行失败后一律放行重试** → 「远端已生效但响应丢了」被重试成
//     重复施加（本包 v0.32.0 起封掉的那条，见 Action.RetrySafety）；
//   - **不把栅栏令牌交给执行器** → 下游无从拒收迟到写，栅栏只在上游侧生效。
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
// # Apply 为什么必须拿到 Lease
//
// Lease 里带着**栅栏令牌**（Fence）。它不是给你看的，是给你**传下去**的：
//
//   - 下游按令牌拒收陈旧写入 → 过期持有者的迟到写被下游挡下（见 FencedSink）；
//   - 下游按令牌/幂等键去重 → 重试不会重复施加（此时才敢声明
//     Action.RetrySafety = RetrySafetySafe）。
//
// 不给令牌，下游就无从区分「这次写」与「那次过期持有者的写」——
// 栅栏就只剩上游那半截，形同虚设。
//
// # 两段的失败语义不同
//
//   - Prepare 失败 → 动作没开始，**未施加任何副作用**（可安全重试）；
//   - Apply 失败 → 副作用**可能已生效**，是否生效取决于远端
//     （见 Action.RetrySafety）。
type Effect struct {
	// Prepare 准备阶段：算结果、构造请求，但**不施加副作用**。
	// 可为 nil（不需要准备阶段）。
	Prepare func(ctx context.Context) (any, error)
	// Apply 施加阶段：真正对外部世界动手。prepared 是 Prepare 的产出，
	// lease 是本次持有（含栅栏令牌）——把它传给下游。
	Apply func(ctx context.Context, prepared any, lease Lease) (Result, error)
}

// Execute 执行一次副作用（单段执行体）。
//
// 它是 ExecuteWith 的特例：无准备阶段，但**持有资格检查照做**
// （抢闸与施加之间仍可能隔着一次远端往返）。
//
// 执行体拿到本次持有（含栅栏令牌），好让下游能拒收陈旧写入、
// 或按幂等键去重——见 Effect 的注释。
//
// # 返回的 Outcome 是判据本身
//
// 调用方不需要从「结果为空」去反推「是别人占着还是我做过了还是上次没干成」。
// 压成一个 bool 会让这几种情况共用同一个 false，调用方要么误把「没轮上我」
// 当成「早已完成」，要么反过来把「上次可能已经做过了」当成「没做过」——
// 后者就是重复施加。
//
// error 的含义：**只表示本次调用没能完成动作**（存储故障、动作失败、
// 记账失败）。OutcomeContended 不带 error——它是正常判据之一，
// 宿主该做的是退避后重试，而不是把它当成失败放弃。
//
// # 三条失败出口的处置各不相同
//
//   - **准备失败** → 未施加任何副作用 → 可安全重试；
//   - **施加失败 + RetrySafetySafe** → 下游会去重 → 可安全重试；
//   - **施加失败 + 未声明/不安全** → 结果未知 → **禁止重试，先对账**；
//   - **记账失败** → 动作已生效但没记上账 → 结果未知 → **禁止重试，先对账**。
//
// 最后两条是本包 v0.32.0 收紧的核心：此前「执行失败 → 释放闸 → 允许重试」
// 是唯一出口，于是「远端已生效但响应丢了」会被下一次重试变成重复施加——
// 本包存在的理由被绕过，且失效是**静默**的（调用方只拿到一个普通错误）。
func (r *Runner) Execute(ctx context.Context, act Action, fn func(ctx context.Context, lease Lease) (Result, error)) (Outcome, Result, error) {
	return r.ExecuteWith(ctx, act, Effect{
		Apply: func(ctx context.Context, _ any, lease Lease) (Result, error) {
			return fn(ctx, lease)
		},
	})
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
// # 长动作的续期责任在宿主
//
// 施加阶段可能耗时超过 TTL——本包只在**施加前**检查一次，之后不再管。
// 闸在施加期间到期，就会被别人接管，两边同时做同一个动作。
// 宿主若知道动作会跑很久，应当在执行器内部**续期**
// （执行器拿得到 lease，可自行调 Store.Renew），并把 lease.Fence
// 传给下游做栅栏——两道防线都比事后补救便宜。
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
	case OutcomeUncertain:
		// 上一次尝试结果未知：可能已经生效。**不执行**，要求先对账。
		return OutcomeUncertain, Result{}, errors.Wrap(ErrOutcomeUnknown,
			"该 Key 上一次尝试结果未知，必须先对账裁决", "key", string(act.Key), "resource", act.Resource)
	case OutcomeContended:
		// 资源正被他人持有且未过期：可重试的拒绝。
		// 刻意**不**返回 error——它是正常判据之一，不是失败。
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

	// 施加：把持有（含栅栏令牌）交给执行器，让它能传给下游。
	res, execErr := eff.Apply(ctx, prepared, lease)
	if execErr != nil {
		if act.RetrySafety == RetrySafetySafe {
			// 下游按幂等键去重 ⇒ 重试不会重复施加。释放闸，允许重试。
			_ = r.store.Release(ctx, act, lease)
			return OutcomeContended, Result{}, execErr
		}
		// 结果未知：闸转入「未知」态，此 Key 在被裁决之前不得再被执行。
		markErr := r.store.MarkUncertain(ctx, act, lease, execErr.Error())
		_ = r.store.Release(ctx, act, lease)
		if markErr != nil {
			// 标记都没写进去 ⇒ 存储侧此刻不可信，**更不能**放行重试：
			// 失败形态必须是变红，不能是变绿。
			return OutcomeUncertain, Result{}, errors.Wrap(
				errors.Append(errors.Append(ErrOutcomeUnknown, execErr), markErr),
				"施加失败且未能标记结果未知（存储侧不可用），禁止重试",
				"key", string(act.Key))
		}
		// 结果未知与原始原因**都**挂进错误链：调用方既要能判
		// errors.Is(err, ErrOutcomeUnknown)（该不该对账），也要能判
		// errors.Is(err, 原始错误)（失败原因是什么）——合成一条就丢一个。
		return OutcomeUncertain, Result{}, errors.Wrap(
			errors.Append(ErrOutcomeUnknown, execErr),
			"施加失败且结果未知（可能已生效），必须先对账裁决",
			"key", string(act.Key), "resource", act.Resource)
	}

	// 记账：带 Fence，存储侧校验令牌仍是最新代。
	recorded, err := r.store.Complete(ctx, act, lease, res)
	if err != nil {
		// 记账失败：动作**已经生效**，但没记上账。
		// 若就此放行，下一次同 Key 的 Acquire 会重新授予执行权 ⇒ 重复施加。
		// 故转入「未知」态：宿主手里有结果（就是 res），裁决时可直接采信。
		markErr := r.store.MarkUncertain(ctx, act, lease, "记账失败: "+err.Error())
		_ = r.store.Release(ctx, act, lease)
		if markErr != nil {
			return OutcomeUncertain, res, errors.Wrap(
				errors.Append(errors.Append(ErrOutcomeUnknown, err), markErr),
				"动作已施加但记账失败，且未能标记结果未知，禁止重试",
				"key", string(act.Key))
		}
		return OutcomeUncertain, res, errors.Wrap(
			errors.Append(ErrOutcomeUnknown, err),
			"动作已施加但记账失败，结果未知，必须先对账裁决",
			"key", string(act.Key), "resource", act.Resource)
	}
	return OutcomeApplied, recorded, nil
}

// Reconcile 对账裁决「结果未知」的 Key。
//
// 流程：宿主先查下游事实（这次动作到底做没做、结果是什么），
// 再把结论交回来：
//
//   - res.Applied == true → 该 Key 记为已完成并留存结果；
//     之后同一 Key 的 Execute 返回 OutcomeReplayed + 该结果。
//   - res.Applied == false → 清除未知标记，该 Key 重新可被执行。
//
// 裁决**不校验栅栏**：它的依据是下游事实，不是持有权——原持有者可能已经
// 死亡、租约早已过期，而对账恰恰要在这种情况下进行。
//
// 同一 Key 的多次裁决**先到者为准**（幂等）：后到的裁决不会覆盖先到的结论。
func (r *Runner) Reconcile(ctx context.Context, act Action, res Resolution) (Outcome, Result, error) {
	if r == nil || r.store == nil {
		return OutcomeContended, Result{}, ErrNoStore
	}
	if act.Key == "" || act.Resource == "" {
		return OutcomeContended, Result{}, errors.Wrap(ErrInvalidAction, "副作用动作缺键或缺资源", "key", string(act.Key), "resource", act.Resource)
	}
	return r.store.ResolveUncertain(ctx, act, res)
}

// Run 是 Execute 的便捷封装：只取结果，忽略判据。
//
// 用它就等于自愿放弃区分「我做完了」「别人做过了我拿回结果」
// 「正被别人占着」「上次结果未知」这几种情况——若调用方需要那个区分
// （审计、计费、给用户看「已完成」而不是「正在做」），请用 Execute。
func (r *Runner) Run(ctx context.Context, act Action, fn func(ctx context.Context, lease Lease) (Result, error)) (Result, error) {
	outcome, res, err := r.Execute(ctx, act, fn)
	if err != nil {
		return Result{}, err
	}
	if outcome == OutcomeContended {
		return Result{}, errors.Wrap(errors.New("副作用资源正被他人持有"), "本次未执行动作", "key", string(act.Key), "resource", act.Resource)
	}
	return res, nil
}
