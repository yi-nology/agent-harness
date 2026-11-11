// Package preset 命名权限预设：把「隔离档位 × 审批模式 × 出口策略」三件散参数
// 收成一份**具名、不可变、可自检**的整份配置。
//
// # 它补的是「零件齐了但组装件没有」
//
// 上一轮之后，本仓三面的零件都在：`sandbox.Level`（执行能触达什么）、
// `policy.Mode`（什么时候必须问人）、`egress.Spec`（出站能到哪）。但它们是
// **三个各自独立的参数**，每一次执行都由宿主自己拼。
//
// 拼装这件事，错了不会报错：
//
//   - 档位配松了（要求 `LevelProcess` 却传了零值）——沙箱层不构成约束，
//     执行照常放行，而调用点看起来「声明了档位」；
//   - 出口配漏了（新加的调用点忘了带 `Egress`）——零值即「不要求」，
//     于是这次执行自由出网，而报告里一切正常；
//   - 两个参数配成互不相容的组合（要求禁网但选了个进程内后端）——
//     没有任何一处会对此说一句话。
//
// 三种错的共同形态是**声明与意图不一致而无人察觉**。这正是「拼装必然漂移」的
// 含义：不是宿主不认真，是**没有一个地方能把这份组合一次性判对**。
//
// # 为什么是「整份」而不是「可变字段」
//
// 沿用 `netguard` / `guardset.Assembly` 的纪律：**换整份，不改其中一项**。
// 改其中一项会让「当前生效的是哪套权限」出现两个真相——装配期读到的与
// 执行期用到的可能是两份。`Set` 构造后只读，`Preset` 是值类型，
// 于是「这一份」在传递过程中不会被人偷偷换掉其中一维。
//
// # 自检项：加载期就把配歪的预设拒掉
//
// `New` 逐条校验（任一不满足即返回错误，不静默降级）：
//
//   - 名字非空、无空白、唯一（名字进留痕与配置文件，带空白会在两处各自 trim 后撞名）；
//   - 档位与审批模式在各自词表内（拼错即拒，不退回缺省档）；
//   - 出口声明自身可兑现（`egress.Spec.Validate`）；
//   - **三面全松必须显式承认**：档位 `LevelNone` + 模式 `ModeFull` + 出口不限，
//     三个都是最松的一档时必须写 `Unsafe: true` 与理由；
//   - **语义相同的两个名字要报错**：同一份组合挂两个名字，等于给「看起来有选择」
//     的假象，而留痕里会出现两个名字指向同一档——之后谁改了一个、谁没改，无从分辨。
//
// # 未知名字即错误，不降级到缺省
//
// `Resolve` 遇到没登记的名字返回错误。若降级到某个缺省预设，方向是**不可预测**的：
// 宿主本想用最严的那档却打错了字，得到缺省（较松）的那档，于是这次执行比意图更松，
// 而没有任何信号。这与本仓「沉默要落在安全的那一侧」是同一条纪律——
// 但这里「安全的那一侧」是**拒绝**，因为降级本身不构成一个明确的意图。
package preset

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/oekit/pkg/errors"
	"github.com/oekit/pkg/slicex"

	"github.com/yi-nology/goagent-kernel/egress"
	"github.com/yi-nology/goagent-kernel/gatecheck"
	"github.com/yi-nology/goagent-kernel/policy"
	"github.com/yi-nology/goagent-kernel/sandbox"
)

// Preset 一份具名权限预设。
//
// 三个字段合起来才是「这次执行被允许做什么」的完整回答；缺任何一维都会让
// 剩下的两维看起来比实际更严（那是假的安全感，比没有更糟）。
type Preset struct {
	// Name 预设名（进留痕；非空、无空白、在集合内唯一）。
	Name string
	// Level 执行隔离的**下限**档位（要求，不是承诺的实际上限）。
	Level sandbox.Level
	// Mode 审批模式。
	Mode policy.Mode
	// Egress 出口策略。
	Egress egress.Spec
	// Unsafe 显式承认「三面全松」这类组合。
	//
	// 它不是一个开关，而是一次**签字**：只有把三个最松的档放在一起时才必填，
	// 且必须写理由。目的是让「反正先跑起来」这个决定留下痕迹，
	// 而不是在半年后靠 git blame 去猜。
	Unsafe bool
	// Justification 为什么是这个组合（`Unsafe` 时必填）。
	Justification string
}

// Set 一组已自检通过的预设（构造后只读，可并发共享）。
type Set struct {
	presets []Preset
	index   map[string]int
}

// New 构造预设集合并逐条自检。
func New(presets []Preset) (*Set, error) {
	if len(presets) == 0 {
		return nil, errors.New("preset: 预设集合为空（宿主至少要给出一份可用配置）")
	}
	seen := make(map[string]struct{}, len(presets))
	keys := make(map[string]string, len(presets))
	out := make([]Preset, 0, len(presets))
	index := make(map[string]int, len(presets))
	for _, p := range presets {
		if err := p.validate(); err != nil {
			return nil, err
		}
		if _, dup := seen[p.Name]; dup {
			return nil, errors.Errorf("preset: 预设名重复 %q（留痕无法定位到具体那一份）", p.Name)
		}
		k := p.Key()
		// 语义相同即撞名：判定按「组合」比，不按「名字」比。
		//
		// 两个名字一份组合在配置里看起来是「两个选择」，而宿主挑哪个都一样；
		// 真正的伤害在留痕：报告里出现两个名字，读者会以为执行过两种权限。
		if other, dup := keys[k]; dup {
			return nil, errors.Errorf("preset: %q 与 %q 的语义完全相同（%s）——两个名字一份组合会让留痕出现两个名字指同一档",
				p.Name, other, k)
		}
		keys[k] = p.Name
		seen[p.Name] = struct{}{}
		index[p.Name] = len(out)
		out = append(out, p)
	}
	return &Set{presets: out, index: index}, nil
}

// validate 单条预设的加载期自检。
func (p Preset) validate() error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return errors.New("preset: 预设名不得为空")
	}
	if name != p.Name || strings.ContainsAny(p.Name, " \t\r\n") {
		return errors.Errorf("preset: 预设名 %q 含空白或首尾空白（名字进配置文件与留痕，两处各自 trim 后会撞名）", p.Name)
	}
	if p.Level > sandbox.LevelMachine {
		return errors.Errorf("preset: 预设 %q 的档位 %d 不在词表内（LevelNone..LevelMachine）", p.Name, p.Level)
	}
	if !policy.ValidMode(string(p.Mode)) {
		return errors.Errorf("preset: 预设 %q 的审批模式 %q 不在词表内", p.Name, string(p.Mode))
	}
	if err := p.Egress.Validate(); err != nil {
		return errors.Wrap(err, "preset: 预设 "+p.Name+" 的出口策略不可兑现")
	}
	allLoose := p.Level == sandbox.LevelNone &&
		p.Mode == policy.ModeFull &&
		p.Egress.Mode == egress.ModeUnrestricted
	if allLoose && !p.Unsafe {
		return errors.Errorf(
			"preset: 预设 %q 三面全松（档位 none × 模式 full × 出口不限）却没有 Unsafe 标记——"+
				"这类组合必须显式承认并写明理由，否则它就是「反正先跑起来」留下的、没人认领的松口", p.Name)
	}
	if p.Unsafe && strings.TrimSpace(p.Justification) == "" {
		return errors.Errorf("preset: 预设 %q 标了 Unsafe 却没写理由（签字不带理由是空签字）", p.Name)
	}
	return nil
}

// Key 预设的语义键：只由三维组成，与名字无关。
//
// 它同时是「换整份」的判定依据——两份预设 `Key()` 相同即**就是同一份权限**，
// 无论叫什么名字。
func (p Preset) Key() string {
	return fmt.Sprintf("level=%s mode=%s egress=%s",
		p.Level.String(), string(p.Mode), p.Egress.Mode.String())
}

// String 面向人的一句话（进报告与日志）。
func (p Preset) String() string {
	s := fmt.Sprintf("%s{%s", p.Name, p.Key())
	if p.Unsafe {
		s += ", unsafe"
	}
	return s + "}"
}

// Resolve 按名字取预设；未登记即报错（**不降级到缺省**）。
func (s *Set) Resolve(name string) (Preset, error) {
	if s == nil {
		return Preset{}, errors.New("preset: 集合未装配（nil）")
	}
	i, ok := s.index[name]
	if !ok {
		return Preset{}, errors.Errorf("preset: 未登记的预设名 %q（已登记：%s）", name, strings.Join(s.Names(), " / "))
	}
	return s.presets[i], nil
}

// Names 已登记的预设名（字典序，供报告与报错信息用）。
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	out := slicex.Map(s.presets, func(p Preset) string { return p.Name })
	sort.Strings(out)
	return out
}

// All 预设快照（按名字排序，改它不影响集合）。
func (s *Set) All() []Preset {
	if s == nil {
		return nil
	}
	out := slices.Clone(s.presets)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Len 预设数量。
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.presets)
}

// GateRequest 按预设名补齐一次联合判定的输入。
//
// 三个字段一次性写全（档位 / 出口 / 审批模式），调用方不再有机会只补其中一两个——
// 那正是「拼装漂移」的入口。`base` 的其余字段（操作类型、工具名、风险、程序体）
// 原样保留：那些是这次**调用**的事实，不是**权限**的声明。
func (s *Set) GateRequest(base gatecheck.Request, name string) (gatecheck.Request, error) {
	p, err := s.Resolve(name)
	if err != nil {
		return gatecheck.Request{}, err
	}
	base.MinLevel = p.Level
	base.Egress = p.Egress
	base.Op.Mode = p.Mode
	return base, nil
}
