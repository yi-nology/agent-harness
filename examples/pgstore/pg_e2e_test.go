//go:build unix

// pg_e2e_test.go —— 真实进程故障恢复（issue #95）。
//
// # 为什么必须有这一层
//
// 「进程死掉」与「进程还活着但函数返回了错误」在持久化语义上是完全不同的
// 两件事：单进程用例（含内存版参考实现）只能模拟后者——所有状态都在同一个
// 进程里，函数返回错误时状态照样能被它收拾。真死（SIGKILL）留下的是：
// 执行意图已落库、没有任何出口被调用、内存里的一切消失。判据是否还成立，
// 只有跨进程的真实用例能证明。
//
// # 用例形态（re-exec 模式）
//
// 父进程（测试进程）用 os/exec 跑**同一个测试二进制**的子进程入口
// （TestPGRefChildProcess，经环境变量区分模式），对它做真实的进程操作：
//
//   - TestPGE2ESigkillMidApply      死在施加之前：SIGKILL → 等 TTL 过期 →
//     同一 Key 不得重新授予执行权（OutcomeUncertain，动作体不重放）；
//     对账「未生效」后重做，动作体恰好再执行一次。
//   - TestPGE2ESigkillAfterApply    死在施加之后记账之前：下游已写入 →
//     SIGKILL → 等 TTL 过期 → 同一 Key 不被重放也不被重做；对账「已生效」
//     采信下游事实 → 之后同一 Key 只重放既有结果，动作体不再执行。
//   - TestPGE2ETwoProcessRace       双进程抢同一 Key：恰好一个 OutcomeApplied、
//     下游恰好一代令牌、动作体恰好执行一次。
//
// 跨进程的共享状态只有 PostgreSQL（GAK_PGREF_DSN + 测试专属 schema）——
// 这正是「判据归存储，不归进程」要成立的那个前提。
package pgstore

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// TestPGE2ESigkillMidApply 死在施加之前：意图在库里，下游没有写入。
//
// 期望：重启后同一 Key 转入需对账（不重放）；对账「未生效」后可重做，
// 动作体恰好再执行一次（合计 2 次——这是判据**允许**且仅允许的那次重做）。
func TestPGE2ESigkillMidApply(t *testing.T) {
	e := freshE2E(t, 2*time.Second)
	ctx := context.Background()

	child := e.startChild(t, childModeRecordThenHang)
	e.waitEffectCalls(t, 1) // 效果体已进入（意图早已落库）
	child.kill(t)

	// 下游必须还干净：死在施加之前（前提自检——前提不成立，断言无意义）。
	if seen, err := e.ds.SeenFence(ctx, e.resource); err != nil || seen != 0 {
		t.Fatalf("前提自检失败：下游应无写入（seen=%d, err=%v）", seen, err)
	}
	e.waitTTLPass(t)

	// 「重启后的进程」：同一逻辑动作再来一次。
	runner := sideeffect.NewRunner(e.eff, e.ttl)
	requireUncertain(t, runner, e, 1)

	// 对账：查下游事实——没有写入 ⇒ 未生效 ⇒ 允许重做。
	if _, _, err := runner.Reconcile(ctx, e.action(), sideeffect.Resolution{
		Applied: false,
		Reason:  "下游无此写入（对账结论）",
	}); err != nil {
		t.Fatalf("裁决「未生效」失败: %v", err)
	}

	// 重做：这一次真的施加并收场。重做恰好一次（合计 2 次调用）。
	outcome, _, err := runner.Execute(ctx, e.action(), func(wctx context.Context, lease sideeffect.Lease) (sideeffect.Result, error) {
		if err := recordEffectCall(wctx, e.pool, e.key); err != nil {
			return sideeffect.Result{}, err
		}
		if err := applyDownstream(wctx, e.ds, e.resource, lease.Fence, "applied"); err != nil {
			return sideeffect.Result{}, err
		}
		return sideeffect.Result{Payload: []byte("applied")}, nil
	})
	if outcome != sideeffect.OutcomeApplied || err != nil {
		t.Fatalf("对账「未生效」后应可重做（applied），实得 %v / %v", outcome, err)
	}
	calls, err := e.effectCalls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("重做后动作体应恰好执行 2 次（首次被杀 + 重做），实得 %d", calls)
	}
	if got, err := e.ds.Marker(ctx, e.resource); err != nil || got != "applied" {
		t.Fatalf("重做应把下游写上（value=%q, err=%v）", got, err)
	}
}

// TestPGE2ESigkillAfterApply 死在施加之后记账之前：下游已写入，账没记上。
//
// 期望：重启后同一 Key **不被重放也不被重做**（先需对账）；对账「已生效」
// 采信下游事实后，同一 Key 只重放既有结果——动作体累计仍为 1 次。
func TestPGE2ESigkillAfterApply(t *testing.T) {
	e := freshE2E(t, 2*time.Second)
	ctx := context.Background()

	child := e.startChild(t, childModeApplyThenHang)
	e.waitDownstream(t, "applied") // 施加已完成（栅栏下游已见写入）
	child.kill(t)                  // 记账（Complete）永远不会发生

	e.waitTTLPass(t)

	// 重启后：同一 Key 不得重新授予执行权，动作体不得重放。
	runner := sideeffect.NewRunner(e.eff, e.ttl)
	requireUncertain(t, runner, e, 1)

	// 对账：下游事实是「已生效」且留了结果 ⇒ 采信并记为已完成。
	resolved, _, err := runner.Reconcile(ctx, e.action(), sideeffect.Resolution{
		Applied: true,
		Result:  sideeffect.Result{Payload: []byte("applied")},
		Reason:  "下游已见该代令牌的写入（对账结论）",
	})
	if err != nil {
		t.Fatalf("裁决「已生效」失败: %v", err)
	}
	if resolved != sideeffect.OutcomeReplayed {
		t.Fatalf("裁决「已生效」应转为已完成（replayed），实得 %v", resolved)
	}

	// 此后同一 Key 只重放结果，动作体不再执行——「同一 Key 不被重放」的终点。
	outcome, res, err := runner.Execute(ctx, e.action(), func(context.Context, sideeffect.Lease) (sideeffect.Result, error) {
		t.Fatal("已生效的裁决之后动作体又被调用了（重放判据失效）")
		return sideeffect.Result{}, nil
	})
	if outcome != sideeffect.OutcomeReplayed || err != nil {
		t.Fatalf("裁决后应重放（replayed），实得 %v / %v", outcome, err)
	}
	if string(res.Payload) != "applied" {
		t.Fatalf("应重放对账采信的结果 applied，实得 %q", res.Payload)
	}
	calls, err := e.effectCalls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("动作体累计应为 1 次（施加于被杀的那个进程），实得 %d", calls)
	}
}

// TestPGE2ETwoProcessRace 双进程抢同一 Key：恰好一个生效。
//
// 两个子进程同时跑同一逻辑动作（同 Key 同资源），断言：
//   - 恰好一个 OutcomeApplied（判据「三判合一」的跨进程形态）；
//   - 动作体恰好执行一次（库里的调用计数为 1）；
//   - 下游恰好一代令牌（没有第二次写入落上去）。
func TestPGE2ETwoProcessRace(t *testing.T) {
	e := freshE2E(t, 60*time.Second) // 竞争窗口要够长：输家必须撞上赢家的租约
	ctx := context.Background()

	c1 := e.startChild(t, childModeOneShot)
	c2 := e.startChild(t, childModeOneShot)

	o1 := c1.outcome(t)
	o2 := c2.outcome(t)

	applied := 0
	for _, o := range []string{o1, o2} {
		switch o {
		case sideeffect.OutcomeApplied.String():
			applied++
		case sideeffect.OutcomeContended.String(), sideeffect.OutcomeReplayed.String():
			// 输家的两种合法落点：正被占（等一等）/ 已被做完（拿回结果）。
		default:
			t.Fatalf("双进程竞争出现非法落点 %q（%q / %q）", o, o1, o2)
		}
	}
	if applied != 1 {
		t.Fatalf("双进程抢同一 Key 应恰好一个 OutcomeApplied，实得 %d（%q / %q）", applied, o1, o2)
	}
	calls, err := e.effectCalls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("动作体应恰好执行一次，实得 %d（重复施加 = 三判合一在跨进程下破了）", calls)
	}
	seen, err := e.ds.SeenFence(ctx, e.resource)
	if err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("下游应恰好一代令牌，实得 %d", seen)
	}
}
