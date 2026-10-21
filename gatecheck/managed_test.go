package gatecheck

// 本文件钉住**受管后端**（Managed）：把真后端包成受联合判据管辖的后端，判据不通过则不委派。

import (
	"context"
	"github.com/oekit/pkg/errors"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
)

//
// 本文件钉住**受管后端**（Managed）：
// 把真后端包成受联合判据管辖的后端，判据不通过则不委派。

// TestManagedDoesNotDelegateOnDeny 钉住**受管后端的核心语义**：
// 判据不通过 ⇒ 真后端**一次都不被调用**。
//
// 这是「判据与执行不可分」的落点：若判据在别处跑、执行在这里跑，
// 中间任何一步走错就产生「判过的 ≠ 跑的」���落差。
func TestManagedDoesNotDelegateOnDeny(t *testing.T) {
	calls := 0
	inner := countingProvider{level: sandbox.LevelOS, calls: &calls}
	reg := sandbox.NewRegistry()
	_ = reg.Register(inner)

	m := NewManaged(inner, gateOf(policy.VerdictDeny), reg, sandbox.LevelNone)
	out, err := m.Exec(context.Background(), sandbox.Request{Program: "return 1;"})
	if err == nil {
		t.Fatal("审批已否时该失败")
	}
	if calls != 0 {
		t.Fatalf("审批已否时真后端不该被调用，实得 %d 次", calls)
	}
	if out.OK {
		t.Fatal("审批已否时不该有成功结果")
	}
	// 失败形态要可诊断：Kind 说得清是哪一档。
	var f *sandbox.Failure
	if !errors.As(err, &f) {
		t.Fatalf("失败应是 sandbox.Failure，实得 %T", err)
	}
	if f.Kind != OutcomeDenied.String() {
		t.Errorf("失败 Kind=%q，应为 %q（调用方据此分处置）", f.Kind, OutcomeDenied.String())
	}
}

// TestManagedDelegatesWhenBothAgree 钉住：两层都同意 ⇒ 委派给真后端执行。
func TestManagedDelegatesWhenBothAgree(t *testing.T) {
	calls := 0
	inner := countingProvider{level: sandbox.LevelProcess, calls: &calls}
	reg := sandbox.NewRegistry()
	_ = reg.Register(inner)

	m := NewManaged(inner, gateOf(policy.VerdictAuto), reg, sandbox.LevelNone)
	out, err := m.Exec(context.Background(), sandbox.Request{Program: "return 1;"})
	if err != nil {
		t.Fatalf("两层都同意不该失败: %v", err)
	}
	if !out.OK {
		t.Fatal("执行应成功")
	}
	if calls != 1 {
		t.Fatalf("真后端应被调用 1 次，实得 %d", calls)
	}
}

// TestManagedRequiresLevelOfInner 钉住：受管后端要求的档位
// **默认取真后端自报的档位**——「这个后端提供多强隔离」是后端的事实。
//
// 误用形态：包装层自己报一个更高的档位（如把进程内说成 OS 级），
// 那就是让档位表说谎——而档位表是宿主做风险判断的依据。
func TestManagedRequiresLevelOfInner(t *testing.T) {
	calls := 0
	// 真后端只提供进程级；登记簿里**没有**更强档位的后端。
	inner := countingProvider{level: sandbox.LevelProcess, calls: &calls}
	reg := sandbox.NewRegistry()
	_ = reg.Register(inner)

	m := NewManaged(inner, gateOf(policy.VerdictAuto), reg, sandbox.LevelNone)
	_, err := m.Exec(context.Background(), sandbox.Request{Program: "return 1;"})
	// 登记簿里只有进程级后端 ⇒ 要求进程级 ⇒ 选得到 ⇒ 放行。
	if err != nil {
		t.Fatalf("档位够时不该失败: %v", err)
	}
	if calls != 1 {
		t.Fatalf("真后端应被调用 1 次，实得 %d", calls)
	}
}

// TestManagedLevelForwardsInner 钉住：受管后端的档位**转发**真后端的。
func TestManagedLevelForwardsInner(t *testing.T) {
	inner := countingProvider{level: sandbox.LevelProcess}
	m := NewManaged(inner, nil, nil, sandbox.LevelNone)
	if got := m.Level(); got != sandbox.LevelProcess {
		t.Errorf("档位=%v，应转发真后端的 %v", got, sandbox.LevelProcess)
	}
}

// TestManagedWithoutInnerFails 钉住：没有内层真后端 ⇒ 显式失败。
func TestManagedWithoutInnerFails(t *testing.T) {
	m := NewManaged(nil, gateOf(policy.VerdictAuto), sandbox.NewRegistry(), sandbox.LevelNone)
	if _, err := m.Exec(context.Background(), sandbox.Request{Program: "return 1;"}); err == nil {
		t.Fatal("缺内层后端时该失败")
	}
}

// TestManagedUsesOpFuncForApproval 钉住：审批层判的是**宿主给的操作映射**。
//
// 误用形态：包装层自己猜「这次是什么操作」——那是宿主的知识
// （哪些工具是变更类、哪些是只读采集）。猜错的后果分两种：
//   - 把变更类当成只读 ⇒ 变更被自动放行（**危险方向**）；
//   - 把只读当成变更 ⇒ 每次都问人（**烦人方向**，但安全）。
//
// 故缺省必须是**保守**的那一档，且宿主的映射必须真的被用上。
func TestManagedUsesOpFuncForApproval(t *testing.T) {
	calls := 0
	inner := countingProvider{level: sandbox.LevelOS, calls: &calls}
	reg := sandbox.NewRegistry()
	_ = reg.Register(inner)

	// 宿主的映射：这个程序是变更类操作。
	// 审批门**只对 execute_step 裁决**（其它 op 类型走矩阵层），
	// 这样「映射有没有被用上」才有可观察的差别。
	gate := gateForOpTypes(policy.OpExecuteStep, policy.VerdictDeny)
	m := NewManaged(inner, gate, reg, sandbox.LevelNone)
	m.OpFunc = func(program string, budget sandbox.Budget) policy.Op {
		return policy.Op{Type: policy.OpExecuteStep, Mode: policy.ModeFull}
	}

	_, err := m.Exec(context.Background(), sandbox.Request{Program: "do_change()"})
	if err == nil {
		t.Fatal("审批已否时该失败")
	}
	if calls != 0 {
		t.Fatalf("审批已否时真后端不该被调用，实得 %d 次", calls)
	}
}

// TestManagedDefaultOpIsToolCall 钉住：宿主没给映射时，缺省按
// `OpToolCall`（只读采集面）——**保守**的那一档。
func TestManagedDefaultOpIsToolCall(t *testing.T) {
	calls := 0
	inner := countingProvider{level: sandbox.LevelOS, calls: &calls}
	reg := sandbox.NewRegistry()
	_ = reg.Register(inner)

	m := NewManaged(inner, gateOf(policy.VerdictDeny), reg, sandbox.LevelNone)
	// 不设 OpFunc ⇒ 缺省 OpToolCall。
	_, err := m.Exec(context.Background(), sandbox.Request{Program: "x()"})
	if err == nil {
		t.Fatal("审批已否时该失败")
	}
	if calls != 0 {
		t.Fatalf("审批已否时真后端不该被调用，实得 %d 次", calls)
	}
}

// TestManagedRequiresAtLeastInnerLevel 钉住：受管后端**至少**要求
// 「达到真后端自报的档位」——不因包装层把要求抹成 LevelNone 就放过弱的执行。
//
// # 怎么造出可观察的差异
//
// 登记簿里放**两个**后端：一个进程级（真后端）、一个 OS 级。
// 「要求 = 真后端的档位（进程级）」⇒ 选到进程级（最弱满足者）。
// 「要求 = LevelNone」⇒ 无档位约束 ⇒ Resolve 可能选中**任意**档位
// （上游取 `p.Level() < best.Level()` 的最弱者，理论上仍是最弱满足者，
// 但约束已经没了，语义不同）。
//
// 判据：**要求的档位被抹掉后，隔离强度就没有下限了**——
// 那正是「档位表说谎」的形态。
func TestManagedRequiresAtLeastInnerLevel(t *testing.T) {
	// 真后端只提供进程级。
	inner := countingProvider{level: sandbox.LevelProcess, calls: new(int)}
	reg := sandbox.NewRegistry()
	_ = reg.Register(inner)

	// 断言受管后端的**档位下限**：不传 MinLevel ⇒ 取真后端自报的档位。
	//
	// 怎么观察？让真后端的档位**高于**登记簿里其它后端能提供的——
	// 于是「要求 = 真后端档位」会选不到，「要求 = LevelNone」却可能选到。
	// 故登记簿里只放一个**更弱**的后端 + 真后端自己：
	// 要求 = 进程级 ⇒ 选得到进程级 ⇒ 放行；
	// 若档位下限被抹成 LevelNone ⇒ 判据形同虚设，但结果仍是放行——
	// ⇒ 观察不到。
	//
	// 故换一个**可观察**的判据：让真后端自报**一个登记簿里没有的档位**
	// （OS 级），而登记簿里只有进程级后端：
	//   - 档位下限取真后端自报（OS）⇒ 登记簿里没有 OS 级 ⇒ **拒绝**；
	//   - 档位下限被抹成 LevelNone ⇒ 无约束 ⇒ 放行。
	// 差异可观察。
	strongInner := countingProvider{level: sandbox.LevelOS, calls: new(int)}
	reg2 := sandbox.NewRegistry()
	_ = reg2.Register(countingProvider{level: sandbox.LevelProcess, calls: new(int)})

	m := NewManaged(strongInner, gateOf(policy.VerdictAuto), reg2, sandbox.LevelNone)
	d := m.Check(context.Background(), Request{
		Op:     policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto},
		Budget: sandbox.Budget{},
	})
	if d.Outcome != OutcomeLevelShort {
		t.Fatalf("受管后端应要求达到真后端自报的档位（OS），登记簿里没有 ⇒ 拒绝；实得 %v（%s）", d.Outcome, d.Reason)
	}
}
