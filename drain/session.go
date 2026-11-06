package drain

import "context"

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
// budget 与 Drain 同型同约定：DrainBudget 的负值 = 脱钩关闭态，无排水动作
// （同一个解析入口 Budget，别在这里另开一个数）。
//
// 收敛循环与 Drain 共用同一份（converge.go），差别只有计数与取消的绑定——
// 停机路径上的并发控制若各写一份，先修的那份与后修的那份必然分叉。
func DrainSession(ctx context.Context, t SessionTurns, sessionID string, budget DrainBudget) {
	if budget < 0 {
		return
	}
	if sessionID == "" {
		// 空会话标识无法寻址。直接拒绝而不是退化成 CancelAll——
		// 退化成全局取消正是本文件要消除的那种误伤。
		log.CtxWarn(ctx, "轮次排水：会话标识为空，跳过（不退化为全局取消）")
		return
	}
	awaitConverged(ctx,
		convergeTarget{
			active: func() int { return t.ActiveOf(sessionID) },
			cancel: func(cause error) { t.CancelOf(sessionID, cause) },
		},
		msgSession, budget, "session", sessionID)
}
