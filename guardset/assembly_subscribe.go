// assembly_subscribe.go —— 换份的**订阅**与通知期纪律。
//
// # 为什么订阅单独成文件
//
// 它与「换份」是两件事：换份是**写面**（谁能把哪一份换上去），订阅是**读面**
// （换份发生时，谁想知道、怎么知道）。混在一个文件里会让「换份的原子性」这条
// 最关键的纪律被订阅表的细节淹没。
//
// # 订阅表的机制不在本文件
//
// 「快照式替换的订阅表 + 铸造句柄 + 锁外通知 + 回调 panic 隔离」这套机制在
// internal/subhub，与 netguard、pii 共用**同一份实现**。它此前在本仓有三份近乎
// 逐字相同的副本（每份包头都写着「与 netguard 的 notifyPolicy 同款」——
// **交叉引用本身就是重复的自白**），而这是一段并发正确性代码：只要有一份先被修，
// 另外两份不会跟着改，同一个不变量于是在三个包里有三种成色，失效形态是
// **偶发漏通知 / 偶发误留订阅**——不红不报，只在长跑进程里慢慢显形。
//
// 本文件因此只剩两件事：**本包的语境**（下面两条纪律）与**语义判定**
// （a 为 nil、fn 为 nil 各报什么错）。
//
// # 订阅者 panic 不得影响装配面
//
// 一次成功的换份**已经落地**，此时让某个订阅者抛异常就把整次变更说成「换份失败」，
// 等于把已经发生的事实改写成没发生——而这恰恰是本包要消灭的那类失效。
// 故逐个 recover（实现见 subhub.Invoke / subhub.Do），让其余订阅者继续收到通知。
//
// 另一条同源的纪律：订阅者回调里再调 `Swap`/`SwapFunc` 是**允许**的
// （无锁，不会自锁），但那会再触发一轮通知，递归深度由订阅者自己收敛，本包不做环检测。

package guardset

import "github.com/yi-nology/goagent-kernel/internal/subhub"

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
// fn 为 nil ⇒ 报错并返回零句柄：调用方能**凭返回值**知道订阅没生效，
// 而不是等到第一次变更时才发现「原来一直没跟上」。
// a 为 nil ⇒ 同样报错（未被装配的装配面不该看起来能用，与 `NewAssembly` 同口径）。
//
// # 与 netguard / pii 的订阅有一处**有意**不同：登记时不立即回调
//
// 那边推的是**策略快照**（「现在生效的是什么」），故登记后立刻补一次当前值——
// 否则订阅者会带着一个从没填过的视图上线。这里推的是 **SwapRecord**（「刚刚换了
// 哪一份、从什么换成什么」）：一条换份记录描述的是**已发生的事件**，把「登记那一刻」
// 伪造成一条换份记录，等于凭空宣布一次没发生过的换份，而 Seq / Generation 会因此
// 与实际代次对不上。
//
// 要「现在这一份」请直接取 `Snapshot()`——两个入口各自表达一件事，
// 而不是让一个入口同时兼职两种语义。
func (a *Assembly) Subscribe(fn func(SwapRecord)) (Subscription, error) {
	if a == nil {
		return 0, ErrUnassembled
	}
	if fn == nil {
		return 0, ErrNilSubscriber
	}
	return Subscription(a.subs.Add(fn)), nil
}

// Unsubscribe 注销一个订阅者。句柄无效或已注销时静默返回（重复注销不是错误）。
func (a *Assembly) Unsubscribe(h Subscription) {
	if a == nil {
		return
	}
	a.subs.Remove(subhub.Handle(h))
}

// notify 把新记录推给留痕回调与全部订阅者。
//
// **由写侧在交换落地之后、锁外调用**。遍历的是一次快照：并发增删订阅者不会让
// 本次通知看到半张名单。
//
// 留痕先于订阅者：留痕是「换份**已经发生**」的记录，订阅者是**据此**做派生动作的人。
// 让订阅者先跑，就会出现「派生视图已按新份重算，而留痕里还没有这次换份」的窗口。
func (a *Assembly) notify(old, next *assemblyState) {
	rec := SwapRecord{
		From:       guardNames(stateSet(old)),
		To:         guardNames(next.set),
		Generation: next.gen,
		Set:        next.set,
	}
	if a.recorder != nil {
		subhub.Do(func() { a.recorder(rec) })
	}
	a.subs.Notify(rec)
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
