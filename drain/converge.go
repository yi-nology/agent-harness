// 收敛等待只有一份实现。
//
// # 为什么单独成文件
//
// Drain 与 DrainSession 的差别**只有两处**：问哪个计数（全局在途 / 某个会话在途）
// 与怎么批量取消（CancelAll / CancelOf）。收敛循环本身——先试一次、轮询到归零、
// ctx 到点强制收刀——逐字相同。
//
// 两份副本的代价不在多写十几行，而在这段代码跑在**停机路径**上：先修一份、另一份
// 不跟，症状是「全局排水修好了，会话级排水还在旧行为」，而这两条路径都只在
// 「进程正在死」的那一刻执行——出了问题最难复现，也最难判断是哪一份没跟上。
// 故收成一份：**计数与取消由调用方绑定，循环逻辑只此一处**。

package drain

import (
	"context"
	"time"
)

// pollInterval 收敛轮询间隔。
//
// 它只决定「多久看一次」，不决定排水时长——期限由 ctx 承载（见 awaitConverged）。
const pollInterval = 200 * time.Millisecond

// convergeLog 一套排水留痕文案。
//
// 全局与会话两档语境不同（会话级还要说清「其余会话不受影响」），但都只描述同一件事
// 的四个时刻：无在途轮 / 开始等待 / 自然收敛 / 超时强制收刀。文案随档位传入而不是
// 在循环里拼前缀——拼出来的句子读着像机制自述，而这四句是给运维看的结论。
type convergeLog struct {
	idle    string
	start   string
	done    string
	timeout string
}

// msgGlobal 全局排水留痕文案。
var msgGlobal = convergeLog{
	idle:    "轮次排水：无在途轮，直接进入关停",
	start:   "轮次排水开始：等在途轮自然收敛",
	done:    "轮次排水完成：在途轮全部自然收敛",
	timeout: "轮次排水超时：强制中断（留启动对账收敛）",
}

// msgSession 会话级排水留痕文案。
var msgSession = convergeLog{
	idle:    "会话排水：无在途轮，直接返回",
	start:   "会话排水开始：等该会话在途轮自然收敛",
	done:    "会话排水完成：在途轮自然收敛",
	timeout: "会话排水超时：仅强杀该会话（其余会话不受影响）",
}

// convergeTarget 一次排水的收敛目标：在途计数 + 批量取消。
//
// 两个函数都由调用方绑定到具体实现（Turns / SessionTurns）。本文件因此**不认识
// 「轮」是什么**，与 Drain 的机制定位一致。
type convergeTarget struct {
	active func() int
	cancel func(error)
}

// awaitConverged 等在途数归零；ctx 到点则强制取消仍未收敛的那些。
//
// # 期限只由 ctx 承载
//
// 本函数不自己计时。budget 只用于留痕（与部署口径对齐，见 Budget 的说明）：
// 排水时长 = 调用方给的 ctx 期限。故调用方**必须**给一个会到点的 ctx，否则本函数
// 会一直等——这正是「import 了排水却忘了给期限」那一档的症状。
//
// # 先试一次再轮询
//
// 进循环前那一次不是优化：在途轮可能在 Drain 被调用的瞬间恰好收敛完，此时根本
// 不该起 ticker，更不该在留痕里写成「开始等待」。
//
// # 不返回错误
//
// 排水失败不阻断关停链（见包注释）：收敛与否只体现在留痕里，最坏情况是「和没排水
// 一样」，不会更坏。
func awaitConverged(ctx context.Context, tgt convergeTarget, msg convergeLog, budget DrainBudget, base ...any) {
	n := tgt.active()
	if n == 0 {
		log.CtxInfo(ctx, msg.idle, base...)
		return
	}
	// 显式转回 time.Duration 再留痕：DrainBudget 是命名类型、不继承 Duration 的
	// String 方法，直接透传会让日志里的预算变成纳秒整数。
	log.CtxInfo(ctx, msg.start, withKV(base, "active", n, "budget", time.Duration(budget))...)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// 关停链二次取消与预算耗尽走同一档处置：都没时间再等了。
			if left := tgt.active(); left > 0 {
				log.CtxWarn(ctx, msg.timeout, withKV(base, "left", left, "cause", ErrDeadline.Error())...)
				tgt.cancel(ErrDeadline)
			}
			return
		case <-ticker.C:
			if tgt.active() == 0 {
				log.CtxInfo(ctx, msg.done, base...)
				return
			}
		}
	}
}

// withKV 复制 base 再追加 kv。
//
// 不复用 append(base, kv...) 的返回：那会写进调用方切片的底层数组（容量足够时），
// 而 base 是「会话标识」这类会被多处展开的上下文——一次意外写入就让后一条日志
// 带上前一条的字段，且日志本身看起来完全正常。
func withKV(base []any, kv ...any) []any {
	out := make([]any, 0, len(base)+len(kv))
	out = append(out, base...)
	return append(out, kv...)
}
