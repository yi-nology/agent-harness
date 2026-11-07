// Package taskboard 多 agent 协调面的**存储中立契约 + 一致性判据**。
//
// # 它补的是哪一段
//
// @dispatch@ 只回答「能不能派」。派出去之后的三件事——谁负责哪个子任务、
// 它依赖谁、怎么把认领还回去——此前**没有任何机制面**：各宿主自己拿一张
// 表或一个 map 兜着，于是「同一个子任务被两个 agent 同时认领」这种错在
// 报告里读起来完全正常（两条分支都干完了活，各自都以为自己是负责人）。
//
// # 本仓给什么、不给什么
//
// **给**：① 存储中立的接口（@@Board@@）；② 快照的**不变量判据**（@@Check@@，
// 纯函数，宿主可以随时拿当前快照跑一遍）；③ 并发正确性的**可跑断言**
// （@@ConcurrentClaim@@，判据本身而不是测试风格）。
//
// **不给**：存储实现。表结构、事务边界、持久化形态是宿主资产——本仓给一份
// 参考实现，宿主就会照着改它，最后两份真相并存。这与 @checkpoint.StoreCAS@
// 的分工完全同构：契约与判据归内核，落库归宿主。
//
// # 为什么「认领」必须是原子的
//
// 认领是**读-判-写**：读当前状态、判依赖是否完成、写 owner。三段分开做，
// 两个 agent 会读到同一个「open」，各自判定自己可以认领，然后先后写入——
// 两个赢家。这与 @@commitgate@@ 的「两条并行分支都赢了」是同一种失效，
// 而这里的代价更直接：同一份工作被做两遍，或者两份互相覆盖的结果被当成一份。
//
// 故契约要求 @@Board.Claim@@ **由存储实现保证原子性**，本包只判结果
// （并发跑 N 次，成功者必须恰好 1 个）。
//
// # 释放认领凭令牌，不凭内容
//
// 与 @@commitgate@@ 的教训同源：用「任务 id + agent 名」认人判的是
// 「内容相同」，不是「同一次占用」。同名 agent（并行分支常同名）或
// 「失败 → 释放 → 重试」这条推荐路径都会撞上，于是输家放掉赢家的闸、
// 上一轮的迟到释放放掉新一轮的闸。故 @Lease@ 里的 token 由存储**铸造**，
// @@Release@@ / @@Complete@@ 只认 token。
package taskboard

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/oekit/pkg/errors"
)

// TaskID 子任务标识。
type TaskID string

// AgentID 执行者标识。
//
// **允许重复**（并行分支常同名）——这正是「凭 token 不凭内容」的理由。
type AgentID string

// State 任务状态。
type State string

const (
	// StateOpen 待认领。
	StateOpen State = "open"
	// StateClaimed 已被认领（owner + lease 必须有值）。
	StateClaimed State = "claimed"
	// StateDone 已完成（终态）。
	StateDone State = "done"
	// StateFailed 已失败（终态；但允许重新认领——失败的工作通常要重做）。
	StateFailed State = "failed"
)

// Terminal 报告该状态是否**不可再被认领**。
//
// 只有 done 是终态：failed 的任务必须能重做，否则一次失败就把子任务永久钉死，
// 而宿主的补救路径（重新派一个人）会静默失效。
func (s State) Terminal() bool { return s == StateDone }

// valid 报告状态是否在词表内。
func (s State) valid() bool {
	switch s {
	case StateOpen, StateClaimed, StateDone, StateFailed:
		return true
	}
	return false
}

// Lease 一次认领的凭证。
type Lease struct {
	// Token 由存储**铸造**的唯一令牌（释放与完成的唯一凭据）。
	Token string
	// Until 租约到期时刻（零值 = 不限期，由宿主自担）。
	//
	// 到期后其他 agent 可以接管：进程崩掉时没有它会留下一把永久占用。
	Until time.Time
}

// Task 一个子任务（值类型，可安全复制与比较）。
type Task struct {
	// ID 任务标识（全局唯一）。
	ID TaskID
	// Title 展示用标题。
	Title string
	// Deps 依赖的任务 id（全部完成才可认领）。
	Deps []TaskID
	// State 当前状态。
	State State
	// Owner 当前认领者（仅 StateClaimed 时有值）。
	Owner AgentID
	// Lease 当前租约（仅 StateClaimed 时有值）。
	Lease Lease
	// Seq 单调递增序号（宿主用它做 CAS 与留痕；本包只要求它随变更增大）。
	Seq uint64
}

// Board 协调面的存储中立契约。
//
// 实现方必须保证：
//
//   - @@Claim@@ **原子**：读-判-写不可分割（并发认领同一任务只能有一个赢家）；
//   - @@Release@@ / @@Complete@@ **只认 token**，且 token 不匹配时报
//     @@ErrLeaseLost@@（不许静默成功——静默成功会让输家以为自己的释放生效了）；
//   - @@List@@ 返回**一致快照**（不是边改边读的流）。
type Board interface {
	// List 返回当前全部任务的快照（顺序不要求，判据会排序）。
	List(ctx context.Context) ([]Task, error)
	// Claim 认领一个任务。
	//
	// 以下情形必须报错而不是「尽力而为」：不存在（ErrNotFound）/
	// 被别人活着持有（ErrTaken）/ 依赖未完成（ErrBlocked）/ 已是终态（ErrTerminal）。
	Claim(ctx context.Context, id TaskID, agent AgentID, lease Lease) (Task, error)
	// Release 归还认领（凭 token）。
	Release(ctx context.Context, id TaskID, token string) error
	// Complete 标记完成（凭 token）。
	Complete(ctx context.Context, id TaskID, token string) error
}

// 错误哨兵（宿主实现必须复用，不要各造一套：判定方按 As/Is 认人）。
var (
	// ErrNotFound 任务不存在。
	ErrNotFound = errors.New("taskboard: 任务不存在")
	// ErrTaken 任务被他人活着持有。
	ErrTaken = errors.New("taskboard: 任务已被他人认领")
	// ErrBlocked 依赖未完成。
	ErrBlocked = errors.New("taskboard: 依赖未完成")
	// ErrTerminal 任务已是终态。
	ErrTerminal = errors.New("taskboard: 任务已是终态")
	// ErrLeaseLost 令牌不匹配（租约已易主或被释放过）。
	ErrLeaseLost = errors.New("taskboard: 租约已易主")
)

// Problem 一条快照不变量违反。
type Problem struct {
	// Task 相关任务 id。
	Task TaskID
	// Code 机器可判的类别（宿主按它分流与留痕）。
	Code string
	// Text 人读的说明。
	Text string
}

// 不变量类别。
const (
	CodeEmptyID         = "empty_id"
	CodeDuplicateID     = "duplicate_id"
	CodeUnknownState    = "unknown_state"
	CodeUnknownDep      = "unknown_dep"
	CodeSelfDep         = "self_dep"
	CodeCycle           = "dep_cycle"
	CodeClaimedNoLease  = "claimed_without_lease"
	CodeLeaseNoClaim    = "lease_without_claim"
	CodeBlockedClaim    = "blocked_claim"
	CodeTerminalClaimed = "terminal_claimed"
)

// Check 判一份快照的**不变量**，按 (Task, Code) 排序返回（确定性）。
//
// 这是宿主随时能跑的「体检」：不必等出问题，配置加载、每个调度周期都可以跑；
// 它判的是**状态自身是否自相矛盾**，不判「策略对不对」。
func Check(tasks []Task) []Problem {
	var out []Problem
	byID := make(map[TaskID]Task, len(tasks))
	dup := map[TaskID]bool{}

	for _, t := range tasks {
		if t.ID == "" {
			out = append(out, Problem{Code: CodeEmptyID, Text: "任务没有 id：既无法被依赖引用，也无法被认领定位"})
			continue
		}
		if _, ok := byID[t.ID]; ok {
			dup[t.ID] = true
			continue
		}
		byID[t.ID] = t
	}
	for id := range dup {
		out = append(out, Problem{Task: id, Code: CodeDuplicateID, Text: "任务 id 重复：依赖引用会指到「其中一个」，而判定方无从知道是哪个"})
	}

	for _, t := range byID {
		if !t.State.valid() {
			out = append(out, Problem{Task: t.ID, Code: CodeUnknownState,
				Text: "状态不在词表内（open / claimed / done / failed）"})
		}
		// 认领态与租约必须**同时**成立：只有 owner 没有 token 时，
		// 释放与完成都无从验证（凭内容认人的洞又回来了）。
		if t.State == StateClaimed {
			if t.Owner == "" || t.Lease.Token == "" {
				out = append(out, Problem{Task: t.ID, Code: CodeClaimedNoLease,
					Text: "已认领但没有 owner 或租约令牌：释放与完成都无从验证"})
			}
		} else if t.Owner != "" || t.Lease.Token != "" {
			// 非认领态残留 owner/token：宿主会照着它以为「还有人负责」。
			out = append(out, Problem{Task: t.ID, Code: CodeLeaseNoClaim,
				Text: "非认领态残留 owner/租约：读起来像还有人负责，实际没人"})
		}
		if t.State.Terminal() && t.Owner != "" {
			out = append(out, Problem{Task: t.ID, Code: CodeTerminalClaimed,
				Text: "终态仍带 owner：终态不可重开，残留 owner 会让「谁做完的」与「谁还占着」混在一起"})
		}
		for _, d := range t.Deps {
			if d == t.ID {
				out = append(out, Problem{Task: t.ID, Code: CodeSelfDep, Text: "任务依赖自己：永远不会就绪"})
				continue
			}
			if d == "" {
				out = append(out, Problem{Task: t.ID, Code: CodeUnknownDep, Text: "依赖里出现空 id"})
				continue
			}
			dep, ok := byID[d]
			if !ok {
				out = append(out, Problem{Task: t.ID, Code: CodeUnknownDep, Text: "依赖指向不存在的任务: " + string(d)})
				continue
			}
			// 依赖未完成却被认领/完成：前置约束被跳过，两段工作会并行施加。
			if t.State == StateClaimed || t.State == StateDone {
				if !dep.State.Terminal() {
					out = append(out, Problem{Task: t.ID, Code: CodeBlockedClaim,
						Text: "依赖 " + string(d) + " 尚未完成，本任务却已认领或完成"})
				}
			}
		}
	}

	// 环：Kahn 拓扑排序排不空即有环。自依赖已在上面单列，这里排除自环避免重复报。
	if cycle := detectCycle(byID); len(cycle) > 0 {
		out = append(out, Problem{Code: CodeCycle,
			Text: "依赖成环（含 " + joinIDs(cycle) + "）：环上的任务永远不会就绪"})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Task != out[j].Task {
			return out[i].Task < out[j].Task
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// detectCycle 用 Kahn 算法找出参与环的任务（只报前若干个，够定位就行）。
func detectCycle(byID map[TaskID]Task) []TaskID {
	indeg := make(map[TaskID]int, len(byID))
	edges := make(map[TaskID][]TaskID, len(byID))
	for id, t := range byID {
		if _, ok := indeg[id]; !ok {
			indeg[id] = 0
		}
		for _, d := range t.Deps {
			if d == id || d == "" {
				continue
			}
			if _, ok := byID[d]; !ok {
				continue
			}
			edges[d] = append(edges[d], id)
			indeg[id]++
		}
	}
	var queue []TaskID
	for id, n := range indeg {
		if n == 0 {
			queue = append(queue, id)
		}
	}
	sort.Slice(queue, func(i, j int) bool { return queue[i] < queue[j] })
	done := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		done++
		for _, next := range edges[id] {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if done == len(indeg) {
		return nil
	}
	var left []TaskID
	for id, n := range indeg {
		if n > 0 {
			left = append(left, id)
		}
	}
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	if len(left) > 5 {
		left = left[:5]
	}
	return left
}

// joinIDs 把 id 列表拼成一段可读文本。
func joinIDs(ids []TaskID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = string(id)
	}
	return strings.Join(parts, ", ")
}
