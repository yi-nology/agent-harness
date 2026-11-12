package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

func main() {
	if err := runDemo(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "参考运行时示例执行失败:", err)
		os.Exit(1)
	}
}

// runDemo 跑完 6 步演示，每步打印一行带编号的记录。
//
// 写成一个可调用的函数（而不是把逻辑塞进 main）有两个好处：
// 端到端用例能直接调它并断言输出，示例本身也被测试覆盖。
func runDemo(ctx context.Context, out io.Writer) error {
	store := newMemStore()
	calls := newCallCounter()
	steps := demoSteps(calls)
	const runID = "run-demo-1"

	// 1) 建一条 3 步骤的任务（步骤间有 DependsOn 依赖）。
	rt := newRuntime(runID, store, steps, 30*time.Second)
	if err := writef(out, "[1] 建任务：%d 个步骤，依赖链 %s\n", len(steps), describeDeps(steps)); err != nil {
		return err
	}

	// 2) 执行前两步，落快照、提交终态。
	rep1, err := rt.run(ctx, 2)
	if err != nil {
		return errors.Wrap(err, "前两步执行失败")
	}
	if err := writef(out, "[2] 执行前两步：完成 %v；进度快照已落库 seq=%d；这 2 步的终态各提交一次\n",
		rep1.Executed, rep1.Seq); err != nil {
		return err
	}

	// 3) 中断：流程在这里停下（示例用 stopAfter 模拟进程在中途中断）。
	if err := writef(out, "[3] 中断：流程停在第 %d 步（%v），第 3 步未开始；已落库的快照就是恢复点\n",
		len(rep1.Executed), rep1.Executed); err != nil {
		return err
	}

	// 4) 恢复续跑：重建运行时（等价于进程重启），从存储回灌序号基线后重建计划。
	rt2 := newRuntime(runID, store, steps, 30*time.Second)
	rep2, err := rt2.run(ctx, 0)
	if err != nil {
		return errors.Wrap(err, "续跑失败")
	}
	if err := writef(out, "[4] 恢复续跑：跳过 %v，只跑 %v；各步骤执行体调用次数 %s（前两步未重放）\n",
		rep2.Skipped, rep2.Executed, describeCalls(calls)); err != nil {
		return err
	}

	// 5) 并发认领：两个执行者同时认领同一条待领任务。
	winners, err := claimRace(ctx, store.tasks)
	if err != nil {
		return errors.Wrap(err, "并发认领失败")
	}
	if err := writef(out, "[5] 并发认领：2 个执行者同时认领同一条待领任务，赢家 %d 个（契约要求恰好 1 个）\n",
		winners); err != nil {
		return err
	}

	// 6) 副作用对账：结果未知 → 拒绝重试 → 裁决 → 重放。
	summary, err := reconcileDemo(ctx, store)
	if err != nil {
		return errors.Wrap(err, "对账演示失败")
	}
	if err := writef(out, "[6] 副作用对账：%s\n", summary); err != nil {
		return err
	}
	return nil
}

// writef 写一行演示输出。
//
// 为什么写失败要当失败：示例的输出本身就是验收物（端到端用例按行断言），
// 写不出去意味着这轮演示没跑成——把它当成成功返回，就会得到一份「绿灯但没内容」
// 的假象。
func writef(out io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(out, format, args...); err != nil {
		return errors.Wrap(err, "写演示输出失败")
	}
	return nil
}

// demoSteps 演示用的三步骤链：prepare → collect → finalize（后两步声明 DependsOn）。
func demoSteps(calls *callCounter) []*stepDef {
	mk := func(id, label string, deps []string, payload string) *stepDef {
		return &stepDef{
			id:        id,
			label:     label,
			dependsOn: deps,
			action: sideeffect.Action{
				Key:      sideeffect.Key("demo/" + id),
				Resource: "resource/demo",
				Payload:  []byte(payload),
				// 未声明重试安全性 ⇒ 按最弱处理：施加失败即「结果未知」，必须先对账。
				RetrySafety: sideeffect.RetrySafetyUnknown,
			},
			apply: func(_ context.Context, _ sideeffect.Lease) (sideeffect.Result, error) {
				calls.inc(id)
				return sideeffect.Result{Payload: []byte(payload + "-done")}, nil
			},
		}
	}
	return []*stepDef{
		mk("prepare", "准备", nil, "prepare"),
		mk("collect", "采集", []string{"prepare"}, "collect"),
		mk("finalize", "归档", []string{"collect"}, "finalize"),
	}
}

// claimRace 让两个执行者同时认领同一条待领任务，返回赢家数。
func claimRace(ctx context.Context, board *boardStore) (int, error) {
	board.Seed(taskboard.Task{
		ID:    "demo-claim",
		Title: "待领任务",
		State: taskboard.StateOpen,
	})
	// 令牌由存储铸造：认领请求里只给「要多长的租约」。
	winners, err := taskboard.ConcurrentClaim(ctx, board, "demo-claim",
		[]taskboard.AgentID{"worker-a", "worker-b"}, time.Minute)
	if err != nil {
		return 0, errors.Wrap(err, "并发认领判定失败")
	}
	return winners, nil
}

// reconcileDemo 演示「结果未知 → 拒绝重试 → 裁决 → 重放」的闭环。
func reconcileDemo(ctx context.Context, store *memStore) (string, error) {
	runner := sideeffect.NewRunner(store.effects, 30*time.Second)
	act := sideeffect.Action{
		Key:         "demo/reconcile",
		Resource:    "resource/reconcile",
		Payload:     []byte("payload"),
		RetrySafety: sideeffect.RetrySafetyUnknown,
	}

	calls := 0
	body := func(_ context.Context, _ sideeffect.Lease) (sideeffect.Result, error) {
		calls++
		// 模拟「动作已生效，但响应在回程丢了」：本包看不见下游事实，
		// 只能把「不知道」显式化，而不是替调用方猜一个「没生效」。
		return sideeffect.Result{}, errors.New("动作已生效但响应丢失")
	}

	outcome1, _, err1 := runner.Execute(ctx, act, body)
	if outcome1 != sideeffect.OutcomeUncertain || !errors.Is(err1, sideeffect.ErrOutcomeUnknown) {
		return "", errors.Wrap(err1, "首次执行应当判为结果未知")
	}

	// 重试必须被拒：闸在未知态不发放执行权，动作体不被再调用。
	outcome2, _, err2 := runner.Execute(ctx, act, body)
	if outcome2 != sideeffect.OutcomeUncertain || !errors.Is(err2, sideeffect.ErrOutcomeUnknown) {
		return "", errors.Wrap(err2, "未知态下的重试应当被拒")
	}
	if calls != 1 {
		return "", errors.New("未知态下的重试被执行了：动作体调用次数增加")
	}

	// 裁决「已生效」⇒ 该键记为已完成并留存结果。
	outcome3, _, err3 := runner.Reconcile(ctx, act, sideeffect.Resolution{
		Applied: true,
		Result:  sideeffect.Result{Payload: []byte("applied")},
		Reason:  "查下游事实：动作已生效",
	})
	if err3 != nil {
		return "", errors.Wrap(err3, "裁决失败")
	}
	if outcome3 != sideeffect.OutcomeReplayed {
		return "", errors.New("裁决「已生效」后应当转为已完成")
	}

	// 之后同一键只重放结果，不再执行动作体。
	outcome4, res4, err4 := runner.Execute(ctx, act, body)
	if err4 != nil {
		return "", errors.Wrap(err4, "重放失败")
	}
	if outcome4 != sideeffect.OutcomeReplayed {
		return "", errors.New("裁决之后应当重放既有结果")
	}
	if calls != 1 {
		return "", errors.New("裁决之后动作体又被调用了：应当只重放结果")
	}

	return fmt.Sprintf("首次 %v（错误可判 ErrOutcomeUnknown）；重试 %v 且执行体调用次数不变（%d 次）；"+
		"裁决后 %v，之后同一键 %v（结果 %q），执行体累计仍为 %d 次，不再执行",
		outcome1, outcome2, calls, outcome3, outcome4, res4.Payload, calls), nil
}

func describeDeps(steps []*stepDef) string {
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		if len(s.dependsOn) == 0 {
			parts = append(parts, s.id)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s(依赖 %s)", s.id, strings.Join(s.dependsOn, ",")))
	}
	return strings.Join(parts, " → ")
}

func describeCalls(c *callCounter) string {
	snap := c.snapshot()
	ids := make([]string, 0, len(snap))
	for id := range snap {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%s=%d", id, snap[id]))
	}
	return strings.Join(parts, ", ")
}
