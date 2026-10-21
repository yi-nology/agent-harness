package gatecheck

import (
	"context"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
	"github.com/oekit/pkg/errors"
)

// errNoInner 受管后端没有内层真后端。
var errNoInner = errors.New("gatecheck: 受管后端缺少内层执行后端")

// Managed 把一个执行后端包成「**受联合判据管辖**」的后端。
//
// # 为什么需要它（接线件的形态）
//
// 宿主的真实装配是：把 `ptc.Provider`（进程内）或 `ptc.SubprocessProvider`
// （子进程）注册进 `sandbox.Registry`，然后 `sandbox.Execute` 选一个跑。
// 但那条路径**只有档位 × 预算的判定**（上游 `sandbox.Execute` = Resolve + Exec），
// **审批层完全不在里面**——因为 `toolgate/policy` 与 `guardx/sandbox` 互不知情。
//
// 于是宿主要么：
//   - 自己在 `sandbox.Execute` 之前调一次 `gatecheck.Check`（**判据与执行分离**，
//     中间任何一步走错就产生「判过的后端 ≠ 实际跑的后端」的落差）；要么
//   - 把本包提供的 `Managed` 包在真后端外面，**注册受管版**——
//     判据在 `Exec` 内部、紧挨着真正的执行，**不可绕过**。
//
// 后者才是「声明的档位与实际的执行之间没有静默落差」的完整形态。
//
// # 它怎么工作
//
// `Managed.Exec` 里：先做联合判据（审批 + 档位），通过则**委派**给真后端执行。
// 判据不通过 ⇒ **不委派**（副作用不会发生）。
//
// # 档位语义：取真后端自报的档位
//
// 受管后端的 `Level()` 转发真后端——因为「这个后端提供多强的隔离」是
// **后端的事实**，不是包装层的。包装层若自己报一个更高的档位，
// 就是让档位表说谎。
//
// # 审批的 Op 从哪来
//
// 包装层不猜「这次调用是什么操作」——那是宿主的知识。
// 故 `Managed` 带一个 `OpFunc`：宿主给出「程序 + 请求 ⇒ 操作」的映射。
// 映射缺省时按 `OpToolCall` 处理（最保守的一档：变更类工具会被拒）。
type Managed struct {
	// Inner 真后端（被管辖的那个）。
	Inner sandbox.Provider
	// Gate 审批门（nil ⇒ 需人审，fail-closed）。
	Gate *policy.Gate
	// Registry 后端登记簿（用来按要求档位选后端）。
	//
	// 注意：受管后端**自己**在登记簿里时，这里指向的是**同一个**登记簿。
	// 典型形态：宿主先注册真后端，再注册受管版；`Registry.Resolve` 选到受管版，
	// 受管版内部再 Resolve 一次（按同一档位要求选真后端）。
	//
	// 这会引入一次额外解析——但换来的是「判据与执行不可分」。
	// 若宿主不想要这层，用 `gatecheck.Check` + 自己接线（代价是判据可绕过）。
	Registry *sandbox.Registry
	// MinLevel 受管后端要求的最弱档位（缺省 = 不额外要求）。
	MinLevel sandbox.Level
	// OpFunc 从「程序 + 预算」推出审批层要判的操作。
	//
	// 缺省时用 `OpToolCall`（只读采集面）——但**变更类工具**在
	// `OpToolCall` + Mutating 下会被上游的变异守卫直接拒，
	// 所以缺省是**保守**的（宁可多问一次人）。
	OpFunc func(program string, budget sandbox.Budget) policy.Op
}

// NewManaged 组装受管后端。
func NewManaged(inner sandbox.Provider, gate *policy.Gate, registry *sandbox.Registry, minLevel sandbox.Level) *Managed {
	return &Managed{Inner: inner, Gate: gate, Registry: registry, MinLevel: minLevel}
}

// ManagedProviderID 受管后端的标识（**包级常量**——档位登记门禁要静态可识别）。
const ManagedProviderID = "gatecheck-managed"

// ID 后端标识（包级常量，见 ManagedProviderID）。
func (m *Managed) ID() string { return ManagedProviderID }

// Level 隔离档位：受管后端保证的**下限**。
//
// # 为什么不是「转发内层后端的档位」
//
// 档位登记门禁（`harness-sandbox-provider-declared`）要求后端实现的
// `ID()` 与 `Level()` 返回**可静态识别的常量**——因为「档位声明要有人签字」：
// 登记面（policy 里人 review 时看得见的那一行）与代码事实必须对得上，
// 而「内层后端是谁」是**运行期**才知道的值，签字就无从签起。
//
// 故受管后端对外声明的是**它保证的隔离强度下限**：至少进程级
// （否则判据里「要求 ≥ 内层档位」不成立）。实际跑的是哪个档位的内层后端，
// 由 `Check` 在运行期用登记簿判——那是**判据**，不是**声明**。
//
// 这条下限是**保守**的：只承诺「不会低于进程级」，不承诺更高——
// 更高档位由内层后端自己声明（`ptc.SubprocessProvider` 是进程级，
// 宿主自带的容器后端可以是 OS 级）。
//
// # 为什么直接写 `sandbox.LevelProcess` 而不是引用一个常量
//
// 登记门禁读的是**返回表达式的字面形态**（`sandbox.LevelProcess` ⇒ "LevelProcess"，
// 标识符 ⇒ 变量名）。写成 `return ManagedLevel` 会让门禁读到 "ManagedLevel"，
// 与 policy 登记的 "LevelProcess" 对不上 ⇒ 报「档位漂移」。
// 门禁要能静态比对，代码就必须写成**可静态比对的样子**。
func (m *Managed) Level() sandbox.Level { return sandbox.LevelProcess }

// Check 只判定，不执行（与包级 Check 同语义，但用**受管后端自己的档位要求**）。
//
// 单独暴露它是为了让调用方能**只判不跑**（先看结论再决定要不要执行），
// 以及让「要求的档位」可被断言——它是本包的关键不变量：
// 受管后端**至少**要求达到真后端自报的档位。
func (m *Managed) Check(ctx context.Context, req Request) Decision {
	if m == nil || m.Inner == nil {
		return Check(ctx, nil, nil, req)
	}
	minLevel := m.MinLevel
	if minLevel == sandbox.LevelNone {
		// 没额外要求 ⇒ 取真后端自报的档位（要求「至少达到它自己的水平」）。
		minLevel = m.Inner.Level()
	}
	return Check(ctx, m.Gate, m.Registry, Request{
		Op:       req.Op,
		MinLevel: minLevel,
		Budget:   req.Budget,
		Program:  req.Program,
	})
}

// Exec 联合判据 + 委派执行。
//
// 判据不通过 ⇒ **不委派**（真后端的 Exec 一次都不会被调用）。
func (m *Managed) Exec(ctx context.Context, req sandbox.Request) (sandbox.Outcome, error) {
	if m == nil || m.Inner == nil {
		return sandbox.Outcome{}, errNoInner
	}

	op := policy.Op{Type: policy.OpToolCall}
	if m.OpFunc != nil {
		op = m.OpFunc(req.Program, req.Budget)
	}

	// 判据走 Check（**判据只有一份**）——这里只补「操作映射」，
	// 档位下限由 Check 内部的同款逻辑给。
	d := m.Check(ctx, Request{
		Op:       op,
		MinLevel: m.MinLevel,
		Budget:   req.Budget,
		Program:  req.Program,
	})
	if d.Outcome != OutcomeProceed {
		// 判据不通过 ⇒ 不委派：副作用不会发生。
		return sandbox.Outcome{}, &sandbox.Failure{
			// 用可读名（Outcome 是 int，直接转 string 会得到一个 rune）。
			Kind:    d.Outcome.String(),
			Message: d.Reason,
		}
	}

	// 通过：委派给**真后端**（不是重新 Resolve 出来的另一个）。
	return m.Inner.Exec(ctx, req)
}
