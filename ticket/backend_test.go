package ticket

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件是 Backend 接缝（issue #145 多副本部署）的判据：
//   - Store 的判定逻辑真的落在 Backend 上（注入替身，逐调用可观察）；
//   - Backend 故障时 Store 的 fail-closed 口径（票不放行、清理/观测退 0）；
//   - 内存参考实现满足 Take 的原子性契约（并发消费恰好一个赢）——
//     这是跨进程实现必须复刻的那条语义，契约套件范式同 sidetest。

// errBackend 全故障替身：四个操作一律报错。
// （ticket 是零依赖纯 stdlib 包，policy requires=[]——错误值用 fmt 构造，
// 不为此给包引第二套依赖。）
type errBackend struct{}

func (errBackend) Put(string, Binding, time.Time) error { return fmt.Errorf("backend down") }
func (errBackend) Take(string, time.Time) (Binding, bool, error) {
	return Binding{}, false, fmt.Errorf("backend down")
}
func (errBackend) GC(time.Time) (int, error) { return 0, fmt.Errorf("backend down") }
func (errBackend) Len() (int, error)         { return 0, fmt.Errorf("backend down") }

// spyBackend 记录调用的替身（判定逻辑是否真的委托，逐调用可查）。
type spyBackend struct {
	mu     sync.Mutex
	puts   int
	takes  int
	gcs    int
	lens   int
	stored map[string]entry
}

func newSpyBackend() *spyBackend { return &spyBackend{stored: map[string]entry{}} }

func (s *spyBackend) Put(id string, b Binding, exp time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.stored[id] = entry{binding: b, expiresAt: exp}
	return nil
}

func (s *spyBackend) Take(id string, _ time.Time) (Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.takes++
	e, ok := s.stored[id]
	if !ok {
		return Binding{}, false, nil
	}
	delete(s.stored, id)
	return e.binding, true, nil
}

func (s *spyBackend) GC(time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcs++
	return 0, nil
}

func (s *spyBackend) Len() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lens++
	return len(s.stored), nil
}

// TestStore判定落在注入Backend上 钉住：Issue/Consume/GC/Len 全部走接缝
// （旧实现把状态焊在 Store 里，替换不了——接缝的存在性判据）。
func TestStore判定落在注入Backend上(t *testing.T) {
	spy := newSpyBackend()
	s := NewWithBackend(spy, time.Now)

	id, _, err := s.Issue(Binding{Subject: "s", Purpose: "p", Target: "t"}, time.Minute)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if spy.puts != 1 {
		t.Fatalf("Issue 必须经 Backend.Put（实得 puts=%d）", spy.puts)
	}
	if _, ok := s.Consume(id); !ok {
		t.Fatal("消费应成功")
	}
	if spy.takes != 1 {
		t.Fatalf("Consume 必须经 Backend.Take（实得 takes=%d）", spy.takes)
	}
	_ = s.GC()
	if spy.gcs != 1 {
		t.Fatalf("GC 必须经 Backend.GC（实得 gcs=%d）", spy.gcs)
	}
	if n := s.Len(); n != 0 {
		t.Fatalf("Len 必须经 Backend.Len（实得 n=%d）", n)
	}
	if spy.lens != 1 {
		t.Fatalf("Len 必须经 Backend.Len（实得 lens=%d）", spy.lens)
	}
	// 第二次消费：票已被 Take 删掉 ⇒ 失败（单次消费语义跨接缝保持）
	if _, ok := s.Consume(id); ok {
		t.Fatal("同一张票第二次消费必须失败")
	}
}

// TestBackend故障时failclosed 钉住：存储故障绝不放行——Consume 拿不准就
// 当失败（票还在，重试即可），GC/Len 退 0，Issue 显式报错上抛。
func TestBackend故障时failclosed(t *testing.T) {
	s := NewWithBackend(errBackend{}, time.Now)

	if _, ok := s.Consume("any"); ok {
		t.Fatal("Backend 故障时消费必须失败（fail-closed：绝不放行没验成的票）")
	}
	if n := s.GC(); n != 0 {
		t.Fatalf("Backend 故障时 GC 应退 0，实得 %d", n)
	}
	if n := s.Len(); n != 0 {
		t.Fatalf("Backend 故障时 Len 应退 0，实得 %d", n)
	}
	if _, _, err := s.Issue(Binding{}, time.Minute); err == nil {
		t.Fatal("Backend 故障时 Issue 必须显式报错")
	}
}

// TestNewWithBackendNil退化进程内 钉住：nil 后端不是 nil 解引用 panic，
// 而是退化为缺省进程内实现（防御性契约，文档已写）。
func TestNewWithBackendNil退化进程内(t *testing.T) {
	s := NewWithBackend(nil, time.Now)
	id, _, err := s.Issue(Binding{Subject: "s"}, time.Minute)
	if err != nil {
		t.Fatalf("nil 后端应退化为进程内实现，签发却失败: %v", err)
	}
	if _, ok := s.Consume(id); !ok {
		t.Fatal("退化实现的消费应成功")
	}
}

// TestTake原子性契约 内存参考实现侧的并发判据：N 个消费者抢同一张票，
// 恰好一个赢。跨进程 Backend 必须复刻同一语义（DB = DELETE ... RETURNING）。
func TestTake原子性契约(t *testing.T) {
	for _, consumers := range []int{8, 32} {
		t.Run(strconv.Itoa(consumers), func(t *testing.T) {
			b := NewMemoryBackend(0)
			now := time.Unix(1700000000, 0)
			id := "same-ticket"
			if err := b.Put(id, Binding{Subject: "s"}, now.Add(time.Minute)); err != nil {
				t.Fatalf("Put 失败: %v", err)
			}
			var wins atomic.Int64
			var wg sync.WaitGroup
			for i := 0; i < consumers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, ok, err := b.Take(id, now); err == nil && ok {
						wins.Add(1)
					}
				}()
			}
			wg.Wait()
			if got := wins.Load(); got != 1 {
				t.Fatalf("并发消费同一张票必须恰好 1 个赢家（实得 %d）——原子性契约破了", got)
			}
			if n, _ := b.Len(); n != 0 {
				t.Fatalf("消费后应无残留，实得 %d", n)
			}
		})
	}
}
