// 跨进程续跑：CAS 存取契约 + 提交序列固化。
//
// # 缺口在哪
//
// Store 的原有契约只要求「Save 对同一 (RunID, Seq) 幂等」。幂等说的是
// **同键同结果**，它挡不住**陈旧覆盖**：进程 A 落库 seq=5，进程 B 拿着更旧的视图
// 落库 seq=3——两次写的键不同，都「幂等」，B 的慢写照样把 A 的新状态盖掉。
// 结果是恢复点静默回退，不报错，只在下一次崩溃时让已完成的步骤重跑。
//
// SeqGate 在进程内挡得住（它排的是本进程见过的最大序号），但它**只在进程内**：
// 执行一旦搬出主进程——后台服务、worker、子进程——两个进程各有各的闸，
// 谁也不知道对方写过什么。这正是编排并行化 / 常驻化之后第一个失效的点。
//
// # 跨进程的判据归存储，不归进程
//
// 正确的判据是「持久化的序号是否严格小于本次」——那是存储层的事（一条
// UPDATE ... WHERE seq < ?），内核在进程内无论怎么加锁都判不出来。所以本包
// 定义的是**契约与语义**，SQL 留给宿主。
//
// # 为什么还要 Committer（有了 CAS 不就完了？）
//
// 因为**顺序**本身也是机制，而且是最容易被写错的那部分：
//
//	gate.Accept(s)                 // 抢序号
//	if err := store.SaveIfNewer(s); err != nil {
//	    gate.Release(s.RunID, s.Seq) // ← 这一行漏掉，闸就永久停在 s.Seq
//	    return err                    //    该 run 从此卡死：重试 s.Seq 被判陈旧，
//	}                               //    跳过它又留一个永远补不回来的空洞
//
// 这段在 SeqGate 的注释里写了，但注释不是门禁——每个宿主手抄一遍，就会有一个
// 抄漏的。Committer 把它收成一个调用，宿主没有机会抄漏。

package checkpoint

import (
	"context"

	"github.com/oekit/pkg/errors"
)

// StoreCAS 进度快照的**跨进程**存取边界。
//
// 相对 Store 的唯一增量是 SaveIfNewer；Load 语义相同。宿主实现本接口即自动
// 获得跨进程的单调保证，SeqGate 退化为进程内的快速过滤（省掉绝大多数注定
// 失败的数据库往返），不再是唯一的防线。
type StoreCAS interface {
	// SaveIfNewer 仅当该 runID **已持久化的序号严格小于** s.Seq 时才写入。
	//
	// 否则必须返回 ErrStaleSeq 且**不得写入**（不得部分写、不得覆盖）。
	// 首次写入（无记录）视作已持久化序号为 0，故 seq>0 的首写总是通过。
	//
	// 并发安全：两个进程同时提交时，只有序号较大的那个成功。典型的实现形态是
	// 条件更新（SQL 的 WHERE seq < ?、带条件的 put、CAS 重试循环）。
	//
	// 注意 Seq == 0 恒视为「不参与单调判定」——它不是一次真实提交。
	SaveIfNewer(ctx context.Context, s Snapshot) error

	// Load 无记录时返回 ErrNotFound，不得返回 (零值, nil)。
	Load(ctx context.Context, runID string) (Snapshot, error)
}

// Committer 把「进程内抢序号 → 落库 → 失败回滚」固化成一个调用。
//
// 零值不可用，请用 NewCommitter。gate 为 nil 时退化为纯跨进程判定（仍安全，
// 只是少了进程内的快速过滤）；store 为 nil 时 Commit 恒失败，不静默放行。
type Committer struct {
	gate  *SeqGate
	store StoreCAS
}

// NewCommitter 组装提交器。gate 传 nil 可退化为仅依赖存储侧 CAS 的形态
// （单进程场景够用，多进程场景仍安全，只是多几次注定失败的存储往返）。
func NewCommitter(gate *SeqGate, store StoreCAS) *Committer {
	return &Committer{gate: gate, store: store}
}

// Commit 提交一次进度快照，返回 nil 表示已成功落库。
//
// 顺序是机制的一部分，调用方不需要（也无法）自己重排：
//  1. 进程内闸 Accept —— 挡掉本进程已知的陈旧写，不惊动存储；
//  2. 存储侧 SaveIfNewer —— 跨进程的最终判据；
//  3. **仅在失败时**回滚进程内闸 —— 这一步不能省，省了该 run 卡死：
//     重试 s.Seq 被判陈旧而永远写不进去，跳过它又留下一条补不回来的空洞。
//
// 反过来，成功时**绝不能**回滚：闸的当前值就是「本进程已见过的最大序号」，
// 落库成功后把它退回去，一次陈旧的 seq=3 慢写就会畅通无阻——正是本机制
// 要防的恢复点静默回退。
//
// 返回 ErrStaleSeq 表示「有更新的快照已经落地」，调用方应丢弃本次并重新 Load。
func (c *Committer) Commit(ctx context.Context, s Snapshot) error {
	if c.store == nil {
		// 未装配存储时静默成功 = 把进度记成已落库，是最坏的一种失败。
		return ErrStoreMissing
	}

	if c.gate != nil {
		if err := c.gate.Accept(s); err != nil {
			return err
		}
	}

	err := c.store.SaveIfNewer(ctx, s)
	if err != nil && c.gate != nil {
		c.gate.Release(s.RunID, s.Seq)
	}
	return err
}

// ErrStoreMissing 未装配存储。这是显式失败，不是静默成功——
// 「没接存储也返回 nil」会让调用方以为进度已落库。
var ErrStoreMissing = errors.New("未装配存储，无法提交进度快照")

// SeedFromStore 用持久化事实回灌进程内闸。宿主启动或重启后应调用一次。
//
// 为什么必须调：SeqGate 是进程内存态，重启后是空的。不回灌的话，上一进程
// 写下的「陈旧快照」（慢写终于落地）畅通无阻——这不是「闸没生效」，是
// **闸被重置后没人告知**。
//
// 该 run 从未跑过时（ErrNotFound）不算错误，直接返回：首次执行就是没有记录。
func (c *Committer) SeedFromStore(ctx context.Context, runID string) error {
	if c.store == nil {
		return ErrStoreMissing
	}
	snap, err := c.store.Load(ctx, runID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // 首次执行，本就没有可回灌的事实
		}
		return err
	}
	if c.gate != nil && snap.Seq > 0 {
		c.gate.Seed(runID, snap.Seq)
	}
	return nil
}

// Last 返回进程内闸已接受的最大序号（无 gate 时恒为 0）。
func (c *Committer) Last(runID string) uint64 {
	if c.gate == nil {
		return 0
	}
	return c.gate.Last(runID)
}
