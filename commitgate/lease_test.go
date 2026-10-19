// lease_test.go —— 终态独占凭证（Acquire / ReleaseLease）的反向验证。
//
// # 为什么需要凭证
//
// 旧接口 Release(c Commit) 靠 (status, branch, seq) 三元组判断「这次 Release
// 是不是在放自己占的那一次」。但那三元组**不唯一**：
//
//   - Branch 的注释自己写明是「分支名 / agent 名」——并行分支常常同名；
//   - 「落库失败 → Release → 重试同一份快照」这条**包文档自己推荐的重试路径**，
//     重试时三元组与上一轮逐字相同。
//
// 于是两种偷放都真实发生：输家的迟到 Release 删掉赢家的闸；ABA 场景下
// 上一轮的迟到 Release 放掉新持有者的闸。实测 8 路同名并发提交出现
// **4 个赢家**——本包存在的理由（防「两条并行分支都赢了」）被绕过，
// 且失效是静默的：Commit 返回 nil，宿主无从察觉。
//
// 凭证路径把「身份」从**内容**换成**令牌**：只有铸造它的那一次占用能被放掉。

package commitgate

import (
	"sync"
	"testing"
)

func leaseIsTerminal(s string) bool {
	return s == "succeeded" || s == "failed" || s == "cancelled"
}

func leaseRef() Ref { return Ref{RunID: "run-1", Step: "s1"} }

// TestLeaseLoserLateReleaseDoesNotStealWinner 输家的迟到 ReleaseLease 不得放掉赢家的闸。
//
// 这一条是旧实现最贵的失效：同名分支的第二次提交被拒后走错误处理调 Release，
// 三元组与赢家相同 → 赢家的记录被删 → 闸对下一次 Commit 敞开。
func TestLeaseLoserLateReleaseDoesNotStealWinner(t *testing.T) {
	g := NewGuard(leaseIsTerminal)

	winner, err := g.Acquire(Commit{Ref: leaseRef(), Status: "succeeded", Branch: "agent-x", Seq: 1})
	if err != nil {
		t.Fatalf("首个终态占用应成功: %v", err)
	}

	// 同名分支的第二次终态占用：被拒，且不产生任何凭证。
	loser, err := g.Acquire(Commit{Ref: leaseRef(), Status: "failed", Branch: "agent-x", Seq: 1})
	if err == nil {
		t.Fatal("同名分支的第二个终态占用必须被拒")
	}
	if loser.Token != 0 {
		t.Fatal("被拒的占用不得返回凭证")
	}

	// 输家的迟到释放：凭的是「零凭证」（拿不到有效凭证）。
	g.ReleaseLease(loser)

	// 赢家的闸必须还在。
	if _, err := g.Acquire(Commit{Ref: leaseRef(), Status: "failed", Branch: "agent-y", Seq: 2}); err == nil {
		t.Fatal("独占权仍在：第三个终态占用必须被拒（实际被放掉了）")
	}
	// 且赢家的凭证仍能精确放掉它。
	g.ReleaseLease(winner)
	if _, err := g.Acquire(Commit{Ref: leaseRef(), Status: "failed", Branch: "agent-y", Seq: 2}); err != nil {
		t.Fatalf("赢家释放后应可重新占用: %v", err)
	}
}

// TestLeaseStaleReleaseDoesNotStealReacquired ABA：同三元组被重新占用后，
// 上一轮迟到的释放不得放掉新持有者的闸。
//
// 这是「落库失败 → Release → 重试同一份快照」这条推荐路径上的真实序列。
func TestLeaseStaleReleaseDoesNotStealReacquired(t *testing.T) {
	g := NewGuard(leaseIsTerminal)
	c := Commit{Ref: leaseRef(), Status: "succeeded", Branch: "agent-x", Seq: 1}

	first, err := g.Acquire(c)
	if err != nil {
		t.Fatal(err)
	}
	g.ReleaseLease(first) // 落库失败 → 回滚

	second, err := g.Acquire(c) // 重试：同分支、同序号
	if err != nil {
		t.Fatal(err)
	}
	if second.Token == first.Token {
		t.Fatal("重新占用必须铸造新令牌（否则凭证无法区分两次占用）")
	}

	// 上一轮迟到的释放（拿着旧令牌）。
	g.ReleaseLease(first)

	// 新持有者的闸必须还在。
	if _, err := g.Acquire(Commit{Ref: leaseRef(), Status: "failed", Branch: "agent-y", Seq: 2}); err == nil {
		t.Fatal("重试者的独占权不得被上一轮的迟到释放放掉（实际被放掉了）")
	}
}

// TestLeaseConcurrentAcquireHasExactlyOneWinner 并发占用必须恰好一个赢家。
//
// 这一条直接对着本包的存在理由：同一 (run, step) 的终态只允许被提交一次。
// 旧实现在同名并发 + 迟到释放下实测 4/8 个赢家。
func TestLeaseConcurrentAcquireHasExactlyOneWinner(t *testing.T) {
	for round := 0; round < 200; round++ {
		g := NewGuard(leaseIsTerminal)
		var wg sync.WaitGroup
		var mu sync.Mutex
		success := 0
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := Commit{Ref: leaseRef(), Status: "succeeded", Branch: "agent-x", Seq: 7}
				lease, err := g.Acquire(c)
				if err != nil {
					// 输家：错误处理里的迟到释放（拿着零凭证）。
					g.ReleaseLease(lease)
					return
				}
				mu.Lock()
				success++
				mu.Unlock()
			}()
		}
		wg.Wait()
		if success != 1 {
			t.Fatalf("第 %d 轮：同一环节应恰好一个赢家，实得 %d", round, success)
		}
	}
}

// TestLeaseReleaseDoesNotTouchOtherRef 释放不得波及别的环节。
func TestLeaseReleaseDoesNotTouchOtherRef(t *testing.T) {
	g := NewGuard(leaseIsTerminal)
	l1, err := g.Acquire(Commit{Ref: Ref{RunID: "run-1", Step: "s1"}, Status: "succeeded", Branch: "a", Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	l2, err := g.Acquire(Commit{Ref: Ref{RunID: "run-1", Step: "s2"}, Status: "succeeded", Branch: "a", Seq: 2})
	if err != nil {
		t.Fatal(err)
	}
	g.ReleaseLease(l1)
	if _, _, ok := g.Status(Ref{RunID: "run-1", Step: "s2"}); !ok {
		t.Fatal("释放 s1 不得影响 s2 的占用")
	}
	g.ReleaseLease(l2)
	if _, _, ok := g.Status(Ref{RunID: "run-1", Step: "s2"}); ok {
		t.Fatal("s2 应已被自己的凭证释放")
	}
}

// TestLegacyReleaseRequiresStatusMatch 旧接口 Release 现在要求状态也相同。
//
// 这一条是旧接口的**加固**：同名分支的输家若提交的是**不同状态**（这是常态：
// 一条分支成功、一条分支失败），它的迟到 Release 不得放掉赢家的闸。
// 旧实现只比 branch 与 seq，挡不住这种偷放。
func TestLegacyReleaseRequiresStatusMatch(t *testing.T) {
	g := NewGuard(leaseIsTerminal)
	winner := Commit{Ref: leaseRef(), Status: "succeeded", Branch: "agent-x", Seq: 1}
	if err := g.Commit(winner); err != nil {
		t.Fatal(err)
	}
	loser := Commit{Ref: leaseRef(), Status: "failed", Branch: "agent-x", Seq: 1}
	if err := g.Commit(loser); err == nil {
		t.Fatal("同名分支的第二个终态提交必须被拒")
	}
	g.Release(loser) // 迟到的释放：状态不同 → 不得动赢家的闸
	if err := g.Commit(Commit{Ref: leaseRef(), Status: "failed", Branch: "agent-y", Seq: 2}); err == nil {
		t.Fatal("独占权仍在：第三个终态提交必须被拒（实际被放掉了）")
	}
}
