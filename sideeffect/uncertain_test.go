package sideeffect

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// 本文件钉住 v0.32.0 新增的「结果未知」语义。
//
// # 这组用例在钉什么
//
// 钉的是一条**不该被绕过的红线**：施加失败且无法判定远端是否已生效时，
// 同一个 Key 不得再被执行——必须先对账。
//
// 失效形态（旧实现就有）：执行失败 → 释放闸 → 允许重试 → 远端其实已经
// 生效（响应丢了）→ 第二次施加 ⇒ **副作用做了两遍**，而调用方全程只看到
// 一个普通错误。失效是**静默**的：没有告警、没有异常，只是世界被改了两次。
//
// 每条判据都成对写：**好实现能过** + **坏实现会红**。只有前者的话，
// 一个把判据整个写坏的实现照样全绿——而这条判据正是本轮改动的全部意义。

// TestUncertainKeyRefusesRetry 钉住核心红线：结果未知的 Key 再来一次，
// **不得**被执行。
func TestUncertainKeyRefusesRetry(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	boom := errors.New("响应丢了")
	if _, _, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		return Result{}, boom
	}); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("施加失败且未声明可重试 ⇒ 应判结果未知，实得 %v", err)
	}

	// 第二次尝试：**不得**再执行动作体。
	calls := 0
	outcome, _, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("second")}, nil
	})
	if outcome != OutcomeUncertain {
		t.Fatalf("结果未知的 Key 再来一次应仍判 uncertain，实得 %v", outcome)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("应报结果未知，实得 %v", err)
	}
	if calls != 0 {
		t.Fatalf("结果未知的 Key 不得再被执行（否则就是重复施加），实得执行 %d 次", calls)
	}
}

// TestUncertainJudgeIsSensitiveToBrokenStore 反向验证（负对照）：
// 把「Acquire 不查未知标记」这条判据摘掉的存储实现，**必须**被放行重试。
//
// 这条用例证明的是**判据有分辨力**：如果坏实现也被拦住，说明拦住它的
// 是别的东西（或者压根没拦住，只是用例写得太松），那么好实现那条用例
// 就成了空断言。
//
// 为什么值得单独钉：这条红线是本轮改动的全部意义，而它的失效形态是
// 「静默地多施加一次」——没有症状，只有后果。
func TestUncertainJudgeIsSensitiveToBrokenStore(t *testing.T) {
	// 正常实现：重试被拦。
	calls, outcome := retryAfterFailure(t, newFakeStore())
	if calls != 0 || outcome != OutcomeUncertain {
		t.Fatalf("正常实现应拦住重试（判结果未知），实得 outcome=%v calls=%d", outcome, calls)
	}

	// 坏实现：Acquire 不查未知标记 ⇒ 重试被放行 ⇒ 副作用会被施加两遍。
	broken := &ignoresUncertainStore{fakeStore: newFakeStore()}
	calls, outcome = retryAfterFailure(t, broken)
	if calls == 0 || outcome == OutcomeUncertain {
		t.Fatalf("坏实现（Acquire 无视未知标记）应放行重试，说明判据确实被摘掉了；"+
			"实得 outcome=%v calls=%d", outcome, calls)
	}
}

// retryAfterFailure 先制造一次「施加失败（结果未知）」，再重试一次，
// 返回重试时的判据与动作体被调用次数。
func retryAfterFailure(t *testing.T, st Store) (calls int, outcome Outcome) {
	t.Helper()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, _, _ = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		return Result{}, errors.New("响应丢了")
	})
	outcome, _, _ = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("second")}, nil
	})
	return calls, outcome
}

// TestRetrySafeActionAllowsRetry 钉住「声明了可安全重试」的另一条路：
// 下游按幂等键去重 ⇒ 重试不会重复施加 ⇒ 允许重试（旧行为保留）。
//
// 这是「按最弱处理」的对称面：显式声明**放宽**判据是允许的，
// 但必须由宿主显式说出来（沉默按最弱）。
func TestRetrySafeActionAllowsRetry(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")
	a.RetrySafety = RetrySafetySafe

	boom := errors.New("第一次没成")
	outcome, _, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		return Result{}, boom
	})
	if outcome != OutcomeContended {
		t.Fatalf("声明可安全重试时失败应判 contended（可重试），实得 %v", outcome)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("原始失败原因应透出，实得 %v", err)
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("声明可安全重试时不该判结果未知，实得 %v", err)
	}

	// 闸已释放 ⇒ 重试能真正执行。
	calls := 0
	outcome, res, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("ok")}, nil
	})
	if err != nil {
		t.Fatalf("重试不该失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("重试应判为 applied，实得 %v", outcome)
	}
	if calls != 1 {
		t.Fatalf("重试应执行 1 次，实得 %d", calls)
	}
	if string(res.Payload) != "ok" {
		t.Fatalf("结果应为 ok，实得 %q", res.Payload)
	}
}

// TestReconcileAppliedRecordsResult 钉住对账的正向裁决：
// 宿主确认「已生效」⇒ 该 Key 记为已完成 ⇒ 之后重放既有结果，**不再执行**。
func TestReconcileAppliedRecordsResult(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, _, _ = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		return Result{}, errors.New("响应丢了")
	})

	outcome, _, err := r.Reconcile(ctx, a, Resolution{
		Applied: true,
		Result:  Result{Payload: []byte("remote")},
		Reason:  "查下游：动作已生效",
	})
	if err != nil {
		t.Fatalf("对账不该失败: %v", err)
	}
	// v0.33.0 起裁决「已生效」的返回值钉为 replayed + 既有结果——
	// 此刻就是「取回既有结果」，与之后的 Acquire 同一口径（契约第 1 条）。
	if outcome != OutcomeReplayed {
		t.Fatalf("裁决「已生效」应返回 replayed，实得 %v", outcome)
	}

	// 之后同一 Key ⇒ 重放既有结果，动作体不再执行。
	calls := 0
	outcome, res, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("again")}, nil
	})
	if err != nil {
		t.Fatalf("对账后不该报错: %v", err)
	}
	if outcome != OutcomeReplayed {
		t.Fatalf("对账确认已生效后应判 replayed，实得 %v", outcome)
	}
	if calls != 0 {
		t.Fatalf("已生效的动作不得再被执行，实得执行 %d 次", calls)
	}
	if string(res.Payload) != "remote" {
		t.Fatalf("应取回对账确认的结果 remote，实得 %q", res.Payload)
	}
}

// TestReconcileNotAppliedAllowsRetry 钉住对账的负向裁决：
// 宿主确认「未生效」⇒ 清除未知标记 ⇒ 该 Key 重新可被执行。
//
// 这一条是「未知态可解」的正向证明：未知不是死局，对账之后路能走。
func TestReconcileNotAppliedAllowsRetry(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	_, _, _ = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		return Result{}, errors.New("响应丢了")
	})

	outcome, _, err := r.Reconcile(ctx, a, Resolution{
		Applied: false,
		Reason:  "查下游：动作未生效",
	})
	if err != nil {
		t.Fatalf("对账不该失败: %v", err)
	}
	if outcome != OutcomeContended {
		t.Fatalf("裁决「未生效」应返回 contended（可再抢闸），实得 %v", outcome)
	}

	calls := 0
	outcome, res, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("ok")}, nil
	})
	if err != nil {
		t.Fatalf("对账后重试不该失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("对账确认未生效后应判 applied，实得 %v", outcome)
	}
	if calls != 1 {
		t.Fatalf("对账确认未生效后应能执行 1 次，实得 %d", calls)
	}
	if string(res.Payload) != "ok" {
		t.Fatalf("结果应为 ok，实得 %q", res.Payload)
	}
}

// TestCompleteFailureMarksUncertain 钉住「动作已生效但记账失败」这一档：
// 它同样是**结果未知**——而且宿主手里**有**结果（执行体返回的那份）。
//
// 旧实现只报一个原始错误，调用方看不出「这动作已经做过了」，
// 于是重试 → 重复施加。
func TestCompleteFailureMarksUncertain(t *testing.T) {
	boom := errors.New("记账失败")
	st := newFakeStore()
	st.completeErr = boom
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	calls := 0
	outcome, res, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("v")}, nil
	})
	if calls != 1 {
		t.Fatalf("动作应已执行 1 次，实得 %d", calls)
	}
	if outcome != OutcomeUncertain {
		t.Fatalf("记账失败应判结果未知（动作已生效），实得 %v", outcome)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("应可判结果未知，实得 %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("原始记账错误也应留在错误链里（排障要看得见根因），实得 %v", err)
	}
	if string(res.Payload) != "v" {
		t.Fatalf("宿主手里应有动作结果 v（对账时可直接采信），实得 %q", res.Payload)
	}
}

// TestMarkUncertainFailureStillBlocksRetry 钉住 fail-closed：
// 连「标记未知」都写不进去（存储侧不可用）时，判据**不得**放宽成
// 「可以重试」——失败形态必须是变红，不能是变绿。
//
// # 为什么必须做**第二次** Acquire
//
// 此前这条用例只断言「当前调用返回未知态」。那只证明了**编排层**没放行，
// 没证明**存储侧**真挡住了重试——而存储侧此刻可能压根没写进任何东西
// （标记失败 + 此前没有执行意图 ⇒ 存储侧一片空白）。
//
// 那样一来，下一次 Acquire 会重新授予执行权 ⇒ 重复施加，而调用方全程
// 只看到一句「结果未知，请勿重试」。失效是**静默**的。
//
// v0.33.0 起存储侧有「执行意图」这条兜底：标记写不进去时，意图还在，
// 同一个 Key 仍拿不到执行权。故这里必须做第二次 Acquire 来证明它。
func TestMarkUncertainFailureStillBlocksRetry(t *testing.T) {
	boom := errors.New("存储不可用")
	st := newFakeStore()
	st.markUncertainErr = boom
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	outcome, _, err := r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		return Result{}, errors.New("响应丢了")
	})
	if outcome != OutcomeUncertain {
		t.Fatalf("标记失败时仍须判结果未知（禁止重试），实得 %v", outcome)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("应可判结果未知，实得 %v", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("存储故障应留在错误链里，实得 %v", err)
	}

	// 第二次尝试：**不得**再执行动作体。
	//
	// 存储侧此刻既没有「完成」也没有「未知」（标记写失败了）——
	// 挡住重试的只剩执行意图。这条断言钉的就是那个兜底。
	calls := 0
	outcome, _, _ = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("second")}, nil
	})
	if calls != 0 {
		t.Fatalf("存储侧未写入未知标记时，执行意图仍必须挡住重试（否则就是重复施加），实得执行 %d 次", calls)
	}
	if outcome == OutcomeApplied {
		t.Fatalf("存储侧未写入未知标记时不得重新授予执行权，实得 %v", outcome)
	}
	// 租约还没过期：判「这次尝试还在进行中」（contended，正常判据、不带 error）。
	if outcome != OutcomeContended {
		t.Fatalf("租约未过期时应判 contended（这次尝试还在进行中），实得 %v", outcome)
	}

	// 租约过期：意图转入未知态 ⇒ 重试必须先对账。
	st.advance(2 * time.Minute)
	outcome, _, err = r.Execute(ctx, a, func(context.Context, Lease) (Result, error) {
		calls++
		return Result{Payload: []byte("third")}, nil
	})
	if calls != 0 {
		t.Fatalf("租约过期后仍不得执行（须先对账），实得执行 %d 次", calls)
	}
	if outcome != OutcomeUncertain {
		t.Fatalf("租约过期后该 Key 应转入未知态（须先对账），实得 %v", outcome)
	}
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("重试仍应判结果未知（须先对账），实得 %v", err)
	}
}

// TestApplyReceivesFence 钉住「栅栏令牌必须交到执行器手里」：
// 执行器拿得到本次持有的令牌，且令牌非零、指向同一资源。
//
// 没有这一条，栅栏就只剩上游那半截——下游无从拒收过期持有者的迟到写。
func TestApplyReceivesFence(t *testing.T) {
	st := newFakeStore()
	r := NewRunner(st, 30*time.Second)
	ctx := context.Background()
	a := act("k1", "res-a")

	var seen Lease
	if _, _, err := r.Execute(ctx, a, func(_ context.Context, lease Lease) (Result, error) {
		seen = lease
		return Result{Payload: []byte("v")}, nil
	}); err != nil {
		t.Fatalf("不该失败: %v", err)
	}
	if seen.Fence == 0 {
		t.Fatalf("执行器应拿到非零栅栏令牌，实得 %d", seen.Fence)
	}
	if seen.Resource != a.Resource {
		t.Fatalf("执行器拿到的持有应指向同一资源，实得 %q", seen.Resource)
	}
}

// ignoresUncertainStore 一个**在 Acquire 里不查未知标记**的存储实现：
// 它把「结果未知」这条判据整个摘掉了——负对照专用。
//
// 模拟方式：抢闸前把该 Key 的未知标记抹掉（等于「Acquire 看不见未知态」）。
type ignoresUncertainStore struct {
	*fakeStore
}

func (s *ignoresUncertainStore) Acquire(ctx context.Context, act Action, ttl time.Duration) (Outcome, Lease, Result, error) {
	s.mu.Lock()
	delete(s.uncertain, act.Key)
	s.mu.Unlock()
	return s.fakeStore.Acquire(ctx, act, ttl)
}
