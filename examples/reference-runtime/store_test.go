package main

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/sidetest"
	"github.com/yi-nology/goagent-kernel/taskboard"
	"github.com/yi-nology/goagent-kernel/taskboard/boardtest"
)

// TestSideEffectStoreContract 跑副作用闸的**共享契约套件**：
// 本仓的 sidetest 把「实现方必须满足什么」固化成 15 个子场景，
// 各写一套判据必然漂移到没人发现为止。
func TestSideEffectStoreContract(t *testing.T) {
	sidetest.Run(t, func(_ *testing.T, _ *sidetest.FakeClock) sidetest.ClockedStore {
		return newEffectStore()
	})
}

// TestTaskBoardContract 跑任务认领面的共享契约套件。
func TestTaskBoardContract(t *testing.T) {
	boardtest.Run(t, func(_ *testing.T) taskboard.Board {
		return newBoardStore()
	})
}

// TestSnapshotSaveIfNewerRejectsStaleSeq 快照序号必须严格递增，陈旧写一律被拒且不落库。
func TestSnapshotSaveIfNewerRejectsStaleSeq(t *testing.T) {
	ctx := context.Background()
	s := newSnapStore()
	if err := s.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 2, Status: "running"}); err != nil {
		t.Fatalf("首次落库失败: %v", err)
	}
	err := s.SaveIfNewer(ctx, checkpoint.Snapshot{RunID: "r", Seq: 1, Status: "running"})
	if !errors.Is(err, checkpoint.ErrStaleSeq) {
		t.Fatalf("陈旧快照必须被拒为 ErrStaleSeq，实得 %v", err)
	}
	snap, err := s.Load(ctx, "r")
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if snap.Seq != 2 {
		t.Fatalf("落库的序号应仍为 2（陈旧写不得覆盖），实得 %d", snap.Seq)
	}
}

// TestSnapshotLoadMissingIsNotFound 无记录时必须返回 ErrNotFound。
//
// 返回 (零值, nil) 会被上游误判成「这个运行从没跑过」从而全量重跑——
// 重复施加正是续跑要避免的那件事。
func TestSnapshotLoadMissingIsNotFound(t *testing.T) {
	s := newSnapStore()
	if _, err := s.Load(context.Background(), "nope"); !errors.Is(err, checkpoint.ErrNotFound) {
		t.Fatalf("无记录的读取必须返回 ErrNotFound，实得 %v", err)
	}
}

// TestAcquirePriorityOrder 判定顺序：已完成 > 结果未知 > 资源被占 > 授予。
func TestAcquirePriorityOrder(t *testing.T) {
	ctx := context.Background()
	s := newEffectStore()

	// 结果未知优先于「资源被占」。
	held := sideeffect.Action{Key: "k1", Resource: "r1"}
	_, lease, _, err := s.Acquire(ctx, held, time.Minute)
	if err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	if err := s.MarkUncertain(ctx, held, lease, "响应丢了"); err != nil {
		t.Fatalf("标记未知失败: %v", err)
	}
	outcome, _, _, err := s.Acquire(ctx, sideeffect.Action{Key: "k2", Resource: "r1"}, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeContended {
		t.Fatalf("资源被他人占着时应为 contended，实得 %v", outcome)
	}
	outcome, _, _, err = s.Acquire(ctx, held, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("结果未知优先于资源占用，实得 %v", outcome)
	}

	// 已完成优先于「资源被占」。
	done := sideeffect.Action{Key: "k3", Resource: "r3"}
	_, lease3, _, err := s.Acquire(ctx, done, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if _, err := s.Complete(ctx, done, lease3, sideeffect.Result{Payload: []byte("v")}); err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	if _, _, _, err := s.Acquire(ctx, sideeffect.Action{Key: "k4", Resource: "r3"}, time.Minute); err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	outcome, _, got, err := s.Acquire(ctx, done, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		t.Fatalf("已完成优先于资源占用，实得 %v", outcome)
	}
	if string(got.Payload) != "v" {
		t.Fatalf("应取回既有结果 v，实得 %q", got.Payload)
	}
}

// TestResolveUncertainFirstWins 同一键的多次裁决先到者为准。
func TestResolveUncertainFirstWins(t *testing.T) {
	ctx := context.Background()
	s := newEffectStore()
	act := sideeffect.Action{Key: "k", Resource: "r"}

	_, lease, _, err := s.Acquire(ctx, act, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if err := s.MarkUncertain(ctx, act, lease, "响应丢了"); err != nil {
		t.Fatalf("标记未知失败: %v", err)
	}
	if err := s.Release(ctx, act, lease); err != nil {
		t.Fatalf("释放持有失败: %v", err)
	}
	if _, _, err := s.ResolveUncertain(ctx, act, sideeffect.Resolution{
		Applied: true,
		Result:  sideeffect.Result{Payload: []byte("first")},
		Reason:  "第一次裁决",
	}); err != nil {
		t.Fatalf("首次裁决失败: %v", err)
	}
	// 第二次裁决给相反结论：不得推翻先到者。
	outcome, _, err := s.ResolveUncertain(ctx, act, sideeffect.Resolution{
		Applied: false,
		Reason:  "第二次裁决（相反结论）",
	})
	if err != nil {
		t.Fatalf("第二次裁决不该报错: %v", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		t.Fatalf("先到者为准，后到的相反裁决不得推翻已完成，实得 %v", outcome)
	}
	outcome, _, got, err := s.Acquire(ctx, act, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		t.Fatalf("裁决后该键应为已完成（replayed），实得 %v", outcome)
	}
	if string(got.Payload) != "first" {
		t.Fatalf("应保留先到者的结果 first，实得 %q", got.Payload)
	}
}

// TestBoardReleaseRequiresHolderToken 释放只认令牌，不认内容。
func TestBoardReleaseRequiresHolderToken(t *testing.T) {
	ctx := context.Background()
	s := newBoardStore()
	s.Seed(taskboard.Task{ID: "t1", Title: "待领", State: taskboard.StateOpen})

	if _, err := s.Claim(ctx, "t1", "a1", taskboard.Lease{Token: "tok-1"}); err != nil {
		t.Fatalf("首次认领应当成功: %v", err)
	}
	if err := s.Release(ctx, "t1", "tok-2"); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("非持有者释放应被拒为 ErrLeaseLost，实得 %v", err)
	}
	if err := s.Release(ctx, "t1", "tok-1"); err != nil {
		t.Fatalf("持有者释放应当成功: %v", err)
	}
	if _, err := s.Claim(ctx, "t1", "a2", taskboard.Lease{Token: "tok-3"}); err != nil {
		t.Fatalf("释放后应当可再认领: %v", err)
	}
}

// TestBoardSnapshotPassesInvariants 完成后的快照必须自洽（终态不带 owner 与令牌）。
func TestBoardSnapshotPassesInvariants(t *testing.T) {
	ctx := context.Background()
	s := newBoardStore()
	s.Seed(taskboard.Task{ID: "t1", Title: "待领", State: taskboard.StateOpen})
	if _, err := s.Claim(ctx, "t1", "a1", taskboard.Lease{Token: "tok-1"}); err != nil {
		t.Fatalf("认领失败: %v", err)
	}
	if err := s.Complete(ctx, "t1", "tok-1"); err != nil {
		t.Fatalf("完成失败: %v", err)
	}
	tasks, err := s.List(ctx)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if problems := taskboard.Check(tasks); len(problems) > 0 {
		t.Fatalf("快照不变量被违反: %v", problems)
	}
}
