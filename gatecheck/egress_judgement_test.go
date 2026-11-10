package gatecheck

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"

	"github.com/yi-nology/agent-harness/egress"
)

// egressProvider 自报档位**与出口能力**的后端。
type egressProvider struct {
	level sandbox.Level
	mode  egress.Mode
}

func (p egressProvider) ID() string           { return "egress-fake" }
func (p egressProvider) Level() sandbox.Level { return p.level }
func (p egressProvider) Exec(context.Context, sandbox.Request) (sandbox.Outcome, error) {
	return sandbox.Outcome{OK: true}, nil
}
func (p egressProvider) Egress() egress.Mode { return p.mode }

func registryWith(p sandbox.Provider) *sandbox.Registry {
	reg := sandbox.NewRegistry()
	if err := reg.Register(p); err != nil {
		panic(err)
	}
	return reg
}

func TestEgressSatisfiedProceeds(t *testing.T) {
	reg := registryWith(egressProvider{level: sandbox.LevelOS, mode: egress.ModeProxy})
	req := Request{
		Op:       policy.Op{Type: policy.OpToolCall, Mutating: policy.Mutating(false)},
		MinLevel: sandbox.LevelProcess,
		Egress:   egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"},
	}
	d := Check(context.Background(), gateOf(policy.VerdictAuto), reg, req)
	if d.Outcome != OutcomeProceed {
		t.Fatalf("应当放行，got %v: %s", d.Outcome, d.Reason)
	}
	if d.Provider == nil {
		t.Fatal("放行时必须给出判出来的后端")
	}
}

// ⭐ 出口要求不能被「更强的档位」蒙混：档位强不等于出口受控。
func TestEgressShortWhenProviderWeaker(t *testing.T) {
	reg := registryWith(egressProvider{level: sandbox.LevelMachine, mode: egress.ModeProxy})
	req := Request{
		Op:       policy.Op{Type: policy.OpToolCall, Mutating: policy.Mutating(false)},
		MinLevel: sandbox.LevelProcess,
		Egress:   egress.Spec{Mode: egress.ModeDenied},
	}
	d := Check(context.Background(), gateOf(policy.VerdictAuto), reg, req)
	if d.Outcome != OutcomeEgressShort {
		t.Fatalf("要求禁网而只能经代理，必须 EgressShort，got %v", d.Outcome)
	}
	if !strings.Contains(d.Reason, "低于要求") {
		t.Fatalf("理由应指出能力不足，got %q", d.Reason)
	}
}

// 沉默的后端按最弱处理：没声明能力就等于兑现不了。
func TestEgressShortWhenProviderSilent(t *testing.T) {
	reg := registryOf(sandbox.LevelOS)
	req := Request{
		Op:       policy.Op{Type: policy.OpToolCall, Mutating: policy.Mutating(false)},
		MinLevel: sandbox.LevelProcess,
		Egress:   egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"},
	}
	d := Check(context.Background(), gateOf(policy.VerdictAuto), reg, req)
	if d.Outcome != OutcomeEgressShort {
		t.Fatalf("未声明出口能力的后端必须判 EgressShort，got %v", d.Outcome)
	}
	if !strings.Contains(d.Reason, "沉默按最弱") {
		t.Fatalf("理由应说明沉默按最弱，got %q", d.Reason)
	}
}

// 声明本身写错（要求代理但没地址）⇒ 拒绝，且优先于档位判定。
func TestEgressInvalidSpecFailsClosed(t *testing.T) {
	reg := registryOf(sandbox.LevelOS)
	req := Request{
		Op:       policy.Op{Type: policy.OpToolCall, Mutating: policy.Mutating(false)},
		MinLevel: sandbox.LevelProcess,
		Egress:   egress.Spec{Mode: egress.ModeProxy},
	}
	d := Check(context.Background(), gateOf(policy.VerdictAuto), reg, req)
	if d.Outcome != OutcomeEgressShort {
		t.Fatalf("不可兑现的声明必须拒绝，got %v", d.Outcome)
	}
	if !strings.Contains(d.Reason, "不可兑现") {
		t.Fatalf("理由应指出声明不可兑现，got %q", d.Reason)
	}
}

// 要求了出口策略却没有登记簿 ⇒ 没有兑现者 ⇒ 拒绝（而不是「没要求隔离所以放行」）。
func TestEgressWithoutRegistryFailsClosed(t *testing.T) {
	req := Request{
		Op:     policy.Op{Type: policy.OpToolCall, Mutating: policy.Mutating(false)},
		Egress: egress.Spec{Mode: egress.ModeProxy, Proxy: "http://127.0.0.1:3128"},
	}
	d := Check(context.Background(), gateOf(policy.VerdictAuto), nil, req)
	if d.Outcome != OutcomeEgressShort {
		t.Fatalf("没有后端就没有兑现者，got %v", d.Outcome)
	}
}

// 不要求出口策略时，判定逐字退回旧行为（零值不影响既有调用点）。
func TestEgressZeroValueUnchanged(t *testing.T) {
	req := Request{Op: policy.Op{Type: policy.OpToolCall, Mutating: policy.Mutating(false)}}
	d := Check(context.Background(), gateOf(policy.VerdictAuto), nil, req)
	if d.Outcome != OutcomeProceed {
		t.Fatalf("既不要档位也不要出口时应放行，got %v", d.Outcome)
	}
}

// 审批拒绝优先于出口判定：审批已否，不必再讨论出口。
func TestApprovalBeatsEgress(t *testing.T) {
	reg := registryOf(sandbox.LevelOS)
	req := Request{
		Op:       policy.Op{Type: policy.OpToolCall, Mutating: policy.Mutating(false)},
		MinLevel: sandbox.LevelProcess,
		Egress:   egress.Spec{Mode: egress.ModeProxy}, // 故意的坏声明
	}
	d := Check(context.Background(), gateOf(policy.VerdictDeny), reg, req)
	if d.Outcome != OutcomeDenied {
		t.Fatalf("审批已否就不该再报出口问题，got %v", d.Outcome)
	}
}

func TestEgressOutcomeString(t *testing.T) {
	if OutcomeEgressShort.String() != "egress_short" {
		t.Fatal("词表漂移")
	}
}
