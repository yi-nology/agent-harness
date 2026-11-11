// contention.go —— 认领的并发正确性**判据**（判据，不是测试风格）。
//
// # 为什么判据要长在包里而不是只在测试里
//
// 「并发认领同一任务只能有一个赢家」是 @@Board@@ 的**契约条款**，不是某个
// 实现的性质。写在测试里 → 每个宿主各写一份、各判一套；写成导出的判据 →
// 宿主的存储实现只要被这个函数跑一遍就算验过契约（@sidetest@ 的同一范式）。
//
// # 判据怎么判
//
// 让 N 个 agent 在同一个起跑线上同时 @@Claim@@ 同一个任务：
//
//   - 成功者必须**恰好 1 个**（0 个说明谁都认领不了；>1 个说明存储没做到原子）；
//   - 失败者的错误必须是 @@ErrTaken@@（不能是 ErrBlocked / ErrTerminal 之类——
//     那说明这些 agent 压根没走到争用那一步，这次判定没测到想测的东西）；
//   - 任何**其它**错误都是基础设施故障（要显式报出来，不能混进「输家」里）。
//
// 第三条容易被忘掉：把「数据库连不上」也算成「输家」，判据就会在存储坏掉时
// **变绿**——它测到的是「没有人赢」而不是「只有一个赢家」，而两者都符合
// 「winners <= 1」。
//
// # 第二条判据：重复令牌（issue #97）
//
// @@DuplicateTokenSubmit@ 让多个 worker 同时用**同一个**令牌提交，成功者
// 必须**恰好 1 个**——它撞的是「校验与写入分离」这一形态：各拿各的令牌的
// 并发认领（@@ConcurrentClaim@@）撞不出它，必须让同一令牌的使用者正面相撞。
package taskboard

import (
	"context"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"
)

// ConcurrentClaim 让多个 agent 同时认领同一个任务，返回成功者数量。
//
// ttl 是本次认领申请的租约有效期（每个 agent 申请同一把长度）——
// **令牌由存储铸造**（见 Board.Claim），本判据不碰令牌。
//
// 返回的 error 只表示**基础设施故障或契约违反**，不表示「有人输」——
// 输家是正常结果。
func ConcurrentClaim(ctx context.Context, b Board, id TaskID, agents []AgentID, ttl time.Duration) (int, error) {
	if len(agents) == 0 {
		return 0, errors.New("taskboard: 并发认领判据需要至少一个 agent")
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		fatal   []error
		blocked int
	)
	start := make(chan struct{})
	for _, a := range agents {
		agent := a
		wg.Add(1)
		// 走 async.GoSafe 而非裸 go func：本仓门禁禁止裸 goroutine
		// （panic 无法被外层 recover 捕获，会直接杀掉宿主进程）。
		async.GoSafe(func() {
			defer wg.Done()
			<-start
			_, err := b.Claim(ctx, id, agent, ClaimRequest{TTL: ttl})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, ErrTaken):
				// 正常的输家：这说明争用真的发生了。
			case errors.Is(err, ErrBlocked):
				// 依赖未完成不是「输」——它意味着这次判定没走到争用那一步。
				blocked++
			default:
				fatal = append(fatal, err)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(fatal) > 0 {
		return winners, errors.Wrap(fatal[0], "taskboard: 并发认领出现非争用类错误（判据无法给出结论）")
	}
	if blocked > 0 {
		return winners, errors.Errorf("taskboard: 有 %d 个 agent 因依赖未完成被拒——"+
			"这次并发判定没测到争用（请只在就绪任务上跑本判据）", blocked)
	}
	return winners, nil
}

// DuplicateTokenSubmit 让多个 worker 同时用**同一个**令牌提交同一任务，
// 返回成功者数量（必须恰好 1）。
//
// # 它补的是哪一面（issue #97）
//
// 「释放/完成只认 token」这条契约有一个并发缺口：若存储把 Complete 的
// **校验**与**写入**拆成两步（先查「claimed 且 token 匹配」、再写 done，
// 中间没有事务），两个同时用同一令牌的提交者会**都**通过校验、先后写入
// ——两个都成功，等于「只认 token」名存实亡。4 个 agent 各拿各的令牌
// 撞不出这个形态；必须让**同一令牌**的并发使用者正面相撞。
//
// # 判据怎么判
//
// 本判据自己先认领一次（令牌由存储铸造），再让 workers 个提交者**同时**
// 用该令牌调 Complete：
//
//   - 成功者必须**恰好 1 个**（>1 说明校验与写入没有原子性；0 说明连真持有者
//     都被拒了，契约同样破）；
//   - 其余必须是 @@ErrLeaseLost@@（正常输家）；任何**其它**错误按基础设施故障
//     显式报出，不混进「输家」里——把存储故障算成输家，判据会在存储坏掉时
//     变绿。
//
// 返回的 error 只表示**基础设施故障或判据无法给出结论**，不表示「有人输」。
func DuplicateTokenSubmit(ctx context.Context, b Board, id TaskID, workers int, ttl time.Duration) (int, error) {
	if workers < 2 {
		return 0, errors.New("taskboard: 重复令牌判据至少需要两个提交者")
	}
	// 先认领一次——令牌由存储铸造（见 Board.Claim），本判据不碰铸造细节。
	task, err := b.Claim(ctx, id, "dup-token-judge", ClaimRequest{TTL: ttl})
	if err != nil {
		return 0, errors.Wrap(err, "taskboard: 重复令牌判据认领失败")
	}
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   int
		fatal []error
		lost  int
	)
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		// 走 async.GoSafe 而非裸 go func（同 ConcurrentClaim：本仓门禁禁止
		// 生产代码裸 goroutine——panic 无法被外层 recover，会杀掉宿主进程）。
		async.GoSafe(func() {
			defer wg.Done()
			<-start
			e := b.Complete(ctx, id, task.Lease.Token)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case e == nil:
				won++
			case errors.Is(e, ErrLeaseLost):
				// 正常的输家：说明令牌校验真的发生了。
				lost++
			default:
				fatal = append(fatal, e)
			}
		})
	}
	close(start)
	wg.Wait()

	if len(fatal) > 0 {
		return won, errors.Wrap(fatal[0], "taskboard: 重复令牌判据出现非争用类错误（判据无法给出结论）")
	}
	return won, nil
}
