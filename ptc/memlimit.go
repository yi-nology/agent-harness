package ptc

import "github.com/oekit/pkg/errors"

// ErrMemoryLimitUnsupported 本环境无法施加内存上限。
//
// 它是**独立**于「执行域异常终止」的一类失败：前者是「这个环境做不到」
// （宿主该换环境或降要求），后者是「执行域中途没了」（可能是兜底生效了）。
// 合成一类的话，宿主只能一律当故障处理。
var ErrMemoryLimitUnsupported = errors.New("ptc: 本环境无法施加内存上限")

// MemoryLimitSupported 报告**当前环境**能否施加内存上限（cgroup v2 的 memory.max）。
//
// 它是**探测**出来的，不是按平台猜的：同一个 Linux 机器，有 cgroup v2 且可写的
// 就能；容器里没委派 memory 控制器就不能；非 Linux 一律不能。
//
// # 宿主为什么需要它
//
// 宿主要做「档位登记」——它得知道**当前环境**这个后端兜不兜得住，
// 否则会在登记簿里选中一个兜不住的后端，然后在运行期收到
// 「选了它却用不了」这种最难排查的失败。
//
// 这与「按平台猜」的写法的差别是实测出来的：同一台 Linux 机器，
// 换个容器跑（有没有委派 memory 控制器）结论就不同。
func MemoryLimitSupported() bool { return memLimitSupported() }

// memCgroup 一次执行专用的内存 cgroup（平台相关实现）。
//
// 为什么是接口而不是结构体：上限的施加机制在平台间完全不同
// （Linux = cgroup v2 memory.max；其他平台 = 没有等价物），
// 而**调用它的流程**（建 → 起子进程 → attach → 收尾 release）是平台无关的。
type memCgroup interface {
	// attach 把子进程放进这个 cgroup。必须在子进程开始跑程序之前调用。
	attach(pid int) error
	// release 回收 cgroup（子进程已退出后调用）。
	release()
}

// newMemCgroupFn 建一个带内存上限的执行环境的构造入口。
//
// 它是**变量**而非函数，好让测试能替换它（平台相关路径在当前环境常常不可用：
// macOS 没有 cgroup、容器里 cgroup 只读——那些环境里「建不出 cgroup」
// 这条分支根本走不到，不注入就测不到）。
//
// 生产路径**不替换**它——它就是平台实现本身。
var newMemCgroupFn = newMemCgroup
