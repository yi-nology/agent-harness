// Package handoff 跨进程任务交接的**契约面**：交接点在哪、交接是否原子、
// 幂等键是什么、回执回到谁手里、交接超时怎么办。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 它补的是哪一段（issue #110）
//
// @@delegate@@ 回答「能不能交给别的运行时」，@@taskboard@@ 回答「同一张
// 板子上的认领」——但两者的认领/负责关系都**局限在同一个存储里**。跨进程
// 交接（进程 A 的一步工作移交给进程 B 续做）此前没有任何机制面：交接点
// 在哪、交接是否原子、幂等键是什么、回执回到谁手里、交接超时怎么办，
// 各宿主自己拿一个队列或一张表兜着，于是「双方都以为对方负责」与
// 「谁都不负责」这两种错在报告里读起来完全正常。
//
// # 本仓给什么、不给什么
//
// 与 taskboard 的分工同构：**给**契约（Ticket / Receipt / Store）与判据
// （Validate / Check / ConcurrentTake，纯函数）；**不给**跨进程存储实现——
// 表结构、事务边界、持久化形态是宿主资产（进程内参考实现见 local.go，
// 它只证契约可满足，不做跨进程承诺）。
//
// # 交接的四问（每问一条契约条款）
//
//  1. **交接点在哪**：交接点是 @@Store@@——它持有交接单，交接单（@@Ticket@@）
//     携带幂等键、双方身份、载荷、回执地址与时限。交接单是数据不是消息：
//     「交接」不是把任务推过一条管道，而是把**负责人字段**改写一次。
//  2. **交接是否原子**：负责人从 From 变为 To 发生在 @@Take@@ 成功的那一次
//     原子操作里（由存储实现保证，本包判结果——@@ConcurrentTake@@）。此前
//     From 负责（可 @@Cancel@@ 收回），此后 To 负责（可 @@Reply@@ 交回执），
//     不存在「双方都以为自己在负责」的中间态。
//  3. **幂等键是什么**：@@Ticket.Key@@ 由调用方按业务给（本包只做集合判定，
//     不造键）。同一 Key 的重复 @@Offer@@ 是**重放**（返回原单，不开新单）
//     ——与 sideeffect 的 apply-once 同一条纪律；超时/收回后的重新交接是
//     **新一代**（@@Generation@@ 同 Key 严格递增，与 taskboard 的 Fence
//     同源——迟到者的动作凭代次与令牌拒收）。
//  4. **回执回到谁手里**：@@Ticket.ReplyTo@@ 在挂出时**必填**——回执送到
//     「源方显式声明的地址」，不假设「谁交的回给谁」（跨进程时源方可能
//     崩溃重启，重启后的身份不一定还是交接时的身份）。领取只允许 To 本人
//     （@@ErrNotAddressee@@），回执凭存储铸造的令牌提交（@@ErrLeaseLost@@
//     ——凭令牌认人，不凭内容，commitgate / taskboard 的同源教训）。
//  5. **交接超时怎么办**：@@Ticket.TTL@@ 是**一条时限线**，覆盖领取与回执
//     两段（截止时刻由存储按**自己的**时钟推算——跨进程不假设时钟一致）。
//     到点未领取或已领取未回执，单落 @@StateExpired@@：迟到的领取与回执
//     一律拒收（fail-closed），源方重新交接即新一代。**无时限的交接单
//     是契约禁止的**（Validate 拒绝 TTL<=0）——没有截止时刻的跨进程交接
//     只会留下一把谁都不敢动的锁。
//
// # 与既有纪律的对齐（同一条纪律，不另造一套）
//
//   - 原子性判结果不判过程：@@ConcurrentTake@@ 跑一遍，赢家必须恰好 1 个
//     （taskboard.ConcurrentClaim 的同源判据）；
//   - 令牌由存储铸造、动作凭令牌不凭内容（taskboard 的 v0.33.0 教训）；
//   - 回执状态三值 done / failed / refused，completed 类结论必填、不得带
//     失败原因（delegate.Receipt 的同源纪律）；
//   - 重新交接 = 新一代（generation 单调，sideeffect.Fence 的同源口径）。
package handoff

import (
	"context"
	"time"

	"github.com/oekit/pkg/errors"
)

// Key 交接单的幂等键（宿主按业务给；同一键的重复挂出是重放不是新单）。
type Key string

// Agent 交接一方的进程/执行体标识。
type Agent string

// State 交接单状态。
type State string

const (
	// StateOffered 已挂出、待领取：负责人仍是 From。
	StateOffered State = "offered"
	// StateAccepted 已交接：负责人是 To（领取的那一次原子操作完成转移）。
	StateAccepted State = "accepted"
	// StateClosed 已收口：回执已结算（结论在 Receipt.Status）。
	StateClosed State = "closed"
	// StateExpired 超时：到点未领取，或已领取未回执（一条时限线的两种落点）。
	StateExpired State = "expired"
	// StateWithdrawn 源方收回（终态；重新交接 = 新一代）。
	StateWithdrawn State = "withdrawn"
)

// Terminal 报告该状态是否终态。
//
// expired 与 withdrawn 都是终态但**不封闭 Key**：重新交接开新一代。
// closed 也是终态——同一个业务动作做第二次是新的幂等键的事。
func (s State) Terminal() bool {
	return s == StateClosed || s == StateExpired || s == StateWithdrawn
}

// valid 报告状态是否在词表内。
func (s State) valid() bool {
	switch s {
	case StateOffered, StateAccepted, StateClosed, StateExpired, StateWithdrawn:
		return true
	}
	return false
}

// Ticket 交接单（当前代）。
type Ticket struct {
	// Key 幂等键。
	Key Key
	// From 交接发起方（负责人在领取前是它；收回权也只在它手里）。
	From Agent
	// To 指定接收方（只有它能领取——回执寻址的领取半边）。
	To Agent
	// Payload 载荷（原样携带；语义由双方约定，本包不解释）。
	Payload []byte
	// ReplyTo 回执地址（必填）：回执送到源方显式声明的地址，不假设
	// 「谁交的回给谁」。
	ReplyTo string
	// TTL 交接时限（必填，>0）：领取与回执共用一条时限线。
	//
	// 给「多长」不给「到什么时候」：截止时刻归存储按**自己的**时钟推算
	// （跨进程不假设时钟一致——taskboard.ClaimRequest 的同源纪律）。
	TTL time.Duration
	// Generation 代次（同 Key 严格递增，由存储铸造）。重新交接 = 新一代；
	// 迟到者凭代次即可被判为旧持有者。
	Generation uint64
	// Deadline 截止时刻（存储按自己的时钟推算；零值 = 无界，契约禁止）。
	Deadline time.Time
	// State 当前状态。
	State State
	// Holder 当前负责人（仅 StateAccepted 时有值）。
	Holder Agent
	// ReceiptAt 回执结算时刻（仅 StateClosed 时有值）。
	ReceiptAt time.Time
}

// Handle 一次领取的凭证（与 taskboard.Lease 同形：令牌 + 代次 + 截止）。
type Handle struct {
	// Token 存储铸造的领取令牌（回执的唯一凭据）。
	Token string
	// Fence 接管代次：与 Token 同一次原子操作铸造，等于领取时刻的
	// Ticket.Generation。
	Fence uint64
	// Until 责任期截止（= 交接单的 Deadline）。
	Until time.Time
}

// ReceiptStatus 回执状态（三值——与 delegate.Status 同一条纪律）。
type ReceiptStatus string

const (
	// ReceiptDone 交接的工作已完成。
	ReceiptDone ReceiptStatus = "done"
	// ReceiptFailed 交接的工作失败（接收方做过尝试）。
	ReceiptFailed ReceiptStatus = "failed"
	// ReceiptRefused 拒绝接收（接收方没有尝试）。
	ReceiptRefused ReceiptStatus = "refused"
)

// Receipt 一次交接的回执。
type Receipt struct {
	// Status 状态。
	Status ReceiptStatus
	// Summary 一句话结论（done 时必填）。
	Summary string
	// Failure 失败/拒绝原因（failed 与 refused 时必填）。
	Failure string
	// Artifacts 产出物引用（路径或标识，原样携带）。
	Artifacts []string
}

// Store 跨进程交接的存储中立契约。
//
// 实现方必须保证（与 taskboard.Board 的实现义务同构）：
//
//   - @@Offer@@ **原子 + 幂等**：同一 Key 的判定与写入在一次操作里完成
//     （重放返回原单 / 终态开新一代，两个分支都必须在这一次操作里定型）；
//   - @@Take@@ **原子**：负责人转移不可分割（并发领取同一单只能有一个赢家）；
//   - @@Reply@@ / @@Cancel@@ **只认身份凭据**（令牌 / 源方身份），不匹配时报
//     哨兵，不静默成功；
//   - @@Snapshot@@ 返回**一致快照**（不是边改边读的流）。
type Store interface {
	// Offer 挂出交接单。
	//
	// 同一 Key 已有未终态单 ⇒ 原样返回该单（重放，Generation 不变）；
	// 已终态 ⇒ 开新一代（Generation 递增）。Deadline 由存储按自己的
	// 时钟从 TTL 推算。不合法的交接单（Validate 不过）必须报错。
	Offer(ctx context.Context, t Ticket) (Ticket, error)
	// Take 领取交接单：负责人在这一次原子操作里从 From 变为 to。
	//
	// 非指定接收方（ErrNotAddressee）/ 已超时（ErrExpired）/ 已被领取或
	// 终态（ErrTaken / ErrAlreadySettled）必须报错。令牌与代次由存储
	// 同一次原子操作铸造。
	Take(ctx context.Context, key Key, to Agent) (Handle, error)
	// Reply 交回执（凭领取令牌）。
	//
	// 令牌不匹配 → ErrLeaseLost；已超时 → ErrExpired（迟到回执被拒，
	// 责任期已过）；回执不自洽 → 报错且状态不动。成功 ⇒ 单落 StateClosed。
	Reply(ctx context.Context, key Key, token string, r Receipt) error
	// Cancel 源方收回（仅 StateOffered）。
	//
	// 非 From（ErrNotSource）/ 已被领取（ErrTaken）/ 已终态
	// （ErrAlreadySettled）必须报错。成功 ⇒ 单落 StateWithdrawn。
	Cancel(ctx context.Context, key Key, by Agent) error
	// Snapshot 当前代的全部交接单（一致快照）。
	Snapshot(ctx context.Context) ([]Ticket, error)
}

// 错误哨兵（宿主实现必须复用，不要各造一套：判定方按 errors.Is 认人）。
var (
	// ErrUnknownKey 交接单不存在。
	ErrUnknownKey = errors.New("handoff: 交接单不存在")
	// ErrTaken 已被领取（Cancel 时撞上，或并发 Take 的输家）。
	ErrTaken = errors.New("handoff: 交接单已被领取")
	// ErrExpired 交接单已超时限（迟到的领取与回执一律拒收）。
	ErrExpired = errors.New("handoff: 交接单已超时限")
	// ErrNotAddressee 领取者不是指定接收方（回执寻址的领取半边）。
	ErrNotAddressee = errors.New("handoff: 领取者不是指定接收方")
	// ErrNotSource 收回者不是源方。
	ErrNotSource = errors.New("handoff: 收回者不是交接发起方")
	// ErrLeaseLost 领取令牌不匹配（回执凭令牌认人，不凭内容）。
	ErrLeaseLost = errors.New("handoff: 领取令牌不匹配")
	// ErrAlreadySettled 交接单已是终态。
	ErrAlreadySettled = errors.New("handoff: 交接单已是终态")
)
