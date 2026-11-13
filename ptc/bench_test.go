// bench_test.go —— PTC 主体的性能基线（issue #101）。
//
// 子进程后端已有基线（subprocess_bench_test.go，管道往返延迟）；这里补的是
// **进程内主体**：goja VM 起停 + 程序执行 + 判定环逐调用过闸——即
// `sandbox.Execute` 选进程内档位时宿主每次付出的成本。
//
// # 怎么读这些数
//
// 这是**基线**不是门槛：给宿主一个量级判断（一次进程内 PTC 执行 ~XX µs 量级，
// 其中 VM 构造占大头），不判定合格与否。数字随机器变化（机器口径见
// docs/BENCH-BASELINE-20261113.md），故不断言阈值——断言阈值会变成
// 「因机器不同而随机红」的 flaky 门槛。
//
// 数据规模：程序内循环调用绑定 calls 次（1 / 10 / 50 三档，50 是缺省
// MaxCalls 上限），绑定返回固定 4 字节，排除工具自身耗时。
package ptc_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/oagentkit/guardx/invoke"
	"github.com/yi-nology/goagent-kernel/ptc"
)

// benchAllowStage 一条恒放行的判定环（排除判定自身耗时之外的因素；
// 判定环逐调用过闸的成本含在本基线里——它是主体路径的一部分）。
func benchAllowStage() invoke.Stage {
	return invoke.StageFunc{
		StageName: "bench-allow",
		Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			return invoke.Verdict{Allow: true}, nil
		},
	}
}

// benchPingBinding 返回固定值的绑定（同 subprocess_bench_test.go 的口径）。
func benchPingBinding() ptc.Binding {
	return ptc.Binding{
		Name:      "ping",
		Signature: "ping()",
		Call: func(context.Context, string) (string, error) {
			return "pong", nil
		},
	}
}

// benchCallProgram 程序内循环 calls 次调用绑定后返回。
func benchCallProgram(calls int) string {
	return fmt.Sprintf(
		`let s = 0; for (let i = 0; i < %d; i++) { s += (await host.ping()).length; } return s;`,
		calls)
}

func BenchmarkPTCRunInProcess(b *testing.B) {
	for _, calls := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("calls=%d", calls), func(b *testing.B) {
			ctx := context.Background()
			bindings := []ptc.Binding{benchPingBinding()}
			opts := ptc.Options{Stages: []invoke.Stage{benchAllowStage()}}
			prog := benchCallProgram(calls)
			for b.Loop() {
				res := ptc.Run(ctx, prog, bindings, opts)
				if !res.OK {
					b.Fatalf("程序执行失败：%s %s", res.ErrorKind, res.Error)
				}
			}
		})
	}
}
