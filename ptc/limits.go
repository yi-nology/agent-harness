package ptc

import (
	"time"

	"github.com/oagentkit/guardx/sandbox"
)

// runLimits 一次执行的三项预算上限（已按「<=0 即缺省」归一）。
type runLimits struct {
	timeout  time.Duration
	maxCalls int
	maxBytes int
}

// normalizeLimits 补齐三项上限的缺省值。
//
// # 为什么必须只有一份
//
// 这三项在**三个入口**各归一一次：进程内 `Run`（ptc.go）、子进程父侧的
// `SubprocessProvider.run`（subprocess.go）、子进程内的 `runProgramInChild`
// （child_run.go）。写的是同一套口径，却分处**两个进程**——任何一处单独改，
// 同一段程序换个后端跑就得到不同预算，而症状是「换成子进程后端后，程序莫名
// 先超时了」。这类故障最容易先被怀疑到程序本身，因为程序一字未改。
//
// # 负值也是缺省
//
// 判据是 `<= 0` 而不是 `== 0`：宿主从配置读到 -1（「没配这一项」）与读到 0
// （「用默认」）在此同义。三项预算**没有「关掉」的写法**——三闸就是执行域的
// 边界，没有边界就不该执行（见包注释）。
func normalizeLimits(timeout time.Duration, maxCalls, maxBytes int) runLimits {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if maxCalls <= 0 {
		maxCalls = DefaultMaxCalls
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return runLimits{timeout: timeout, maxCalls: maxCalls, maxBytes: maxBytes}
}

// effectiveBudget 装配期配置与每请求预算**逐项取严**（issue #66）。
//
// 同一协议的两个后端此前预算语义分叉：进程内 Provider 把 req.Budget 全量
// 透传，本后端整个不读它——宿主经 sandbox.Execute 请求更紧的墙钟/调用数/
// 字节时，子进程路静默按装配期宽限跑，与 #46 修掉的「判过的程序 ≠ 实际跑的
// 程序」同族，这次是预算。
//
// 取严而不是「不一致即拒」：请求比装配紧 ⇒ 按请求（宿主的要求被兑现）；
// 请求比装配宽 ⇒ 按装配（安全方向，不放大权限）；req 项零值 = 该项没要求 ⇒
// 沿用装配期配置（Budget 的零值语义，也是既有调用点的行为）。两个方向都不
// 产生「请求了 A、实际按 B 跑」的静默落差。
//
// 内存同理：请求了上限而后端没配 ⇒ 按请求建 cgroup（建不出即 fail closed，
// 与 Supports 的探测口径一致——不会退回「不设上限地跑」）。
func (p *SubprocessProvider) effectiveBudget(req sandbox.Budget) (runLimits, int64) {
	lim := normalizeLimits(p.opts.Timeout, p.opts.MaxCalls, p.opts.MaxBytes)
	if req.WallClock > 0 && req.WallClock < lim.timeout {
		lim.timeout = req.WallClock
	}
	if req.MaxCalls > 0 && req.MaxCalls < lim.maxCalls {
		lim.maxCalls = req.MaxCalls
	}
	if req.MaxBytes > 0 && req.MaxBytes < lim.maxBytes {
		lim.maxBytes = req.MaxBytes
	}
	maxMemory := p.opts.MaxMemory
	if req.MaxMemory > 0 && (maxMemory <= 0 || req.MaxMemory < maxMemory) {
		maxMemory = req.MaxMemory
	}
	return lim, maxMemory
}
