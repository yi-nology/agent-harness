// Package gatecheck 审批层与沙箱层的**联合判据**。
//
// # 它补的是哪一段
//
// Codex 的安全模型是**两层**：沙箱强制（技术上能做什么）+ 审批策略
// （什么时候必须问人），且**两层都同意才放行**——
// 「沙箱技术上允许，但审批策略仍可要求确认」。
//
// 本仓这两层分别由两个上游模块提供，各自都很完整：
//
//   - `guardx/sandbox`：`Registry.Resolve(min Level, budget...)`——
//     档位 × 预算的联合判据，**选不到满足者即拒绝，绝不降级**；
//   - `toolgate/policy`：`Gate.Decide(ctx, op)`——
//     审批裁决（auto / need_human / deny / plan_only）。
//
// 但它们**互不知情**（policy 不 import sandbox，反之亦然），
// 于是「两层是否都同意」这个判定**没有单点**——它落在每个宿主手里。
//
// 而这条规则若由各宿主各接一遍，必然漂移：同一个调用在 A 宿主被判
// 「需人审」、在 B 宿主被判「自动放行」，两边都没写错，
// 只是各自选了不同的合成规则。这与本仓反复强调的
// 「判定口径必须跨宿主一致」是同一条纪律。
//
// # 本包做什么、不做什么
//
// **做**：把两层的结论合成**一条**判定，并给出**可诊断**的拒绝原因。
//
// **不做**：不重新实现任何一层的判定（那是上游的事），
// 也不新增审批模式或档位（那会造成与上游并存的第二份真相）。
//
// # 合成规则：任一层不通过即不通过（fail-closed）
//
// | 审批层 | 沙箱层 | 结论 |
// |---|---|---|
// | 放行（auto） | 选得到满足要求的后端 | **放行** |
// | 放行（auto） | 选不到（档位不够 / 预算兜不住） | **拒绝**（档位不足） |
// | 需人审（need_human） | 任意 | **需人审** |
// | 需人审（need_human） | 选不到 | **需人审**（人审之前不必先判档位） |
// | 拒绝（deny） | 任意 | **拒绝**（审批已否，不必再看档位） |
// | 计划模式（plan_only） | 任意 | **只审不执行** |
//
// # 为什么「审批需人审」优先于「档位不足」
//
// 两条拒绝对宿主的**处置完全不同**：
//   - 档位不足 ⇒ 宿主该**换环境**（要求更弱的档位，或换到更强的后端）；
//   - 需人审 ⇒ 宿主该**去问人**。
//
// 若两者同时成立而先报「档位不足」，宿主会去找后端而不是找人——
// 于是这次调用既没执行也没人管，**静默丢失**。
// 故「需人审」优先：**有人在场**是更强的信号。
//
// 而「审批已否」（deny）优先于一切：审批已经否了，
// 再去讨论档位毫无意义（档位再高也不该执行）。
//
// # 计划模式（plan_only）为什么单列
//
// 计划模式是「只审不执行」——它**不是**拒绝，而是一种**不同的处置**：
// 宿主该去出方案给人看，而不是「重试」或「换环境」。
// 把它算成拒绝会让宿主以为这次调用失败了。
package gatecheck

import (
	"context"
	"fmt"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/toolgate/policy"
)

// Outcome 联合判定的结论。
type Outcome int

const (
	// OutcomeProceed 两层都同意，可以执行。
	OutcomeProceed Outcome = iota
	// OutcomeNeedHuman 需要人工确认。
	OutcomeNeedHuman
	// OutcomeDenied 审批层已拒绝。
	OutcomeDenied
	// OutcomeLevelShort 沙箱层选不到满足要求的后端（档位不足 / 预算兜不住）。
	OutcomeLevelShort
	// OutcomePlanOnly 计划模式：只审不执行。
	OutcomePlanOnly
	// OutcomeExecutionFailed 两层都同意、但执行本身失败。
	//
	// 它**独立**于前面的几态：不是「不该执行」而是「执行了但没成」——
	// 宿主的处置是「看错误决定要不要重试」，与「需人审」完全不同。
	OutcomeExecutionFailed
)

func (o Outcome) String() string {
	switch o {
	case OutcomeProceed:
		return "proceed"
	case OutcomeNeedHuman:
		return "need_human"
	case OutcomeDenied:
		return "denied"
	case OutcomeLevelShort:
		return "level_short"
	case OutcomePlanOnly:
		return "plan_only"
	case OutcomeExecutionFailed:
		return "execution_failed"
	default:
		return "unknown"
	}
}

// Decision 联合判定的结论与依据。
type Decision struct {
	// Outcome 结论（见上）。
	Outcome Outcome
	// Provider 选中的执行后端（仅 OutcomeProceed 时非 nil）。
	Provider sandbox.Provider
	// PolicyVerdict 审批层的原始裁决（宿主可据此展示「为什么」）。
	PolicyVerdict policy.Verdict
	// PolicyReason 审批层的理由。
	PolicyReason string
	// Level 要求的最弱档位（诊断用：宿主能直接告诉用户「要什么档位」）。
	Level sandbox.Level
	// Reason 面向人的判定理由。
	Reason string
}

// Request 一次联合判定的输入。
type Request struct {
	// Op 审批层要判的操作。
	Op policy.Op
	// MinLevel 要求的最弱隔离档位。
	//
	// 零值（LevelNone）表示不要求隔离强度——那沙箱层不构成约束。
	MinLevel sandbox.Level
	// Budget 资源预算要求（墙钟 / 调用数 / 输出字节 / 内存上限）。
	//
	// 零值表示不要求任何预算上限。
	Budget sandbox.Budget
	// Program 要执行的程序体（语义由后端定，见 sandbox.Provider）。
	//
	// 只有 ExecuteAndRun 用它；纯判定（Check）不看它。
	Program string
}

// Check 做一次联合判定。
//
// gate 为 nil 时**按需人审处理**（fail-closed）：
// 没装审批门就放行，等于「没人看着的调用看起来能用」——
// 与 guardset「空集合拒绝构造」同一条纪律。
//
// registry 为 nil 时沙箱层**不构成约束**（跳过档位判定）：
// 「不要求隔离强度」与「没装沙箱」是两件事——前者是意图，后者是缺失。
// 但只要 MinLevel 非零（真要求了隔离强度）而 registry 缺失，
// 那是**要求兑现不了**⇒ 判 OutcomeLevelShort（fail-closed）。
//
// # 判据顺序是有意的
//
// 1. 审批先判：审批已否 ⇒ 直接否（档位再高也不该执行）；
// 2. 计划模式 ⇒ 只审不执行（不是失败）；
// 3. 需人审 ⇒ 人审（人比档位更该先管）；
// 4. 最后才判档位：只有前三条都放行，才问「档位够不够」。
func Check(ctx context.Context, gate *policy.Gate, registry *sandbox.Registry, req Request) Decision {
	d := Decision{Level: req.MinLevel}

	// ① 审批层。
	if gate == nil {
		// 没装审批门 ⇒ 没有人看着 ⇒ 按需人审处理。
		d.Outcome = OutcomeNeedHuman
		d.PolicyVerdict = policy.VerdictNeedHuman
		d.Reason = "未装配审批门，按需人审处理（fail-closed）"
		return d
	}
	verdict := gate.Decide(ctx, req.Op)
	d.PolicyVerdict = verdict.Verdict
	d.PolicyReason = verdict.Reason

	switch verdict.Verdict {
	case policy.VerdictDeny:
		d.Outcome = OutcomeDenied
		d.Reason = "审批层拒绝：" + verdict.Reason
		return d
	case policy.VerdictPlanOnly:
		d.Outcome = OutcomePlanOnly
		d.Reason = "计划模式：只审不执行"
		return d
	case policy.VerdictNeedHuman, policy.VerdictUnknown:
		// 需人审（unknown 是灰区待仲裁，fail-safe 也升人审）。
		d.Outcome = OutcomeNeedHuman
		d.Reason = "需要人工确认：" + verdict.Reason
		return d
	}

	// ② 沙箱层：只有审批放行了才轮到它。
	if req.MinLevel == sandbox.LevelNone {
		// 不要求隔离强度 ⇒ 沙箱层不构成约束，放行。
		d.Outcome = OutcomeProceed
		d.Reason = "审批放行，且未要求隔离强度"
		return d
	}
	if registry == nil {
		// 要求了隔离强度却没有登记簿 ⇒ 兑现不了 ⇒ 拒绝（fail-closed）。
		d.Outcome = OutcomeLevelShort
		d.Reason = fmt.Sprintf("要求隔离档位 %s，但没有可用的执行后端登记簿", req.MinLevel)
		return d
	}
	provider, err := registry.Resolve(req.MinLevel, req.Budget)
	if err != nil {
		d.Outcome = OutcomeLevelShort
		d.Reason = fmt.Sprintf("选不到满足档位 %s 的执行后端：%v", req.MinLevel, err)
		return d
	}
	d.Outcome = OutcomeProceed
	d.Provider = provider
	d.Reason = "两层都同意"
	return d
}

// Guard 是「审批门 + 执行后端登记簿」的组合件：宿主装配一次，之后每次执行
// 走同一套联合判据。
//
// # 为什么需要它（而不是让宿主每次手写 Check + ExecuteAndRun）
//
// 装配是**宿主的事**（上游 sandbox 明确「本包只认协议，不认装配」），
// 但**装配的形态若不固定，各宿主就会各接一遍**——于是同一条联合判据
// 在 A 宿主是「审批→档位→执行」，在 B 宿主是「档位→审批→执行」，
// 合成规则的次序就漂移了（这正是本包要消灭的东西）。
//
// Guard 把「次序」固化在**一处**，宿主只提供零件：
//
//	g := gatecheck.NewGuard(gate, registry)
//	out, dec := g.Run(ctx, gatecheck.Request{...})
//
// # 零值不可用
//
// 零值 Guard（门与登记簿都 nil）⇒ 审批缺失 ⇒ 按需人审（fail-closed），
// 与 `Check` 的语义一致——不会因为忘了装配就放行。
type Guard struct {
	gate     *policy.Gate
	registry *sandbox.Registry
}

// NewGuard 组装组合件。gate 为 nil 时按需人审（fail-closed）；
// registry 为 nil 时「要求了档位但兑现不了」⇒ 拒绝。
func NewGuard(gate *policy.Gate, registry *sandbox.Registry) *Guard {
	return &Guard{gate: gate, registry: registry}
}

// Check 只判定，不执行（与包级 Check 同语义）。
func (g *Guard) Check(ctx context.Context, req Request) Decision {
	if g == nil {
		return Check(ctx, nil, nil, req)
	}
	return Check(ctx, g.gate, g.registry, req)
}

// Run 判定并执行（与包级 ExecuteAndRun 同语义）。
func (g *Guard) Run(ctx context.Context, req Request) (sandbox.Outcome, Decision) {
	if g == nil {
		return ExecuteAndRun(ctx, nil, nil, req)
	}
	return ExecuteAndRun(ctx, g.gate, g.registry, req)
}

// ExecuteAndRun 做一次联合判定，**通过则就地执行**。
//
// # 为什么不能只有 Check
//
// `Check` 判完把后端放进 `Decision.Provider`，**但不执行**。于是调用方得自己
// 拿那个 provider 去 `Exec`——中间任何一步走错（拿了别的 provider、
// 重新 Resolve 了一次、抄错了字段），就产生「**判过的后端 ≠ 实际跑的后端**」
// 的落差。而「声明与实际之间没有信号」正是本包要消灭的东西：
// 判据一旦与执行分离，它就退化成一个可绕过的建议。
//
// 故这里把两者**焊在一起**：判定通过就用**判出来的那一个** provider 执行，
// 调用方没有「换一个后端跑」的机会。
//
// # 与上游 sandbox.Execute 的分工
//
// 上游 `sandbox.Execute(ctx, reg, min, req)` = `Resolve` + `Exec`，
// **只管档位×预算**。本包管的是**审批 × 档位**的联合：
// 审批那一层上游没有（`toolgate/policy` 与 `guardx/sandbox` 互不知情），
// 而本包在**执行前**把两层都判完。
//
// 重复的部分（Resolve）是有意的：它让「判出来的 provider」与「跑起来的
// provider」是**同一个**，而不是两次独立解析的结果。
//
// # 审批判定在执行前，失败即不执行
//
// 审批层判 need_human / deny / plan_only 时**根本不执行**——
// 尤其 deny：执行了再判「已拒绝」是没有意义的（副作用已经发生）。
func ExecuteAndRun(ctx context.Context, gate *policy.Gate, registry *sandbox.Registry, req Request) (sandbox.Outcome, Decision) {
	d := Check(ctx, gate, registry, req)
	if d.Outcome != OutcomeProceed {
		return sandbox.Outcome{}, d
	}
	// 用**判出来的那个** provider 执行——不给调用方换后端的机会。
	out, err := d.Provider.Exec(ctx, sandbox.Request{
		Program: req.Program,
		Budget:  req.Budget,
	})
	if err != nil {
		d.Outcome = OutcomeExecutionFailed
		d.Reason = "执行失败：" + err.Error()
		return sandbox.Outcome{}, d
	}
	return out, d
}
