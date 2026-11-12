package egress

import (
	"fmt"
	"github.com/oekit/pkg/errors"
)

// Strength 兑现强度：**实际兑现**出来的强度。
//
// # 为什么需要这一层
//
// 本包的判据有两个入口，而它们的强度天然不同：
//
//   - Verify(env)：看实际下发给子进程的环境变量。环境变量是**建议**——
//     静态链接的二进制、UDP、原始 socket 都绕得过去。故它兑现出来的
//     永远是 StrengthAdvisory（建议型），**不是** OS 级隔离。
//   - SatisfiedVerdict(provider)：问后端「你能强制哪一档」。
//
// # 自报 ≠ 实测（v0.33.0 起拆开）
//
// 后端说「我能强制哪一档」（Enforcer.Egress()）是**自报**：它陈述的是
// 自己的能力，不是对当前这次执行的观测。把它直接判成 StrengthEnforced，
// 等于让「声明强度」冒充「实际强度」——这正是评估里点出的误读：
// 「代理档已兑现」很容易被读成「出站已被强制隔离」，而这两件事在
// **威胁模型**里差着一整个量级。
//
// 故本包把两者分成两个取值：
//
//   - StrengthDeclared：后端**自报**能力，未经实测；
//   - StrengthEnforced：**实测**过（EgressVerifier 提供了实测证据）。
//
// 而「实测证据」必须由后端**真的去探测**得出——本包不代它发请求，
// 也不接受「我保证我拦得住」这种断言当证据。没拿到实测证据时，
// 最强只能说 declared：与本仓「沉默按最弱处理」同源——
// **没证据 ≠ 已强制**。
//
// # 四个取值（按「我们有多确定」排序）
//
//   - StrengthNotRequired：未要求限制（ModeUnrestricted）。无从兑现，也无需兑现；
//   - StrengthAdvisory：建议型（环境变量等）。可被绕过；
//   - StrengthDeclared：后端自报能强制某档（未经实测）；
//   - StrengthEnforced：强制型，且**有实测证据**。
type Strength int

const (
	// StrengthNotRequired 未要求限制，无从兑现。
	StrengthNotRequired Strength = iota
	// StrengthAdvisory 建议型兑现（环境变量等建议手段），可被绕过。
	StrengthAdvisory
	// StrengthDeclared 后端**自报**能力：它声称能在执行环境层面强制某档，
	// 但**没有实测证据**。
	//
	// 它单独一档，而不是并进 StrengthEnforced：并进去的话，只看 Strength
	// 的读者会把「后端自称」读成「已经强制」——那正是这一档要拆的那件事。
	StrengthDeclared
	// StrengthEnforced 强制型兑现，且**经实测验证**（实测出站确实被挡住）。
	StrengthEnforced
)

// String 人类可读的强度名（诊断与日志面，不参与判定）。
func (s Strength) String() string {
	switch s {
	case StrengthNotRequired:
		return "not_required"
	case StrengthAdvisory:
		return "advisory"
	case StrengthDeclared:
		return "declared"
	case StrengthEnforced:
		return "enforced"
	default:
		return "unknown"
	}
}

// EgressVerifier 执行后端的**实测验证**能力。
//
// # 与 Enforcer 的区别是决定性的
//
//   - Enforcer.Egress() 是**自报**：「我能强制哪一档」；
//   - EgressVerifier.VerifyEgress() 是**实测**：「我刚才真的去试过，
//     确实连不上」。
//
// 自报不等于事实：两者之间必须有信号，否则「声明强度」与「实际强度」
// 之间就没有落差可言——而后端自报恰恰是最容易被读成事实的那种声明。
//
// # 为什么方法不带 ctx
//
// 探测怎么做是**执行后端**的事（它知道在那个环境里怎么试），本包不代它
// 发请求；实现方可以在自己的装配阶段探测一次并缓存结果，再通过本接口
// 把它交出来。判据面因此保持无副作用、可重复。
//
// # 为什么定义在本仓而不是上游
//
// 上游 guardx/sandbox 定了档位词表与能力声明面（Enforcer），但没有
// 「实测证据」这一档。本包是出口**判据**的归属地，故在此定义；
// 上游若日后补上同名能力，本包的别名可逐字替换（调用面不动）。
type EgressVerifier interface {
	// VerifyEgress 返回**实测**得到的出站档位。
	//
	// 实现方负责完成探测（必要时自行缓存结果）。返回值应如实反映
	// 「当前这次执行环境下，出站被挡到了哪一档」——实测不到就返回较低档位，
	// 不要把「我以为挡住了」当成实测结论。
	VerifyEgress() (Mode, error)
}

// Verdict 一条兑现结论：兑现了没有，以什么强度兑现的。
//
// 为什么要有它：只有「成立/不成立」二值时，调用方无法区分
// 「后端在执行环境里强制了代理」与「只是把代理变量塞进了环境」。
// 而这两种在**威胁模型**里差着一整个量级。
type Verdict struct {
	// Mode 本次判定的声明档位。
	Mode Mode
	// Strength 兑现强度（按「我们有多确定」排序）。
	Strength Strength
	// Detail 人可读的说明（为什么是这个强度）。
	//
	// 它给的是**差距**：调用方拿这条结论去做风险判断时，看到的不只是
	// 「代理已就位」，还有「这只是建议型」或「这只是后端自报」。
	Detail string
}

// VerifyVerdict 判「实际下发的 env 是否兑现了声明」，并给出兑现强度。
//
// 与 Verify 的差别只有一个：**多给了强度**。判定口径完全相同
// （同一份实现，见 VerifyVerdict 内部），故两者的结论不可能打架。
//
// 强度口径：环境变量兑现出来的**永远是建议型**——环境变量拦不住
// 静态链接二进制、UDP 与原始 socket。这一点不是判断，是事实。
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
			"需要 OS 级强制隔离时，由执行后端负责（后端能力声明 ⇒ declared；实测通过 ⇒ enforced）",
	}, nil
}

// SatisfiedVerdict 判「后端能否兑现本声明」，并给出兑现强度。
//
// 与 SatisfiedBy 的差别只有一个：**多给了强度**。判定口径完全相同。
//
// # 强度口径（v0.33.0 起：自报与实测分开）
//
//   - 后端实现了 EgressVerifier（能**实测**）→ 实测档位达标 ⇒
//     StrengthEnforced；
//   - 后端只实现了 Enforcer（**自报**能力）→ 声明档位达标 ⇒
//     StrengthDeclared（**不是** enforced：自报不是实测）；
//   - 两者都没实现 → 判**不满足**（沉默按最弱处理，见 SatisfiedBy）。
//
// 另有一条例外：同一个后端若**同时**实现了两个接口，以**实测**为准——
// 实测证据优先于自报声明（证据强度不同级时，取强的那个）。
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

	// 实测证据优先于自报声明。
	if v, ok := provider.(EgressVerifier); ok {
		got, err := v.VerifyEgress()
		if err != nil {
			return Verdict{Mode: s.Mode}, errors.Wrap(err, "egress: 后端实测出站隔离失败")
		}
		if !got.Satisfies(s.Mode) {
			return Verdict{Mode: s.Mode}, fmt.Errorf("egress: 后端实测只能兑现 %s，低于要求的 %s", got, s.Mode)
		}
		return Verdict{
			Mode:     s.Mode,
			Strength: StrengthEnforced,
			Detail:   "后端实测通过：出站确已被强制隔离（实测证据，不是自报）",
		}, nil
	}

	enf, ok := provider.(Enforcer)
	if !ok {
		return Verdict{Mode: s.Mode}, fmt.Errorf("egress: 后端未声明出口能力，无法兑现 %s（沉默按最弱处理）", s.Mode)
	}
	got := enf.Egress()
	if !got.Satisfies(s.Mode) {
		return Verdict{Mode: s.Mode}, fmt.Errorf("egress: 后端只能兑现 %s，低于要求的 %s", got, s.Mode)
	}
	return Verdict{
		Mode:     s.Mode,
		Strength: StrengthDeclared,
		Detail: "后端自报可在执行环境层面强制该档（Enforcer 能力声明）——这是**自报**，不是实测；" +
			"要断言真的被强制，需后端提供实测证据（EgressVerifier）",
	}, nil
}
