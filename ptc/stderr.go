package ptc

import (
	"sync"
)

// childStderrLimit 子进程 stderr 保留的上限（字节）。
//
// 保留**末尾**而不是开头：崩溃信息（Go runtime 的 fatal error、panic 栈）在末尾。
const childStderrLimit = 8 << 10

// tailBuffer 只保留末尾 N 字节的并发安全缓冲。
//
// 为什么要留：子进程死了，父侧从管道 EOF 只看得到「没了」——**死因在它的 stderr 里**
// （例：`fatal error: runtime: out of memory`）。不接住它，宿主就只看到一个
// 退出码，而退出码分不清「被内核杀掉」与「自己 fatal 退出」。
//
// 为什么只留末尾：子进程可能狂写日志（无界缓冲会把父进程的内存也吃进去——
// 那正是这个后端要防的形态，不能自己犯）。
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

// Write 实现 io.Writer。
func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}
	return len(p), nil
}

// String 取当前保留的内容。
func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
