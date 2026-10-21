package sideeffect

import (
	"context"
	"testing"
	"time"
)

// 本文件钉住**编排层的正常路径**：三态（applied / replayed / contended）的流转，
// 以及「同一 Key 重试不再执行」这条幂等键的核心语义。
//
// 「失败形态」（各失败档的可观测性）在 failure_test.go；
// 「存储侧判据」（顺序 / 单调性 / 过期拒收）在 store_test.go。

func TestExecuteAppliesOnce(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	calls := 0
	fn := func(context.Context) (Result, error) {
		calls++
		return Result{Payload: []byte("done")}, nil
	}

	outcome, res, err := r.Execute(ctx, a, fn)
	if err != nil {
		t.Fatalf("首次执行不该失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("首次执行应判为 applied，实得 %v", outcome)
	}
	if calls != 1 {
		t.Fatalf("执行体应被调用 1 次，实得 %d", calls)
	}
	if string(res.Payload) != "done" {
		t.Fatalf("结果应为 done，实得 %q", res.Payload)
	}
}

// TestRetryWithSameKeyReplays 钉住幂等键的核心：同一 Key 重试**不再执行**，
// 而是取回先到者的结果。这是「至多一次」的正面形态。

func TestRetryWithSameKeyReplays(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	calls := 0
	fn := func(context.Context) (Result, error) {
		calls++
		return Result{Payload: []byte("first")}, nil
	}

	if _, _, err := r.Execute(ctx, a, fn); err != nil {
		t.Fatalf("首次执行不该失败: %v", err)
	}

	// 重试：同一 Key，同一动作体（这次若被执行即说明幂等失效）。
	outcome, res, err := r.Execute(ctx, a, func(context.Context) (Result, error) {
		calls++
		return Result{Payload: []byte("second")}, nil
	})
	if err != nil {
		t.Fatalf("重试不该失败: %v", err)
	}
	if outcome != OutcomeReplayed {
		t.Fatalf("重试应判为 replayed（取回既有结果），实得 %v", outcome)
	}
	if calls != 1 {
		t.Fatalf("动作体应总共只被调用 1 次，实得 %d（重试又执行了 = 幂等失效）", calls)
	}
	if string(res.Payload) != "first" {
		t.Fatalf("重试应取回先到者结果 first，实得 %q", res.Payload)
	}
}

// TestContendedIsRetryableNotFailure 钉住三态里最容易误用的一态：
// 资源被别人占着时判为 OutcomeContended（可重试），**且不带 error**。
//
// 误用形态：宿主把它当成失败 → 动作被永久丢弃。宿主要退避重试。

func TestContendedIsRetryableNotFailure(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	// 手动占住资源（模拟他人持有，未过期）。
	st.mu.Lock()
	st.holders["res-a"] = fakeHold{
		lease:   Lease{Resource: "res-a", Fence: 1, TTL: 30 * time.Second},
		expires: st.now.Add(30 * time.Second),
	}
	st.mu.Unlock()

	calls := 0
	outcome, res, err := r.Execute(ctx, a, func(context.Context) (Result, error) {
		calls++
		return Result{Payload: []byte("x")}, nil
	})
	if err != nil {
		t.Fatalf("被占用应返回可重试态而非错误: %v", err)
	}
	if outcome != OutcomeContended {
		t.Fatalf("被占用应判为 contended，实得 %v", outcome)
	}
	if calls != 0 {
		t.Fatalf("被占用时不该调用动作体，实得 %d 次", calls)
	}
	if len(res.Payload) != 0 {
		t.Fatalf("被占用时不该返回结果，实得 %q", res.Payload)
	}
}

// TestContendedThenFreeThenApplies 钉住「可重试」的完整闭环：
// 被占用 → 等到对方过期 → 同一 Key 仍能拿到执行权（动作没被丢掉）。
//
// 这条是 OutcomeContended 语义的反面：若被占用后动作再也做不了，
// 那就是「永久丢弃」，与本包承诺的「至多一次」相悖（成了「至多零次」）。

func TestContendedThenFreeThenApplies(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	st.mu.Lock()
	st.holders["res-a"] = fakeHold{
		lease:   Lease{Resource: "res-a", Fence: 1, TTL: 30 * time.Second},
		expires: st.now.Add(30 * time.Second),
	}
	st.mu.Unlock()

	calls := 0
	fn := func(context.Context) (Result, error) {
		calls++
		return Result{Payload: []byte("x")}, nil
	}

	if _, _, err := r.Execute(ctx, a, fn); err != nil {
		t.Fatalf("被占用不该报错: %v", err)
	}
	if calls != 0 {
		t.Fatalf("被占用时不该执行，实得 %d 次", calls)
	}

	// 对方租约过期 → 同一 Key 重新获得执行权。
	st.advance(31 * time.Second)

	outcome, res, err := r.Execute(ctx, a, fn)
	if err != nil {
		t.Fatalf("过期后执行不该失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("对方过期后应判为 applied，实得 %v", outcome)
	}
	if calls != 1 {
		t.Fatalf("动作体应被调用 1 次，实得 %d", calls)
	}
	if string(res.Payload) != "x" {
		t.Fatalf("结果应为 x，实得 %q", res.Payload)
	}
}

// TestFailedExecutionIsNotRecorded 钉住最容易写错的一条：
// 执行失败**不记账**——没做成的动作不能被记成做成了。
//
// 误用形态：失败也 Complete → 后续重试拿到「已完成」的空结果，
// 而真相是「没做过」，动作永远丢失。

func TestRunReturnsReplayedResult(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	fn := func(context.Context) (Result, error) {
		return Result{Payload: []byte("v")}, nil
	}
	if _, err := r.Run(ctx, a, fn); err != nil {
		t.Fatalf("首次 Run 不该失败: %v", err)
	}
	res, err := r.Run(ctx, a, fn)
	if err != nil {
		t.Fatalf("重放的 Run 不该报错: %v", err)
	}
	if string(res.Payload) != "v" {
		t.Fatalf("重放应返回既有结果 v，实得 %q", res.Payload)
	}
}

// TestNilRunnerFailsLoudly 钉住零值 Runner 不静默放行。

func TestRunRejectsContended(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	st.mu.Lock()
	st.holders["res-a"] = fakeHold{
		lease:   Lease{Resource: "res-a", Fence: 1, TTL: 30 * time.Second},
		expires: st.now.Add(30 * time.Second),
	}
	st.mu.Unlock()

	_, err := r.Run(ctx, a, func(context.Context) (Result, error) {
		return Result{Payload: []byte("x")}, nil
	})
	if err == nil {
		t.Fatal("被占用时 Run 应报错（不能返回空结果冒充成功）")
	}
}

// TestRunReturnsReplayedResult 钉住 Run 在重放时正常返回既有结果
// （不是错误）——「早已完成」不是失败。
