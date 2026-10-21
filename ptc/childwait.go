package ptc

import (
	"os/exec"
	"time"

	"github.com/oekit/concurrency/async"
)

// waitChildNonBlocking 非阻塞地试取子进程的退出状态。
//
// 拿得到 ⇒ 子进程**已经结束**（那个 err 就是真死因：被内核杀 / 自己退出）。
// 拿不到 ⇒ 子进程**还活着**（没回结果也没退出——典型是宿主忘了调
// MaybeRunChild，子进程跑起了宿主的完整 main）⇒ 收刀后再取，
// 否则 `Wait()` 会一直等它退出，把「等结果有期限」变成「等退出没期限」。
//
// # 为什么不用 `Process.Signal(0)` 探活
//
// 那样要多一次系统调用，且**跨平台语义不一致**（Windows 上没有 signal 0）。
// 而 `Wait()` 本身是「取状态并回收」的**唯一**正确入口（调两次会报错），
// 故用「在 goroutine 里 Wait + select 一下」来达成非阻塞：
// 拿到就是真死因；超时（极短）就说明它还活着——
// 此时那个 goroutine 会**一直挂着**直到子进程真被收掉，这是可接受的
// （子进程马上就会被 Kill，Wait 随即返回，goroutine 自然结束）。
func waitChildNonBlocking(child *exec.Cmd) error {
	type res struct{ err error }
	ch := make(chan res, 1)
	// 走 async.GoSafe 而非裸 go func（门禁禁止裸 goroutine：panic 杀宿主）。
	async.GoSafe(func() {
		ch <- res{err: child.Wait()}
	})
	select {
	case r := <-ch:
		return r.err
	case <-time.After(childWaitProbe):
		// 子进程还活着：收刀，让那个 goroutine 的 Wait 尽快返回。
		_ = child.Process.Kill()
		r := <-ch
		return r.err
	}
}

// childWaitProbe 非阻塞探测的等待时长。
//
// 取 50ms：足够让「已经结束」的子进程被 Wait 收割（那是微秒级的事），
// 又不至于让「还活着」的情形多等。**它只影响探测的延迟，不影响正确性**
// ——两种情形分别走「真死因」与「收刀后取」两条确定的路径。
const childWaitProbe = 50 * time.Millisecond
