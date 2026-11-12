// bench_test.go —— GuardSet 的性能基线（issue #101）。
//
// GuardSet 在每次工具调用的判定环上：Evaluate 是热路径，多守卫装配下的
// 线性求值成本直接进每次调用的延迟。基线给出「N 守卫全放行」与「N 守卫
// 一票拒绝」两个形态的量级——拒绝形态多一次 Denials 聚合。
//
// # 怎么读这些数
//
// 基线不是门槛（口径见 docs/BENCH-BASELINE-20261113.md）。数据规模：
// 8 守卫（Rank 递增，判定函数立即返回，排除守卫自身耗时）。
package guardset_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/oagentkit/guardx/invoke"
	"github.com/yi-nology/goagent-kernel/guardset"
)

// benchGuards n 条恒放行守卫（Rank 0..n-1，判定立即返回）。
func benchGuards(n int) []guardset.Guard {
	guards := make([]guardset.Guard, 0, n)
	for i := range n {
		guards = append(guards, guardset.Guard{
			Name: fmt.Sprintf("guard-%d", i),
			Rank: i,
			Check: func(context.Context, invoke.Call) invoke.Verdict {
				return invoke.Verdict{Allow: true}
			},
		})
	}
	return guards
}

func BenchmarkGuardSetEvaluateAllow(b *testing.B) {
	s, err := guardset.New(benchGuards(8)...)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	call := invoke.Call{Name: "bench-tool"}
	for b.Loop() {
		out := s.Evaluate(ctx, call)
		if !out.Allowed {
			b.Fatal("恒放行守卫被判拒")
		}
	}
}

func BenchmarkGuardSetEvaluateDeny(b *testing.B) {
	guards := benchGuards(8)
	guards[4].Check = func(context.Context, invoke.Call) invoke.Verdict {
		return invoke.Verdict{Allow: false, Reason: "bench deny"}
	}
	s, err := guardset.New(guards...)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	call := invoke.Call{Name: "bench-tool"}
	for b.Loop() {
		out := s.Evaluate(ctx, call)
		if out.Allowed {
			b.Fatal("带拒绝守卫被判放行")
		}
	}
}

func BenchmarkGuardSetNew(b *testing.B) {
	guards := benchGuards(8)
	for b.Loop() {
		if _, err := guardset.New(guards...); err != nil {
			b.Fatal(err)
		}
	}
}
