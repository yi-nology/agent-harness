// contention.go —— 领取的并发正确性**判据**（判据，不是测试风格）。
//
// 与 taskboard.ConcurrentClaim 同一形态：让 N 个接收方在同一个起跑线上
// 同时 Take 同一单，赢家必须恰好 1 个。判据长在包里，宿主的存储实现
// 被它跑一遍就算验过契约。
package handoff

import (
	"context"
	"sync"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"
)

// ConcurrentTake 让多个接收方同时领取同一交接单，返回成功者数量。
//
// 判定（与 taskboard.ConcurrentClaim 同源）：
//
//   - 成功者必须**恰好 1 个**（0 = 谁都领不了；>1 = 存储没做到原子转移）；
//   - 输家的错误必须是 ErrTaken（不能是 ErrExpired / ErrNotAddressee 之类
//     ——那说明这些领取者压根没走到争用那一步，这次判定没测到想测的东西）；
//   - 任何**其它**错误都是基础设施故障，显式报出——把「数据库连不上」算成
//     输家，判据会在存储坏掉时变绿（测到的是「没人赢」而不是「只有一个
//     赢家」）。
//
// ttl 用于给判定留一个整体时限（避免存储实现挂死拖垮调用方）。
func ConcurrentTake(ctx context.Context, s Store, key Key, tos []Agent, ttl time.Duration) (int, error) {
	if len(tos) == 0 {
		return 0, errors.New("handoff: 并发领取判据需要至少一个接收方")
	}
	if ttl <= 0 {
		return 0, errors.New("handoff: 并发领取判据需要正的整体时限")
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		fatal   []error
		wrong   []Agent
	)
	start := make(chan struct{})
	for _, to := range tos {
		receiver := to
		wg.Add(1)
		// 走 async.GoSafe 而非裸 go func：本仓门禁禁止裸 goroutine
		// （panic 无法被外层 recover 捕获，会直接杀掉宿主进程）。
		async.GoSafe(func() {
			defer wg.Done()
			<-start
			_, err := s.Take(ctx, key, receiver)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, ErrTaken):
				// 正常的输家：争用真的发生了。
			case errors.Is(err, ErrExpired), errors.Is(err, ErrNotAddressee), errors.Is(err, ErrAlreadySettled):
				// 没走到争用那一步：这次判定没测到想测的东西。
				wrong = append(wrong, receiver)
			default:
				fatal = append(fatal, err)
			}
		})
	}
	close(start)
	done := make(chan struct{})
	// 收口时限：判据自己也不能挂死（走 GoSafe 同一纪律）。
	async.GoSafe(func() {
		defer close(done)
		wg.Wait()
	})
	select {
	case <-done:
	case <-time.After(ttl):
		return 0, errors.New("handoff: 并发领取判据超时——存储实现可能挂死")
	}
	if len(fatal) > 0 {
		return 0, errors.Wrap(fatal[0], "handoff: 并发领取判据遭遇基础设施故障")
	}
	if len(wrong) > 0 {
		return 0, errors.New("handoff: 有接收方因 " + joinAgents(wrong) + " 未走到争用（判定无效）")
	}
	if winners != 1 {
		return winners, errors.New("handoff: 并发领取赢家数量违反契约（应为 1）")
	}
	return winners, nil
}
