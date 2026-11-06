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
package taskboard

import (
	"context"
	"sync"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"
)

// ConcurrentClaim 让多个 agent 同时认领同一个任务，返回成功者数量。
//
// lease 为每个 agent 铸造租约（测试里通常是固定 token；真实存储应当自己铸）。
// 返回的 error 只表示**基础设施故障或契约违反**，不表示「有人输」——
// 输家是正常结果。
func ConcurrentClaim(ctx context.Context, b Board, id TaskID, agents []AgentID, lease func(AgentID) Lease) (int, error) {
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
			_, err := b.Claim(ctx, id, agent, lease(agent))
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
