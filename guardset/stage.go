// stage.go —— 把守卫集合接成 invoke 链上的一环。
//
// 本包不自己提供链：顺序执行、留痕、panic 兜底都由 oagentkit/toolgate 的
// invoke.Chain 负责（理由见 guardset.go 包注释「与 invoke.Chain 的关系」）。
// 这里只做一件上游按设计不做的事——把「一并求值 + 拒绝合成」压成单独一环，
// 于是链照旧短路，而模型在这一环就已经拿到了全部拒绝。

package guardset

import (
	"context"

	"github.com/oagentkit/toolgate/invoke"
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
