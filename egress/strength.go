package egress

import (
	"github.com/oekit/pkg/errors"
)

// Strength 兑现强度：**声明**与**实际**之间的差距用它表达。
//
// # 为什么需要这一层
//
// 本包的判据有两个入口，而它们的强度天然不同：
//
//   - Verify(env)：看实际下发给子进程的环境变量。环境变量是**建议**——
//     静态链接的二进制、UDP、原始 socket 都绕得过去。故它兑现出来的
//     永远是 StrengthAdvisory（建议型），**不是** OS 级隔离。
//   - SatisfiedVerdict(provider)：问后端「你能强制哪一档」。后端做出
//     能力声明（Enforcer）时，兑现面在**执行环境**里 ⇒ StrengthEnforced。
//
// 此前这两个入口只返回 error（成立/不成立），调用方拿不到「以什么强度成立」。
// 于是「代理档已兑现」这句话很容易被读成「出站已被强制隔离」——
// 而这正是评估里点出的误读：**声明强度 ≠ 实际强度**，两者之间必须有信号。
//
// # 三个取值
//
//   - StrengthNotRequired：未要求限制（ModeUnrestricted）。无从兑现，也无需兑现；
//   - StrengthAdvisory：建议型（环境变量等）。可被绕过；
//   - StrengthEnforced：强制型（执行环境层面强制）。
//
// 判据的方向是**保守**：拿不到强制证据时，最强只能说 advisory。
// 这与本仓「沉默按最弱处理」同源——没拿到强制证据 ≠ 已强制。
type Strength int

const (
	// StrengthNotRequired 未要求限制，无从兑现。
	StrengthNotRequired Strength = iota
	// StrengthAdvisory 建议型兑现（环境变量等建议手段），可被绕过。
	StrengthAdvisory
	// StrengthEnforced 强制型兑现（执行环境层面强制）。
	StrengthEnforced
)

// String 人类可读的强度名（诊断与日志面，不参与判定）。
func (s Strength) String() string {
	switch s {
	case StrengthNotRequired:
		return "not_required"
	case StrengthAdvisory:
		return "advisory"
	case StrengthEnforced:
		return "enforced"
	default:
		return "unknown"
	}
}

// Verdict 一条兑现结论：兑现了没有，以什么强度兑现的。
//
// 为什么要有它：只有「成立/不成立」二值时，调用方无法区分
// 「后端在执行环境里强制了代理」与「只是把代理变量塞进了环境」。
// 而这两种在**威胁模型**里差着一整个量级。
type Verdict struct {
	// Mode 本次判定的声明档位。
	Mode Mode
	// Strength 兑现强度。
	Strength Strength
	// Detail 人可读的说明（为什么是这个强度）。
	//
	// 它给的是**差距**：调用方拿这条结论去做风险判断时，看到的不只是
	// 「代理已就位」，还有「这只是建议型」。
	Detail string
}

// VerifyVerdict 判「实际下发的 env 是否兑现了声明」，并给出兑现强度。
//
// 与 Verify 的差别只有一个：**多给了强度**。判定口径完全相同
// （同一份实现，见 VerifyVerdict 内部），故两者的结论不可能打架。
//
// 强度口径：环境变量兑现出来的**永远是建议型**——环境变量拦不住
// 静态二进制、UDP 与原始 socket。这一点不是判断，是事实。
func (s Spec) VerifyVerdict(env map[string]string) (Verdict, error) {
	if err := s.Validate(); err != nil {
		return Verdict{Mode: s.Mode}, err
	}
	switch s.Mode {
	case ModeUnrestricted:
		return Verdict{
			Mode:     s.Mode,
			Strength: StrengthNotRequired,
			Detail:   "未要求出站限制，无从兑现也无需兑现",
		}, nil
	case ModeDenied:
		// 环境变量兑现不了「禁网」——不伪造「看起来禁了」的变量。
		return Verdict{Mode: s.Mode}, errEnvCannotEnforce
	}
	if err := s.verifyBypass(env); err != nil {
		return Verdict{Mode: s.Mode}, err
	}
	if err := s.verifyProxyVars(env); err != nil {
		return Verdict{Mode: s.Mode}, err
	}
	return Verdict{
		Mode:     s.Mode,
		Strength: StrengthAdvisory,
		Detail: "代理变量已就位：建议型兑现——静态链接二进制、UDP 与原始 socket 仍可绕过；" +
			"需要 OS 级强制隔离时，由执行后端负责（后端能力声明 ⇒ 强制型）",
	}, nil
}

// SatisfiedVerdict 判「后端能否兑现本声明」，并给出兑现强度。
//
// 与 SatisfiedBy 的差别只有一个：**多给了强度**。判定口径完全相同。
//
// 强度口径：后端做出 Enforcer 能力声明 ⇒ 强制型（兑现面在执行环境里）；
// 未实现 Enforcer 的后端一律判**不满足**（沉默按最弱处理，见 SatisfiedBy）。
func (s Spec) SatisfiedVerdict(provider any) (Verdict, error) {
	if err := s.Validate(); err != nil {
		return Verdict{Mode: s.Mode}, err
	}
	if s.Mode == ModeUnrestricted {
		return Verdict{
			Mode:     s.Mode,
			Strength: StrengthNotRequired,
			Detail:   "未要求出站限制",
		}, nil
	}
	enf, ok := provider.(Enforcer)
	if !ok {
		return Verdict{Mode: s.Mode}, errors.Errorf("egress: 后端未声明出口能力，无法兑现 %s（沉默按最弱处理）", s.Mode)
	}
	got := enf.Egress()
	if !got.Satisfies(s.Mode) {
		return Verdict{Mode: s.Mode}, errors.Errorf("egress: 后端只能兑现 %s，低于要求的 %s", got, s.Mode)
	}
	return Verdict{
		Mode:     s.Mode,
		Strength: StrengthEnforced,
		Detail:   "后端声明可在执行环境层面强制该档（Enforcer 能力声明）",
	}, nil
}
