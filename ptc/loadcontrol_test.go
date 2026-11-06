package ptc_test

// 本文件提供一条**与负载无关的标尺**，供「墙钟上界」类判据换算成比值。
//
// # 为什么需要它（issue #33）
//
// ptc 有两条判据量的是**墙钟**：子进程启动+一次往返、每次工具调用的边际成本。
// 墙钟里混着两个量：
//
//  1. 被测代码的成本（想守的那个界）；
//  2. 机器当前的排队长短（与本仓无关，runner 上完全不可控）。
//
// min-of-5 只是把「三连运气差」换成「五连运气差」，没有改变混合的事实。实测：
// 机器同时压着 5 路重活时，同一条用例从 13ms 量级跳到 **40s 量级**（判红 127.87s），
// 而把重活停掉、代码一行不改就立刻回到 22ms——那是**假红**。
//
// 假红比漏判更贵：它训练人忽略这条门禁，下一次真劣化就一起被忽略。
//
// # 标尺是什么
//
// 「在这台机器上、**现在**，起一个进程再让它退出，要多久」——把本测试二进制
// 用 -test.run=^$ 重跑一遍（不跑任何用例，起来即退），取多次采样的最小值。
//
// 它不是一个绝对量，而是**同一份负载的读数**：机器越忙，它越大。于是
//
//	上界 = max(固定下限, K × 标尺)
//
// 在空载机器上由固定下限兜住（判据保持原来的严格度），在满载机器上随负载
// 同步放大（不再假红）。而被测代码的真劣化**不会**抬高标尺——它只抬高自己。
//
// # 这个做法判不了什么（写明，而不是假装覆盖）
//
//   - 它抓不住「子进程本身变慢」：那是另一个量，标尺不跟着变；
//   - 标尺自身也受负载影响，两次读数可能差一倍 ⇒ 比值判据的余量不能贴着实测，
//     这里给的是 K 倍余量，不是 1.0 倍。

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// loadControlSamples 标尺采样数。取 3：标尺要的是量级，不是精度——
// 再多采样只是多起几个进程，换不来更好的结论。
const loadControlSamples = 3

// loadControl 现测「这台机器此刻有多忙」的标尺：重跑本测试二进制（不跑用例）
// 的启动+退出耗时，取最小值。
//
// 用最小值而不是均值/中位数：噪声只把耗时**推高**，不会产生更快的样本，
// 故最小值最接近真成本（与两条被测判据的统计量口径一致，见 subprocess_bench_test.go）。
func loadControl(t *testing.T) time.Duration {
	t.Helper()
	var best time.Duration
	for i := 0; i < loadControlSamples; i++ {
		// -test.run=^$ 不匹配任何用例 ⇒ 进程起来就退出；-test.bench=^$ 同理。
		// 带 ctx 的超时保护：标尺卡住时不能把整条判据一起拖住，宁可直接红并说明原因。
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$", "-test.bench=^$")
		start := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatalf("标尺进程启动失败（上界无从换算，判据给不出结论）: %v", err)
		}
		if d := time.Since(start); i == 0 || d < best {
			best = d
		}
	}
	return best
}

// boundWithControl 把「固定下限」与「K 倍标尺」取大，得到本次运行适用的上界。
//
// 两个量缺一不可：只有固定下限时满载会假红（本 issue）；只有比值时空载判据
// 会松到没有意义（标尺再小，K×标尺也不该允许被测成本到任意量级）。
func boundWithControl(floor time.Duration, k float64, control time.Duration) time.Duration {
	scaled := time.Duration(float64(control) * k)
	if scaled > floor {
		return scaled
	}
	return floor
}
