package gatecheck

import (
	"context"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
)

// gateOf 造一个裁决固定的审批门。
//
// # 为什么用「例外规则」而不是 arbiter
//
// 上游 `Gate.Decide` 的判定顺序是：**例外规则 → 矩阵层 → 兜底**，
// 而 `Arbiter` 只在**灰区规则**（verdict=unknown）命中时才被调用。
// 矩阵层对 `OpToolCall`（非变异）恒判 auto ⇒ 用 arbiter 根本控不住。
//
// 故用 `Policy.Rules`（例外规则，**首个命中即裁决**，优先于矩阵层）把结论固定下来。
// 这也更贴近真实用法：宿主的安全策略本来就是用规则表达的。
func gateOf(v policy.Verdict) *policy.Gate {
	p := policy.DefaultPolicy()
	p.Rules = []policy.Rule{{
		ID:      "test_fixed_verdict",
		OpTypes: []string{string(policy.OpToolCall), string(policy.OpExecuteStep), string(policy.OpPlanSubmit)},
		Verdict: v,
		Reason:  "测试固定裁决",
	}}
	return policy.NewGate(p, nil)
}

// fakeProvider 一个自称某个档位的后端（只实现接口，测试不真执行）。
type fakeProvider struct {
	level sandbox.Level
}

func (f fakeProvider) ID() string           { return "fake" }
func (f fakeProvider) Level() sandbox.Level { return f.level }
func (f fakeProvider) Exec(context.Context, sandbox.Request) (sandbox.Outcome, error) {
	return sandbox.Outcome{OK: true}, nil
}

// registryOf 造一个登记了指定档位后端的登记簿。
func registryOf(levels ...sandbox.Level) *sandbox.Registry {
	reg := sandbox.NewRegistry()
	for i, lv := range levels {
		_ = reg.Register(fakeProvider{level: lv})
		_ = i
	}
	return reg
}

// TestBothAgreeProceeds 钉住最基本的一条：审批放行 + 档位选得到 ⇒ 放行。
func TestBothAgreeProceeds(t *testing.T) {
	d := Check(context.Background(),
		gateOf(policy.VerdictAuto),
		registryOf(sandbox.LevelProcess),
		Request{Op: policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}, MinLevel: sandbox.LevelProcess})

	if d.Outcome != OutcomeProceed {
		t.Fatalf("两层都同意应放行，实得 %v（%s）", d.Outcome, d.Reason)
	}
	if d.Provider == nil {
		t.Fatal("放行时应带出选中的后端")
	}
}

// TestDenyBeatsLevelShort 钉住判据顺序①：**审批已否** ⇒ 直接否，
// 不再看档位（档位再高也不该执行）。
//
// 误用形态：先判档位 → 宿主收到「档位不足」，去换后端，
// 而真正该做的是「这次操作被否了，别换环境」。
func TestDenyBeatsLevelShort(t *testing.T) {
	d := Check(context.Background(),
		gateOf(policy.VerdictDeny),
		registryOf(), // 连一个后端都没登记
		Request{Op: policy.Op{Type: policy.OpToolCall}, MinLevel: sandbox.LevelOS})

	if d.Outcome != OutcomeDenied {
		t.Fatalf("审批已否应判为 denied（优先于档位），实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestNeedHumanBeatsLevelShort 钉住判据顺序③：**需人审**优先于「档位不足」。
//
// 理由：两条拒绝对宿主处置完全不同——档位不足该「换环境」，
// 需人审该「去问人」。若先报档位不足，宿主会去找后端而不是找人，
// 于是这次调用既没执行也没人管——**静默丢失**。
func TestNeedHumanBeatsLevelShort(t *testing.T) {
	d := Check(context.Background(),
		gateOf(policy.VerdictNeedHuman),
		registryOf(), // 档位选不到
		Request{Op: policy.Op{Type: policy.OpToolCall}, MinLevel: sandbox.LevelOS})

	if d.Outcome != OutcomeNeedHuman {
		t.Fatalf("需人审应优先于档位不足，实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestLevelShortWhenBackendUnavailable 钉住：审批放行但档位选不到 ⇒ 拒绝。
//
// 这是「要求了隔离强度却兑现不了」的那一档——必须**拒绝**，不能降级放行。
func TestLevelShortWhenBackendUnavailable(t *testing.T) {
	d := Check(context.Background(),
		gateOf(policy.VerdictAuto),
		registryOf(), // 没有后端
		Request{Op: policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}, MinLevel: sandbox.LevelOS})

	if d.Outcome != OutcomeLevelShort {
		t.Fatalf("档位选不到应判为 level_short，实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestWeakerBackendRejected 钉住：登记的后端**档位不够** ⇒ 拒绝。
//
// 误用形态：只看「有没有后端」而不看档位 ⇒ 要求 OS 级却跑在进程内。
func TestWeakerBackendRejected(t *testing.T) {
	d := Check(context.Background(),
		gateOf(policy.VerdictAuto),
		registryOf(sandbox.LevelProcess), // 只有进程级
		Request{Op: policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}, MinLevel: sandbox.LevelOS})

	if d.Outcome != OutcomeLevelShort {
		t.Fatalf("档位不够应判为 level_short，实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestNoLevelRequiredSkipsSandboxLayer 钉住：**不要求隔离强度**时，
// 沙箱层不构成约束（审批放行就放行）。
//
// 「不要求」与「没装登记簿」是两件事：前者是意图，后者是缺失。
func TestNoLevelRequiredSkipsSandboxLayer(t *testing.T) {
	d := Check(context.Background(),
		gateOf(policy.VerdictAuto),
		nil, // 压根没装登记簿
		Request{Op: policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}, MinLevel: sandbox.LevelNone})

	if d.Outcome != OutcomeProceed {
		t.Fatalf("不要求隔离强度时应放行，实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestMissingRegistryWithLevelRequiredFailsClosed 钉住：要求了隔离强度
// 却**没有登记簿** ⇒ 拒绝（fail-closed）。
//
// 这与上一条是同一件事的两面：LevelNone 是「不要求」，
// 而 LevelOS + nil 登记簿是「要求了但兑现不了」——后者必须拒。
func TestMissingRegistryWithLevelRequiredFailsClosed(t *testing.T) {
	d := Check(context.Background(),
		gateOf(policy.VerdictAuto),
		nil,
		Request{Op: policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}, MinLevel: sandbox.LevelOS})

	if d.Outcome != OutcomeLevelShort {
		t.Fatalf("要求了档位却没有登记簿应判为 level_short，实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestMissingGateNeedsHuman 钉住：**没装审批门** ⇒ 按需人审处理。
//
// fail-closed：没装审批门就放行，等于「没人看着的调用看起来能用」——
// 与 guardset「空集合拒绝构造」同一条纪律。
func TestMissingGateNeedsHuman(t *testing.T) {
	d := Check(context.Background(),
		nil, // 没装审批门
		registryOf(sandbox.LevelOS),
		Request{Op: policy.Op{Type: policy.OpToolCall}, MinLevel: sandbox.LevelOS})

	if d.Outcome != OutcomeNeedHuman {
		t.Fatalf("未装配审批门应判为 need_human，实得 %v（%s）", d.Outcome, d.Reason)
	}
}

// TestPlanOnlyIsNotDenied 钉住：计划模式 ⇒ **只审不执行**，
// 它**不是**拒绝（算成拒绝会让宿主以为这次调用失败了）。
//
// # 为什么不用例外规则固定 plan_only
//
// 上游规定：例外规则的 verdict 只允许 auto/need_human/deny/unknown——
// `plan_only` 写进规则会被判「非法 verdict」并 fail-safe 升人审。
// 计划模式是由「操作类型 + 模式」推导出来的（`OpPlanSubmit` + `ModePlan`），
// 故这里走**矩阵层**的真实路径，不硬造裁决。
func TestPlanOnlyIsNotDenied(t *testing.T) {
	// 无例外规则 ⇒ 走矩阵层；plan_submit + plan 模式 ⇒ plan_only。
	base := policy.DefaultPolicy()
	gate := policy.NewGate(base, nil)

	d := Check(context.Background(),
		gate,
		registryOf(sandbox.LevelOS),
		Request{Op: policy.Op{Type: policy.OpPlanSubmit, Mode: policy.ModePlan}, MinLevel: sandbox.LevelOS})

	if d.Outcome != OutcomePlanOnly {
		t.Fatalf("计划模式应判为 plan_only，实得 %v（%s）", d.Outcome, d.Reason)
	}
	if d.Outcome == OutcomeDenied {
		t.Fatal("计划模式不该算成拒绝")
	}
}

// TestOutcomeString 钉住五态的可读名（诊断与日志面，不参与判定）。
func TestOutcomeString(t *testing.T) {
	cases := map[Outcome]string{
		OutcomeProceed:    "proceed",
		OutcomeNeedHuman:  "need_human",
		OutcomeDenied:     "denied",
		OutcomeLevelShort: "level_short",
		OutcomePlanOnly:   "plan_only",
		Outcome(42):       "unknown",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Fatalf("Outcome(%d).String() = %q，应为 %q", int(o), got, want)
		}
	}
}
