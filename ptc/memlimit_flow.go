package ptc

import (
	"github.com/oekit/pkg/errors"
)

// 本文件是**内存兜底闸的生命周期**：建 → 放进子进程 → 回收。
//
// 它从 subprocess.go 的 run() 里抽出来，因为那三步是**另一个关注点**：
// run() 管的是「怎么与子进程交换数据」，闸管的是「怎么让子进程别把宿主撑爆」。
// 混在一起时 run() 超过了单文件行数阈值，而两件事的读者也不同——
// 一个读数据流的人不该被 cgroup 的细节打断。
//
// # 为什么闸由**父侧**建，而不是子进程自己设
//
// 子进程能设的只有 rlimit（RLIMIT_AS / RLIMIT_DATA），而 rlimit 限的是
// **虚拟地址空间**——Go runtime 的虚拟 arena 预留远超任何有意义的内存帽：
//
//	RLIMIT_AS=256MB  → fatal error: runtime: out of memory（runtime 自己起不来）
//	RLIMIT_AS=4GB    → runtime 起来了，但 4GB 里已有 2.2GB 是 runtime 的虚拟预留
//
// 即：上限必须设到 GB 量级才起得来，而那时虚拟预留已经吃掉大半——
// 作为「程序快速分配把宿主撑爆」的兜底，它既兜不住（大数才生效）
// 又会把 runtime 一起掐死（小数直接 fatal）。**两条路都错。**
//
// cgroup v2 的 memory.max 限的是**物理内存**，不管虚拟预留：
// runtime 的虚拟 arena 预留不落地 ⇒ 不占配额 ⇒ runtime 起得来；
// 程序真去拿物理内存 ⇒ 超过 memory.max ⇒ 内核 OOM-killer 杀它（SIGKILL）。
// 不可信程序撑爆的是**物理内存**，不是虚拟地址——这才是要兜的东西。
//
// 详见 memlimit_linux.go 的包头（那里有完整的实测数据）。
//
// # 不用上限时给一个「无约束闸」，而不是 nil
//
// 调用点若到处判 `if cg != nil`，会出现「某条路径忘了判 ⇒ 闸被漏回收」
// 这类夹缝——而漏回收的代价是宿主里累积垃圾 cgroup。
// 故这里统一返回一个**什么都不做**的闸：release 是空操作，attach 是空操作。
// 形状统一，漏判无从发生。

// openMemoryGate 打开本次执行的内存兜底闸。
//
// 不要求内存上限（MaxMemory <= 0）⇒ 返回一个**无约束闸**（不做任何事）。
// 要求上限 ⇒ 建一个带 memory.max 的 cgroup；建不出 ⇒ **报错**（fail closed）。
func (p *SubprocessProvider) openMemoryGate() (memCgroup, error) {
	if p.opts.MaxMemory <= 0 {
		return noopCgroup{}, nil
	}
	cg, err := newMemCgroupFn(p.opts.MaxMemory)
	if err != nil {
		return nil, errors.Wrap(err, "建不出带内存上限的执行环境")
	}
	return cg, nil
}

// noopCgroup 不施加任何内存约束的闸。
//
// 它存在的唯一理由是**形状统一**：让「不要求上限」与「要求上限」两条路径
// 走同一套调用代码（建 → attach → 回收），不必到处判空。
type noopCgroup struct{}

func (noopCgroup) attach(int) error { return nil }
func (noopCgroup) release()         {}
