//go:build linux

package ptc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 内存兜底靠 **cgroup v2 的 memory.max**（限物理内存），不靠 rlimit。
//
// # 为什么不用 RLIMIT_AS / RLIMIT_DATA（实测结论，Linux 7.0 / Ubuntu 26.04）
//
// 两者都限制**虚拟地址空间**（DATA 自 Linux 4.7 起覆盖匿名 mmap），而 Go 运行时的
// 虚拟地址占用远超任何有意义的内存帽：
//
//	RLIMIT_AS=256MB  → fatal error: runtime: out of memory（runtime 自己起不来）
//	RLIMIT_AS=1GB    → fatal error: runtime: out of memory（同上）
//	RLIMIT_AS=4GB    → runtime 起来了，但 4GB 里已有 2.2GB 是 runtime 的虚拟预留
//	RLIMIT_DATA=1GB  → fatal error: out of memory allocating heap arena metadata
//
// 即：**上限必须设到 4GB 量级才起得来**，而那时 2GB 已经被 runtime 的虚拟预留占掉——
// 作为「程序快速分配把宿主撑爆」的兜底，它既兜不住（大数才生效）又会把 runtime
// 一起掐死（小数直接 fatal）。两条路都错。
//
// # 为什么 cgroup v2 才是对的
//
// cgroup 的 memory.max 限的是**物理内存**（RSS + page cache），**不管虚拟预留**：
//
//   - Go runtime 的虚拟 arena 预留不落地 ⇒ 不占 cgroup 配额 ⇒ runtime 起得来；
//   - 程序真的去拿 1GB 物理内存 ⇒ 超过 memory.max ⇒ **内核 OOM-killer 杀它**
//     （SIGKILL）⇒ 父侧从管道 EOF 认出「执行域异常终止」，且**能分辨是被信号杀的**。
//
// 这正是「兜底」要买的东西：不可信程序撑爆的是**物理内存**，不是虚拟地址。
//
// # 依赖与降级
//
// cgroup v2 需要**写权限**：非 root 的宿主通常不能在自己的 cgroup 下建带
// memory.max 的子 cgroup（no-internal-process 约束 + subtree_control 未启用
// memory 控制器）。故本包**探测**能不能施加：不能 ⇒ 如实「兜不住」⇒
// `Supports` 返回 false ⇒ 登记簿不选中本后端 ⇒ **fail closed**，而不是
// 「以为兜住了、实际没有」。

// cgroupRoot cgroup v2 的统一层级挂载点。
const cgroupRoot = "/sys/fs/cgroup"

// linuxMemCgroup cgroup v2 的一次执行专用 cgroup（子进程住在里面）。
type linuxMemCgroup struct {
	dir string
}

// newMemCgroup 为一次执行建一个带 memory.max 的 cgroup。
//
// 候选位置：进程自己的 cgroup 之下（特权不足时也可用），再退到统一层级根下
// （需要更高的权限）。挑第一个**建成且真的有 memory.max** 的。
func newMemCgroup(maxMemory int64) (memCgroup, error) {
	if maxMemory <= 0 {
		return nil, fmt.Errorf("ptc: 内存上限须为正数")
	}
	var tried []string
	for _, base := range cgroupCandidates() {
		dir := filepath.Join(base, fmt.Sprintf("ptc-%d-%d", os.Getpid(), time.Now().UnixNano()))
		if err := os.Mkdir(dir, 0o700); err != nil {
			tried = append(tried, fmt.Sprintf("%s（%v）", base, err))
			continue
		}
		// 建了但没有 memory.max ⇒ 这个层级没启用 memory 控制器，白建。
		if _, err := os.Stat(filepath.Join(dir, "memory.max")); err != nil {
			_ = os.Remove(dir)
			tried = append(tried, fmt.Sprintf("%s（无 memory.max）", base))
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "memory.max"), []byte(strconv.FormatInt(maxMemory, 10)), 0o600); err != nil {
			_ = os.Remove(dir)
			tried = append(tried, fmt.Sprintf("%s（写 memory.max 失败：%v）", base, err))
			continue
		}
		return &linuxMemCgroup{dir: dir}, nil
	}
	return nil, fmt.Errorf("ptc: 建不出带内存上限的 cgroup（试过：%s）", strings.Join(tried, "、"))
}

// cgroupCandidates 候选 cgroup 父目录：先进程自己的，再统一层级根。
func cgroupCandidates() []string {
	var out []string
	if own := ownCgroupDir(); own != "" {
		out = append(out, own)
	}
	out = append(out, cgroupRoot)
	return out
}

// ownCgroupDir 读本进程所在的 cgroup 目录（/proc/self/cgroup 的统一层级行）。
func ownCgroupDir() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		// 统一层级行形如 "0::/user.slice/user-0.slice/session-1.scope"
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		rel := strings.TrimPrefix(line, "0::")
		if rel == "" || rel == "/" {
			return ""
		}
		return filepath.Join(cgroupRoot, rel)
	}
	return ""
}

// attach 把子进程放进这个 cgroup。
//
// **必须在子进程开始跑程序之前**完成：子进程在收到 Start 帧之前不执行任何程序，
// 故这里没有「子进程已经吃掉内存」的竞态。
func (c *linuxMemCgroup) attach(pid int) error {
	if c == nil {
		return nil
	}
	path := filepath.Join(c.dir, "cgroup.procs")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("ptc: 把子进程放进 cgroup 失败: %w", err)
	}
	return nil
}

// release 回收 cgroup（子进程已退出，cgroup 空了才删得掉）。
func (c *linuxMemCgroup) release() {
	if c == nil {
		return
	}
	_ = os.Remove(c.dir)
}

// memLimitSupported 报告当前环境能否施加内存上限（cgroup v2 + 可写）。
//
// 它是**探测**出来的，不是按平台猜的：同一个 Linux 机器，有 cgroup v2 且可写的
// 就能，容器里没委派就不能。
func memLimitSupported() bool {
	c, err := newMemCgroup(1 << 20)
	if err != nil {
		return false
	}
	c.release()
	return true
}
