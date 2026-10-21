// Command ptcchild 是「子进程形态」的小程序，供跨环境验证用。
//
// # 它有两个形态
//
// 默认：调 `ptc.MaybeRunChild()`，是**正常的子进程**（会回 Result 帧）。
//
// 设了 `AGENT_HARNESS_PTC_CHILD_MODE=noop`：**不回结果**——读掉 stdin
// （模拟收到了 Start 帧）然后干等。这模拟「宿主忘了在 main 里调
// `ptc.MaybeRunChild()`」的真实形态：子进程跑起了宿主的完整 main，
// 不退出、也不回 Result 帧。
//
// # 为什么要有它
//
// 验证「子进程不回结果时父侧必须有限时间内收场」这条，需要一个
// **真的不回结果的子进程**。现场编译需要目标环境有 Go 工具链——
// 而唯一能验的 Linux 环境（能建出子 cgroup 的那台）恰恰没有 Go。
//
// 故把它做成一个**可交叉编译的入口**：本地交叉编译好，传到目标机跑，
// 用环境变量 `AGENT_HARNESS_PTC_CHILD_BIN` / `AGENT_HARNESS_PTC_NOOP_BIN`
// 指给测试（见 ptc/subprocess_exec_test.go 与 ptc/noreport_test.go）。
//
// 用法：
//
//	go build -o ptcchild ./cmd/ptcchild/                       # 正常子进程
//	AGENT_HARNESS_PTC_CHILD_MODE=noop ./ptcchild                # 不回结果
package main

import (
	"io"
	"os"
	"time"

	"github.com/yi-nology/agent-harness/ptc"
)

func main() {
	if os.Getenv("AGENT_HARNESS_PTC_CHILD_MODE") == "noop" {
		// 不回结果：读掉 stdin（像收到了 Start 帧），然后干等。
		// 父侧应当在期限到了时收场并杀掉本进程。
		_, _ = io.Copy(io.Discard, os.Stdin)
		time.Sleep(60 * time.Second)
		return
	}
	ptc.MaybeRunChild()
}
