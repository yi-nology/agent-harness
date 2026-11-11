package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/checkpoint"
	"github.com/yi-nology/goagent-kernel/contract"
	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// testSteps 合成数据的三步骤链（无任何领域词面）。
func testSteps(calls *callCounter) []*stepDef {
	mk := func(id string, deps []string) *stepDef {
		return &stepDef{
			id:        id,
			label:     id,
			dependsOn: deps,
			action: sideeffect.Action{
				Key:         sideeffect.Key("test/" + id),
				Resource:    "resource/test",
				Payload:     []byte(id),
				RetrySafety: sideeffect.RetrySafetyUnknown,
			},
			apply: func(_ context.Context, _ sideeffect.Lease) (sideeffect.Result, error) {
				calls.inc(id)
				return sideeffect.Result{Payload: []byte(id)}, nil
			},
		}
	}
	return []*stepDef{
		mk("prepare", nil),
		mk("collect", []string{"prepare"}),
		mk("finalize", []string{"collect"}),
	}
}

// TestInterruptedRunResumesWithoutReplay 中断后续跑：前两步被跳过、只跑第三步，
// 且**已完成步骤的副作用没有被重放**（执行体调用次数是证据）。
func TestInterruptedRunResumesWithoutReplay(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	calls := newCallCounter()
	steps := testSteps(calls)

	rt := newRuntime("run-1", store, steps, time.Minute)
	rep1, err := rt.run(ctx, 2)
	if err != nil {
		t.Fatalf("前两步执行失败: %v", err)
	}
	if len(rep1.Executed) != 2 {
		t.Fatalf("应执行 2 步，实得 %v", rep1.Executed)
	}
	if !rep1.Interrupted {
		t.Fatal("应标记为中断")
	}

	// 重建运行时 = 进程重启：从存储回灌序号基线后重建计划。
	rt2 := newRuntime("run-1", store, steps, time.Minute)
	rep2, err := rt2.run(ctx, 0)
	if err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	if len(rep2.Skipped) != 2 || rep2.Skipped[0] != "prepare" || rep2.Skipped[1] != "collect" {
		t.Fatalf("续跑应跳过前两步，实得跳过 %v", rep2.Skipped)
	}
	if len(rep2.Executed) != 1 || rep2.Executed[0] != "finalize" {
		t.Fatalf("续跑应只跑第三步，实得执行 %v", rep2.Executed)
	}
	for _, id := range []string{"prepare", "collect", "finalize"} {
		if got := calls.get(id); got != 1 {
			t.Fatalf("步骤 %s 的执行体被调用 %d 次（续跑不得重放已完成的步骤，应恰好 1 次）", id, got)
		}
	}
}

// TestInterruptedAndUninterruptedAgree 中断与不中断两条路径的**最终结果一致**。
func TestInterruptedAndUninterruptedAgree(t *testing.T) {
	ctx := context.Background()

	final := func(store *memStore) checkpoint.Snapshot {
		t.Helper()
		snap, err := store.snaps.Load(ctx, "run-x")
		if err != nil {
			t.Fatalf("读取快照失败: %v", err)
		}
		return snap
	}

	// 路径一：一路跑完。
	storeA := newMemStore()
	callsA := newCallCounter()
	rtA := newRuntime("run-x", storeA, testSteps(callsA), time.Minute)
	if _, err := rtA.run(ctx, 0); err != nil {
		t.Fatalf("一路执行失败: %v", err)
	}
	snapA := final(storeA)

	// 路径二：跑两步 → 中断 → 重建运行时续跑。
	storeB := newMemStore()
	callsB := newCallCounter()
	stepsB := testSteps(callsB)
	rtB := newRuntime("run-x", storeB, stepsB, time.Minute)
	if _, err := rtB.run(ctx, 2); err != nil {
		t.Fatalf("前两步执行失败: %v", err)
	}
	rtB2 := newRuntime("run-x", storeB, stepsB, time.Minute)
	if _, err := rtB2.run(ctx, 0); err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	snapB := final(storeB)

	if snapA.Status != snapB.Status {
		t.Fatalf("两条路径的终态不一致：%s vs %s", snapA.Status, snapB.Status)
	}
	if strings.Join(snapA.Done, ",") != strings.Join(snapB.Done, ",") {
		t.Fatalf("两条路径的完成集不一致：%v vs %v", snapA.Done, snapB.Done)
	}
	if snapA.Status != contract.StatusSucceeded {
		t.Fatalf("终态应为 succeeded，实得 %s", snapA.Status)
	}
	// 失败那条路径同样不得重放已完成的步骤。
	for _, id := range []string{"prepare", "collect", "finalize"} {
		if got := callsB.get(id); got != 1 {
			t.Fatalf("步骤 %s 的执行体被调用 %d 次（中断路径不得重放，应恰好 1 次）", id, got)
		}
	}
}

// TestConcurrentClaimHasSingleWinner 两个执行者同时认领同一条待领任务 ⇒ 恰好一个赢家。
func TestConcurrentClaimHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	store.tasks.Seed(taskboard.Task{ID: "t-1", Title: "待领", State: taskboard.StateOpen})

	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for _, agent := range []taskboard.AgentID{"a1", "a2"} {
		wg.Add(1)
		// 走 async.GoSafe 而非裸 go func：panic 留痕不杀进程。
		async.GoSafe(func() {
			defer wg.Done()
			_, err := store.tasks.Claim(ctx, "t-1", agent, taskboard.ClaimRequest{TTL: time.Minute})
			if err == nil {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("并发认领的赢家数 = %d，契约要求恰好 1 个", winners)
	}
	// 输家的释放必须被拒（凭令牌认人，不认内容）。
	if err := store.tasks.Release(ctx, "t-1", "tok-not-the-holder"); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("非持有者释放应被拒为 ErrLeaseLost，实得 %v", err)
	}
}

// TestUncertainBlocksRetryUntilReconciled 副作用对账闭环：
// 失败 → 结果未知 → 重试被拒（动作体不再被调用）→ 裁决 → 重放。
func TestUncertainBlocksRetryUntilReconciled(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	runner := sideeffect.NewRunner(store.effects, time.Minute)
	act := sideeffect.Action{
		Key:         "k",
		Resource:    "r",
		Payload:     []byte("p"),
		RetrySafety: sideeffect.RetrySafetyUnknown,
	}

	calls := 0
	body := func(_ context.Context, _ sideeffect.Lease) (sideeffect.Result, error) {
		calls++
		return sideeffect.Result{}, errors.New("动作已生效但响应丢失")
	}

	outcome, _, err := runner.Execute(ctx, act, body)
	if outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("首次执行应判为 uncertain，实得 %v", outcome)
	}
	if !errors.Is(err, sideeffect.ErrOutcomeUnknown) {
		t.Fatalf("错误应可判 ErrOutcomeUnknown，实得 %v", err)
	}

	outcome, _, err = runner.Execute(ctx, act, body)
	if outcome != sideeffect.OutcomeUncertain || !errors.Is(err, sideeffect.ErrOutcomeUnknown) {
		t.Fatalf("未知态下的重试应被拒为 uncertain，实得 %v / %v", outcome, err)
	}
	if calls != 1 {
		t.Fatalf("重试被执行了：动作体调用次数 = %d（应仍为 1）", calls)
	}

	outcome, _, err = runner.Reconcile(ctx, act, sideeffect.Resolution{
		Applied: true,
		Result:  sideeffect.Result{Payload: []byte("v")},
		Reason:  "查下游：已生效",
	})
	if err != nil {
		t.Fatalf("裁决失败: %v", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		t.Fatalf("裁决「已生效」后应为 replayed，实得 %v", outcome)
	}

	outcome, res, err := runner.Execute(ctx, act, body)
	if err != nil {
		t.Fatalf("重放失败: %v", err)
	}
	if outcome != sideeffect.OutcomeReplayed {
		t.Fatalf("裁决后应重放既有结果，实得 %v", outcome)
	}
	if string(res.Payload) != "v" {
		t.Fatalf("应取回裁决时记录的结果 v，实得 %q", res.Payload)
	}
	if calls != 1 {
		t.Fatalf("裁决后动作体又被调用：调用次数 = %d（应仍为 1）", calls)
	}
}

// decideRetryIgnoringUncertain 是**故意写坏**的对照实现：忽略未知态直接放行。
//
// 它只存在于测试里，用途是证明判据有分辨力（见下面的用例）。
func decideRetryIgnoringUncertain(_ sideeffect.Outcome) error { return nil }

// TestUnknownOutcomeDecisionHasDiscriminatingPower 负对照：
// 同一把尺子量两个实现——正确实现判「拒绝重试」，坏实现判「放行」。
// 把生产实现换成坏的那个，下面第一条断言必红。
func TestUnknownOutcomeDecisionHasDiscriminatingPower(t *testing.T) {
	blocked := func(decide func(sideeffect.Outcome) error) bool {
		return decide(sideeffect.OutcomeUncertain) != nil
	}

	if !blocked(decideRetry) {
		t.Fatal("结果未知时必须拒绝放行：正确实现应返回 error")
	}
	if err := decideRetry(sideeffect.OutcomeUncertain); !errors.Is(err, sideeffect.ErrOutcomeUnknown) {
		t.Fatalf("拒绝理由应可判 ErrOutcomeUnknown，实得 %v", err)
	}
	if blocked(decideRetryIgnoringUncertain) {
		t.Fatal("负对照失效：坏实现也被判为拒绝——本用例失去分辨力")
	}
	// 坏实现的返回值为 nil ⇒ 若把它装到 decideRetry 上，上面的
	// 「blocked(decideRetry) 必须为真」断言必红。这就是分辨力的证据。
	if decideRetryIgnoringUncertain(sideeffect.OutcomeUncertain) != nil {
		t.Fatal("坏实现应当放行（返回 nil）")
	}
}

// TestDemoRunsAllSixSteps 端到端：演示跑完 6 步，每步都有带编号的记录。
func TestDemoRunsAllSixSteps(t *testing.T) {
	var buf bytes.Buffer
	if err := runDemo(context.Background(), &buf); err != nil {
		t.Fatalf("演示执行失败: %v", err)
	}
	out := buf.String()
	for i := 1; i <= 6; i++ {
		marker := fmt.Sprintf("[%d]", i)
		if !strings.Contains(out, marker) {
			t.Fatalf("演示输出缺少第 %d 步的记录：\n%s", i, out)
		}
	}
}
