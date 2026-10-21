package gatecheck

// 本文件钉住**执行入口**（ExecuteAndRun / Guard）：判据与执行不可分——判据不通过就根本不执行。

import (
	"context"
	"github.com/oekit/pkg/errors"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
)

// 本文件钉住**执行入口**（ExecuteAndRun / Guard）：
// 判据与执行不可分——判据不通过就根本不执行。

// TestExecuteAndRunRunsTheJudgedProvider 钉住**判据与执行不可分**：
// 判出来的后端**就是**跑起来的后端。
//
// 这是本包存在的核心理由：若判据与执行分离，调用方可以拿判出来的 provider
// 去跑**另一个**后端，于是「声明的档位」与「实际的隔离」之间又没了信号——
// 而那正是本包要消灭的东西。
func TestExecuteAndRunRunsTheJudgedProvider(t *testing.T) {
	calls := 0
	reg := sandbox.NewRegistry()
	_ = reg.Register(countingProvider{level: sandbox.LevelProcess, calls: &calls})

	out, d := ExecuteAndRun(context.Background(),
		gateOf(policy.VerdictAuto),
		reg,
		Request{
			Op:       policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto},
			MinLevel: sandbox.LevelProcess,
			Program:  "return 1;",
		})

	if d.Outcome != OutcomeProceed {
		t.Fatalf("两层都同意应放行，实得 %v（%s）", d.Outcome, d.Reason)
	}
	if !out.OK {
		t.Fatal("执行应成功")
	}
	if calls != 1 {
		t.Fatalf("判出来的后端应被执行 1 次，实得 %d（判据与执行分离了？）", calls)
	}
}

// TestExecuteAndRunDoesNotExecuteOnDeny 钉住：审批已否 ⇒ **根本不执行**。
//
// 误用形态：先执行再判「已拒绝」——副作用已经发生，拒绝毫无意义。
func TestExecuteAndRunDoesNotExecuteOnDeny(t *testing.T) {
	calls := 0
	reg := sandbox.NewRegistry()
	_ = reg.Register(countingProvider{level: sandbox.LevelOS, calls: &calls})

	out, d := ExecuteAndRun(context.Background(),
		gateOf(policy.VerdictDeny),
		reg,
		Request{
			Op:       policy.Op{Type: policy.OpToolCall},
			MinLevel: sandbox.LevelOS,
			Program:  "return 1;",
		})

	if d.Outcome != OutcomeDenied {
		t.Fatalf("审批已否应判为 denied，实得 %v（%s）", d.Outcome, d.Reason)
	}
	if calls != 0 {
		t.Fatalf("审批已否时不该执行，实得 %d 次", calls)
	}
	if out.OK {
		t.Fatal("审批已否时不该有成功结果")
	}
}

// TestExecuteAndRunDoesNotExecuteOnNeedHuman 钉住：需人审 ⇒ 也不执行。
func TestExecuteAndRunDoesNotExecuteOnNeedHuman(t *testing.T) {
	calls := 0
	reg := sandbox.NewRegistry()
	_ = reg.Register(countingProvider{level: sandbox.LevelOS, calls: &calls})

	_, d := ExecuteAndRun(context.Background(),
		gateOf(policy.VerdictNeedHuman),
		reg,
		Request{
			Op:       policy.Op{Type: policy.OpToolCall},
			MinLevel: sandbox.LevelOS,
			Program:  "return 1;",
		})

	if d.Outcome != OutcomeNeedHuman {
		t.Fatalf("需人审应判为 need_human，实得 %v（%s）", d.Outcome, d.Reason)
	}
	if calls != 0 {
		t.Fatalf("需人审时不该执行，实得 %d 次", calls)
	}
}

// TestExecuteAndRunReportsExecutionFailure 钉住：两层都同意但**执行失败**
// ⇒ 独立的一态（不是「不该执行」，是「执行了没成」）。
//
// 宿主的处置是「看错误决定要不要重试」——与「需人审」完全不同，
// 故不能共用一态。
func TestExecuteAndRunReportsExecutionFailure(t *testing.T) {
	boom := errors.New("后端执行失败")
	reg := sandbox.NewRegistry()
	_ = reg.Register(failingProvider{err: boom})

	out, d := ExecuteAndRun(context.Background(),
		gateOf(policy.VerdictAuto),
		reg,
		Request{
			Op:       policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto},
			MinLevel: sandbox.LevelProcess,
			Program:  "return 1;",
		})

	if d.Outcome != OutcomeExecutionFailed {
		t.Fatalf("执行失败应有独立的一态，实得 %v（%s）", d.Outcome, d.Reason)
	}
	if out.OK {
		t.Fatal("执行失败不该返回成功结果")
	}
	if !strings.Contains(d.Reason, "执行失败") {
		t.Errorf("理由应说明执行失败，实得 %q", d.Reason)
	}
}

// TestGuardZeroValueFailsClosed 钉住：零值 Guard（门与登记簿都缺）⇒ 按需人审。
//
// 零值不可用是本仓纪律（「空集合拒绝构造」「没人看着的调用不该看起来能用」）。
// 尤其不能因为「忘了装配」就放行。
func TestGuardZeroValueFailsClosed(t *testing.T) {
	var g Guard
	d := g.Check(context.Background(), Request{
		Op:       policy.Op{Type: policy.OpToolCall},
		MinLevel: sandbox.LevelProcess,
	})
	if d.Outcome != OutcomeNeedHuman {
		t.Fatalf("零值 Guard 应按需人审处理，实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestGuardRunUsesJudgedProvider 钉住：Guard.Run 跑的**就是**判出来的后端。
func TestGuardRunUsesJudgedProvider(t *testing.T) {
	calls := 0
	reg := sandbox.NewRegistry()
	_ = reg.Register(countingProvider{level: sandbox.LevelProcess, calls: &calls})

	g := NewGuard(gateOf(policy.VerdictAuto), reg)
	out, d := g.Run(context.Background(), Request{
		Op:       policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto},
		MinLevel: sandbox.LevelProcess,
		Program:  "return 1;",
	})
	if d.Outcome != OutcomeProceed {
		t.Fatalf("应放行，实得 %v（%s）", d.Outcome, d.Reason)
	}
	if !out.OK {
		t.Fatal("执行应成功")
	}
	if calls != 1 {
		t.Fatalf("判出来的后端应被执行 1 次，实得 %d", calls)
	}
}

// TestGuardDoesNotExecuteOnDeny 钉住：Guard 路径同样**先判后执行**。
func TestGuardDoesNotExecuteOnDeny(t *testing.T) {
	calls := 0
	reg := sandbox.NewRegistry()
	_ = reg.Register(countingProvider{level: sandbox.LevelOS, calls: &calls})

	g := NewGuard(gateOf(policy.VerdictDeny), reg)
	_, d := g.Run(context.Background(), Request{
		Op:       policy.Op{Type: policy.OpToolCall},
		MinLevel: sandbox.LevelOS,
		Program:  "return 1;",
	})
	if d.Outcome != OutcomeDenied {
		t.Fatalf("审批已否应判为 denied，实得 %v", d.Outcome)
	}
	if calls != 0 {
		t.Fatalf("审批已否时不该执行，实得 %d 次", calls)
	}
}
