// cas.go —— CAS 循环与并发原子性判据（适配形态约定的执行面）。
//
// # casLoop：判（快照）与写（事务）之间的窗口通用收尾
//
// 快照读与条件事务是两次后端交互——之间有窗口，别的进程可能改了状态。
// 适配器不假装窗口不存在：断言失败就取新快照重判，收敛为止。
// 上限只是防挂死的兜底（持续冲突 = 有高频写者或后端没实现原子性，
// 显式报 ErrTxnConflict，不无限重试）。
//
// # ConcurrentTxn：原语原子性的可跑判据
//
// 「Txn 的断言与写入不可分割」是 Backend 的**契约条款**，不是实现细节。
// 与 taskboard.ConcurrentClaim 同一形态：写成导出判据，宿主的后端实现
// 跑一遍即算验过——两个进程内竞争者同时断言同一版本、各自写不同的值，
// 恰好一个提交成功、版本只前进一格。断言与写入分离的坏实现
// （先查后写没有原子性）在这里必红。
package coord

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"
)

// casMaxAttempts CAS 循环重试上限（正常争用几次内收敛；触顶即报冲突）。
const casMaxAttempts = 32

// casLoop 泛型 CAS 循环：plan 从快照算出结论与待提交批次。
//
// plan 返回 error ⇒ 直接终止（判定类失败——ErrTaken / ErrLeaseLost 等
// 是**结论**不是冲突，重试不改变它；前提是 plan 把判定读过的键全部列进了
// asserts，窗口由断言护住）。断言失败（applied=false）⇒ 取新快照重判。
func casLoop[T any](
	ctx context.Context,
	b Backend,
	keys []string,
	plan func(snap map[string]Entry) (result T, asserts []Assert, mutations []Mutation, err error),
) (T, error) {
	for range casMaxAttempts {
		snap, err := b.Snapshot(ctx, keys...)
		if err != nil {
			var zero T
			return zero, errors.Wrap(err, "coord: 快照读失败")
		}
		result, asserts, mutations, err := plan(snap)
		if err != nil {
			var zero T
			return zero, err
		}
		// 纯结论路径：判定在快照上已做出（replayed / contended 等），
		// 没有要提交的状态——直接返回结论，不再走一次空事务。
		if len(asserts) == 0 && len(mutations) == 0 {
			return result, nil
		}
		applied, err := b.Txn(ctx, asserts, mutations)
		if err != nil {
			var zero T
			return zero, errors.Wrap(err, "coord: 条件事务失败")
		}
		if applied {
			return result, nil
		}
		// 断言失败：状态在快照与提交之间被人改了——取新快照重判。
	}
	var zero T
	return zero, errors.Wrap(ErrTxnConflict,
		"coord: 重试上限内未提交（高争用或后端原子性可疑）", "attempts", casMaxAttempts)
}

// ConcurrentTxn 并发原子性判据：两个竞争者同时断言同一版本，恰好一个提交。
//
// 判法（与 taskboard.ConcurrentClaim 同一形态）：
//
//  1. 先取 key 的当前版本 v（不存在则 VerMissing）；
//  2. workers 个竞争者**同时**提交 Txn([key@v], 各自写自己的值)；
//  3. 成功者必须**恰好 1 个**（0 = 全被断言拒了，说明判据没测到争用；
//     >1 = 断言与写入没有原子性，契约破）；
//  4. 其余必须 applied=false（正常输家）；任何 error 都是基础设施故障，
//     显式报出，不混进「输家」里——把后端故障算成输家，判据会在后端
//     坏掉时变绿；
//  5. 版本必须恰好 v+1（版本断言之外多写一格或少写一格都是版本语义破）。
//
// 返回的 error 只表示**基础设施故障或契约违反**，不表示「有人输」。
func ConcurrentTxn(ctx context.Context, b Backend, key string, workers int) error {
	if workers < 2 {
		return errors.New("coord: 并发原子性判据至少需要两个竞争者")
	}
	init, err := b.Snapshot(ctx, key)
	if err != nil {
		return errors.Wrap(err, "coord: 判据起跑前的快照读失败")
	}
	v := init[key].Ver

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		won   int
		lost  int
		fatal []error
	)
	start := make(chan struct{})
	for i := range workers {
		val := []byte("winner-" + strconv.Itoa(i))
		wg.Add(1)
		// 走 async.GoSafe 而非裸 go func（本仓门禁禁止裸 goroutine：
		// panic 无法被外层 recover，会直接杀掉宿主进程）。
		async.GoSafe(func() {
			defer wg.Done()
			<-start
			applied, err := b.Txn(ctx, []Assert{{Key: key, Ver: v}}, []Mutation{{Key: key, Val: val}})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				fatal = append(fatal, err)
			case applied:
				won++
			default:
				lost++
			}
		})
	}
	close(start)
	wg.Wait()

	if len(fatal) > 0 {
		return errors.Wrap(fatal[0], "coord: 并发原子性判据出现非争用类错误（判据无法给出结论）")
	}
	if won != 1 {
		return fmt.Errorf("coord: %d 个竞争者同时断言版本 %d，成功者 = %d，契约要求恰好 1 个（>1 = 断言与写入未原子；0 = 判据没测到争用，输家 %d 个）",
			workers, v, won, lost)
	}
	after, err := b.Snapshot(ctx, key)
	if err != nil {
		return errors.Wrap(err, "coord: 判据收尾的快照读失败")
	}
	if after[key].Ver != v+1 {
		return fmt.Errorf("coord: 一次提交后版本必须恰好 +1（实得 %d → %d）——版本单调语义破了，断言的锚点不再可靠",
			v, after[key].Ver)
	}
	return nil
}
