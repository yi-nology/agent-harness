//go:build unix

// pg_e2e_snapshot_test.go —— 「两个进程同时提交快照」的跨进程判据。
//
// # 这一条补的是什么
//
// 评审第三节的场景表里有一条：「两个进程同时提交快照 ⇒ 持久化 Seq 严格单调」。
// 仓里已有 TestSnapshotConcurrentSaveIfNewer，但它并发的是**同一个进程里的
// 8 个 goroutine**——它们共享同一个连接池、同一个 Go 运行时、同一份内存。
// 于是它证明的是「一个进程内的并发提交不会写坏单调性」，
// **没有**证明「两个互不相识的进程同时提交也不会写坏单调性」。
//
// # 为什么这个差别要紧
//
// 单调闸（checkpoint.StoreCAS 的 SaveIfNewer）承诺的是**存储侧**的判定：
// 只有已持久化的序号严格小于本次序号时才写入。这条承诺要能跨进程成立，
// 前提是判据不依赖任何进程内状态——而这一点**只能**由真实进程证明。
// 进程内 goroutine 并发证明不了它：两个 goroutine 至少还共享着连接池与
// 运行时调度，让「写-判」窗口有机会被测出来的概率，比两个独立进程低得多。
//
// # 判据链
//
//  1. 两个子进程**同时**提交同一 (RunID, Seq)：恰好一个 SAVED、恰好一个 STALE
//     （单调闸在跨进程竞争下不会「两个都写进去」，也不会「两个都被拒」）；
//  2. 两个子进程**同时**提交不同 Seq：落库的必然是**较大**的那个
//     （这才是「严格单调」的真正含义——不是「不越界」，是「只进不退」）；
//  3. 竞争结束后，较小的 Seq 再补一次提交：必须被拒为 STALE
//     （闸在竞争之后依然立着，不是一次性的）。
//
// # 技术前提
//
// 两个子进程要真的撞在一起，就得**同时**发起提交。故父进程给它们同一个
// 绝对时刻（Unix 纳秒），子进程等到那一刻再提交——不是「谁先起跑谁先写完」。
package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/checkpoint"
)

// TestPGE2ETwoProcessSnapshotSameSeq 跨进程并发提交同一序号：恰好一个赢家。
func TestPGE2ETwoProcessSnapshotSameSeq(t *testing.T) {
	e := freshE2E(t, 60*time.Second)
	runID := e.key + "-run"

	// 屏障时刻：给两个子进程各留 300ms 启动余量，然后同一刻发起提交。
	startAt := time.Now().Add(300 * time.Millisecond).UnixNano()

	c1 := e.startSnapshotChild(t, runID, 7, startAt)
	c2 := e.startSnapshotChild(t, runID, 7, startAt)

	o1 := c1.outcome(t)
	o2 := c2.outcome(t)

	saved, stale := 0, 0
	for _, o := range []string{o1, o2} {
		switch o {
		case snapSaved:
			saved++
		case snapStale:
			stale++
		default:
			t.Fatalf("子进程落点非法 %q（%q / %q）", o, o1, o2)
		}
	}
	if saved != 1 || stale != 1 {
		t.Fatalf("跨进程并发提交同一序号应恰好一个写入、一个被拒（单调闸不能两个都写，也不能两个都不写），实得 saved=%d stale=%d（%q / %q）",
			saved, stale, o1, o2)
	}

	// 落库的序号必须是竞争的那个（不得凭空变大或变小）。
	snap, err := e.snaps.Load(context.Background(), runID)
	if err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	if snap.Seq != 7 {
		t.Fatalf("落库序号应为 7，实得 %d", snap.Seq)
	}
}

// TestPGE2ETwoProcessSnapshotDifferentSeq 跨进程并发提交不同序号：只进不退。
//
// 这是「严格单调」的核心形态：两个进程分别提交 5 与 9，无论谁先落库，
// 最终持久化的必然是 **9**——序号只进不退。判据看的是**最终状态**，
// 不是「谁赢」：较大序号的提交若先落库，较小的那次会被拒（STALE）；
// 若较小的那次先落库，较大的那次会覆盖它（SAVED）。两条路径下
// 最终落库的序号都必须等于 9。
func TestPGE2ETwoProcessSnapshotDifferentSeq(t *testing.T) {
	e := freshE2E(t, 60*time.Second)
	runID := e.key + "-run"

	startAt := time.Now().Add(300 * time.Millisecond).UnixNano()

	low := e.startSnapshotChild(t, runID, 5, startAt)
	high := e.startSnapshotChild(t, runID, 9, startAt)

	oLow := low.outcome(t)
	oHigh := high.outcome(t)
	for _, o := range []string{oLow, oHigh} {
		if o != snapSaved && o != snapStale {
			t.Fatalf("子进程落点非法 %q（%q / %q）", o, oLow, oHigh)
		}
	}

	snap, err := e.snaps.Load(context.Background(), runID)
	if err != nil {
		t.Fatalf("读快照失败: %v", err)
	}
	if snap.Seq != 9 {
		t.Fatalf("跨进程并发提交后落库的必须是较大序号 9（序号只进不退），实得 %d（%q / %q）",
			snap.Seq, oLow, oHigh)
	}

	// 竞争结束后闸依然立着：较小的序号补一次提交必须被拒。
	// （若这里被接受，说明闸是一次性的——竞争一过就失效。）
	if err := e.snaps.SaveIfNewer(context.Background(), checkpoint.Snapshot{
		RunID:  runID,
		Seq:    5,
		Status: "running",
	}); err == nil {
		t.Fatalf("竞争结束后较小的序号必须仍被单调闸拒绝（实得成功）")
	}
}
