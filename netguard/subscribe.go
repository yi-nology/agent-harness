// 策略变更订阅：把「换过没有」从**可查询**变成**可响应**。
//
// # 为什么在代次之上还要这一层
//
// policy.go 已经把策略并成单指针、每次交换递增代次，代次变化说明「换过」。但代次
// 只是**信号**：调用方得自己起一个轮询循环，前后各取一次代次比对。轮询有三件它
// 解决不了的事——
//
//   - 空转期仍在读全局状态，每个订阅者一份重复读取；
//   - 两次轮询之间若换了两次，只能看出「变了」，看不出「变成了什么」，要拿内容
//     仍得再取一次快照，而那一刻可能又变了；
//   - 代次单调只保证「不漏掉发生过变更」，不保证「每一代都被谁记下过」。
//
// 订阅把这件事一次做完：变更发生的**那一刻**由写侧直接把新快照推到订阅者手上，
// 收到即所见，不靠事后比对推断。
//
// # 为什么订阅表是快照式替换，而不是一把读写锁
//
// 通知必须在**锁外**调用订阅者。订阅者要做 I/O（重算派生视图、写审计、打日志），
// 在锁内调用等于把策略面锁死；订阅者回调里再调 SetPolicy 则会自锁死锁。
//
// 读写锁解决不了这件事——它只能让「取名单」不阻塞，「遍历名单并逐个调用」仍然
// 落在读锁里。故订阅表用 atomic.Pointer 整体替换：写侧复制出新切片再换回去
// （读侧无锁，也永远看不到改了一半的名单），通知时取一次快照、在锁外遍历这个快照。
// 快照一经发布便不再被改写，故遍历期间的并发增删不会把名单撕裂。
//
// 这与本包「不可撕裂读」是同一条纪律，只是作用对象从策略换成了订阅名单：
// 通知遍历看到的必须是某一时点的完整名单，不能是半张。
//
// # 为什么订阅者 panic 不得影响策略面
//
// 一次成功的交换**已经落地**，此时让某个订阅者抛异常就把整次变更说成「交换失败」，
// 等于把已经发生的事实改写成没发生——而这恰恰是本文件要消灭的那类失效。故逐个
// recover，让其余订阅者继续收到通知。
//
// 另一条同源的纪律：订阅者回调里再调 SetPolicy 是**允许**的（无锁，不会自锁），
// 但那会再触发一轮通知，递归深度由订阅者自己收敛，本包不做环检测。

package netguard

import (
	"sync/atomic"

	"github.com/oekit/pkg/errors"
)

// Subscription 订阅句柄，传给 Unsubscribe 注销。0 表示无效。
type Subscription uint64

// ErrNilSubscriber 订阅回调为空。
//
// 登记一个必然 panic 的订阅者毫无意义，故直接报错并返回零句柄：调用方能**凭返回值**
// 知道订阅没生效，而不是等到第一次变更时才发现「原来一直没跟上」。
var ErrNilSubscriber = errors.New("扩展策略订阅回调为空")

// subscriber 一个订阅者：句柄 + 回调。
type subscriber struct {
	handle Subscription
	fn     func(*Policy)
}

// subs 当前订阅表。**只整体替换，永不原地改**——读侧一次 Load 拿到的就是某一时点
// 的完整名单，遍历它不会被并发增删撕裂。
var subs atomic.Pointer[[]subscriber]

// subSeq 句柄发号器。单调递增，0 留给「无效句柄」。
var subSeq atomic.Uint64

// Subscribe 登记一个策略变更订阅者，返回句柄与是否成功。
//
// # 为什么注册完立刻回调一次当前快照
//
// 不这么做，订阅者对「现在生效的是什么」一无所知，要等到下一次变更才知道。热重载
// 跑在长生命周期进程里的典型形态恰恰是「注册完之后很久没有下一次变更」，于是订阅者
// 带着一个从没填过的视图上线——比没有订阅更危险，因为它以为自己已经跟上了。
//
// 注册先于立即回调：宁可让订阅者多收一次（立即回调给旧快照、紧接着的变更通知给新
// 快照，顺序仍单调向前），也不能在两者之间漏掉一次变更。
//
// 立即回调同样在锁外调用、panic 同样被 recover，与变更通知完全一致。
func Subscribe(fn func(*Policy)) (Subscription, error) {
	if fn == nil {
		return 0, ErrNilSubscriber
	}
	h := Subscription(subSeq.Add(1))
	addSubscriber(subscriber{handle: h, fn: fn})
	callSubscriber(fn, CurrentPolicy())
	return h, nil
}

// Unsubscribe 注销订阅。重复注销同一句柄是安全的空操作。
//
// 有了它，订阅者才能在长生命周期场景下解绑：否则回调闭包连同它捕获的一切
// （宿主对象、配置缓存）会被订阅表永久留住，而这条泄漏不会自己显形。
func Unsubscribe(h Subscription) {
	if h == 0 {
		return
	}
	for {
		cur := subs.Load()
		if cur == nil {
			return
		}
		old := *cur
		idx := -1
		for i, s := range old {
			if s.handle == h {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		next := make([]subscriber, 0, len(old)-1)
		next = append(next, old[:idx]...)
		next = append(next, old[idx+1:]...)
		if subs.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// notifyPolicy 把新快照推给全部订阅者。**由写侧在交换落地之后、锁外调用。**
//
// 遍历的是一次快照：并发增删订阅者不会让本次通知看到半张名单，且同一次变更对每个
// 订阅者送出的都是**同一份**策略——与「不可撕裂读」同源。
func notifyPolicy(p *Policy) {
	cur := subs.Load()
	if cur == nil {
		return
	}
	for _, s := range *cur {
		callSubscriber(s.fn, p)
	}
}

// addSubscriber 以整体替换的方式加一个订阅者，CAS 失败则重试。
func addSubscriber(s subscriber) {
	for {
		cur := subs.Load()
		var old []subscriber
		if cur != nil {
			old = *cur
		}
		next := make([]subscriber, 0, len(old)+1)
		next = append(next, old...)
		next = append(next, s)
		if subs.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// callSubscriber 调用一个订阅者并吞掉它的 panic。
func callSubscriber(fn func(*Policy), p *Policy) {
	defer func() { _ = recover() }()
	fn(p)
}
