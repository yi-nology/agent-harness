package sideeffect

import (
	"time"

	"github.com/oekit/pkg/errors"
)

// ErrOutcomeUnknown 动作的**结果未知**：上一次尝试可能已经生效，也可能没有。
//
// # 为什么它是一等状态而不是错误路径的边角料
//
// 远端动作是否生效，由远端说了算。本仓能看见的只有「响应没回来」——
// 而「响应没回来」同时兼容两种事实：
//
//   - 远端**没**执行（请求根本没送到）；
//   - 远端**执行了**，只是响应在回程丢了（超时、网络中断、进程被杀）。
//
// 这两种事实在调用方看来**完全一样**。此时若放行重试，就是把
// 「可能已经做过的动作」再做一遍——**重复施加的入口就在这里**。
//
// 故本包把「不知道」显式化成一个状态（OutcomeUncertain）与一个哨兵：
// 遇到它，调用方**必须先对账**（查下游事实），不得盲目重试。
//
// 这与本仓反复强调的同一条纪律一致：**没结论必须是可观测的状态，
// 不是让下游自己挑一个默认值**。默认挑「没生效」＝默认重复施加。
var ErrOutcomeUnknown = errors.New("副作用结果未知：需对账裁决后才能再动这个 Key")

// ErrNotUncertain 该 Key 不处于「结果未知」态，无需裁决。
//
// 单独一个哨兵而不是复用通用错误：调用方需要区分
// 「我要裁决的东西不存在」（可能已正常完成，也可能压根没失败过）
// 与「裁决过程本身出了问题」——两者的处置完全不同。
var ErrNotUncertain = errors.New("该 Key 不处于结果未知态")

// ErrUnsafeNotRetryable 对 RetrySafetyUnsafe 的动作，「未生效」的裁决
// **不足以**重新打开施加的门（v0.33.0 起）。
//
// # 为什么 Unknown 与 Unsafe 必须在判据上分开，而不只是注释里分开
//
// 三档重试安全性里，Unknown（未声明）与 Unsafe（明确不可重试）此前在
// **行为上完全相同**：都走未知态、都靠对账、对账说「未生效」就都放行。
// 「不可重试」只是注释里的一句意图——判据上它不存在。
//
// 但这两档的含义天差地别：Unknown 是「我不知道下游去不去重」——对账
// 查明「没生效」后重试，风险可接受；Unsafe 是「我明确知道它不幂等」——
// 「没生效」的结论来自宿主的一次**查询**，查询本身可能出错（查错对象、
// 读到旧值、并发窗口），查错的代价就是重复施加。对后者，放行必须是
// **显式决策**（Resolution.ForceRetryable），不能是「对账流程跑完了」
// 的自然结果。
//
// 遇到它怎么办：要么承认这次查询不可信、重新对账；要么确认「未生效」
// 结论可靠并接受风险后，带着 ForceRetryable=true 重新裁决。
var ErrUnsafeNotRetryable = errors.New("动作声明为不可重试：未生效的裁决不解除阻塞，需显式决策")

// ErrReconcileTaken 该 Key 已有未过期的对账占用（别人正在对账）。
//
// 同一时刻至多一个对账者。等占用过期后可重新认领（BeginReconcile 可重入）
// ——对账者死亡不永久占住这个 Key。
var ErrReconcileTaken = errors.New("该 Key 已有对账占用：同一时刻至多一个对账者")

// RetrySafety 动作在**施加失败**后的重试安全性声明。
//
// # 为什么它必须是宿主声明而不是内核推断
//
// 内核看不见远端，只能看见「响应没回来」。远端按不按幂等键去重，是
// **下游的事实**：本仓无从判断，只能要求宿主把这条事实说出来。
//
// 而「不说」的代价是不对称的：说「可安全重试」错了 → 重复施加；
// 不说（沉默）按最弱处理 → 一次对账。换句话说，**沉默的代价是一次对账，
// 误报的代价是一次重复施加**——所以沉默必须按最弱处理。
type RetrySafety int

const (
	// RetrySafetyUnknown 未声明（零值）。按最弱处理：施加失败即视为
	// **结果未知**，闸转入未知态，必须对账后才能再动这个 Key。
	//
	// 零值即最弱档，不是「默认安全」——与安全判据里「沉默按最弱处理」
	// 同一条方向：漏声明得到的是「兜不住」，不是「兜得住」。
	RetrySafetyUnknown RetrySafety = iota

	// RetrySafetySafe 该动作在施加失败后**可安全重试**：下游按幂等键
	// 去重，重试不会重复施加（如把幂等键传到了远端 API）。
	//
	// 声明这个值等于断言「我的下游会自己去重」——**断言错了就是重复施加**，
	// 故它必须是宿主的显式选择，而不是内核的乐观假设。
	RetrySafetySafe

	// RetrySafetyUnsafe 该动作在施加失败后**不可重试**：重试会重复施加。
	//
	// v0.33.0 起它与 RetrySafetyUnknown 在**判据上**分开（此前只是注释里
	// 的意图差别）：施加失败后的路径相同（都转未知态、都需对账），但
	// **裁决后分道**——Unknown 裁决「未生效」即重新可执行；Unsafe 的
	// 「未生效」裁决**不解除阻塞**（ErrUnsafeNotRetryable），必须
	// Resolution.ForceRetryable 的显式决策才放行。理由见该哨兵的注释：
	// 「不可重试」若只是注释，改一行分支就能让它静默变回「可重试」——
	// 而没有任何用例变红。
	RetrySafetyUnsafe
)

// String 人类可读的档位名。
func (r RetrySafety) String() string {
	switch r {
	case RetrySafetySafe:
		return "safe"
	case RetrySafetyUnsafe:
		return "unsafe"
	default:
		return "unknown"
	}
}

// Resolution 对账裁决：宿主查过下游事实之后给出的结论。
//
// # 为什么裁决是宿主的责任
//
// 「远端到底做没做」是**下游的事实**，内核看不见也不该猜。
// 内核能做的只有两件：把「不知道」如实标出来（OutcomeUncertain），
// 以及提供一个**唯一的裁决入口**，让裁决这件事有迹可循、不各判一套。
type Resolution struct {
	// Applied 确认动作**已生效**吗？
	//
	// true  → 闸把该 Key 记为已完成并留存 Result：后续同一 Key 的 Acquire
	//         返回 OutcomeReplayed + 该结果（重放而不是重做）。
	// false → 确认动作**未生效**：清除未知标记，该 Key 重新可被执行。
	//         RetrySafetyUnsafe 的动作例外——未生效不解除阻塞，见
	//         ForceRetryable 与 ErrUnsafeNotRetryable。
	Applied bool
	// Result 已生效时的动作结果（Applied=false 时忽略）。
	Result Result
	// Reason 裁决依据（留痕）。事后复盘时，「凭什么判定已生效」比
	// 「判定结果是什么」更有价值——前者能定位判据的薄弱处。
	Reason string
	// ForceRetryable 对 RetrySafetyUnsafe 的动作，**显式决策**「接受
	// 「未生效」结论、重新允许施加」。
	//
	// 只有 Applied == false 且动作声明为 Unsafe 时它才有意义：没有它，
	// 裁决被拒（ErrUnsafeNotRetryable），Key 保持阻塞。它是宿主在确认
	// 「对账结论可靠、重试风险已评估」后的**签字**，不是内核的默认放行。
	// 对 Unknown / Safe 动作本字段被忽略（它们本来就不阻塞）。
	ForceRetryable bool
}

// Reconcile 一次对账认领（Store.BeginReconcile 的产出）。
//
// 它回答的是「这个 Key 的对账权归谁、到什么时候」——对账是跨进程查下游的
// 慢动作，查询期间别人不得重复查；认领过期后可被重新认领，对账者死亡
// 不永久占住这个 Key。与 Lease 不是一回事：Lease 管施加侧的资源独占，
// Reconcile 管裁决侧的对账独占。
type Reconcile struct {
	// Holder 认领者身份（存储侧铸造，语义同 Lease.Holder）。
	Holder string
	// ExpiresAt 认领到期时刻（存储时钟口径）。到期后占用失效，可被重新认领。
	ExpiresAt time.Time
}
