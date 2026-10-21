// assembly.go —— 守卫集合的**装配面**：撤销的归属与机械路径。
//
// # 为什么需要它
//
// `Set` 构造成功后只读、可并发共享——那是它的**不变量**，不能为了「支持撤销」
// 而改成可变：并发共享下的可变集合就是竞态源，且它会引入**第二个真相**
// （「当前有哪些守卫」这件事有两个答案）。
//
// 但只读带来的代价是真实的：**运行期想临时停掉某个守卫**（应急降级、灰度、
// 某条守卫误伤后的临时止损），宿主只能**整个进程重启**——而重启期间
// 守卫要么全在、要么全不在，中间没有「临时撤掉这一条」这个状态。
//
// 所以本文件提供的不是「可变的 Set」，而是**换整份**的能力：
//
//	撤销 = 重建一份新的 Set（少了某条守卫）+ 整份换上去
//
// 不是「改其中一条」——改其中一条会让「当前有哪些守卫」这件事有两个真相。
//
// # 三条纪律
//
// ① **撤销的归属明确**：换整份这件事只发生在**一个地方**（`Assembly.Swap` /
// `Assembly.SwapFunc`），不是一个散落在各处的自由动作。宿主想撤销，调用点只有
// 一个，审计面也只有一个。
//
// ② **换份要整份换，且原子**：`atomic.Pointer` 交换指针 ⇒ 在途判定跑在**旧份**
// 上（旧份不可变 ⇒ 没有撕裂），下一次判定跑在新份上。不存在「一半旧一半新」
// 的中间态。
//
// ③ **「全部撤销」不是降级，是失守**：换上去的那份不允许为空、不允许是未装配的。
// 空集合在 `New` 那里就被拒绝了（没人看着的调用不该看起来能用），
// 换份面是同一条纪律的运行期落点——**降级可以撤一条，不能撤到一条不剩**。
//
// # 本包的形态对齐同仓的 netguard（不是另发明一套）
//
// 「换整份」这件事本仓已有先例：`netguard` 的「扩展名单整份原子交换」。
// 本文件刻意**沿用它的全部机制**，而不是借个名字另写一版——
// 各写一套必然各判一套，漂移到没人发现为止。对齐的四处：
//
//	netguard                        本包
//	─────────────────────────────    ─────────────────────────────
//	curPolicy atomic.Pointer[Policy]  current atomic.Pointer[assemblyState]
//	代次住在指针目标里（Policy.gen）    代次住在指针目标里（assemblyState.gen）
//	swapPolicy(mutate) 走 CAS 循环     SwapFunc(mutate) 走 CAS 循环
//	订阅表 copy-on-write + 锁外通知     订阅表 copy-on-write + 锁外通知
//
// **为什么代次必须住在被换的那份里**：`PolicyGeneration()` 是当前指针的**投影**，
// 不是另一个计数器。两个计数器并存时它们会短暂不一致，而「快照代次 == 全局代次」
// 这条承诺恰恰是调用方据以判断「我读的这段时间里换过没有」的依据。
// 本包同构：`Assembly.Generation()` 是 `current` 的投影，`Snapshot` 一次读出
// 集合与代次——它们不可能来自两个真相。
//
// **为什么换份要走 CAS 而不是直接 Swap 指针**：直接换指针的语义是「整份替换为
// 这一份」，它**不基于当前那一份**。于是两个并发换份者各自拿旧份算出一份新的，
// 后写者把先写者**静默覆盖**——对守卫撤销来说，这是「**撤销被撤销**」：
// 管理员 A 撤掉了 g1，管理员 B 同时撤掉了 g2，B 的换份把 A 的撤销整个盖掉，
// g1 悄悄复活，且没有任何东西会提示。
//
// 故本包提供两个换份口，语义**必须分清**：
//
//	SwapFunc(ctx, mutate)  基于**当前那一份**的读改写（CAS 循环）——**撤销用这个**
//	Swap(ctx, next)        整份替换为 `next`（不基于当前份）——「我要换成这一份」
//
// # 与 DSH 安全模型下半句的对应
//
// DSH 那套「一切插件都应被声明权限、限定边界、记录轨迹并能**随时撤销**」，
// 本文件补的是后两件里本仓缺的那半：**记录轨迹**（`SwapRecord`）与
// **随时撤销**（`Swap`/`SwapFunc`）——而「限定边界」由 `Set` 的只读性保证，
// 「声明权限」由宿主的审批路径保证（本包不替宿主决定谁能撤销）。
//
// # 谁在什么条件下可以换整份
//
// 本包**不判断**这件事——授权是宿主的事（宿主有 `policy` 审计门与 `gatecheck`）。
// 本包提供的是一个**可判定的接入点**：`SwapGate` 钩子。宿主把自己的授权
// 挂在上面，于是「谁能撤销」变成一条**机械路径**，而不是「改代码 + 重启」。
//
// 闸门返回错误即**拒绝换份**：当前份保持不变（fail-closed——
// 撤销失败不该变成「换上一份没人管的东西」）。

package guardset

import (
	"context"
	"sync/atomic"
)

// assemblyState 装配面发布出去的当前状态：一份集合 + 它的代次。
//
// 代次与集合**在同一处**发布 ⇒ 快照与代次不可能来自两个真相。
// 这是 netguard「代次只存在于策略指针内部，PolicyGeneration() 是它的投影」
// 那条纪律在本包的落点。
type assemblyState struct {
	set *Set
	gen uint64
}

// Snapshot 某一时刻的快照：当前那一份守卫集合 + 它的代次。
//
// 两个字段**必须一起读**——分开读会得到「集合是第 N 代、代次是第 N-1 代」这种
// 自相矛盾的一对（那正是 netguard 修掉的「两套真相」的可观测形态）。
// 故本包只提供 `Assembly.Snapshot()` 这一个读入口。
type Snapshot struct {
	// Set 当前那一份守卫集合（只读，可长期持有）。
	Set *Set

	// Generation 当前那一份的代次。单调递增，不回卷；0 表示「从未换过」。
	Generation uint64
}

// SwapRecord 一次换份的留痕。
//
// 留痕要能回答四个问题：**换了几次**（Seq）、**换走了什么**（From）、
// **换上了什么**（To）、**换完之后是哪一代**（Generation）。
type SwapRecord struct {
	// Seq 换份序号，从 1 起。单调整调。
	//
	// 与 `Generation` 是**同一个计数**（每次成功换份 +1）。保留 `Seq` 是为兼容
	// v0.9.0 的既有调用方；新代码用 `Generation`。
	Seq uint64

	// From 换走的那一份的守卫名（按优先级序）。首次装配时为空。
	From []string

	// To 换上来的那一份的守卫名（按优先级序）。
	To []string

	// Generation 换完之后当前那一份的代次。
	Generation uint64

	// Set 换完之后当前那一份。持有它就一直有效（旧份不被修改）。
	Set *Set
}

// SwapGate 换份闸：判断「这一次换整份是否被允许」。
//
// 返回非 nil 错误即**拒绝换份**，当前份保持不变。
//
// 为什么不把授权判据写进本包：授权是宿主的业务决定（谁能停掉哪条守卫、
// 要不要人审），本包只提供**接入点**——闸门钩子。宿主把自己的审批路径挂上来，
// 「撤销」就从「改代码 + 重启」变成一条机械路径。
//
// # 调用次数
//
// 闸门在**每次发布尝试前**跑一次。CAS 重试（有别的写者抢先）会**重复调用**它，
// 故它必须是**幂等的纯谓词**：写审计、发通知这类副作用请放在别处
// （或放在 mutate 里，但要自己保证幂等）。
type SwapGate func(ctx context.Context, next *Set) error

// Subscription 订阅句柄，传给 `Unsubscribe` 注销。0 表示无效。
type Subscription uint64

// subscriber 一个订阅者：句柄 + 回调。
type subscriber struct {
	handle Subscription
	fn     func(SwapRecord)
}

// AssemblyOption 装配面的可选装配件。
type AssemblyOption func(*assemblyConfig)

type assemblyConfig struct {
	gate     SwapGate
	recorder func(SwapRecord)
}

// WithSwapGate 挂换份闸。g 为 nil 表示不挂（默认不拦）。
//
// 闸在换份**之前**跑，且闸看到的是**即将换上去的那一份**——
// 授权判据要针对「新配置」，而不是「旧配置」。
func WithSwapGate(g SwapGate) AssemblyOption {
	return func(c *assemblyConfig) { c.gate = g }
}

// WithSwapRecorder 挂换份留痕。f 为 nil 表示不留痕（不留痕是显式选择，不是默认）。
//
// 留痕回调在**锁外**调用，故它可以安全地读 `Current()`；
// 但它**不得**回调 `Swap`（那会与正在进行的换份互相等待）。
//
// 回调 panic 会被吞掉（不掀翻整次换份）：换份**已经落地**，让回调抛异常就把
// 已经发生的事实改写成「没发生」——那正是本包要消灭的那类失效。
func WithSwapRecorder(f func(SwapRecord)) AssemblyOption {
	return func(c *assemblyConfig) { c.recorder = f }
}

// Assembly 守卫集合的装配面：持有「当前这一份」，并提供**整份换掉**的能力。
//
// # 不变式
//
//   - 当前份永远非空、非 nil（构造期与换份期都保证）；
//   - 换份是**整份**的，不存在「部分换」；
//   - 换份是原子的：在途判定跑在旧份上，下一次判定跑在新份上；
//   - 旧份**不被修改**——它可能正被在途判定读着；
//   - 代次单调递增、不回卷，且**只有一个真相**（住在被换的那份里）。
//
// # 并发
//
// 判定路径**不加锁**（读一个 atomic.Pointer）；换份走 CAS 循环。
// 订阅表用 `atomic.Pointer` 整体替换（copy-on-write）：通知时取一次快照、
// 在锁外遍历它——读侧永远看不到改了一半的名单。
type Assembly struct {
	current atomic.Pointer[assemblyState]

	gate     SwapGate
	recorder func(SwapRecord)

	// subs 订阅表。整体替换（copy-on-write），与 netguard 同款。
	subs atomic.Pointer[[]subscriber]

	// subSeq 订阅句柄的分配器。它**不是**状态投影（不回答「换过没有」），
	// 只是句柄 ID 的来源，故与代次无关。
	subSeq atomic.Uint64
}

// NewAssembly 构造装配面，初始装入 s。
//
// s 为 nil 即拒绝：装配面一开始就没有守卫，那不是「还没装」，是「没人看着」，
// 而没人看着的调用不该看起来能用（与 `New` 拒绝空集合同源）。
func NewAssembly(s *Set, opts ...AssemblyOption) (*Assembly, error) {
	if s == nil {
		return nil, ErrUnassembled
	}
	cfg := assemblyConfig{}
	for _, o := range opts {
		if o != nil {
			o(&cfg)
		}
	}
	a := &Assembly{gate: cfg.gate, recorder: cfg.recorder}
	a.current.Store(&assemblyState{set: s, gen: 0})
	return a, nil
}

// Current 返回当前这一份（只读快照）。
//
// 返回的指针在**任何时候都仍然有效**——旧份不会被修改，
// 调用方可以放心地把它读到底（这是「撤销靠换整份、不靠改旧份」的直接后果）。
//
// 要同时拿到代次，请用 `Snapshot`（分开读会得到自相矛盾的一对）。
func (a *Assembly) Current() *Set {
	return a.Snapshot().Set
}

// Generation 当前那一份的代次。调用方可在前后各取一次，变化即说明期间换过。
//
// 它是 `current` 的**投影**，不是另一个计数器——与 netguard 的
// `PolicyGeneration()` 同构。
func (a *Assembly) Generation() uint64 {
	return a.Snapshot().Generation
}

// Snapshot 一次读出当前那一份与它的代次。
//
// 两个字段来自**同一次装载**，故不可能自相矛盾。
func (a *Assembly) Snapshot() Snapshot {
	if a == nil {
		return Snapshot{}
	}
	st := a.current.Load()
	if st == nil {
		return Snapshot{}
	}
	return Snapshot{Set: st.set, Generation: st.gen}
}

// SwapFunc 基于**当前那一份**的读改写：mutate 从观察到的当前份算出下一份，
// 整份换上去。
//
// # 这是撤销的正确姿势
//
// mutate 看到的永远是**最新**那一份：CAS 失败（有别的写者抢先）时重试，
// mutate 重新看到新的当前份。于是两个并发撤销者**不会互相覆盖**——
// 管理员 A 撤 g1、管理员 B 撤 g2，两次撤销都生效。
//
// 旧实现若是「读当前份 → 算新的 → 直接换指针」，两个写者会拿到**同一份旧**，
// 后写者把先写者静默盖掉 ⇒ **撤销被撤销**（被撤掉的守卫悄悄复活）。
//
// # mutate 的契约
//
// mutate 可能被调用**多次**（CAS 重试），故它必须是**纯函数**：
// 只读 `cur`、只依赖 `cur` 产出下一份，不做 I/O、不发通知、不改外部状态。
//
// mutate 返回错误 ⇒ 换份放弃，当前份**保持不变**。
// mutate 返回 nil 集合 ⇒ 拒绝（没人看着的调用不该看起来能用）。
//
// # 闸门
//
// 闸（`WithSwapGate`）在**每次发布尝试前**跑，看到即将换上去的那一份。
// CAS 重试会重复调用它 ⇒ 闸门必须是幂等的纯谓词。
func (a *Assembly) SwapFunc(ctx context.Context, mutate func(cur Snapshot) (*Set, error)) error {
	if a == nil {
		return ErrUnassembled
	}
	if mutate == nil {
		return ErrNilMutate
	}
	for {
		old := a.current.Load()
		cur := Snapshot{}
		if old != nil {
			cur = Snapshot{Set: old.set, Generation: old.gen}
		}
		next, err := mutate(cur)
		if err != nil {
			return err
		}
		if next == nil {
			return ErrUnassembled
		}
		if a.gate != nil {
			if err := a.gate(ctx, next); err != nil {
				return err
			}
		}
		// 代次从**观察到的**旧份推导（不是另一个计数器 +1），
		// CAS 保证同一时刻只有一个写者胜出 ⇒ 代次天然唯一、单调、不回卷。
		newState := &assemblyState{set: next, gen: cur.Generation + 1}
		if a.current.CompareAndSwap(old, newState) {
			// 通知在交换真正落地之后、锁外：发了就等于告诉订阅者「换过了」，
			// 而它此刻确实换了。
			a.notify(old, newState)
			return nil
		}
		// 有别的写者抢先 ⇒ 重来一次（mutate 会重新看到最新的那份）。
	}
}

// Swap 整份替换为 next。
//
// # 与 SwapFunc 的区别（必须分清）
//
//	SwapFunc(ctx, mutate)  基于**当前那一份**算下一份 ⇒ 并发下**不丢更新**
//	Swap(ctx, next)        直接换成 next ⇒ **不基于当前那一份**
//
// `Swap` 的语义是「我要换成这一份」（例如从配置中心读到的全新名单），
// 不是「在当前那份上改一改」。**拿它做读改写就会丢更新**——
// 两个并发撤销者会互相覆盖，被撤掉的守卫悄悄复活，且没有任何东西会提示。
//
// 要撤销某条守卫，请用 `SwapFunc`。
//
// # 三条语义
//
//  1. **整份换**：next 取代当前份的全部内容，不是「加一条」「删一条」；
//  2. **原子**：在途判定跑在旧份上，下一次判定跑在新份上，没有中间态；
//  3. **fail-closed**：next 为 nil ⇒ 拒绝换份，当前份**保持不变**
//     （撤销失败不等于「换上一份没人管的东西」）。
//
// 换份被闸门拒绝时，返回闸门的错误，当前份同样保持不变。
func (a *Assembly) Swap(ctx context.Context, next *Set) error {
	return a.SwapFunc(ctx, func(Snapshot) (*Set, error) { return next, nil })
}
