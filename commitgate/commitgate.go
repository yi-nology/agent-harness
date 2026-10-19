// Package commitgate 环节终态的独占提交闸：同一环节的终态只允许被提交一次。
//
// # 它防的是什么
//
// 本仓的 statemachine 是**纯谓词**：CanTransition / MustTransition 只回答
// 「这次迁移合不合法」，不回答「有没有人已经提交过了」。当编排把一个环节拆成
// 两条并发分支推进时，两条分支会各自通过同一个纯谓词检查，各自把活干完，
// 各自提交终态——**没有任何一层会发现这件事发生过**。
//
// 这不是理论形态。eventbus 里有 EventChainParallel（并行子图起跑标记），
// 并行分支的编排语义是宿主在跑的；一旦宿主并行化，「两个分支都以为自己赢了
// 同一个环节」就从「写不出来」变成「写得出来且无人拦截」。
//
// checkpoint.SeqGate 挡不住这个：它按 RunID 排**快照序号**，只管「不倒退」。
// 两个分支先后提交 seq=5 与 seq=6 是完全合法的递增，两个都会被接受，后写的
// 覆盖先写的——而先写的那条分支的副作用早已发生。它能排序记录，排不出
// 「有两条分支都以为自己赢了」。
//
// # 为什么不是放进 statemachine / checkpoint 里
//
//   - statemachine 自我声明是「纯函数件」，零依赖。给它加带锁的有状态闸，
//     是把两个性质相反的东西塞进一个包，调用方就无法再按性质选用。
//   - checkpoint 管的是**崩溃恢复**（快照存取 + 续跑判定）；本包管的是
//     **并发执行**。两者都在说「编排推进的正确性」，但故障形态与恢复手段
//     完全不同，混在一处会让两边都变得难推理。
//
// # 与 checkpoint 的关系
//
// 两者互补，不是替代：SeqGate 管快照序号单调，本闸管终态独占。一个环节的
// 推进要先过本闸拿到终态独占权，再由 SeqGate 保证快照不倒退。
//
// # 必须成对使用：Commit 与 Release
//
// 与 SeqGate 同一处陷阱：**落库失败时必须 Release**，否则该环节的闸被永久占住，
// 这个环节从此再也无法提交终态——不是「多花一个序号」，是彻底卡死。
//
// 正确顺序（用 Acquire + ReleaseLease——凭唯一令牌释放，承诺无条件成立）：
//
//	lease, err := g.Acquire(c)              // 抢终态独占权
//	if err != nil {
//	    return err                          // 抢不到就别干后面的活
//	}
//	if err := store.Save(ctx, snap); err != nil {
//	    g.ReleaseLease(lease)               // 落库失败必须回滚
//	    return err
//	}
//
// 旧接口 Commit + Release(c Commit) 仍在，但它靠 (status, branch, seq) 认人，
// 而那三元组不唯一（并行分支同名、重试同一份快照时逐字相同）——迟到/重复的
// Release 会把别人的闸放掉。分支可能同名或会重试的场景请用上面的凭证路径。
//
// # 终态判定由宿主注入
//
// 环节状态的词表属宿主（statemachine 那一组常量只是本仓的一份示例实现），
// 故 IsTerminal 由宿主注入。本包只管「独占」，不认任何状态取值。
package commitgate

import (
	"sync"

	"github.com/oekit/pkg/errors"
)

// Ref 环节引用：一次编排运行里的一个环节。
//
// 必须是「运行 + 环节」两级，**不能只用环节名**：不同 run 里的同名环节是
// 不同对象，只按环节名去重会把第二次运行整个判成重复提交。
type Ref struct {
	RunID string
	Step  string
}

// Commit 一次状态提交。
type Commit struct {
	Ref Ref
	// Status 目标状态。非终态提交不受本闸独占约束（中间态本来就可以多次推进）。
	Status string
	// Branch 提交者标识（分支名 / agent 名），只用于留痕与排障定位。
	Branch string
	// Seq 提交序号。与 checkpoint.Snapshot.Seq 同一口径，交由 SeqGate 判单调。
	Seq uint64
}

var (
	// ErrAlreadyCommitted 该环节的终态已被提交过。
	ErrAlreadyCommitted = errors.New("环节终态已被提交")
	// ErrEmptyRef 引用缺字段。空 RunID 或空 Step 无法定位，不得静默通过。
	ErrEmptyRef = errors.New("环节引用不完整")
)

// 已提交记录：谁提交的、提交成了什么。
type record struct {
	status string
	branch string
	seq    uint64
	// token 本次占用的唯一令牌，由 Acquire 铸造。ReleaseLease 凭它精确释放。
	//
	// 为什么需要它：(status, branch, seq) 三元组**不是唯一的占用身份**——
	// 并行分支常常同名（Branch 的注释自己写明是「分支名 / agent 名」），
	// 重试同一份快照时三元组逐字相同。于是「本次 Release 是不是在放自己占的那一次」
	// 用三元组判定**判不准**，迟到/重复的 Release 会把别人的闸放掉。
	token uint64
}

// Lease 一次终态独占的凭证。
//
// 只有持有它的那次占用能被精确释放——这是「迟到的 Release 不得放掉后来者的闸」
// 这条承诺唯一能兑现的方式。
type Lease struct {
	Ref   Ref
	Token uint64
}

// Guard 终态独占提交闸。并发安全。
//
// 零值不可用，请用 NewGuard——闸的零值若表示「放行」，等于缺配置即失效。
type Guard struct {
	mu sync.Mutex
	// done 已终态提交的环节。
	done map[Ref]record
	// tokenSeq 占用令牌的自增序列（单调，永不回卷）。
	tokenSeq uint64
	// isTerminal 由宿主注入的状态词表判定；nil 时按「全部都是终态」处理，
	// 这是更保守的一侧（宁可多拦，不可在不知道词表时漏拦）。
	isTerminal func(status string) bool
}

// NewGuard 构造闸。isTerminal 判定某状态是否为终态；传 nil 表示所有提交都按终态处理。
func NewGuard(isTerminal func(status string) bool) *Guard {
	return &Guard{
		done:       make(map[Ref]record),
		isTerminal: isTerminal,
	}
}

// Seed 用**已持久化的**终态回灌闸（宿主进程重启后调用一次）。
//
// 不灌的后果与 SeqGate 同源：进程重启后闸是空的，上一进程已提交的终态被畅通无阻
// 地重复提交——正是本包要防的那件事。这是「闸被重置后没人告知」，不是「闸没生效」。
//
// 幂等：以同一条记录重复 Seed 结果相同。已存在的记录不会被更旧的覆盖。
func (g *Guard) Seed(c Commit) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.Ref.RunID == "" || c.Ref.Step == "" {
		return
	}
	if _, ok := g.done[c.Ref]; ok {
		return // 已有记录：内存里的现状比回灌的数据新，不覆盖
	}
	g.done[c.Ref] = record{status: c.Status, branch: c.Branch, seq: c.Seq}
}

// Commit 提交一次状态。非终态直接放行且不记录；终态走独占判定。
//
// 判为重复时返回 *AlreadyCommittedError，它带**先前是谁提交的**——
// 「两个分支都以为自己赢了」之所以难查，正是因为日志里只有两次提交、
// 没有「第二次本该被谁挡下」。
//
// # 与 Acquire 的取舍
//
// 本方法不返回占用凭证，因此**无法被精确释放**：调用方要交还闸，只能用
// Release(c Commit)——而 Release 靠 (status, branch, seq) 认人，那三元组
// **不唯一**（并行分支同名、重试同一份快照时逐字相同），于是迟到的 Release
// 可能把别人的闸放掉。
//
// 需要「落库失败必须回滚」且分支可能同名/重试的场景，请用
// Acquire + ReleaseLease（凭唯一令牌释放，承诺无条件成立）。
func (g *Guard) Commit(c Commit) error {
	_, err := g.Acquire(c)
	return err
}

// Acquire 抢终态独占权并返回**占用凭证**。成功时持凭证者独占该环节；
// 落库失败时用 ReleaseLease 精确交还——只有持有该凭证的那一次占用会被放掉，
// 迟到的/重复的 ReleaseLease 对别人的闸无效。
//
// 这才兑现了包头承诺：「Release 只交还自己占的那一次，否则一次迟到的 Release
// 会把后来者的独占权也一起放掉」。旧接口用 (status, branch, seq) 认人，
// 而那三元组不唯一——同名并行分支、或重试同一份快照时，三元组逐字相同，
// 于是输家的迟到 Release 会删掉赢家的记录，闸对下一次 Commit 敞开，
// 同一环节的终态被提交两次（实测 8 路并发提交出现 4 个赢家）。
func (g *Guard) Acquire(c Commit) (Lease, error) {
	if c.Ref.RunID == "" || c.Ref.Step == "" {
		return Lease{}, errors.Wrap(ErrEmptyRef, "提交缺环节引用", "run_id", c.Ref.RunID, "step", c.Ref.Step)
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	// 词表未知时按终态处理：宁可多拦一道，也不因缺配置漏放。
	// 判据写反的后果是闸被整个架空——宿主漏注入 isTerminal 时，
	// 每次提交都走「非终态直接放行」分支，本包形同不存在。
	terminal := g.isTerminal == nil || g.isTerminal(c.Status)
	if !terminal {
		return Lease{}, nil
	}
	if prev, ok := g.done[c.Ref]; ok {
		return Lease{}, &AlreadyCommittedError{
			Ref:       c.Ref,
			Prev:      prev,
			PrevSeq:   prev.seq,
			Status:    prev.status,
			Branch:    prev.branch,
			NewStatus: c.Status,
			NewBranch: c.Branch,
		}
	}
	g.tokenSeq++
	g.done[c.Ref] = record{
		status: c.Status,
		branch: c.Branch,
		seq:    c.Seq,
		token:  g.tokenSeq,
	}
	return Lease{Ref: c.Ref, Token: g.tokenSeq}, nil
}

// AlreadyCommittedError 终态重复提交。带先前提交者与先前状态。
type AlreadyCommittedError struct {
	Ref       Ref
	Prev      record
	PrevSeq   uint64
	Status    string
	Branch    string
	NewStatus string
	NewBranch string
}

// Error 实现 error。
func (e *AlreadyCommittedError) Error() string {
	return "环节 " + e.Ref.Step + "（run " + e.Ref.RunID + "）终态已被 " +
		e.Prev.branch + " 以 " + e.Prev.status + " 提交；" + e.NewBranch +
		" 的 " + e.NewStatus + " 不再受理"
}

// Unwrap 出哨兵，调用方用 errors.Is 判定。
func (e *AlreadyCommittedError) Unwrap() error { return ErrAlreadyCommitted }

// Release 交还终态独占权。**落库失败时必须调用**，否则该环节的闸被永久占住。
//
// # 这个接口认不出「自己占的那一次」
//
// 它靠 (status, branch, seq) 三元组比对——而那三元组**不唯一**：
//
//   - Branch 的注释自己写明是「分支名 / agent 名」，并行分支常常同名；
//   - 「落库失败 → Release → 重试同一份快照」这条推荐路径里，重试的三元组
//     与上一轮**逐字相同**。
//
// 于是两种真实的偷放都会发生：
//
//   - **输家的迟到 Release**：同名分支的第二次提交被拒后走错误处理调 Release，
//     三元组与赢家相同 → 把**赢家的闸删掉**；
//   - **ABA**：同三元组被重新占用后，上一轮迟到的 Release 放掉新持有者的闸。
//
// 实测 8 路同名并发提交出现 4 个赢家——本包存在的理由（防「两条并行分支都赢了」）
// 被绕过，且失效是静默的：Commit 返回 nil，宿主无从察觉。
//
// 故本接口只释放「三元组逐字相同」的占用，且**状态也要相同**——
// 这比旧实现严格（旧的只比 branch 与 seq），能挡住「不同状态的输家偷放」。
// 但它挡不住「三元组完全相同」的两次占用：那种情形调用方**根本无法区分**。
//
// # 正确做法
//
// 用 Acquire + ReleaseLease 凭唯一令牌释放，那条路径的承诺无条件成立：
//
//	lease, err := g.Acquire(c)
//	if err != nil {
//	    return err
//	}
//	if err := store.Save(ctx, snap); err != nil {
//	    g.ReleaseLease(lease) // 只有这一次占用会被放掉
//	    return err
//	}
func (g *Guard) Release(c Commit) {
	g.mu.Lock()
	defer g.mu.Unlock()
	prev, ok := g.done[c.Ref]
	if !ok {
		return
	}
	// 只交还自己占的那一次：状态、分支与序号都对得上才算。
	if prev.status != c.Status || prev.branch != c.Branch || prev.seq != c.Seq {
		return
	}
	delete(g.done, c.Ref)
}

// ReleaseLease 交还终态独占权，**凭占用凭证精确释放**。
//
// 只有令牌对得上才会放掉闸：迟到的、重复的、别人的 ReleaseLease 一律无效。
// 这是「一次迟到的 Release 不得把后来者的独占权一起放掉」唯一能兑现的形式。
//
// 凭证对不上时静默返回——与 Release 同口径（交还闸不是错误路径，
// 闸被回灌/已释放时都属正常）。
func (g *Guard) ReleaseLease(l Lease) {
	if l.Token == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	prev, ok := g.done[l.Ref]
	if !ok {
		return
	}
	if prev.token != l.Token {
		return // 不是这次占用：不动后来者的闸
	}
	delete(g.done, l.Ref)
}

// Status 查该环节当前已提交的终态。第二个返回值为是否已提交。
func (g *Guard) Status(ref Ref) (status string, branch string, committed bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.done[ref]
	if !ok {
		return "", "", false
	}
	return r.status, r.branch, true
}

// Committed 返回已终态提交的环节数（自检与留痕用）。
func (g *Guard) Committed() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.done)
}
