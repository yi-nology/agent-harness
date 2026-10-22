package ptc_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/yi-nology/agent-harness/ptc"
)

// 本文件是**管道往返延迟**的实测（设计档 §九 第 3 条：「管道往返有延迟……
// 但要**实测**」——此前从未测过）。
//
// # 为什么这个数据重要
//
// 子进程后端把一次工具调用从「进程内函数调用」变成「一次 IPC 往返」。
// 这个量级差决定了两件事：
//   1. 宿主能不能在**程序内循环**调几百次工具（若单次 50µs，500 次就是 25ms，
//      可接受；若单次 5ms，500 次就是 2.5s，不可接受）；
//   2. 宿主该把「重活」放工具里还是放程序里。
//
// 没有实测数据，宿主只能凭「IPC 应该挺快」拍脑袋——而拍脑袋的代价是
// 上线后才发现某个用法撑不住。
//
// # 测的是什么
//
// 两条路径，**同一个绑定**（返回固定值），只差「在父进程内直接调」还是
// 「子进程发请求、父侧经管道应答」：
//
//   - 进程内：ptc.Run（goja 在本进程解释程序，绑定是普通函数调用）
//   - 子进程：SubprocessProvider.Exec（goja 在子进程，绑定经管道往返）
//
// 差值即**一次工具调用的 IPC 往返开销**。
//
// # 怎么读这些数
//
// 这是**基准**，不是门槛：它给宿主一个量级判断的依据，不判定合格与否。
// 数字随机器与负载变化，故每项都报 ns/op 的分布而不是断言某个阈值
// （断言阈值会变成一条「因机器不同而随机红」的 flaky 门槛）。

// benchBinding 一个返回固定值的绑定（排除工具自身的耗时，只量往返）。
func benchBinding() ptc.Binding {
	return ptc.Binding{
		Name:      "ping",
		Signature: "ping()",
		Call: func(context.Context, string) (string, error) {
			return "pong", nil
		},
	}
}

// benchProgram 程序内循环 n 次调用绑定。
//
// 绑定挂在 **host.<名字>** 下（子进程里只有远程桩，形态与进程内同一套），
// 故程序里写 `host.ping()` 而不是 `ping()`——写成后者会得到
// 「ReferenceError: ping is not defined」。
func benchProgram(n int) string {
	return fmt.Sprintf(`
		var total = 0;
		for (var i = 0; i < %d; i++) { total += host.ping().length; }
		return total;
	`, n)
}

// BenchmarkToolCallInProcess 进程内：n 次工具调用的总耗时。
func BenchmarkToolCallInProcess(b *testing.B) {
	const calls = 50
	bindings := []ptc.Binding{benchBinding()}
	prog := benchProgram(calls)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res := ptc.Run(ctx, prog, bindings, ptc.Options{})
		if !res.OK {
			b.Fatalf("进程内执行失败: %v", res.Error)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(calls), "calls/op")
}

// BenchmarkToolCallSubprocess 子进程：n 次工具调用的总耗时（含一次 IPC 往返/次）。
func BenchmarkToolCallSubprocess(b *testing.B) {
	bin := buildChildProgramTB(b)
	const calls = 50
	prog := benchProgram(calls)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
			ExePath:  bin,
			Program:  prog,
			Bindings: []ptc.Binding{benchBinding()},
			Timeout:  30 * time.Second,
		})
		out, err := p.Exec(ctx, sandbox.Request{Program: prog})
		if err != nil {
			b.Fatalf("子进程执行失败: %v", err)
		}
		if !out.OK {
			b.Fatalf("子进程内程序失败: %v", out.Err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(calls), "calls/op")
}

// TestSubprocessMarginalCallCost 实测**边际往返成本**：同一个子进程内，
// 「多调一次」到底多花多少时间。
//
// # 为什么用差分法而不是直接测
//
// 直接测「N 次调用耗时」会把**子进程启动成本**（起进程 + goja 初始化 +
// 管道建立）摊进每次调用——而启动是**一次性**的，摊进去会严重高估往返成本
// （实测：50 次调用的总耗时里，启动占了大头）。
//
// 差分法：(T(50) − T(1)) / 49 = **每次调用的边际成本**——启动成本被消掉了。
//
// 这个数字才是宿主做决策时需要的：「程序内多循环一次工具调用，代价是多少」。
func TestSubprocessMarginalCallCost(t *testing.T) {
	bin := buildChildProgram(t)
	ctx := context.Background()

	// T(n)：同一个子进程内跑 n 次调用，取多次的中位数（减少调度抖动）。
	measure := func(n int) time.Duration {
		prog := benchProgram(n)
		var samples []time.Duration
		for i := 0; i < 3; i++ {
			p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
				ExePath:  bin,
				Program:  prog,
				Bindings: []ptc.Binding{benchBinding()},
				// 调高调用数预算：测量本身要调很多次（否则被预算闸拦下——
				// 那正是它该做的事，见「预算闸真的在工作」这条备注）。
				MaxCalls: 10000,
				Timeout:  30 * time.Second,
			})
			start := time.Now()
			out, err := p.Exec(ctx, sandbox.Request{Program: prog})
			elapsed := time.Since(start)
			if err != nil {
				t.Fatalf("子进程执行失败: %v", err)
			}
			if !out.OK {
				t.Fatalf("子进程内程序失败: %v", out.Err)
			}
			samples = append(samples, elapsed)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
		return samples[len(samples)/2]
	}

	const one, many = 1, 51
	t1 := measure(one)
	tN := measure(many)
	marginal := (tN - t1) / (many - one)

	t.Logf("1 次调用: %v；%d 次调用: %v；边际每次: %v", t1, many, tN, marginal)

	// 门槛：固定的绝对值，**贴着实测值**（实测 ≈ 174µs）。
	//
	// # 为什么不用宽松上界
	//
	// 本条初版取 50ms/次（比实测宽三个数量级）。但实测发现：**测量噪声本身
	// 就能掩盖一个数量级的劣化**——给每次调用加一次额外管道往返后，
	// 边际成本反而从 174µs 变成 112µs（噪声 > 信号）。
	// 那种门槛**永远碰不到**，等于没有判据。
	//
	// ⇒ 门槛必须**贴近实测值**：1ms/次比实测（174µs）宽约 5.7 倍——
	// 机器差异（CI runner 比开发机慢）由这个余量吸收，而真正的劣化
	// （多一次往返、多一次拷贝、缓冲区策略变了）会明显越线。
	//
	// 为什么**不**拿「本次测到的值」当基线（那会恒真）：
	// 判据必须独立于被测量——否则它测的是「本次测量与本次测量之比」。
	const limit = time.Millisecond
	if marginal > limit {
		t.Errorf("每次工具调用的边际成本 %v，超过上界 %v（往返成本劣化了一个数量级？）", marginal, limit)
	}
}

// TestSubprocessOverheadIsBounded 钉住一条**上界**：子进程的一次执行（含启动）
// 不应是「慢到没法用」的量级。
//
// # 为什么测「启动」而不是「摊薄到每次调用」
//
// 初版测的是「含启动的摊薄值」（20 次调用 ÷ 20），上界 50ms。
// 而实测摊薄值 ≈ 23ms ⇒ **余量只有 2.2 倍**，不是初版注释写的「三个数量级」。
// ⇒ Go 的测试套件**并行跑包**（ptc 的子进程测试与其他包的测试抢 CPU），
// 负载一上来摊薄值就越线——**本轮实测红过一次**：131.9ms/次（≈5.7 倍）。
// ⇒ 一条会**偶发红**的判据比没有判据更糟：它训练人忽略红色。
//
// # 现在的形态
//
//   - 测**启动成本**（一次调用：子进程起 + 一次往返），**取 3 次采样的最小值**
//     ——最小值是「最不受干扰」的那一次；负载抬高的是均值与中位数，不是最小值；
//   - 上界**宽松**（250ms）：本地实测 ≈13.4ms ⇒ 约 18 倍余量，CI runner 慢 10 倍也碰不到。
//
// 它**不是**性能承诺，是「劣化告警线」：真出事故（管道握手多一轮、启动路径多做了一件事）
// 会明显越线。
//
// # 每次往返的劣化由另一条钉
//
// 「一次工具调用的往返成本」由同文件的 `TestSubprocessMarginalCallCost` 用
// **差分法**判（(T(51)−T(1))/50 ≤ 1ms）——差分对负载不敏感，故那条**不偶发红**。
// 本条管启动，两者不重叠。
func TestSubprocessOverheadIsBounded(t *testing.T) {
	bin := buildChildProgram(t)
	ctx := context.Background()

	// 启动成本：一次调用（含子进程启动）。取多次采样的**最小值**。
	const samples = 3
	var best time.Duration
	for i := 0; i < samples; i++ {
		prog := benchProgram(1)
		start := time.Now()
		p := ptc.NewSubprocessProvider(ptc.SubprocessOptions{
			ExePath:  bin,
			Program:  prog,
			Bindings: []ptc.Binding{benchBinding()},
			Timeout:  30 * time.Second,
		})
		out, err := p.Exec(ctx, sandbox.Request{Program: prog})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("子进程执行失败: %v", err)
		}
		if !out.OK {
			t.Fatalf("子进程内程序失败: %v", out.Err)
		}
		if i == 0 || elapsed < best {
			best = elapsed
		}
	}

	const limit = 250 * time.Millisecond
	if best > limit {
		t.Errorf("子进程启动+一次往返 %v，超过上界 %v（启动路径劣化了一个数量级？）", best, limit)
	}
	t.Logf("子进程启动+一次往返: %v（%d 次采样的最小值）", best, samples)
}
