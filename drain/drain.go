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
// ## 日志：走 logx.Delegating，不收回调
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

// pollInterval 收敛轮询间隔。
const pollInterval = 200 * time.Millisecond

// Budget 解析排水预算（0=缺省）。日志与部署口径共用此函数，避免两处各写一遍。
func Budget(d time.Duration) time.Duration {
	if d == 0 {
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
func Drain(ctx context.Context, t Turns, budget time.Duration) {
	if budget < 0 {
		return // 脱钩关闭态：无排水语义（存量行为）
	}
	active := t.Active()
	if active == 0 {
		log.CtxInfo(ctx, "轮次排水：无在途轮，直接进入关停")
		return
	}
	log.CtxInfo(ctx, "轮次排水开始：等在途轮自然收敛",
		"active", active, "budget", Budget(budget))
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if left := t.Active(); left > 0 {
				log.CtxWarn(ctx, "轮次排水超时：强制中断（留启动对账收敛）",
					"left", left, "cause", ErrDeadline.Error())
				t.CancelAll(ErrDeadline)
			}
			return
		case <-ticker.C:
			if t.Active() == 0 {
				log.CtxInfo(ctx, "轮次排水完成：在途轮全部自然收敛")
				return
			}
		}
	}
}

// DetachCtx 轮 ctx 与根 ctx 取消脱钩：值保留、取消传播切断。
// budget < 0 时原样返回（未脱钩=存量语义）。
//
// 脱钩后**必须**仍有一条取消通路兜底（轮内超时 / Drain 的强制收刀），
// 否则轮会真的泄漏——这是本包唯一危险的使用前提。
func DetachCtx(ctx context.Context, budget time.Duration) context.Context {
	if budget < 0 {
		return ctx
	}
	return context.WithoutCancel(ctx)
}
