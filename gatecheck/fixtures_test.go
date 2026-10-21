package gatecheck

import (
	"context"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
)

// 本文件是三个测试文件共用的夹具（判据替身与后端替身）。
//
// # 为什么单放一处
//
// 抄三份会漂移——而漂移的夹具比没有夹具更坏（看起来有覆盖，实际测的是
// 两份不同的实现）。三个测试文件共用同一份，差异只来自**被测的路径**。
//
// # 判据替身的两条注意
//
// ① `gateOf` 的例外规则**不限 op 类型** ⇒ 任何 Op 都命中同一条规则。
//    这适合「固定一个裁决」的场景，但**不能**用来观察「Op 映射有没有被用上」——
//    那种场景要用 `gateForOpTypes`（只对指定类型裁决），否则差异不可观察。
//
// ② 上游 `Gate.Decide` 的顺序是「例外规则 → 矩阵层 → 兜底」，
//    `Arbiter` 只在**灰区规则**（verdict=unknown）命中时才被调用。
//    故固定裁决走**例外规则**而非 arbiter。

// gateOf 造一个对**所有** op 类型裁决固定结论的审批门。
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

// gateForOpTypes 造一个**只对指定 op 类型**裁决固定结论的审批门。
//
// 限定 op 类型后：「宿主给的 Op 映射有没有被真的用上」才**可观察**——
// 映射给对了类型 ⇒ 命中规则（裁决固定）；映射没被用上（恒按 OpToolCall）
// ⇒ 走矩阵层（结论不同）⇒ 差异可观察。
func gateForOpTypes(opType policy.OpType, v policy.Verdict) *policy.Gate {
	p := policy.DefaultPolicy()
	p.Rules = []policy.Rule{{
		ID:      "test_fixed_verdict",
		OpTypes: []string{string(opType)},
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
	for _, lv := range levels {
		_ = reg.Register(fakeProvider{level: lv})
	}
	return reg
}

// countingProvider 记录自己被调了几次的后端
// （用来验证「判出来的就是跑的」）。
type countingProvider struct {
	level sandbox.Level
	calls *int
}

func (f countingProvider) ID() string           { return "counting" }
func (f countingProvider) Level() sandbox.Level { return f.level }
func (f countingProvider) Exec(context.Context, sandbox.Request) (sandbox.Outcome, error) {
	*f.calls++
	return sandbox.Outcome{OK: true}, nil
}

// failingProvider 一个执行必失败的后端。
type failingProvider struct {
	err error
}

func (f failingProvider) ID() string           { return "failing" }
func (f failingProvider) Level() sandbox.Level { return sandbox.LevelProcess }
func (f failingProvider) Exec(context.Context, sandbox.Request) (sandbox.Outcome, error) {
	return sandbox.Outcome{}, f.err
}
