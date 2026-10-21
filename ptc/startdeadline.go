package ptc

import (
	"context"
	"time"

	"github.com/oagentkit/guardx/wire"
	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"
)

// startWithDeadline 在**有限期限**内等子进程的结果。
//
// # 为什么不能直接用 starter.Start(ctx, ...)
//
// 上游 `guardx/wire` 的 `Starter.Start(ctx, ...)` **收了 ctx 却从不用它**：
// 内部是 `ReadFrame(r, &res)`——**无超时的阻塞读**。
//
// 于是「子进程不回结果」这一档会**永久阻塞**，`ctx` 取消也不管用。
// 那一档不是假想：宿主若忘了在 `main` 里调 `ptc.MaybeRunChild()`，
// 子进程就会跑起**宿主的完整 main**——它不退出（正常启动服务）、
// 也不回 Result 帧 ⇒ 父侧永久挂死。
//
// 症状是「执行卡住」，而真实原因（宿主少加了一行）**完全看不出来**。
//
// # 本包怎么兜
//
// 父侧**自己**给「等结果」加期限：取 `ctx` 的 deadline 与墙钟预算
// `timeout` 中**较早**者。到期就放弃等待并报错。
//
// 期限到了之后还要处理**子进程仍活着**的情形（它没回结果也没退出）：
// 调用方负责收刀（见 subprocess.go 的收尾段）——
// 放着不管的话，子进程会继续跑，占着内存与 CPU。
//
// # 期限取「较早者」的理由
//
//   - `ctx` 的 deadline：调用方的期限是**硬约束**（它可能已超时了）；
//   - 墙钟预算 `timeout`：子进程自己也用它管自己的执行时长，
//     父侧等它不该比这更久——否则子进程已经放弃了、父侧还在等。
//
// 取较早者 ⇒ 两条期限都 respected，且**永远有一个有限的期限**
// （`timeout` 缺省为 `DefaultTimeout`，不会是无限）。
func startWithDeadline(ctx context.Context, starter *wire.Starter, start wire.Start, timeout time.Duration) (wire.Result, error) {
	limit := timeout
	if dl, ok := ctx.Deadline(); ok {
		if limit <= 0 || time.Until(dl) < limit {
			limit = time.Until(dl)
		}
	}
	if limit <= 0 {
		// 没有期限就等于回到上游的挂死形态——不接。
		limit = DefaultTimeout
	}

	// 先看 ctx 是不是已经取消了（不该再等）。
	if err := ctx.Err(); err != nil {
		return wire.Result{}, errors.Wrap(err, "等子进程结果前调用方已取消")
	}

	type outcome struct {
		res wire.Result
		err error
	}
	ch := make(chan outcome, 1)
	// 走 async.GoSafe 而非裸 go func：本仓的门禁禁止生产代码里的裸 goroutine
	// （panic 无法被外层 recover 捕获，会直接杀掉宿主进程）。
	async.GoSafe(func() {
		// 用 context.Background()：**不能**把 ctx 传下去——
		// 上游忽略 ctx，但若将来上游改成尊重 ctx，
		// 这里传 ctx 会让「期限到了」与「ctx 取消」两种放弃原因糊在一起，
		// 诊断信息变差。故这里用独立的 ctx，由本包自己管期限。
		r, err := starter.Start(context.Background(), start)
		ch <- outcome{res: r, err: err}
	})

	timer := time.NewTimer(limit)
	defer timer.Stop()

	select {
	case o := <-ch:
		return o.res, o.err
	case <-timer.C:
		return wire.Result{}, errors.New("wire: 等子进程结果超时——子进程未回报结果且未结束（宿主是否忘了在 main 里调 ptc.MaybeRunChild()？）")
	case <-ctx.Done():
		return wire.Result{}, errors.Wrap(ctx.Err(), "等子进程结果时调用方取消")
	}
}
