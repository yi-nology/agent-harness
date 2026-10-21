//go:build !linux

package ptc

import "fmt"

// memLimitSupported 报告当前环境能否施加内存上限。
//
// **非 Linux 一律不能**：cgroup v2 是 Linux 的机制，而 macOS 上没有等价物
// （seatbelt 裁的是能力面，不裁内存；`RLIMIT_AS` 在 Darwin 上直接被拒——
// 实测 `Setrlimit(RLIMIT_AS, 256MB)` 返回 invalid argument）。
//
// ⇒ 宿主若在这类平台上要求内存兜底，正确的反应是「这个平台兜不住」，
// 而不是「跑吧，反正大概没事」——那正是这套机制要消灭的东西。
func memLimitSupported() bool { return false }

// newMemCgroup 非 Linux：建不出带内存上限的 cgroup。
func newMemCgroup(maxMemory int64) (memCgroup, error) {
	return nil, fmt.Errorf("%w（本平台无 cgroup v2 之类的机制）", ErrMemoryLimitUnsupported)
}
