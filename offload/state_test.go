package offload

// State 的并发安全：编排可能从多个环节并发推进，共享同一个 State 是常态。

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
)

// hashOf 与生产代码同样的摘要算法，保证 State 的键形态一致。
func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestStateConcurrentMarkSeen 并发 Mark/Seen/Len 不得崩、不得数据竞争。
// 未经保护的 map 在并发读写下是**不可 recover 的致命错误**，这条就是守它。
func TestStateConcurrentMarkSeen(t *testing.T) {
	s := NewState()
	const n = 64
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			s.Mark(hashOf(string(rune('a' + i%26))))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_ = s.Seen(hashOf("seed"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			_ = s.Len()
		}
	}()
	wg.Wait()
	if s.Len() == 0 {
		t.Fatal("并发 Mark 后应有记录")
	}
}

// TestStateNilStillSafe nil State 的三个方法仍须安全返回（Apply 会传入 nil）。
func TestStateNilStillSafe(t *testing.T) {
	var s *State
	if s.Seen("x") {
		t.Fatal("nil State 的 Seen 应为 false")
	}
	s.Mark("x") // 不得 panic
	if s.Len() != 0 {
		t.Fatal("nil State 的 Len 应为 0")
	}
}

// TestStateZeroValueUsable 零值 State 必须可用（Map 惰性初始化）。
func TestStateZeroValueUsable(t *testing.T) {
	var s State
	h := hashOf("item-01")
	if s.Seen(h) {
		t.Fatal("零值 State 不应见过任何摘要")
	}
	s.Mark(h)
	if !s.Seen(h) {
		t.Fatal("零值 State 应可写入并读回")
	}
	if s.Len() != 1 {
		t.Fatalf("Len 应为 1，实得 %d", s.Len())
	}
}
