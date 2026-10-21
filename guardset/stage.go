// stage.go —— 把守卫集合接成 invoke 链上的一环。
//
// 本包不自己提供链：顺序执行、留痕、panic 兜底都由 oagentkit/guardx 的
// invoke.Chain 负责（理由见 guardset.go 包注释「与 invoke.Chain 的关系」）。
// 这里只做一件上游按设计不做的事——把「一并求值 + 拒绝合成」压成单独一环，
// 于是链照旧短路，而模型在这一环就已经拿到了全部拒绝。
//
// # 本文件有两套：绑死某一份的，与绑装配面的
//
//	Stage(*Set)          绑死**某一份**——换份对它无效（要重接链）
//	(*Assembly).Stage()  绑**装配面**——每次判定取当前那一份（换份即生效）
//
// 差别不是风格：撤销守卫时，前者要重接链才能生效，后者不用。
// 装配面那一套连同它的求值都放在本文件，因为它与上面是**同一件事**：
// 「把守卫接成判定环上的一环」。

package guardset

import (
	"context"

	"github.com/oagentkit/guardx/invoke"
)

// StageName 本环在链上的名字。固定值：留痕要能回答「是哪一环拒的」。
const StageName = "guard_set"

// Stage 把守卫集合接成 invoke 链上的一环。
//
// 约定与 invoke 链一致：拒绝走 (Verdict, nil)，不返回 error——本环的「拒绝」是
// 正常判定结论，而 error 在链上被归口成「这一环自己坏了」，两者对调用方的处置不同，
// 混起来会让一次正常的策略否定看起来像故障。
//
// set 为 nil 时按「未装配」拒绝（fail-closed）：没装配守卫集合的链等于没人看着
// 调用，与 New 拒绝构造空集合是同一条纪律的运行期落点。
func Stage(s *Set) invoke.Stage {
	return invoke.StageFunc{
		StageName: StageName,
		Fn: func(ctx context.Context, c invoke.Call) (invoke.Verdict, error) {
			out := s.Evaluate(ctx, c)
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
// 为什么提供两个而不是给 Stage 加选项：Reason 的用途由宿主决定（有人拿去留痕，
// 有人直接送进模型可见的失败消息），而这两种用途要的文本不同
// （留痕要全且准，提示词要能照着做）。做成两个明确出口，
// 比留一个布尔开关让每个宿主自己记得切换要可靠。
func StageWithGuidance(s *Set) invoke.Stage {
	return invoke.StageFunc{
		StageName: StageName,
		Fn: func(ctx context.Context, c invoke.Call) (invoke.Verdict, error) {
			out := s.Evaluate(ctx, c)
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

// Evaluate 按**当前那一份**求值。
//
// 装载当前份只做一次 ⇒ 一次判定全程对着同一份走，
// 不会因为中途换份而出现「一半旧、一半新」的合成结论。
//
// 结论里带**代次**（`Outcome.Generation`）——它回答「这次判定是按第几代做的」，
// 是审计要问的第一个问题之一。
func (a *Assembly) Evaluate(ctx context.Context, c invoke.Call) Outcome {
	if a == nil {
		return (&Set{}).Evaluate(ctx, c)
	}
	st := a.current.Load()
	if st == nil {
		return (&Set{}).Evaluate(ctx, c)
	}
	out := st.set.Evaluate(ctx, c)
	out.Generation = st.gen
	return out
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
