// memplane_test.go —— 参考实现（MemPlane / MemAuditor）的介入契约与运行时面用例。
//
// 本文件钉的是「介入走运行时同一套判据」的具体形态：强释后旧令牌提交被拒、
// 下一次认领代次严格递增、裁决先到者为准、终止凭代次拒绝过期视图——
// 这些判据必须对着运行时的同一份租约/代次状态演示，只测返回码测不出
// 「有没有真的经闸」。
package controlplane

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/contract"
	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/taskboard"
)

// TestForceReleaseContract 钉介入的核心判据：强释后旧令牌提交被拒、
// 下一次认领的代次严格递增。
func TestForceReleaseContract(t *testing.T) {
	m, aud := seededPlane(t)
	ctx := tenantCtx()

	first, err := m.Claim(ctx, "t1", "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if first.Lease.Token == "" || first.Lease.Fence == 0 {
		t.Fatalf("认领应铸造令牌与代次： %+v", first.Lease)
	}

	err = m.ForceRelease(ctx, ForceRelease{TaskID: "t1", Operator: "op-1", Basis: "持有者已失联超时"})
	if err != nil {
		t.Fatalf("ForceRelease: %v", err)
	}

	// 旧持有者迟到的提交凭 ErrLeaseLost 被拒（不绕闸）
	if err := m.Complete(ctx, "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("旧令牌迟到提交应被拒 ErrLeaseLost，got %v", err)
	}
	// 旧持有者迟到的续期同样被拒（含续期语义的接口面以令牌为准）
	if err := m.Release(ctx, "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("旧令牌迟到释放应被拒 ErrLeaseLost，got %v", err)
	}

	// 任务回到可认领态，租约清零
	tv, err := m.TaskView(ctx, "t1")
	if err != nil {
		t.Fatalf("TaskView: %v", err)
	}
	if tv.Task.State != taskboard.StateOpen || tv.Task.Lease.Token != "" || tv.Task.Owner != "" {
		t.Fatalf("强释后任务应回到无租约的可认领态： %+v", tv.Task)
	}

	// 对不在认领态的任务强释 → ErrLeaseLost（介入重试撞上它说明首次已生效）
	if err := m.ForceRelease(ctx, ForceRelease{TaskID: "t1", Operator: "op-1", Basis: "重试"}); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("对已释放任务再强释应 ErrLeaseLost，got %v", err)
	}

	// 下一次认领铸造的代次严格大于被释放的那一代（下游栅栏的依据）
	second, err := m.Claim(ctx, "t1", "worker-b", time.Minute)
	if err != nil {
		t.Fatalf("第二次 Claim: %v", err)
	}
	if second.Lease.Fence <= first.Lease.Fence {
		t.Fatalf("接管代次未严格递增： %d -> %d", first.Lease.Fence, second.Lease.Fence)
	}
	if second.Lease.Token == first.Lease.Token {
		t.Fatal("接管铸造了同一个令牌")
	}

	// 留痕：介入动作全部留痕（成功与被拒都有）
	entries, err := aud.Entries(ctx, "t1")
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("强释 + 被拒重试应至少 2 条留痕，got %d", len(entries))
	}
	for _, e := range entries {
		if e.Action != ActionForceRelease || e.Operator != "op-1" || e.Basis == "" || e.At.IsZero() {
			t.Fatalf("留痕字段漂移： %+v", e)
		}
	}
}

func TestClaimSemantics(t *testing.T) {
	m, _ := seededPlane(t)
	ctx := tenantCtx()

	// 依赖未完成 → ErrBlocked
	if _, err := m.Claim(ctx, "t2", "w", time.Minute); !errors.Is(err, taskboard.ErrBlocked) {
		t.Fatalf("依赖未完成应 ErrBlocked，got %v", err)
	}
	// 正常认领
	held, err := m.Claim(ctx, "t1", "w1", time.Minute)
	if err != nil {
		t.Fatalf("Claim t1: %v", err)
	}
	// 被占用 → ErrTaken
	if _, err := m.Claim(ctx, "t1", "w2", time.Minute); !errors.Is(err, taskboard.ErrTaken) {
		t.Fatalf("重复认领应 ErrTaken，got %v", err)
	}
	// 释放后重新认领：代次严格递增（接管语义由同一次租约变更保证）
	fence := held.Lease.Fence
	if err := m.Release(ctx, "t1", held.Lease.Token); err != nil {
		t.Fatalf("Release: %v", err)
	}
	next, err := m.Claim(ctx, "t1", "w2", time.Minute)
	if err != nil {
		t.Fatalf("重新认领: %v", err)
	}
	if next.Lease.Fence <= fence {
		t.Fatalf("重新认领代次未递增： %d -> %d", fence, next.Lease.Fence)
	}
	// 旧令牌完成 → ErrLeaseLost（释放后迟到的提交）
	if err := m.Complete(ctx, "t1", held.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("旧令牌应 ErrLeaseLost，got %v", err)
	}
	// 完成 → 终态；再认领 → ErrTerminal
	if err := m.Complete(ctx, "t1", next.Lease.Token); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := m.Claim(ctx, "t1", "w3", time.Minute); !errors.Is(err, taskboard.ErrTerminal) {
		t.Fatalf("终态任务再认领应 ErrTerminal，got %v", err)
	}
	// 坏令牌 / 非认领态 → ErrLeaseLost
	if err := m.Complete(ctx, "t2", "stale-token"); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("坏令牌应 ErrLeaseLost，got %v", err)
	}
}

// TestAdjudicateContract 钉裁决判据：凭事实不凭持有权、先到者为准、
// Unsafe 动作需显式决策。
func TestAdjudicateContract(t *testing.T) {
	m, aud := seededPlane(t)
	ctx := tenantCtx()

	// 已生效的裁决
	err := m.Adjudicate(ctx, Adjudication{EffectKey: "effect-1", Verdict: VerdictApplied, Operator: "op-1", Basis: "下游确认已写入"})
	if err != nil {
		t.Fatalf("Adjudicate: %v", err)
	}
	// 先到者为准：后到的裁决不覆盖（ErrNotUncertain，与 sideeffect 同哨兵）
	err = m.Adjudicate(ctx, Adjudication{EffectKey: "effect-1", Verdict: VerdictNotApplied, Operator: "op-2", Basis: "另一次查询"})
	if !errors.Is(err, sideeffect.ErrNotUncertain) {
		t.Fatalf("已裁决的键再裁应 ErrNotUncertain，got %v", err)
	}

	// Unsafe 动作：「未生效」不自动开门
	if err := m.SeedEffect("effect-unsafe", true); err != nil {
		t.Fatalf("SeedEffect unsafe: %v", err)
	}
	err = m.Adjudicate(ctx, Adjudication{EffectKey: "effect-unsafe", Verdict: VerdictNotApplied, Operator: "op-1", Basis: "下游查无此动作"})
	if !errors.Is(err, sideeffect.ErrUnsafeNotRetryable) {
		t.Fatalf("Unsafe 未生效裁决不自动开门，got %v", err)
	}
	// 显式决策（ForceRetryable）才开门
	err = m.Adjudicate(ctx, Adjudication{EffectKey: "effect-unsafe", Verdict: VerdictNotApplied, ForceRetryable: true, Operator: "op-1", Basis: "确认查询可靠，接受重试风险"})
	if err != nil {
		t.Fatalf("显式决策后应放行，got %v", err)
	}

	// 被拒的介入同样留痕
	entries, err := aud.Entries(ctx, "effect-unsafe")
	if err != nil || len(entries) != 2 {
		t.Fatalf("Unsafe 两次裁决应 2 条留痕： %v %d", err, len(entries))
	}
	// 未知效果键
	err = m.Adjudicate(ctx, Adjudication{EffectKey: "nope", Verdict: VerdictApplied, Operator: "op-1", Basis: "b"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知效果键应 ErrNotFound，got %v", err)
	}
}

// TestTerminateContract 钉终止判据：凭代次拒绝过期视图、终止本身推进代次。
func TestTerminateContract(t *testing.T) {
	m, _ := seededPlane(t)
	ctx := tenantCtx()

	view, err := m.RunView(ctx, "r1")
	if err != nil {
		t.Fatalf("RunView: %v", err)
	}

	// 过期视图（代次不匹配）→ ErrStaleRun
	err = m.Terminate(ctx, Termination{RunID: "r1", ExpectedGen: view.Gen + 1, Operator: "op-1", Basis: "b"})
	if !errors.Is(err, ErrStaleRun) {
		t.Fatalf("过期视图应 ErrStaleRun，got %v", err)
	}

	// 正常终止：状态进终态，代次前进
	err = m.Terminate(ctx, Termination{RunID: "r1", ExpectedGen: view.Gen, Operator: "op-1", Basis: "超时未产出"})
	if err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	view, err = m.RunView(ctx, "r1")
	if err != nil {
		t.Fatalf("RunView after: %v", err)
	}
	if view.Status != contract.StatusCancelled {
		t.Fatalf("终止后状态应为 cancelled，got %s", view.Status)
	}
	if view.Gen != 2 {
		t.Fatalf("终止应推进代次到 2，got %d", view.Gen)
	}

	// 并发介入者的同一份旧视图从此失效（先到者生效，后到者凭代次被拒）
	err = m.Terminate(ctx, Termination{RunID: "r1", ExpectedGen: view.Gen - 1, Operator: "op-2", Basis: "另一个介入者"})
	if !errors.Is(err, ErrStaleRun) {
		t.Fatalf("并发介入者的旧视图应被拒，got %v", err)
	}
	// 终态运行再终止 → ErrTerminalRun（介入重试说明首次已生效）
	err = m.Terminate(ctx, Termination{RunID: "r1", ExpectedGen: view.Gen, Operator: "op-2", Basis: "重试"})
	if !errors.Is(err, ErrTerminalRun) {
		t.Fatalf("终态运行再终止应 ErrTerminalRun，got %v", err)
	}
	// 未知运行
	err = m.Terminate(ctx, Termination{RunID: "nope", ExpectedGen: 1, Operator: "op-1", Basis: "b"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知运行应 ErrNotFound，got %v", err)
	}
}

func TestSeedGuards(t *testing.T) {
	m, _ := seededPlane(t)
	if err := m.SeedRun("r1", "", nil); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("重复 run 应 ErrAlreadyExists，got %v", err)
	}
	if err := m.SeedRun("r9", "", []taskboard.Task{{ID: "t1"}}); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("跨 run 重复任务 id 应 ErrAlreadyExists，got %v", err)
	}
	if err := m.SeedRun("r9", "", []taskboard.Task{{}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("缺任务 id 应 ErrInvalidRequest，got %v", err)
	}
	if err := m.SeedRun("", "", nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("缺 run id 应 ErrInvalidRequest，got %v", err)
	}
	if err := m.SeedEffect("effect-1", false); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("重复效果键应 ErrAlreadyExists，got %v", err)
	}
	if err := m.SeedEffect("", false); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("缺效果键应 ErrInvalidRequest，got %v", err)
	}
}

func TestMemAuditor(t *testing.T) {
	var aud MemAuditor // 零值可用
	ctx := context.Background()

	// 不完整的留痕拒收（谁/动作/目标/依据缺一不可；动作须在介入词表内）
	if err := aud.Append(ctx, AuditEntry{Operator: "op", Target: "t", Basis: "b"}); !errors.Is(err, ErrInvalidAudit) {
		t.Fatalf("缺动作应拒收，got %v", err)
	}
	if err := aud.Append(ctx, AuditEntry{Operator: "op", Action: ActionQuery, Target: "t", Basis: "b"}); !errors.Is(err, ErrInvalidAudit) {
		t.Fatalf("查询动作不经留痕，应拒收，got %v", err)
	}
	if err := aud.Append(ctx, AuditEntry{Action: ActionTerminate, Target: "t", Basis: "b"}); !errors.Is(err, ErrInvalidAudit) {
		t.Fatalf("缺 operator 应拒收，got %v", err)
	}

	// 零值 At 由实现侧补
	e := AuditEntry{Operator: "op-1", Action: ActionTerminate, Target: "r1", Basis: "b"}
	if err := aud.Append(ctx, e); err != nil {
		t.Fatalf("Append: %v", err)
	}
	all, err := aud.Entries(ctx, "")
	if err != nil || len(all) != 1 {
		t.Fatalf("Entries: %v %d", err, len(all))
	}
	if all[0].At.IsZero() {
		t.Fatal("实现侧应补留痕时刻")
	}
	// 按目标过滤
	if err := aud.Append(ctx, AuditEntry{Operator: "op-2", Action: ActionForceRelease, Target: "t1", Basis: "b"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	byTarget, err := aud.Entries(ctx, "t1")
	if err != nil || len(byTarget) != 1 || byTarget[0].Operator != "op-2" {
		t.Fatalf("按目标过滤漂移： %v %+v", err, byTarget)
	}
	// 返回拷贝：改写条目不影响留痕面
	byTarget[0].Operator = "改写"
	again, _ := aud.Entries(ctx, "t1")
	if again[0].Operator != "op-2" {
		t.Fatal("留痕条目被调用方改写")
	}
}
