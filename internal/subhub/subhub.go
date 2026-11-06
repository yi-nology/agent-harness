// Package subhub 是「快照式订阅表」的**唯一实现**（本仓内部件，不进公开 API）。
//
// # 它替掉了什么
//
// 这段机制此前在本仓有三份**近乎逐字相同**的实现：`netguard/subscribe.go`、
// `pii/subscribe.go`、`guardset/assembly_subscribe.go`。三份的包头各自写着
// 「与 netguard 的 notifyPolicy 同款」——**交叉引用本身就是重复的自白**。
//
// 重复的代价不在于多写了百来行，而在于：这是一段**并发正确性代码**
// （copy-on-write 发布、句柄单调、通知在锁外、回调 panic 隔离）。
// 只要有一份先被修（比如「注销时 CAS 失败要重试」），另外两份不会跟着改，
// 于是同一个不变量在不同包里有三种成色，而失效形态是**偶发漏通知 / 偶发误留订阅**
// ——不会红、不会报错，只会在长跑进程里慢慢显形。
//
// # 三条不变量（收在这里，改就改这一处）
//
//  1. **整体替换，永不原地改**：订阅表用 `atomic.Pointer` 挂一个切片，写侧复制出
//     新切片再换回去。读侧一次 Load 拿到的就是某一时点的完整名单——遍历期间并发
//     增删不会把名单撕裂。不用读写锁：锁只能让「取名单」不阻塞，「遍历并逐个调用」
//     仍落在锁里。
//  2. **通知必须在锁外**：订阅者要做 I/O（重算派生视图、写审计、打日志）。
//     本包根本没有锁，这条由结构保证而不是靠纪律。
//  3. **回调 panic 不得影响发布面**：一次成功的交换**已经落地**，此时让某个订阅者
//     抛异常就把已发生的事实改写成「没发生」。故逐个 recover（见 Invoke / Do）。
//
// # 句柄
//
// 句柄由本包铸造（单调递增，0 表示无效），注销只认句柄、不认内容——「这次注销是不是
// 我登记的那一次」由句柄回答，而不是靠回调指针或序号比对认人（业务字段允许重复时，
// 那种认法必然误放）。
package subhub

import "sync/atomic"

// Handle 订阅句柄，交给 Registry.Remove 注销。0 表示无效（从未登记 / 已注销）。
type Handle uint64

// entry 一个订阅者：句柄 + 回调。
type entry[T any] struct {
	handle Handle
	fn     func(T)
}

// Registry 快照式订阅表。**零值可用**：atomic.Pointer 与 atomic.Uint64 的零值
// 就是「空表 + 0 号已占用」，故使用方不必构造函数（全局变量与结构体字段都能直接声明）。
type Registry[T any] struct {
	subs atomic.Pointer[[]entry[T]]
	seq  atomic.Uint64
}

// Add 登记一个订阅者并返回句柄。
//
// fn 为 nil 的**语义判定归调用方**：三个使用方各有自己的公开哨兵
// （netguard.ErrNilSubscriber 等），报错文案也各有语境。本包不替它们判，
// 也不替它们编一个共用哨兵——共用哨兵会让 `errors.Is(err, netguard.ErrNilSubscriber)`
// 这类既有调用点判定链断掉。
func (r *Registry[T]) Add(fn func(T)) Handle {
	h := Handle(r.seq.Add(1))
	e := entry[T]{handle: h, fn: fn}
	for {
		cur := r.subs.Load()
		var old []entry[T]
		if cur != nil {
			old = *cur
		}
		next := make([]entry[T], 0, len(old)+1)
		next = append(next, old...)
		next = append(next, e)
		if r.subs.CompareAndSwap(cur, &next) {
			return h
		}
	}
}

// Remove 注销一个订阅者。句柄无效或已注销时静默返回——**重复注销不是错误**。
//
// 静默而非报错：注销通常挂在 defer 上，注销路径报错只会淹掉真正的错误，
// 而「叫的号已经不在册」这件事本身没有可采取的动作。
func (r *Registry[T]) Remove(h Handle) {
	if h == 0 {
		return
	}
	for {
		cur := r.subs.Load()
		if cur == nil {
			return
		}
		old := *cur
		idx := -1
		for i, e := range old {
			if e.handle == h {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		next := make([]entry[T], 0, len(old)-1)
		next = append(next, old[:idx]...)
		next = append(next, old[idx+1:]...)
		if r.subs.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// Notify 把 v 推给**这一次快照**里的全部订阅者。
//
// 遍历的是一次快照：并发增删不会让本次通知看到半张名单，且同一次变更对每个订阅者
// 送出的都是**同一个** v。**由写侧在变更落地之后调用**（发布顺序即通知顺序）。
func (r *Registry[T]) Notify(v T) {
	cur := r.subs.Load()
	if cur == nil {
		return
	}
	for _, e := range *cur {
		Invoke(e.fn, v)
	}
}

// Len 当前在册订阅者数（观测用；与并发增删相比天然是「某一时刻」的值）。
func (r *Registry[T]) Len() int {
	cur := r.subs.Load()
	if cur == nil {
		return 0
	}
	return len(*cur)
}

// Invoke 调用一个订阅回调并吞掉它的 panic。
//
// 为什么要吞：调用点全都在「变更已经落地」之后（否则订阅者拿到的就不是事实）。
// 让其中一个订阅者把进程或整次变更说成失败，等于把已发生的事实改写成没发生。
func Invoke[T any](fn func(T), v T) {
	defer func() { _ = recover() }()
	fn(v)
}

// Do 调用一个无参回调并吞掉 panic——留痕/通知类副作用的同一条纪律，
// 供「没有载荷要传」的回调（如换份留痕）使用，免得每个使用方各写一份 recover。
func Do(fn func()) {
	defer func() { _ = recover() }()
	fn()
}
