// Package drain 优雅排水：计划内重启的零孤儿收刀。
//
// 问题形态：进程收到终止信号时立即取消根 ctx 并逆序关资源 —— 在途轮瞬时被杀，
// 用户侧表现为「问题问到一半没了」，而这在每次计划内重启、滚动更新、
// 容器停止时都会复现。
//
// 双件配合把「被杀」变成「自然收敛」：
//   - 轮 ctx 与根 ctx **取消脱钩**（值保留、取消传播切断）：根 ctx 死不再即时杀轮，
//     轮内的用户取消 / 超时 / 接管语义原样保留（取消源仍挂在脱钩后的基座上）；
//   - 本包 Drain 是唯一收刀点：轮询等在途轮收敛，预算耗尽才强制取消遗留轮。
//
// 收尾语义：强制路径按宿主的「进程停机」分支处理（不写终态，留给启动对账）。
// **排水失败不阻断关停链** —— Drain 恒返回 nil，最坏情况是「和没排水一样」，
// 不会更坏。
//
// 本包不关心「轮」是什么，只认 Turns 提供的在途计数与批量取消能力。
//
// ## 两个排水档位共用同一份收敛循环
//
// 全局（Drain）与会话级（DrainSession，见 session.go）只差「问哪个计数」与
// 「怎么取消」，循环本身在 converge.go——停机路径上的并发控制不该有两份。
//
// ## 日志：走 logx.Delegating，不回收调
//
// 留痕一律经 oekit/logx 的包级 Delegating logger（懒解析宿主后端）。宿主用
// logx.SetDefault 换后端即可，**不需要把日志函数传进来**——旧版 Drain 带
// logf func(string, ...any) 参数，把 printf 风格焊进了公开 API 签名：宿主每次
// 调用都要包一层 adapter，且内核无法统一日志级别与 kv 结构。现签名只收三参。
package drain

import (
	"context"
	"time"

	"github.com/oekit/logx"
	"github.com/oekit/pkg/errors"
)

// log 排水留痕。Delegating 懒解析：宿主 SetDefault 之前也能用，之后自动跟随。
var log = logx.Delegating("drain")

// ErrDeadline 排水预算耗尽的强制取消原因（宿主按此与普通停机区分留痕来源）。
var ErrDeadline = errors.New("停机排水超时，轮次强制中断")

// DefaultTimeout 缺省排水预算：覆盖绝大多数轮内余量。
//
// 部署侧的宽限期（docker stop --grace-period / k8s terminationGracePeriodSeconds）
// **须 ≥ 本值 + 启动对账余量**，否则外层 SIGKILL 会先于本包收刀，排水形同虚设。
const DefaultTimeout = 60 * time.Second

// DrainBudget 排水预算。
//
// 它是**命名类型**而不是 time.Duration 别名，这是刻意的编译期护栏：
// `context.WithTimeout(ctx, drain.Budget(x))` 因此是编译错误，而不是运行期
// 得到一个立即过期的 ctx——收尾协程秒退、一句错都不报，症状是「优雅停机
// 静默变硬切」。收尾预算一律走 ShutdownBudget（它返回普通 time.Duration）。
//
// 本类型只在 Budget / Drain / DrainSession / DetachCtx 之间流动。需要当普通
// 时长用时显式写 `time.Duration(b)`——那个转换就是一次「我知道自己在做什么」。
type DrainBudget time.Duration

// Budget 解析排水预算（0=缺省）。日志与部署口径共用此函数，避免两处各写一遍。
//
// **负值原样透传，且这是刻意的**：<0 是本包 Drain / DrainSession / DetachCtx 的
// 「整体关闭排水」配置语义。本函数**不得**把负值归一——那会把「关闭排水」
// 静默翻成「排水 60 秒」，方向由「少做」变成「多做」且配置意图被吞掉（issue #24）。
//
// **返回值不能喂给 context.WithTimeout**：一是语义不对（负值得到立即过期的 ctx），
// 二是类型不对（DrainBudget 不是 time.Duration，编译期即拒）。收尾预算一律走
// ShutdownBudget；两者的正确接线范式见 docs/USAGE.md §3.8.2 的 ③。
//
// 两个方向由 drain_test.go 的 TestBudget、TestShutdownBudget 与
// TestBudgetAndShutdownBudgetAreNotInterchangeable 钉住，
// 后者的注释写明如何反向验证（把 d == 0 改成 d <= 0 必红）。
func Budget(d time.Duration) DrainBudget {
	if d == 0 {
		return DrainBudget(DefaultTimeout)
	}
	return DrainBudget(d)
}

// ShutdownBudget 解析**停机收尾预算**（0=缺省；负值同 0=缺省）。
//
// 与 Budget 的分工：宿主的「关闭排水」配置语义普遍用负值表达（排水关闭
// ≠ 停机路径不要收尾余量），把这种配置值直接喂给 context.WithTimeout 会
// 得到一个立即过期的 ctx——优雅停机静默变硬切。停机收尾场景（给
// WithTimeout / 自定义硬上限用的预算）一律走本函数：d <= 0 都归
// DefaultTimeout。要不要做轮排水（Budget 的负值语义）仍由 Drain 自己判。
func ShutdownBudget(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	return d
}

// Turns 在途轮次的最小能力面：计数 + 批量取消。
//
// 实现须**快照后逐个调用**，不得持锁调取消回调——取消会触发收尾路径，
// 持锁重入即死锁。
type Turns interface {
	// Active 在途轮数。
	Active() int
	// CancelAll 强制取消全部在途轮（cause 传 ErrDeadline）。
	CancelAll(cause error)
}

// Drain 等在途轮自然收敛；ctx 预算耗尽（或关停链二次取消）时强制收刀。
//
// budget < 0 = 整体关闭（轮 ctx 未脱钩，天然随根 ctx 取消，本函数无操作）。
// 留痕经 logx 出往宿主后端；本函数无返回值——排水失败不阻断关停链。
//
// 期限由 ctx 承载、不由 budget 承载：budget 进留痕并决定部署侧宽限口径
// （见 DefaultTimeout），超时判定看 ctx。故调用方须传入会到点的 ctx。
func Drain(ctx context.Context, t Turns, budget DrainBudget) {
	if budget < 0 {
		return // 脱钩关闭态：无排水语义（存量行为）
	}
	awaitConverged(ctx,
		convergeTarget{active: t.Active, cancel: t.CancelAll},
		msgGlobal, budget)
}

// DetachCtx 轮 ctx 与根 ctx 取消脱钩：值保留、取消传播切断。
// budget < 0 时原样返回（未脱钩=存量语义）。
//
// 脱钩后**必须**仍有一条取消通路兜底（轮内超时 / Drain 的强制收刀），
// 否则轮会真的泄漏——这是本包唯一危险的使用前提。
func DetachCtx(ctx context.Context, budget DrainBudget) context.Context {
	if budget < 0 {
		return ctx
	}
	return context.WithoutCancel(ctx)
}
