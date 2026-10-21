//go:build unix

package ptc

import (
	"fmt"
	"syscall"
)

// applyMemoryLimit 在**子进程内**设置地址空间上限（RLIMIT_AS）。
//
// # 为什么在子进程内设，而不是父侧用 SysProcAttr
//
// Go 的 `syscall.SysProcAttr` **没有** rlimit 字段——rlimit 只能在 fork 之后
// 由子进程自己设（`syscall.Setrlimit`）。故父侧只负责把上限**传过去**
// （环境变量），由子进程在跑任何程序之前应用。
//
// # 为什么是 RLIMIT_AS（地址空间）而不是 RLIMIT_RSS
//
// RLIMIT_RSS 在 Linux 上**不被强制**（内核不阻止进程超过它），在 macOS 上语义
// 又是「当前驻留集上限」——都不是可靠的内存封顶。RLIMIT_AS 限制**虚拟地址
// 空间**，内核会真的拒绝超额的映射，故它是「这个进程总共能占多少地址空间」
// 的硬上限。
//
// # 代价：地址空间 ≠ 物理内存
//
// RLIMIT_AS 限的是虚拟地址空间，可能远大于物理内存（Go 运行时会预留很大一
// 块虚拟空间不落地）。故它是**粗但硬**的上限：设得合适能兜住「程序快速分配
// 把宿主撑爆」这个具体形态，但可能比「真实内存占用」更早触发。
//
// 这正是本后端**如实声明**自己在兑现哪一项的原因（Supports）：它兑现的是
// 「地址空间有硬上限」，不是「物理内存精确封顶」。
//
// # 失败即拒
//
// 设不上限就**拒绝执行**（返回错误让父侧看到），而不是继续跑——
// 「以为兜住了、实际没有」正是这套机制要消灭的东西。
func applyMemoryLimit(maxMemory int64) error {
	if maxMemory <= 0 {
		return nil
	}
	lim := syscall.Rlimit{
		Cur: uint64(maxMemory),
		Max: uint64(maxMemory),
	}
	if err := syscall.Setrlimit(syscall.RLIMIT_AS, &lim); err != nil {
		return fmt.Errorf("ptc: 设地址空间上限失败（要求 %d 字节）: %w", maxMemory, err)
	}
	return nil
}
