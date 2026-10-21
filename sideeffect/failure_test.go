package sideeffect

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// 本文件钉住**编排层的失败形态**：每一档失败都必须**可观测且不撒谎**。
//
// 具体是：执行失败不记账、但要释放闸；存储故障原样透出（不能被吞成
// 「别人占着」）；记账失败如实报错（既不假装成功也不假装没做过）；
// 未知结论显式失败（内核不猜）。
//
// 「正常路径」在 runner_test.go；「存储侧判据」在 store_test.go。

func TestFailedExecutionIsNotRecorded(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	boom := errors.New("boom")
	if _, _, err := r.Execute(ctx, a, func(context.Context) (Result, error) {
		return Result{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("执行失败应原样透出，实得 %v", err)
	}

	// 关键断言：失败**没有**被记账（completeCalls == 0）。
	if st.completeCalls != 0 {
		t.Fatalf("执行失败不该记账，实得 completeCalls=%d", st.completeCalls)
	}

	// 失败时**必须释放闸**，否则后续重试被自己的残留闸挡成 contended，
	// 动作从此做不成（这正是本用例存在的第二个理由）。
	if st.releaseCalls != 1 {
		t.Fatalf("执行失败应释放闸 1 次，实得 %d", st.releaseCalls)
	}

	// 于是后续重试仍能真正执行（动作没被丢掉）。
	calls := 0
	outcome, res, err := r.Execute(ctx, a, func(context.Context) (Result, error) {
		calls++
		return Result{Payload: []byte("ok")}, nil
	})
	if err != nil {
		t.Fatalf("重试不该失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("失败后重试应判为 applied，实得 %v", outcome)
	}
	if calls != 1 {
		t.Fatalf("重试应执行 1 次，实得 %d", calls)
	}
	if string(res.Payload) != "ok" {
		t.Fatalf("结果应为 ok，实得 %q", res.Payload)
	}
}

// TestFenceIsMonotonicPerResource 钉住栅栏令牌单调递增且不回退。
//
// 这是「过期持有者的迟到写入能被下游识别」的前提：
// 令牌若会回退（先发 5 后发 3），下游「见过的最大令牌」判据就失效。

func TestNoStoreFailsLoudly(t *testing.T) {
	r := NewRunner(nil, time.Second)
	_, _, err := r.Execute(context.Background(), act("k1", "res-a"), func(context.Context) (Result, error) {
		return Result{}, nil
	})
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("未装配存储应显式失败，实得 %v", err)
	}
}

// TestInvalidActionRejected 钉住动作定义校验：缺 Key 或缺 Resource 即拒。

func TestInvalidActionRejected(t *testing.T) {
	r := NewRunner(newFakeStore(), time.Second)
	ctx := context.Background()
	fn := func(context.Context) (Result, error) { return Result{}, nil }

	for _, a := range []Action{
		{Key: "", Resource: "res-a"},
		{Key: "k1", Resource: ""},
	} {
		if _, _, err := r.Execute(ctx, a, fn); !errors.Is(err, ErrInvalidAction) {
			t.Fatalf("动作 %+v 应被判非法，实得 %v", a, err)
		}
	}
}

// TestReleaseDoesNotMarkDone 钉住 Release 的语义边界：
// 释放持有**不**把 Key 标记为已完成——动作没做完就是没做完。
//
// 误用形态：Release 时顺手标完成 → 动作被记成做过，实际没做，
// 后续谁都不会再做它。

func TestUnknownOutcomeFailsLoudly(t *testing.T) {
	st := newFakeStore()

	// 用一个返回未知结论的存储包装 fakeStore。
	weird := weirdStore{Store: st}
	r := NewRunner(weird, time.Second)
	_, _, err := r.Execute(context.Background(), act("k1", "res-a"), func(context.Context) (Result, error) {
		return Result{}, nil
	})
	if !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("存储侧返回未知结论应显式失败，实得 %v", err)
	}
}

// TestStoreFailurePropagates 钉住存储故障必须原样透出，
// 不能被吞成「没做过」或「别人占着」。
//
// 误用形态：Acquire 出错时返回 OutcomeContended + nil error
// → 宿主以为「正被别人占着」而退避重试，实际是存储挂了，重试也是白重。

func TestStoreFailurePropagates(t *testing.T) {
	boom := errors.New("存储不可用")
	st := newFakeStore()
	st.acquireErr = boom
	r := NewRunner(st, time.Second)

	_, _, err := r.Execute(context.Background(), act("k1", "res-a"), func(context.Context) (Result, error) {
		t.Fatal("存储故障时不该执行动作")
		return Result{}, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("存储故障应原样透出，实得 %v", err)
	}
}

// TestCompleteFailureIsReported 钉住「动作可能已生效但没记上账」
// 这一档必须**如实报错**：既不能假装成功，也不能假装没做过。
//
// 这一档最容易被写成「吞掉错误返回成功」——那会让宿主以为动作做成了，
// 实际账没记上，后续重试会再施加一次。

func TestCompleteFailureIsReported(t *testing.T) {
	boom := errors.New("记账失败")
	st := newFakeStore()
	st.completeErr = boom
	r := NewRunner(st, time.Second)
	ctx := context.Background()

	calls := 0
	_, _, err := r.Execute(ctx, act("k1", "res-a"), func(context.Context) (Result, error) {
		calls++
		return Result{Payload: []byte("v")}, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("记账失败应如实报错，实得 %v", err)
	}
	if calls != 1 {
		t.Fatalf("动作应已执行 1 次，实得 %d", calls)
	}
	// 记账失败后闸被释放，后续重试能重新尝试（不被残留闸挡住）。
	if st.releaseCalls != 1 {
		t.Fatalf("记账失败应释放闸 1 次，实得 %d", st.releaseCalls)
	}
}

// TestRunRejectsContended 钉住便捷封装 Run 的取舍：
// 它只取结果，故「被别人占着」必须转成错误，不能返回空结果冒充成功。

func TestNilRunnerFailsLoudly(t *testing.T) {
	var r *Runner
	_, _, err := r.Execute(context.Background(), act("k1", "res-a"), func(context.Context) (Result, error) {
		return Result{}, nil
	})
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("零值 Runner 应显式失败，实得 %v", err)
	}
}

// TestOutcomeString 钉住三态的可读名（诊断与日志面，不参与判定）。

func TestOutcomeString(t *testing.T) {
	cases := map[Outcome]string{
		OutcomeApplied:   "applied",
		OutcomeReplayed:  "replayed",
		OutcomeContended: "contended",
		Outcome(42):      "unknown",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Fatalf("Outcome(%d).String() = %q，应为 %q", int(o), got, want)
		}
	}
}

// weirdStore 返回一个未定义的 Outcome。
type weirdStore struct{ Store }

func (w weirdStore) Acquire(ctx context.Context, act Action, ttl time.Duration) (Outcome, Lease, Result, error) {
	_, lease, _, err := w.Store.Acquire(ctx, act, ttl)
	if err != nil {
		return OutcomeContended, Lease{}, Result{}, err
	}
	return Outcome(42), lease, Result{}, nil
}
