// Package cmdpolicy 命令级判据（机制面）。
//
// 生命周期: Experimental（v0.33.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 它补的是判定面的**宽度**，不是深度
//
// 本仓的判定链已经覆盖「工具调用」这一层（policy / tooldisclose / watchdog），
// 但工具**内部**执行的那条命令没有任何机制面看得见：`rm -rf`、`curl | sh`、
// 带凭据的推送——宿主只能靠正则黑名单，而黑名单在「写法与语义不一致」时必然失效
// （`rm  -rf`、`r""m`、`$(echo cm0=)…` 都能绕）。
//
// 本包把这一层收成三件事：**前缀规则**、**三态裁决**、**规则自带样例**。
//
// # 为什么是「前缀规则」而不是正则
//
// 命令的语义由**前几个 token** 决定：`git push` 与 `git status` 是两条完全不同的
// 风险，而它们的区别就在第二个 token。前缀匹配：
//
//   - 与正则不同，它**不可能因为引号/转义/空白而漂移**（先分词再比 token）；
//   - 判据可解释（人能一眼看出这条规则管什么），于是拒绝理由能直接回给模型；
//   - 每段可以是**备选集合**，表达 `git (push|commit)` 而不写正则。
//
// # 三态裁决与既有词汇对齐
//
// `allow` / `prompt` / `forbidden` 与 `toolgate/policy` 的裁决词表同义，不引入第二套
// 口径：`prompt` 直接接 `gatecheck`（「需人审」优先于其他拒绝理由）。
//
// # 规则自带样例，加载即自检
//
// 每条规则可以自带 `Match`（必须命中本规则）与 `NotMatch`（必须不命中本规则）。
// `New` 在**加载期**逐条跑一遍，不一致即报错——于是「这条规则还活着吗」不是
// 事故后才回答的问题，而是启动时的断言。
//
// 这正是本仓「从没见过它触发的规则等于没有规则」那条纪律的机器化：
// 规则会随包名/命令名漂移，而漂移**不会报错**，只会静静地不再命中。
//
// # 冲突口径：更具体者胜，同等具体取更严
//
// 多条规则同时命中时的裁决必须**确定性**，否则同一个命令在不同装配顺序下
// 得到不同结论（这类漂移最难查）。本包的口径：
//
//  1. 前缀**更长**的规则胜（它更具体）；
//  2. 长度相同 ⇒ 取更严的一档（forbidden > prompt > allow）；
//  3. 仍相同 ⇒ 取**名字字典序最小**者，只为了让结果确定（不是有语义偏好）。
package cmdpolicy

import (
	"fmt"
	"slices"
	"strings"

	"github.com/oekit/pkg/errors"
)

// Decision 是一条规则对某个命令的裁决。
type Decision int

const (
	// DecisionAllow 放行。
	DecisionAllow Decision = iota
	// DecisionPrompt 需人审：先问人，不直接拒。
	DecisionPrompt
	// DecisionForbidden 禁止。
	DecisionForbidden
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionPrompt:
		return "prompt"
	case DecisionForbidden:
		return "forbidden"
	default:
		return "unknown"
	}
}

// Prefix 是命令前缀的一段：一个 token 的**备选集合**（空集合非法）。
//
// 例如 `git` 后面要表达 push 或 commit，就写成两段：
// `Prefix{{"git"}, {"push", "commit"}}`。
type Prefix [][]string

// Rule 一条前缀规则。
type Rule struct {
	// Name 规则名（唯一；进报告，宿主用它留痕）。
	Name string
	// Prefix 前缀的各段（每段是备选集合）。
	Prefix Prefix
	// Decision 命中后的裁决。
	Decision Decision
	// Justification 给模型/人看的理由与替代建议（可选）。
	Justification string
	// Match 正例命令：必须在加载期被**本规则**命中。
	Match []string
	// NotMatch 反例命令：必须在加载期被**本规则**判不中。
	NotMatch []string
}

// Verdict 一次判定的结论。
type Verdict struct {
	// Decision 裁决。
	Decision Decision
	// Rule 命中的规则名（未命中任何规则时为空）。
	Rule string
	// Justification 命中规则的理由（未命中时为空）。
	Justification string
	// Matched 命中的 token 数（0 表示没有规则命中）。
	Matched int
	// Wrote 含写重定向（任一非读侧重定向）。写重定向是**语句级事实**，
	// 不在 argv 里——前缀规则结构上看不见它，故它驱动「事实面下限」
	// （见 DecideCommand）。仅由 DecideCommand 填写；Decide(argv) 不知情、恒 false。
	Wrote bool
	// Dynamic 含动态展开（$() / 反引号 / $VAR）。判据只看字面，
	// 展开内容在判定时不可知 ⇒ 同样驱动事实面下限。同上，仅由 DecideCommand 填写。
	Dynamic bool
}

// Policy 一组已自检通过的前缀规则（构造后只读，可并发共享）。
type Policy struct {
	rules []compiled
}

type compiled struct {
	rule   Rule
	tokens [][]string
}

// New 构造判据并在**加载期**逐条自检。
//
// 自检项（任一不满足即返回错误，不静默降级）：
//
//   - 规则名非空且唯一；
//   - Prefix 非空、每段非空；
//   - Decision 在词表内；
//   - 每条 Match 样例都真的被本规则命中；
//   - 每条 NotMatch 样例都真的不被本规则命中。
func New(rules []Rule) (*Policy, error) {
	seen := make(map[string]struct{}, len(rules))
	out := make([]compiled, 0, len(rules))
	for _, r := range rules {
		if strings.TrimSpace(r.Name) == "" {
			return nil, errors.New("cmdpolicy: 规则名不得为空")
		}
		if _, dup := seen[r.Name]; dup {
			return nil, fmt.Errorf("cmdpolicy: 规则名重复 %q（重名会让留痕无法定位到规则）", r.Name)
		}
		seen[r.Name] = struct{}{}
		if r.Decision < DecisionAllow || r.Decision > DecisionForbidden {
			return nil, fmt.Errorf("cmdpolicy: 规则 %q 的裁决 %d 不在词表内", r.Name, r.Decision)
		}
		if len(r.Prefix) == 0 {
			return nil, fmt.Errorf("cmdpolicy: 规则 %q 的前缀为空（会匹配一切命令，必是配置写错）", r.Name)
		}
		for i, seg := range r.Prefix {
			if len(seg) == 0 {
				return nil, fmt.Errorf("cmdpolicy: 规则 %q 第 %d 段没有备选（空段会让规则永不命中）", r.Name, i)
			}
			for _, tok := range seg {
				if tok == "" {
					return nil, fmt.Errorf("cmdpolicy: 规则 %q 第 %d 段含空 token", r.Name, i)
				}
			}
		}
		out = append(out, compiled{rule: r, tokens: r.Prefix})
	}
	p := &Policy{rules: out}
	if err := p.selfCheckExamples(); err != nil {
		return nil, err
	}
	return p, nil
}

// selfCheckExamples 用**真实判定路径**跑一遍规则自带的样例。
func (p *Policy) selfCheckExamples() error {
	for _, c := range p.rules {
		for _, cmd := range c.rule.Match {
			v, err := p.DecideCommand(cmd)
			if err != nil {
				return err
			}
			if v.Rule != c.rule.Name {
				return fmt.Errorf("cmdpolicy: 规则 %q 的正例 %q 没被它命中（实际命中 %q）", c.rule.Name, cmd, v.Rule)
			}
		}
		for _, cmd := range c.rule.NotMatch {
			v, err := p.DecideCommand(cmd)
			if err != nil {
				return err
			}
			if v.Rule == c.rule.Name {
				return fmt.Errorf("cmdpolicy: 规则 %q 的反例 %q 被它命中了（判据比声明宽）", c.rule.Name, cmd)
			}
		}
	}
	return nil
}

// Decide 按 argv 判定（调用方已分好词时用这个，最省事也最没有歧义）。
func (p *Policy) Decide(argv []string) Verdict {
	var best *compiled
	bestLen := -1
	for i := range p.rules {
		c := &p.rules[i]
		n, ok := matchPrefix(c.tokens, argv)
		if !ok {
			continue
		}
		if best == nil || n > bestLen ||
			(n == bestLen && (c.rule.Decision > best.rule.Decision ||
				(c.rule.Decision == best.rule.Decision && c.rule.Name < best.rule.Name))) {
			best, bestLen = c, n
		}
	}
	if best == nil {
		return Verdict{Decision: DecisionAllow}
	}
	return Verdict{
		Decision:      best.rule.Decision,
		Rule:          best.rule.Name,
		Justification: best.rule.Justification,
		Matched:       bestLen,
	}
}

// DecideCommand 按命令行字符串判定（宿主手上只有命令行时用这个）。
//
// # 判定面是全调用 AST，不只第一个 token
//
// 前缀判据有三类典型绕过，逐类在此闭合（事实面走上游 bashguard，见 facts.go）：
//
//   - **多段**：git status; rm -rf / 的前缀是 git——全调用面把 rm 那段也判了，
//     取最严（SplitCommands 仍保留，供宿主逐段留痕）；
//   - **命令替换**：echo $(rm -rf /) 里的 rm 是嵌套调用——全调用面看得见它；
//   - **写重定向**：git status > /etc/passwd 的前缀仍是 git status，而 > 是
//     语句级事实、不在 argv 里——前缀规则结构上看不见它。
//
// 语义三条：
//
//  1. **全调用面取最严**：管道后半、$(…) 嵌套、子 shell、多段里的每次调用
//     都过一遍前缀规则，取最严者；同等严取名字字典序最小（确定性）。
//  2. **事实面下限**：含写重定向或动态展开（$()/反引号/$VAR）时，裁决
//     **不低于 prompt**——这两类语义前缀规则看不见，静默 allow 就是
//     「判据比声明宽」。调用方若已把命令展开成 argv、且确知无重定向，
//     用 Decide(argv) 即可（那条路径不做事实面判定）。
//  3. **判不了就不放行**：分词失败（未闭合引号 / 尾随转义）与 AST 未评估
//     （超长 / 解析失败）⇒ 返回错误，让调用方按自己的 fail-closed 口径处置，
//     而不是把它当成「没有命中规则」。
func (p *Policy) DecideCommand(cmdline string) (Verdict, error) {
	if _, err := Split(cmdline); err != nil {
		return Verdict{}, err
	}
	facts, err := commandFactsOf(cmdline)
	if err != nil {
		return Verdict{}, err
	}
	v := Verdict{Decision: DecisionAllow}
	for _, argv := range facts.calls {
		got := p.Decide(argv)
		strictest := got.Decision > v.Decision
		if got.Decision == v.Decision && got.Rule != "" && (v.Rule == "" || got.Rule < v.Rule) {
			strictest = true
		}
		if strictest {
			v = got
		}
	}
	v.Wrote = facts.wrote
	v.Dynamic = facts.dynamic
	if (facts.wrote || facts.dynamic) && v.Decision < DecisionPrompt {
		v.Decision = DecisionPrompt
	}
	return v, nil
}

// matchPrefix 返回命中的 token 数。
func matchPrefix(prefix [][]string, argv []string) (int, bool) {
	if len(argv) < len(prefix) {
		return 0, false
	}
	for i, seg := range prefix {
		hit := false
		for _, want := range seg {
			if argv[i] == want {
				hit = true
				break
			}
		}
		if !hit {
			return 0, false
		}
	}
	return len(prefix), true
}

// Rules 返回已加载规则的快照（按名字排序），供宿主自检与报告使用。
func (p *Policy) Rules() []Rule {
	out := make([]Rule, 0, len(p.rules))
	for _, c := range p.rules {
		out = append(out, c.rule)
	}
	slices.SortFunc(out, func(a, b Rule) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Explain 把裁决渲染成一句可回给模型的说明（拒绝/需人审时用）。
//
// 事实面信号（写重定向 / 动态展开）追加在后：它们常常是裁决被下限抬到
// prompt 的**真正原因**，不写出来，模型看到「prompt (rule ...)" 会以为
// 是那条规则判的——下次就学会了「绕开这条规则」而不是「别写重定向」。
func (v Verdict) Explain() string {
	if v.Rule == "" && !v.Wrote && !v.Dynamic {
		return "no rule matched"
	}
	msg := v.Decision.String()
	if v.Rule != "" {
		msg += fmt.Sprintf(" (rule %q)", v.Rule)
		if v.Justification != "" {
			msg += ": " + v.Justification
		}
	} else {
		msg += " (no rule matched)"
	}
	if v.Wrote {
		msg += "；含写重定向"
	}
	if v.Dynamic {
		msg += "；含动态展开（判定只看字面）"
	}
	return msg
}
