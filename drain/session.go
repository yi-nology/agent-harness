package drain

import (
	"context"
	"time"
)

// 会话寻址面。
//
// ## 为什么需要
//
// 原有 Turns 是**全局**口径：Active 数所有在途轮、CancelAll 一次杀光。
// 于是一个卡死的会话会把**所有**其他会话一起陪葬——预算耗尽时
// CancelAll(ErrDeadline) 不区分是谁把预算耗掉的，健康会话的在途轮被无辜取消。
// 用户侧表现是「别人的问题把我的话也问没了」。
//
// octop-harness 的取消按 thread_id 寻址（cancel() 只置该 thread 的事件），
// 正是为了避免这个爆炸半径。本文件把该语义补进内核，仍是**加法**：
// 原有 Turns / Drain 签名不动，宿主可以只用全局口径。

// SessionTurns 在途轮的可寻址能力面（Turns 的超集）。
//
// 与 Turns 同样的实现纪律：不得持锁调取消回调。
type SessionTurns interface {
	Turns

	// ActiveOf 指定会话的在途轮数；无该会话返回 0。
	ActiveOf(sessionID string) int

	// CancelOf 取消指定会话的在途轮（cause 传 ErrDeadline）。
	// 该会话无在途轮时**必须无操作**——不得把 cause 广播给其他会话。
	CancelOf(sessionID string, cause error)
}

// DrainSession 只排指定会话的在途轮：预算耗尽时**只**强杀该会话，
// 其余会话的在途轮原样继续。
//
// 这是 DrainSessions 的会话级版本；两者都不改全局 Drain 的语义。
// budget < 0 = 脱钩关闭态，无排水动作（与 Drain 同约定）。
func DrainSession(ctx context.Context, t SessionTurns, sessionID string, budget time.Duration) {
	if budget < 0 {
		return
	}
	if sessionID == "" {
		// 空会话标识无法寻址。直接拒绝而不是退化成 CancelAll——
		// 退化成全局取消正是本文件要消除的那种误伤。
		log.CtxWarn(ctx, "轮次排水：会话标识为空，跳过（不退化为全局取消）")
		return
	}
	active := t.ActiveOf(sessionID)
	if active == 0 {
		log.CtxInfo(ctx, "会话排水：无在途轮，直接返回", "session", sessionID)
		return
	}
	log.CtxInfo(ctx, "会话排水开始：等该会话在途轮自然收敛",
		"session", sessionID, "active", active, "budget", Budget(budget))
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if left := t.ActiveOf(sessionID); left > 0 {
				log.CtxWarn(ctx, "会话排水超时：仅强杀该会话（其余会话不受影响）",
					"session", sessionID, "left", left, "cause", ErrDeadline.Error())
				t.CancelOf(sessionID, ErrDeadline)
			}
			return
		case <-ticker.C:
			if t.ActiveOf(sessionID) == 0 {
				log.CtxInfo(ctx, "会话排水完成：在途轮自然收敛", "session", sessionID)
				return
			}
		}
	}
}
