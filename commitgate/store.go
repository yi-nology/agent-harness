// store.go —— 终态记录存储接缝（issue #145 多副本部署）。
//
// # 为什么要把状态抽出来
//
// 原实现把判定与状态焊在一起（mu + done map）：闸的判定语义正确，但状态只
// 活在本进程里——副本 A 抢到的终态独占，副本 B 看不见，两副本可以同时赢得
// 同一个环节的闸（「同一 (run, 环节) 终态只提交一次」只在单进程成立）。
// 抽成 Store 之后：**判定逻辑（词表判定、空引用拒绝、错误构造）留在 Guard，
// 跨进程状态与原子占用归存储**——与 sideeffect / coord 同一条分工。
//
// # 跨进程实现的硬要求（原子性契约）
//
//   - **Claim 必须原子**：多副本并发 Claim 同一 Ref，恰好一个 claimed=true。
//     DB 形态 = 唯一索引上的 `INSERT ... ON CONFLICT DO NOTHING`（或事务内
//     先查后插 + 唯一约束兜底）；「先查后插」的两步形态会把赢家电窗口
//     重新打开，正是本闸要防的那件事。
//   - **Token 由存储铸造**（Claim 返回的 Record 带出）：占用令牌必须跨进程
//     唯一，否则副本 B 铸出与副本 A 迟到的 ReleaseLease 相同的令牌时，
//     A 的迟到释放会放掉 B 的闸——这与 commitgate 自己踩过的「三元组不唯一」
//     是同一个坑，只是从进程内挪到了进程间。DB 形态 = 序列/雪花 id。
//   - **ReleaseToken 条件删除**：只删令牌匹配的记录（`DELETE ... WHERE
//     ref=? AND token=?`），不匹配返回 released=false。
//
// # 进程内参考实现
//
// NewMemoryStore：单进程成立，**不得上生产**（跨进程原子性与令牌全局唯一
// 它都给不了）。职责是把判定逻辑垫在契约之下让契约可测——与 coord 的内存
// 后端同口径。
package commitgate

import "sync"

// Record 存储面的已提交记录（Guard 内部 record 的接缝形态；字段一一同构）。
type Record struct {
	// Status 目标状态。
	Status string
	// Branch 提交者标识（只用于留痕与排障定位）。
	Branch string
	// Seq 提交序号（交由 SeqGate 判单调的口径）。
	Seq uint64
	// Token 本次占用的唯一令牌——**由存储铸造**（见文件头原子性契约）。
	// 回灌（SeedIfAbsent）的记录没有占用，Token 恒 0。
	Token uint64
}

// Store 终态记录存储接缝（并发安全；原子性契约见文件头）。
type Store interface {
	// Claim 原子占用：不存在 ⇒ 写入 rec（Token 由实现铸造后随 out 返回）
	// 且 claimed=true；已存在 ⇒ claimed=false，out 为先前记录（供
	// AlreadyCommittedError 构造「先前是谁提交的」）。基础设施故障 ⇒ error。
	Claim(ref Ref, rec Record) (out Record, claimed bool, err error)
	// ReleaseToken 凭令牌精确释放：令牌匹配 ⇒ 删除并 released=true；
	// 记录不存在或令牌不匹配 ⇒ released=false（迟到/重复释放的正确形态）。
	ReleaseToken(ref Ref, token uint64) (released bool, err error)
	// SeedIfAbsent 回灌已持久化的终态：已存在不覆盖（内存里的现状更新）。
	SeedIfAbsent(ref Ref, rec Record) error
	// Get 查记录。第二个返回值为是否已提交。
	Get(ref Ref) (Record, bool, error)
	// Count 已终态提交的环节数。
	Count() (int, error)
}

// MemoryStore 进程内参考实现（并发安全；单进程成立，不得上生产）。
type MemoryStore struct {
	mu sync.Mutex
	// done 已终态提交的环节。
	done map[Ref]Record
	// tokenSeq 占用令牌的自增序列（进程内单调；跨进程唯一性由宿主的
	// 存储实现负责——见文件头契约）。
	tokenSeq uint64
}

// NewMemoryStore 构造进程内参考实现。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{done: map[Ref]Record{}}
}

// Claim 原子占用（单锁内判-占-铸令牌，进程内恰好一个赢家）。
func (s *MemoryStore) Claim(ref Ref, rec Record) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.done[ref]; ok {
		return prev, false, nil
	}
	s.tokenSeq++
	rec.Token = s.tokenSeq
	s.done[ref] = rec
	return rec, true, nil
}

// ReleaseToken 凭令牌精确释放（令牌不匹配 = 不是这次占用，不动后来者的闸）。
func (s *MemoryStore) ReleaseToken(ref Ref, token uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.done[ref]
	if !ok || prev.Token != token {
		return false, nil
	}
	delete(s.done, ref)
	return true, nil
}

// SeedIfAbsent 回灌（已存在不覆盖）。
func (s *MemoryStore) SeedIfAbsent(ref Ref, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.done[ref]; ok {
		return nil
	}
	s.done[ref] = rec
	return nil
}

// Get 查记录。
func (s *MemoryStore) Get(ref Ref) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.done[ref]
	return rec, ok, nil
}

// Count 已终态提交的环节数。
func (s *MemoryStore) Count() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.done), nil
}
