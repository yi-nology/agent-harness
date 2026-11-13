// backend.go —— 票据状态存储接缝（issue #145 多副本部署）。
//
// # 为什么要把状态抽出来
//
// 原实现把判定与状态焊在一起（mu + map）：签发/消费的判定语义正确，
// 但状态只活在本进程里——多副本宿主在副本 A 签发的票据，副本 B 消费时
// 找不到，一次性短时授权的防重放语义跨副本失效。抽成 Backend 之后：
// **判定逻辑（TTL、单次消费、容量策略的调用时机）留在 Store，跨进程状态
// 由宿主接 DB / 协调后端**——与 sideeffect.Acquire「判定归包内、原子性归
// 存储」同一条分工。
//
// # 跨进程实现的硬要求（原子性契约）
//
//   - **Take 必须原子**：多副本并发 Take 同一 id，恰好一个返回 ok=true。
//     DB 形态 = `DELETE ... RETURNING`（或等价事务）；Redis 形态 = Lua。
//     判定「过期则失败」也在同一次取出里完成——先查后删的两步形态会把
//     「两个副本同时消费」的窗口重新打开，正是本包要防的那件事。
//   - **Put 写入即可见**，同 id 覆盖（与进程内语义一致；id 是 192 bit
//     随机，碰撞不在现实里）。
//   - GC / Len 是清理与观测面，一致性要求弱于 Take。
//
// # 进程内参考实现
//
// NewMemoryBackend：单进程成立，**不得上生产**（跨进程原子性它给不了）。
// 它的职责是把判定逻辑垫在契约之下，让契约可测——与 coord 的内存后端、
// examples/pgstore 对 taskboard 的分工同口径（内核证契约，宿主接生产）。
package ticket

import (
	"fmt"
	"sync"
	"time"
)

// Backend 票据状态存储（并发安全；原子性契约见文件头）。
//
// 实现可以是进程内的（MemoryBackend）、SQL 的、Redis 的——本包只依赖
// 这四个操作的语义，不依赖它们怎么达成。
type Backend interface {
	// Put 写入一张未消费票据。容量策略由实现自判（内存参考实现满了报错，
	// 与旧实现「票据空间已满」同一拒发口径）。
	Put(id string, binding Binding, expiresAt time.Time) error
	// Take 原子取出并删除：不存在或已过期 ⇒ ok=false。
	//
	// ⚠️ 过期票据**也已被删除**——与进程内旧实现「消费尝试顺手清过期」
	// 逐字一致（先删后判的顺序是语义的一部分，不是实现巧合）。
	Take(id string, now time.Time) (Binding, bool, error)
	// GC 删除已过期票据，返回清理张数。
	GC(now time.Time) (int, error)
	// Len 返回现存票据数（含过期未清理的——它们只是不可消费，仍占空间）。
	Len() (int, error)
}

// memoryBackend 进程内参考实现（并发安全；单进程成立，不得上生产）。
type memoryBackend struct {
	mu      sync.Mutex
	m       map[string]entry
	maxSize int
}

// NewMemoryBackend 构造进程内参考实现（maxSize <= 0 ⇒ DefaultMaxEntries）。
//
// 这是 Store 缺省构造（New / NewWithClock）背后的实现；宿主的跨进程
// Backend 由宿主自己实现并经 NewWithBackend 注入。
func NewMemoryBackend(maxSize int) Backend {
	if maxSize <= 0 {
		maxSize = DefaultMaxEntries
	}
	return &memoryBackend{m: map[string]entry{}, maxSize: maxSize}
}

// Put 写入；容量已满 ⇒ 显式报错（与旧实现同一口径：满了就说满，不默默淘汰）。
func (b *memoryBackend) Put(id string, binding Binding, expiresAt time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.m) >= b.maxSize {
		return fmt.Errorf("票据空间已满（上限 %d 张）", b.maxSize)
	}
	b.m[id] = entry{binding: binding, expiresAt: expiresAt}
	return nil
}

// Take 原子取出并删除（单锁内完成取/删/判过期，顺序与旧 Consume 逐字一致）。
func (b *memoryBackend) Take(id string, now time.Time) (Binding, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tk, ok := b.m[id]
	if !ok {
		return Binding{}, false, nil
	}
	delete(b.m, id)
	if now.After(tk.expiresAt) {
		return Binding{}, false, nil
	}
	return tk.binding, true, nil
}

// GC 清掉过期票据。
func (b *memoryBackend) GC(now time.Time) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for id, tk := range b.m {
		if now.After(tk.expiresAt) {
			delete(b.m, id)
			n++
		}
	}
	return n, nil
}

// Len 现存票据数（含过期未清理的）。
func (b *memoryBackend) Len() (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.m), nil
}
