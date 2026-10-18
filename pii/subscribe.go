// 处置策略变更订阅：把「换过没有」从**可查询**变成**可响应**。
//
// # 为什么单指针还不够
//
// 上面那份 Policy 解决的是**读的一致性**：一次 Load 拿到的就是同一份策略，不会读到
// 「面已关却回报阻断」这种从未被任何写侧设置过的组合。但它只让读侧自洽，写侧的
// 下游仍然是空的——Gen() 能回答「换过没有」，回答不了「换成了什么」，宿主只能自己
// 起轮询比对，而两次比对之间若换了两次，代次只说得出「变了」。
//
// 订阅把这件事一次做完：变更发生的**那一刻**由写侧把新快照推给订阅者，收到即所见。
//
// # 为什么回调参数是 Policy 值而不是 *Policy
//
// 读侧 CurrentPolicy() 已经是值返回，订阅沿用同型：订阅者拿到的是一份独立副本，
// 写侧后续换策略不影响它手里的那份，订阅者也无需操心指针的存活期。代价是订阅者
// 想长期留档就得自己留副本——这是显式的，优于「指针还能用吗」这种隐式问题。
//
// # 为什么订阅表是快照式替换，而不是一把读写锁
//
// 通知必须在**锁外**调用订阅者。订阅者要做 I/O（重算派生视图、写审计、打日志），
// 在锁内调用等于把策略面锁死；订阅者回调里再调 SetStrategy 则会自锁死锁。
//
// 读写锁解决不了——它只能让「取名单」不阻塞，「遍历名单并逐个调用」仍落在读锁里。
// 故订阅表用 atomic.Pointer 整体替换：写侧复制出新切片再换回去（读侧无锁，也永远
// 看不到改了一半的名单），通知时取一次快照、在锁外遍历这个快照。快照一经发布便不再
// 被改写，故遍历期间的并发增删不会把名单撕裂。
//
// 这与本包「不可撕裂读」是同一条纪律，只是作用对象从策略换成了订阅名单。
//
// # 为什么订阅者 panic 不得影响策略面
//
// 一次成功的 CAS **已经落地**，此时让某个订阅者抛异常就把整次变更说成「没发生」，
// 等于把已经发生的事实改写成没发生——而这恰恰是本文件要消灭的那类失效。故逐个
// recover，让其余订阅者继续收到通知。
//
// 另一条同源的纪律：订阅者回调里再调 SetStrategy 是**允许**的（无锁，不会自锁），
// 但那会再触发一轮通知，递归深度由订阅者自己收敛，本包不做环检测。

package pii

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
var ErrNilSubscriber = errors.New("处置策略订阅回调为空")

// subscriber 一个订阅者：句柄 + 回调。
type subscriber struct {
	handle Subscription
	fn     func(Policy)
}

// subs 当前订阅表。**只整体替换，永不原地改**——读侧一次 Load 拿到的就是某一时点
// 的完整名单，遍历它不会被并发增删撕裂。
var subs atomic.Pointer[[]subscriber]

// subSeq 句柄发号器。单调递增，0 留给「无效句柄」。
var subSeq atomic.Uint64

// Subscribe 登记一个处置策略变更订阅者，返回句柄与是否成功。
//
// # 为什么注册完立刻回调一次当前快照
//
// 不这么做，订阅者对「现在生效的是什么」一无所知，要等到下一次变更才知道。策略注入
// 的典型形态恰恰是「注册完之后很久没有下一次变更」，于是订阅者带着一个从没填过的
// 视图上线——比没有订阅更危险，因为它以为自己已经跟上了。
//
// 注册先于立即回调：宁可让订阅者多收一次（立即回调给旧快照、紧接着的变更通知给新
// 快照，顺序仍单调向前），也不能在两者之间漏掉一次变更。
//
// 立即回调同样在锁外调用、panic 同样被 recover，与变更通知完全一致。
func Subscribe(fn func(Policy)) (Subscription, error) {
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
// 有了它，订阅者才能在长生命周期场景下解绑：否则回调闭包连同它捕获的一切会被订阅表
// 永久留住，而这条泄漏不会自己显形。
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

// notifyPolicy 把新快照推给全部订阅者。**由写侧在 CAS 成功之后、锁外调用。**
//
// 遍历的是一次快照：并发增删订阅者不会让本次通知看到半张名单，且同一次变更对每个
// 订阅者送出的都是**同一份**策略——与「不可撕裂读」同源。
func notifyPolicy(p Policy) {
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
func callSubscriber(fn func(Policy), p Policy) {
	defer func() { _ = recover() }()
	fn(p)
}
