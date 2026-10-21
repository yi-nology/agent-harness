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
// ① **撤销的归属明确**：换整份这件事只发生在**一个地方**（`Assembly.Swap`），
// 不是一个散落在各处的自由动作。宿主想撤销，调用点只有一个，审计面也只有一个。
//
// ② **换份要整份换，且原子**：`atomic.Pointer` 交换指针 ⇒ 在途判定跑在**旧份**
// 上（旧份不可变 ⇒ 没有撕裂），下一次判定跑在新份上。不存在「一半旧一半新」
// 的中间态。
//
// ③ **「全部撤销」不是降级，是失守**：换上去的那份不允许为空、不允许是未装配的。
// 空集合在 `New` 那里就被拒绝了（没人看着的调用不该看起来能用），
// 换份面是同一条纪律的运行期落点——**降级可以撤一条，不能撤到一条不剩**。
//
// # 与 DSH 安全模型下半句的对应
//
// DSH 那套「一切插件都应被声明权限、限定边界、记录轨迹并能**随时撤销**」，
// 本文件补的是后两件里本仓缺的那半：**记录轨迹**（`SwapRecord`）与
// **随时撤销**（`Swap`）——而「限定边界」由 `Set` 的只读性保证，
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
	"sync"
	"sync/atomic"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oekit/pkg/errors"
)

// SwapRecord 一次换份的留痕。
//
// 留痕要能回答三个问题：**换了几次**（Seq）、**换走了什么**（From）、
// **换上了什么**（To）。守卫名按优先级序，与 `Set.Guards()` 同一口径。
type SwapRecord struct {
	// Seq 换份序号，从 1 起。单调整调。
	Seq uint64

	// From 换走的那一份的守卫名（按优先级序）。首次装配时为空。
	From []string

	// To 换上来的那一份的守卫名（按优先级序）。
	To []string
}

// SwapGate 换份闸：判断「这一次换整份是否被允许」。
//
// 返回非 nil 错误即**拒绝换份**，当前份保持不变。
//
// 为什么不把授权判据写进本包：授权是宿主的业务决定（谁能停掉哪条守卫、
// 要不要人审），本包只提供**接入点**——闸门钩子。宿主把自己的审批路径挂上来，
// 「撤销」就从「改代码 + 重启」变成一条机械路径。
type SwapGate func(ctx context.Context, next *Set) error

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
//   - 旧份**不被修改**——它可能正被在途判定读着。
//
// # 并发
//
// 判定路径**不加锁**（读一个 atomic.Pointer）；只有换份的**留痕序号**需要串行化，
// 用一把短锁保护。换份本身靠 `atomic.Pointer.Swap` 保证原子，
// 不依赖那把锁——锁只保证「Seq 的分配顺序 == 换份顺序」。
type Assembly struct {
	current atomic.Pointer[Set]

	// mu 只保护 seq 的分配与记录构造，不保护 current 的读写。
	mu   sync.Mutex
	seq  uint64
	gate SwapGate

	recorder func(SwapRecord)
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
	a.current.Store(s)
	return a, nil
}

// Current 返回当前这一份（只读快照）。
//
// 返回的指针在**任何时候都仍然有效**——旧份不会被修改，
// 调用方可以放心地把它读到底（这是「撤销靠换整份、不靠改旧份」的直接后果）。
func (a *Assembly) Current() *Set {
	if a == nil {
		return nil
	}
	return a.current.Load()
}

// Swap 整份换掉当前集合。
//
// # 三条语义
//
//  1. **整份换**：next 取代当前份的全部内容，不是「加一条」「删一条」；
//  2. **原子**：在途判定跑在旧份上，下一次判定跑在新份上，没有中间态；
//  3. **fail-closed**：next 为 nil ⇒ 拒绝换份，当前份**保持不变**
//     （撤销失败不等于「换上一份没人管的东西」）。
//
// 换份被闸门拒绝时，返回闸门的错误，当前份同样保持不变。
//
// 换份成功后会调用留痕回调（若挂了）。回调在锁外执行，且**不得**回调 Swap。
func (a *Assembly) Swap(ctx context.Context, next *Set) error {
	if a == nil {
		return ErrUnassembled
	}
	if next == nil {
		return ErrUnassembled
	}
	if a.gate != nil {
		if err := a.gate(ctx, next); err != nil {
			return err
		}
	}
	a.mu.Lock()
	prev := a.current.Swap(next)
	a.seq++
	rec := SwapRecord{Seq: a.seq, From: guardNames(prev), To: guardNames(next)}
	a.mu.Unlock()

	if a.recorder != nil {
		a.recorder(rec)
	}
	return nil
}

// Evaluate 按**当前这一份**求值。
//
// 装载当前份只做一次 ⇒ 一次判定全程对着同一份走，
// 不会因为中途换份而出现「一半旧、一半新」的合成结论。
func (a *Assembly) Evaluate(ctx context.Context, c invoke.Call) Outcome {
	if a == nil {
		return (&Set{}).Evaluate(ctx, c)
	}
	s := a.current.Load()
	if s == nil {
		return (&Set{}).Evaluate(ctx, c)
	}
	return s.Evaluate(ctx, c)
}

// Stage 把装配面接成 invoke 链上的一环。
//
// 与 `Stage(*Set)` 的关键差别：**每次判定都取当前那一份**。
// 于是换份之后**不需要重接链**——链上那一环始终指向装配面，
// 装配面指向当前份。这就是「撤销有机械路径」的落点：
// 换份即生效，不需要重启、不需要重建链。
//
// 约定与 `Stage(*Set)` 一致：拒绝走 (Verdict, nil)，不返回 error。
func (a *Assembly) Stage() invoke.Stage {
	return invoke.StageFunc{
		StageName: StageName,
		Fn: func(ctx context.Context, c invoke.Call) (invoke.Verdict, error) {
			out := a.Evaluate(ctx, c)
			if out.Allowed {
				return invoke.Verdict{Allow: true}, nil
			}
			return invoke.Verdict{
				Allow:     false,
				Reason:    out.Reason(),
				Retryable: out.Retryable,
			}, nil
		},
	}
}

// StageWithGuidance 与 Stage 同义，但把**面向模型的指引**放进 Reason。
//
// 为什么要单独一个出口而不是给 Stage 加选项：Reason 的用途由宿主决定
// （有人拿去留痕，有人直接送进模型可见的失败消息），而这两种用途要的文本不同。
// 与 `StageWithGuidance(*Set)` 同一条理由。
func (a *Assembly) StageWithGuidance() invoke.Stage {
	return invoke.StageFunc{
		StageName: StageName,
		Fn: func(ctx context.Context, c invoke.Call) (invoke.Verdict, error) {
			out := a.Evaluate(ctx, c)
			if out.Allowed {
				return invoke.Verdict{Allow: true}, nil
			}
			return invoke.Verdict{
				Allow:     false,
				Reason:    out.Guidance(),
				Retryable: out.Retryable,
			}, nil
		},
	}
}

// guardNames 取一份集合的守卫名快照（按优先级序）。
func guardNames(s *Set) []string {
	if s == nil {
		return nil
	}
	return s.Guards()
}

// Without 返回一份**去掉指定守卫**的新集合。原集合**不变**。
//
// # 为什么是「返回新集合」而不是「删掉这一条」
//
// 因为 `Set` 不可变是它的不变量。删一条若能改到原集合，就有了「当前有哪些守卫」
// 两个真相，且并发读它就是竞态。故撤销一条守卫的机械路径是：
//
//	next, err := cur.Without("rate_limiter")   // 产出新的一份
//	err = asm.Swap(ctx, next)                  // 整份换上去
//
// # 两条拒绝
//
//   - 守卫名不在集合里 ⇒ 拒绝（**不静默忽略**）。静默忽略会让宿主以为
//     「撤掉了某条守卫」，实际那份集合里根本没有它——撤销的是个不存在的东西。
//   - 去掉之后集合为空 ⇒ 拒绝（`ErrEmptySet`）。**全部撤销不是降级，是失守**。
func (s *Set) Without(names ...string) (*Set, error) {
	if s == nil {
		return nil, ErrUnassembled
	}
	if len(names) == 0 {
		return s, nil
	}
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	kept := make([]Guard, 0, len(s.guards))
	for _, g := range s.guards {
		if !drop[g.Name] {
			kept = append(kept, g)
		}
	}
	for _, n := range names {
		if !s.hasGuard(n) {
			return nil, errors.Wrap(ErrUnknownGuard, "guardset: 撤销不存在的守卫", "guard", n)
		}
	}
	// New 会重新校验（空集合拒绝构造、名字不得为空/重复）并按 (Rank, 装配序) 排序。
	return New(kept...)
}

// hasGuard 判断集合里有没有这个守卫名。
func (s *Set) hasGuard(name string) bool {
	for _, g := range s.guards {
		if g.Name == name {
			return true
		}
	}
	return false
}
