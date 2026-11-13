package commitgate

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/oekit/pkg/errors"
)

// 本文件是 Store 接缝（issue #145 多副本部署）的判据：
//   - 两个 Guard 共享一份存储 = 两个副本的最小模拟：迟到的 ReleaseLease
//     不得放掉另一「副本」的闸（令牌由存储铸造，跨副本不撞）；
//   - 并发 Claim 恰好一个赢家（跨进程实现必须复刻的原子性）；
//   - Store 故障时的 fail-closed 口径（占用报错、观测退 0）。

// errStore 全故障替身。
type errStore struct{}

func (errStore) Claim(Ref, Record) (Record, bool, error) {
	return Record{}, false, errors.New("store down")
}
func (errStore) ReleaseToken(Ref, uint64) (bool, error) { return false, errors.New("store down") }
func (errStore) SeedIfAbsent(Ref, Record) error         { return errors.New("store down") }
func (errStore) Get(Ref) (Record, bool, error)          { return Record{}, false, errors.New("store down") }
func (errStore) Count() (int, error)                    { return 0, errors.New("store down") }

// Test共享存储下迟到释放不放别人的闸 —— 接缝存在的核心理由：
//
// 旧实现每个 Guard 自带进程内 tokenSeq：两个 Guard（=两个副本）各自从 1
// 开始铸令牌，副本 A 迟到的 ReleaseLease(token=1) 会命中副本 B 的 token=1，
// 把 B 的闸放掉 ⇒ 同一环节终态被提交两次。令牌改由 Store 铸造后，
// 共享存储里序列连续，迟到释放对不上号。
func Test共享存储下迟到释放不放别人的闸(t *testing.T) {
	st := NewMemoryStore()
	gA := NewGuardWithStore(st, nil)
	gB := NewGuardWithStore(st, nil)
	c := Commit{Ref: Ref{RunID: "r1", Step: "s1"}, Status: "done", Branch: "a", Seq: 1}

	// 副本 A 占用 → 落库失败 → 交还
	leaseA, err := gA.Acquire(c)
	if err != nil {
		t.Fatalf("A 首次占用失败: %v", err)
	}
	gA.ReleaseLease(leaseA)

	// 副本 B 随后占用同一环节（闸已交还，合法）
	leaseB, err := gB.Acquire(c)
	if err != nil {
		t.Fatalf("B 占用失败: %v", err)
	}
	if leaseB.Token == leaseA.Token {
		t.Fatalf("令牌必须由存储铸造且全局递增（A=%d B=%d 相撞 = 迟到释放可误伤）",
			leaseA.Token, leaseB.Token)
	}

	// A 的迟到/重复释放：必须无效——B 的闸还在
	gA.ReleaseLease(leaseA)
	if _, err := gB.Acquire(c); err == nil {
		t.Fatal("A 的迟到释放放掉了 B 的闸（终态可被二次提交）")
	} else if !errors.Is(err, ErrAlreadyCommitted) {
		t.Fatalf("期望 ErrAlreadyCommitted，实得 %v", err)
	}
	// B 自己的释放仍然有效（凭证路径承诺无条件成立）
	gB.ReleaseLease(leaseB)
	if _, err := gA.Acquire(c); err != nil {
		t.Fatalf("B 释放后闸应交还: %v", err)
	}
}

// Test并发Claim恰好一个赢家 —— 跨进程原子性契约的进程内判据：
// N 个「副本」同时抢同一环节，恰好一个 claimed。
func Test并发Claim恰好一个赢家(t *testing.T) {
	for _, racers := range []int{8, 32} {
		st := NewMemoryStore()
		ref := Ref{RunID: "r", Step: "s"}
		var wins atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, ok, err := st.Claim(ref, Record{Status: "done", Branch: "b", Seq: 1}); err == nil && ok {
					wins.Add(1)
				}
			}()
		}
		wg.Wait()
		if got := wins.Load(); got != 1 {
			t.Fatalf("并发 Claim 恰好 1 个赢家（实得 %d）——原子性契约破了", got)
		}
	}
}

// TestStore故障时的口径 钉住：占用故障显式报错（不静默放行）、
// 观测面退 0（不把自检打成假红）、交还路径静默（交还不是错误路径）。
func TestStore故障时的口径(t *testing.T) {
	g := NewGuardWithStore(errStore{}, nil)
	c := Commit{Ref: Ref{RunID: "r", Step: "s"}, Status: "done"}

	if _, err := g.Acquire(c); err == nil {
		t.Fatal("Store 故障时 Acquire 必须显式报错")
	}
	if g.Committed() != 0 {
		t.Fatal("Store 故障时 Committed 应退 0")
	}
	if _, _, ok := g.Status(c.Ref); ok {
		t.Fatal("Store 故障时 Status 应按未提交处理")
	}
	g.ReleaseLease(Lease{Ref: c.Ref, Token: 1}) // 静默：不得 panic
	g.Seed(c)                                   // 静默
}

// Test种子不覆盖已有记录 回灌幂等在 Store（SeedIfAbsent）：已有记录不被
// 更旧的回灌覆盖——跨副本重启回灌同一份数据结果一致。
func Test种子不覆盖已有记录(t *testing.T) {
	st := NewMemoryStore()
	ref := Ref{RunID: "r", Step: "s"}
	_ = st.SeedIfAbsent(ref, Record{Status: "done", Branch: "first", Seq: 1})
	_ = st.SeedIfAbsent(ref, Record{Status: "done", Branch: "second", Seq: 9})
	rec, ok, _ := st.Get(ref)
	if !ok || rec.Branch != "first" {
		t.Fatalf("已存在的记录不得被回灌覆盖（实得 %+v）", rec)
	}
}
