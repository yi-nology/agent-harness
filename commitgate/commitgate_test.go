package commitgate

import (
	"sync"
	"testing"

	"github.com/oekit/pkg/errors"
)

// 宿主的状态词表：本仓只认「是不是终态」，不认具体取值。
var terminalSet = map[string]bool{
	"succeeded": true,
	"failed":    true,
	"cancelled": true,
}

func isTerminal(s string) bool { return terminalSet[s] }

func g() *Guard { return NewGuard(isTerminal) }

func ref(step string) Ref { return Ref{RunID: "run-1", Step: step} }

// TestFirstTerminalWins 首个终态提交应当通过。
func TestFirstTerminalWins(t *testing.T) {
	if err := g().Commit(Commit{Ref: ref("s1"), Status: "succeeded", Branch: "b1", Seq: 1}); err != nil {
		t.Fatalf("首个终态提交应通过，实得 %v", err)
	}
}

// TestSecondTerminalRejected 同一环节的第二个终态提交必须被拒——
// 这是本包存在理由的正面断言。
func TestSecondTerminalRejected(t *testing.T) {
	gd := g()
	_ = gd.Commit(Commit{Ref: ref("s1"), Status: "succeeded", Branch: "branch-A", Seq: 1})

	err := gd.Commit(Commit{Ref: ref("s1"), Status: "failed", Branch: "branch-B", Seq: 2})
	if err == nil {
		t.Fatal("同一环节的第二个终态提交必须被拒")
	}
	if !errors.Is(err, ErrAlreadyCommitted) {
		t.Fatalf("应归口 ErrAlreadyCommitted，实得 %v", err)
	}
}

// TestRejectCarriesIncumbent 拒绝错误必须说清「先前是谁提交的」。
//
// 「两个分支都以为自己赢了」之所以难查，正是因为日志里只有两次提交、
// 没有「第二次本该被谁挡下」。错误里少了这条，排查就只能靠猜。
func TestRejectCarriesIncumbent(t *testing.T) {
	gd := g()
	_ = gd.Commit(Commit{Ref: ref("s1"), Status: "succeeded", Branch: "branch-A", Seq: 7})

	err := gd.Commit(Commit{Ref: ref("s1"), Status: "failed", Branch: "branch-B", Seq: 9})
	var ae *AlreadyCommittedError
	if !errors.As(err, &ae) {
		t.Fatalf("应能取出结构化错误，实得 %T", err)
	}
	if ae.Prev.branch != "branch-A" {
		t.Fatalf("应带出先前提交者 branch-A，实得 %q", ae.Prev.branch)
	}
	if ae.Prev.status != "succeeded" {
		t.Fatalf("应带出先前状态 succeeded，实得 %q", ae.Prev.status)
	}
	if ae.PrevSeq != 7 {
		t.Fatalf("先前序号应为 7（不是本次的 9），实得 %d", ae.PrevSeq)
	}
}

// TestNonTerminalPassesRepeatedly 中间态本来就可以多次推进，不受独占约束。
// 若这里也拦，正常的多轮推进会被误判成重复提交。
func TestNonTerminalPassesRepeatedly(t *testing.T) {
	gd := g()
	for _, s := range []string{"running", "waiting_approval", "running"} {
		if err := gd.Commit(Commit{Ref: ref("s1"), Status: s, Branch: "b1", Seq: 1}); err != nil {
			t.Fatalf("中间态 %q 应放行，实得 %v", s, err)
		}
	}
	if gd.Committed() != 0 {
		t.Fatalf("中间态不应占用终态闸，实得已提交 %d", gd.Committed())
	}
}

// TestReleaseFreesForRetry 落库失败后 Release，该环节必须能重新提交。
// 不 Release 的后果不是「多占一个名额」，是这个环节永远卡死。
func TestReleaseFreesForRetry(t *testing.T) {
	gd := g()
	c := Commit{Ref: ref("s1"), Status: "succeeded", Branch: "b1", Seq: 1}
	if err := gd.Commit(c); err != nil {
		t.Fatal(err)
	}
	// 落库失败 → 回滚。
	gd.Release(c)

	if _, _, committed := gd.Status(ref("s1")); committed {
		t.Fatal("Release 后不应仍占用闸")
	}
	// 重试应能成。
	if err := gd.Commit(Commit{Ref: ref("s1"), Status: "succeeded", Branch: "b1", Seq: 2}); err != nil {
		t.Fatalf("Release 后重试应成功，实得 %v", err)
	}
}

// TestReleaseDoesNotStealOtherHolder 迟到的 Release 不得放掉别人的独占权。
//
// 这是 Release 最危险的写法：分支 A 提交成功、分支 B 提交被拒，B 若在错误处理里
// 无脑 Release，就会把 A 正当持有的闸一起放掉——等于给第三次提交开了门。
func TestReleaseDoesNotStealOtherHolder(t *testing.T) {
	gd := g()
	a := Commit{Ref: ref("s1"), Status: "succeeded", Branch: "A", Seq: 1}
	if err := gd.Commit(a); err != nil {
		t.Fatal(err)
	}
	// B 的提交被拒后错误处理里调 Release（分支/序号都对不上）。
	gd.Release(Commit{Ref: ref("s1"), Status: "succeeded", Branch: "B", Seq: 2})

	if _, branch, committed := gd.Status(ref("s1")); !committed || branch != "A" {
		t.Fatalf("A 的独占权不得被 B 的 Release 放掉，实得 branch=%q committed=%v", branch, committed)
	}
	if err := gd.Commit(Commit{Ref: ref("s1"), Status: "failed", Branch: "C", Seq: 3}); err == nil {
		t.Fatal("独占权仍在，第三次提交必须被拒")
	}
}

// TestRefIsPerRunAndStep 引用必须按「运行 + 环节」两级判。
// 只按环节名去重会把第二次运行整个判成重复提交——这是会直接跑挂业务的错。
func TestRefIsPerRunAndStep(t *testing.T) {
	gd := g()
	if err := gd.Commit(Commit{Ref: Ref{RunID: "run-1", Step: "s1"}, Status: "succeeded", Branch: "b", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	// 同一 run 的另一个环节：独立。
	if err := gd.Commit(Commit{Ref: Ref{RunID: "run-1", Step: "s2"}, Status: "succeeded", Branch: "b", Seq: 2}); err != nil {
		t.Fatalf("同 run 的不同环节应独立，实得 %v", err)
	}
	// 另一个 run 的同名环节：独立。
	if err := gd.Commit(Commit{Ref: Ref{RunID: "run-2", Step: "s1"}, Status: "succeeded", Branch: "b", Seq: 1}); err != nil {
		t.Fatalf("不同 run 的同名环节应独立，实得 %v", err)
	}
	if gd.Committed() != 3 {
		t.Fatalf("应记三条终态，实得 %d", gd.Committed())
	}
}

// TestEmptyRefRejected 引用不完整必须拒绝，不得静默通过。
func TestEmptyRefRejected(t *testing.T) {
	for _, c := range []Commit{
		{Ref: Ref{Step: "s1"}, Status: "succeeded", Branch: "b"},
		{Ref: Ref{RunID: "run-1"}, Status: "succeeded", Branch: "b"},
	} {
		if err := g().Commit(c); !errors.Is(err, ErrEmptyRef) {
			t.Fatalf("引用不完整应被拒，实得 %v", err)
		}
	}
}

// TestNilIsTerminalTreatsEverythingAsTerminal 词表未知时按终态处理，宁可多拦。
// 「不知道是不是终态就当不是」会在宿主漏注入时把闸整个架空。
func TestNilIsTerminalTreatsEverythingAsTerminal(t *testing.T) {
	gd := NewGuard(nil)
	if err := gd.Commit(Commit{Ref: ref("s1"), Status: "running", Branch: "A", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if err := gd.Commit(Commit{Ref: ref("s1"), Status: "running", Branch: "B", Seq: 2}); err == nil {
		t.Fatal("词表未知时应按终态从严处理")
	}
}

// TestSeedSurvivesRestart 进程重启后回灌，先前终态必须仍然挡得住。
// 不 Seed 的后果是「闸被重置后没人告知」，不是「闸没生效」。
func TestSeedSurvivesRestart(t *testing.T) {
	prior := Commit{Ref: ref("s1"), Status: "succeeded", Branch: "before-restart", Seq: 3}

	// 场景：崩溃前 A 已提交终态并落库，重启后 B 又来提交。
	restarted := g()
	restarted.Seed(prior)

	err := restarted.Commit(Commit{Ref: ref("s1"), Status: "failed", Branch: "after-restart", Seq: 4})
	if err == nil {
		t.Fatal("重启后 Seed 的终态必须仍然生效")
	}
	var ae *AlreadyCommittedError
	if errors.As(err, &ae) && ae.Prev.branch != "before-restart" {
		t.Fatalf("应带出重启前的提交者，实得 %q", ae.Prev.branch)
	}
}

// TestSeedIsIdempotentAndDoesNotOverwrite 重复 Seed 幂等，且不覆盖内存里的新记录。
func TestSeedIsIdempotentAndDoesNotOverwrite(t *testing.T) {
	gd := g()
	seed := Commit{Ref: ref("s1"), Status: "succeeded", Branch: "persisted", Seq: 1}
	gd.Seed(seed)
	gd.Seed(seed)

	// 内存里已有更新的记录（本次进程内提交过），旧的回灌不得覆盖。
	gd.Release(Commit{Ref: ref("s1"), Status: "succeeded", Branch: "persisted", Seq: 1})
	fresh := Commit{Ref: ref("s1"), Status: "failed", Branch: "live", Seq: 9}
	if err := gd.Commit(fresh); err != nil {
		t.Fatal(err)
	}
	gd.Seed(seed)

	_, branch, _ := gd.Status(ref("s1"))
	if branch != "live" {
		t.Fatalf("过期回灌不得覆盖内存中的现状，实得 %q", branch)
	}
}

// TestConcurrentTerminalCommitHasExactlyOneWinner 并发提交终态，必须恰好一个赢家。
// 这条是本包在真实并发编排下的正面断言（配 -race 才有意义）。
func TestConcurrentTerminalCommitHasExactlyOneWinner(t *testing.T) {
	for round := 0; round < 200; round++ {
		gd := g()
		var wg sync.WaitGroup
		var mu sync.Mutex
		winners := 0

		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				err := gd.Commit(Commit{
					Ref: ref("s1"), Status: "succeeded",
					Branch: string(rune('A' + n)), Seq: uint64(n + 1),
				})
				if err == nil {
					mu.Lock()
					winners++
					mu.Unlock()
				}
			}(i)
		}
		wg.Wait()

		if winners != 1 {
			t.Fatalf("第 %d 轮：并发终态提交应恰好一个赢家，实得 %d", round, winners)
		}
	}
}
