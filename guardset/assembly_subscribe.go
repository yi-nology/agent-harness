// assembly_subscribe.go —— 换份的**订阅**与通知期纪律。
//
// # 为什么订阅单独成文件
//
// 它与「换份」是两件事：换份是**写面**（谁能把哪一份换上去），订阅是**读面**
// （换份发生时，谁想知道、怎么知道）。混在一个文件里会让「换份的原子性」这条
// 最关键的纪律被订阅表的细节淹没。
//
// # 订阅表为什么是整份替换，而不是一把读写锁
//
// 通知必须在**锁外**调用订阅者。订阅者要做 I/O（重算派生视图、写审计、打日志），
// 在锁内调用等于把装配面锁死；订阅者回调里再调 `Swap` 则会自锁死锁。
//
// 读写锁解决不了这件事——它只能让「取名单」不阻塞，「遍历名单并逐个调用」仍然
// 落在读锁里。故订阅表用 `atomic.Pointer` 整体替换：写侧复制出新切片再换回去
// （读侧无锁，也永远看不到改了一半的名单），通知时取一次快照、在锁外遍历这个快照。
// 快照一经发布便不再被改写，故遍历期间的并发增删不会把名单撕裂。
//
// 这与本包「不可撕裂读」是同一条纪律，只是作用对象从守卫集合换成了订阅名单：
// 通知遍历看到的必须是某一时点的完整名单，不能是半张。
//
// # 订阅者 panic 不得影响装配面
//
// 一次成功的换份**已经落地**，此时让某个订阅者抛异常就把整次变更说成「换份失败」，
// 等于把已经发生的事实改写成没发生——而这恰恰是本包要消灭的那类失效。
// 故逐个 recover，让其余订阅者继续收到通知。
//
// 另一条同源的纪律：订阅者回调里再调 `Swap`/`SwapFunc` 是**允许**的
// （无锁，不会自锁），但那会再触发一轮通知，递归深度由订阅者自己收敛，本包不做环检测。

package guardset

// Subscribe 订阅换份：换份真正落地的那一刻，把新快照推给回调。
//
// # 为什么在代次之上还要这一层
//
// 代次只是**信号**：调用方得自己起一个轮询循环，前后各取一次代次比对。
// 轮询有三件它解决不了的事：
//
//   - 空转期仍在读全局状态，每个订阅者一份重复读取；
//   - 两次轮询之间若换了两次，只能看出「变了」，看不出「变成了什么」；
//   - 代次单调只保证「不漏掉发生过变更」，不保证「每一代都被谁记下过」。
//
// 订阅把这件事一次做完：变更发生的**那一刻**由写侧直接把新记录推给订阅者，
// 收到即所见，不靠事后比对推断。
//
// # 订阅者 panic 不得影响装配面
//
// 一次成功的换份**已经落地**，此时让某个订阅者抛异常就把整次变更说成「换份失败」，
// 等于把已经发生的事实改写成没发生。故逐个 recover，让其余订阅者继续收到通知。
//
// 订阅者回调里再调 `Swap`/`SwapFunc` 是**允许**的（无锁，不会自锁），
// 但那会再触发一轮通知，递归深度由订阅者自己收敛，本包不做环检测。
//
// fn 为 nil ⇒ 报错并返回零句柄：调用方能**凭返回值**知道订阅没生效，
// 而不是等到第一次变更时才发现「原来一直没跟上」。
func (a *Assembly) Subscribe(fn func(SwapRecord)) (Subscription, error) {
	if a == nil {
		return 0, ErrUnassembled
	}
	if fn == nil {
		return 0, ErrNilSubscriber
	}
	h := Subscription(a.subSeq.Add(1))
	s := subscriber{handle: h, fn: fn}
	for {
		cur := a.subs.Load()
		var old []subscriber
		if cur != nil {
			old = *cur
		}
		next := make([]subscriber, 0, len(old)+1)
		next = append(next, old...)
		next = append(next, s)
		if a.subs.CompareAndSwap(cur, &next) {
			return h, nil
		}
	}
}

// Unsubscribe 注销一个订阅者。句柄无效或已注销时静默返回（重复注销不是错误）。
func (a *Assembly) Unsubscribe(h Subscription) {
	if a == nil || h == 0 {
		return
	}
	for {
		cur := a.subs.Load()
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
		if a.subs.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// notify 把新记录推给留痕回调与全部订阅者。
//
// **由写侧在交换落地之后、锁外调用**（与 netguard 的 notifyPolicy 同款）。
// 遍历的是一次快照：并发增删订阅者不会让本次通知看到半张名单。
func (a *Assembly) notify(old, next *assemblyState) {
	rec := SwapRecord{
		Seq:        next.gen,
		From:       guardNames(stateSet(old)),
		To:         guardNames(next.set),
		Generation: next.gen,
		Set:        next.set,
	}
	if a.recorder != nil {
		callSafely(func() { a.recorder(rec) })
	}
	cur := a.subs.Load()
	if cur == nil {
		return
	}
	for _, s := range *cur {
		callSafely(func() { s.fn(rec) })
	}
}

// callSafely 调用一个回调并吞掉它的 panic。
//
// 为什么吞：换份**已经落地**，让回调抛异常就把已经发生的事实改写成「没发生」。
// 与 netguard 的 callSubscriber 同款。
func callSafely(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// stateSet 取一份状态里的集合（nil 视为没有）。
func stateSet(st *assemblyState) *Set {
	if st == nil {
		return nil
	}
	return st.set
}

// guardNames 取一份集合的守卫名快照（按优先级序）。
func guardNames(s *Set) []string {
	if s == nil {
		return nil
	}
	return s.Guards()
}
