// Package guardset 守卫装配契约：一组**相互独立**的判定被一并求值，拒绝被合成一条。
//
// # 为什么需要它
//
// 本仓的守卫（tooldisclose 的披露门、netguard 的出站守卫、policy 的审计门、
// pii 的脱敏面、toolfail 的失败契约）此前是并列的独立机制，接进执行链的活儿
// 全推给宿主。后果不是「少个功能」，而是**每个宿主各写一遍组合逻辑**，而组合
// 逻辑里恰好藏着三个必须全局一致的判定：
//
//	① 顺序：这些守卫谁先说话；
//	② 冲突：一次调用被多个守卫拒时，以谁的结论为准；
//	③ 合成：多条拒绝怎么变成模型看得懂的一句话。
//
// 这三件事与「用哪个守卫」无关，只与「守卫怎么被装配」有关——正是内核该定死的部分。
// 各自实现的结果不是风格不统一，是行为漂移：同一个调用在 A 宿主被报成「不可重试」，
// 在 B 宿主被报成「可重试」，两边都没写错，只是各自选了不同的合成规则。
//
// # 与 invoke.Chain 的关系：这是组合，不是替代
//
// oagentkit/toolgate 的 invoke.Chain 已经提供了顺序执行、首个拒绝即短路、留痕与
// panic 兜底。本包**不重复其中任何一条**，而是补它按设计不覆盖的那一面：
//
//	invoke.Chain 的语义是「排成一队逐个过，第一个拒就收口」——顺序即语义，
//	但这意味着**后面的守卫永远没机会说话**。模型一次只听得到第一个理由，
//	于是它改掉那个理由、重试、再撞上下一个，如此往复若干轮。
//
//	本包的语义是「一并求值，把全部拒绝合成一条」——只适用于那些
//	**纯判定、无副作用、彼此独立**的守卫（它们互不依赖对方的结论，
//	所以并行/一并求值不改变任何单独一条的判定结果）。
//
// 二者叠加而非取代：guardset.Set 把自己接成 invoke.Chain 上的**一环**
// （见 stage.go），链照旧短路，只是这一环内部已经把该说的都说了。
// 需要严格先后（前一环的结论是后一环的输入）时，仍应直接用 invoke.Chain 排多个 Stage。
//
// # 关键取舍一：不可重试优先（fail-closed）
//
// 合成多条拒绝时的重试结论是「**全部可重试**才算可重试」，不是「任一可重试」。
//
// 理由：只要有一条拒绝是不可恢复的，重试整个调用就绝无可能成功。
// 取「任一」的话，一个永久性拒绝会被一条无关的可重试拒绝「洗白」成可重试，
// 退避层于是把一次策略否定拖成无穷重试——这正是本仓反复拒绝的那类失效
// （invoke.Chain 里认不出的否定一律不可重试，是同一条纪律的另一个落点）。
//
// # 关键取舍二：空集合构造失败，不构造一个看起来能用的东西
//
// 没有守卫的集合等于没人看着这次调用。与其让它在运行期放行或静默拒绝，
// 不如在装配期直接失败——与 invoke.NewChain 对空执行体的处置同源：
// 一个「空转但看起来能用」的守卫集合，比构造失败危险得多。
//
// # 边界
//
// 本包只管**组合语义**：顺序、冲突裁决、合成。它不认识任何具体守卫，
// 也不认识任何具体工具——守卫从哪来、判什么，全部由宿主装配时注入。
package guardset

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/oagentkit/toolgate/invoke"
	"github.com/oekit/pkg/errors"
)

// 装配期与运行期的哨兵。用 oekit/pkg/errors（与全家族一致），调用方用 errors.Is 判定。
var (
	// ErrEmptySet 空守卫集合。构造期即失败：没人看着的调用不该看起来能用。
	ErrEmptySet = errors.New("guardset: 守卫集合为空")

	// ErrUnnamedGuard 守卫缺名字。留痕要能回答「是哪一环拒的」，空名等于没有答案。
	ErrUnnamedGuard = errors.New("guardset: 守卫未命名")

	// ErrDuplicateGuard 守卫重名。重名会让同一个答案有两个候选，留痕与指引同时失去指向。
	ErrDuplicateGuard = errors.New("guardset: 守卫重名")

	// ErrUnassembled 未经构造的 Set 被拿来求值（运行期落点的 fail-closed）。
	ErrUnassembled = errors.New("guardset: 守卫集合未装配")
)

// Denial 一条拒绝。
//
// 三字段与 invoke.Verdict 的拒绝面一一对应，不引入新概念：
// 多存下来的只是**是哪一环拒的**，而那正是留痕要回答的第一个问题。
type Denial struct {
	// Guard 拒的那一环的名字。
	Guard string
	// Reason 该环给出的依据。
	Reason string
	// Retryable 该环认为值不值得重试。
	Retryable bool
}

// Outcome 一组守卫对一次调用的合并结论。
type Outcome struct {
	// Denials 全部拒绝，按优先级升序、同级按装配序（稳定）。
	// 放行为空；有任何拒绝则非空。
	Denials []Denial

	// Primary 留痕用的主拒绝：优先级最高的那条（Denials[0]）。放行为零值。
	Primary Denial

	// Allowed 是否全部放行。
	Allowed bool

	// Retryable 合成后的重试结论。语义见包注释「关键取舍一」：
	// 只有**全部**拒绝都可重试时才为真。
	Retryable bool
}

// Reason 合并后的留痕依据：把全部拒绝按序拼成一行。
//
// 为什么不用 Primary 的理由单独留痕：只记一条会让审计误以为「就这一处有问题」，
// 而模型拿到完整清单后可能只改掉了优先级最高的那一条——下一轮又拒，
// 排查的人却在留痕里看不到第二处曾经也拒过。
func (o Outcome) Reason() string {
	if o.Allowed {
		return ""
	}
	parts := make([]string, 0, len(o.Denials))
	for _, d := range o.Denials {
		// 去掉结尾标点再拼：各环的理由本身常以句号收尾，直接用分号连接会
		// 产出「……。；……」这种双标点——在审计留痕里长期占位，没人看但也没人清。
		parts = append(parts, "["+d.Guard+"] "+strings.TrimRight(d.Reason, "。；;，,"))
	}
	return strings.Join(parts, "；")
}

// Guidance 合并后面向模型的指引：逐条列出未通过的检查，并点明该先处理哪一条。
//
// 为什么要单独一个面：留痕是给人看的（要全、要准），提示词是给模型看的
// （要能照着做）。用同一段文字服务两头，结果是留痕太啰嗦而提示词抓不住重点。
//
// 指向「先处理哪一条」用的是不可重试优先——那一条才是真正卡住的地方，
// 让模型先去修一条修得动、但修完仍然过不去的检查，是白跑一轮。
func (o Outcome) Guidance() string {
	if o.Allowed {
		return ""
	}
	var b strings.Builder
	b.WriteString("该调用未通过 ")
	b.WriteString(strconv.Itoa(len(o.Denials)))
	b.WriteString(" 项检查：")
	for i, d := range o.Denials {
		b.WriteString("\n")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(") ")
		b.WriteString(d.Reason)
	}
	// 指出真正卡住的那一条：不可重试的拒绝里优先级最高的一条。
	for i, d := range o.Denials {
		if !d.Retryable {
			b.WriteString("\n其中第 ")
			b.WriteString(strconv.Itoa(i + 1))
			b.WriteString(" 项无法通过重试解决，请优先处理它。")
			break
		}
	}
	if o.Retryable {
		b.WriteString("\n全部检查都可通过重试恢复，请修正后重新调用。")
	}
	return b.String()
}

// Guard 一项带优先级的独立判定。
//
// 之所以是「独立」：Check 不得依赖其他 Guard 的结论，否则一并求值就改变了
// 单条判定的语义，本包的整个前提（合成不改变任何一条的结论）随之失效。
// 需要有先后的守卫请直接排进 invoke.Chain，不要塞进一个 Set 里。
type Guard struct {
	// Name 该环名字，进留痕与模型指引。不得为空、不得重名（见 New）。
	Name string

	// Rank 优先级，**小者先说话**。Rank 相同的按装配序（稳定）。
	//
	// 为什么要显式声明而不是靠装配顺序：装配顺序是「怎么写的」，Rank 是
	// 「什么意思」。把语义藏在切片下标里，读代码的人得数到第几个才知道谁说了算，
	// 而插入一个新守卫就会把所有人的次序悄悄改掉。
	Rank int

	// Check 判定函数。为 nil 视为该环坏了，按拒绝收口（fail-closed）。
	Check func(ctx context.Context, c invoke.Call) invoke.Verdict
}

// Set 一组独立守卫，合成一次判定。
type Set struct {
	guards []Guard
}

// New 构造守卫集合。校验三条装配期的硬约束（都在这里失败，而不是留到运行期）：
//
//   - 空集合拒绝构造：没人看着的调用不该看起来能用（包注释「关键取舍二」）；
//   - 名字不得为空：留痕要能回答「是哪一环拒的」，空名等于这个问题没有答案；
//   - 名字不得重复：重名会让同一个答案有两个候选，审计与指引同时失去指向。
//
// 构造成功后内部顺序按 (Rank, 装配序) 固定下来，此后 Set 只读，可并发共享。
func New(guards ...Guard) (*Set, error) {
	if len(guards) == 0 {
		return nil, ErrEmptySet
	}
	seen := make(map[string]bool, len(guards))
	for _, g := range guards {
		if strings.TrimSpace(g.Name) == "" {
			return nil, ErrUnnamedGuard
		}
		if seen[g.Name] {
			return nil, ErrDuplicateGuard
		}
		seen[g.Name] = true
	}
	ordered := make([]Guard, len(guards))
	copy(ordered, guards)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Rank < ordered[j].Rank })
	return &Set{guards: ordered}, nil
}

// Guards 返回已装配的守卫名（按优先级序，只读快照，供自检与调试）。
func (s *Set) Guards() []string {
	out := make([]string, len(s.guards))
	for i, g := range s.guards {
		out[i] = g.Name
	}
	return out
}

// Evaluate 一并求值全部守卫，合成一份结论。
//
// 三条求值纪律：
//   - **不短路**：每条拒绝都要拿到，否则模型只听得到第一个理由；
//   - **单项坏了按拒绝收口**（fail-closed）：判定函数报错或 panic 一律计入拒绝，
//     而不是当成「这环管不着」放行——后者会让一次故障的门禁静默变成不设防；
//   - **合成取保守侧**：全部可重试才算可重试（包注释「关键取舍一」）。
func (s *Set) Evaluate(ctx context.Context, c invoke.Call) Outcome {
	if s == nil {
		// 未经构造的 Set 等于没装配守卫。按拒绝而非放行。
		return Outcome{
			Denials:   []Denial{{Guard: stageUnassembled, Reason: ErrUnassembled.Error()}},
			Primary:   Denial{Guard: stageUnassembled, Reason: ErrUnassembled.Error()},
			Allowed:   false,
			Retryable: false,
		}
	}
	var denials []Denial
	for _, g := range s.guards {
		v, ok := runGuard(ctx, g, c)
		if v.Allow && ok {
			continue
		}
		if !ok {
			// 该环自身出问题：不可恢复（重试同样的坏环节没有意义），
			// 且必须与「正常判定得出的拒绝」区分开——两者对处置完全不同。
			denials = append(denials, Denial{
				Guard: g.Name, Reason: v.Reason, Retryable: false,
			})
			continue
		}
		denials = append(denials, Denial{
			Guard: g.Name, Reason: v.Reason, Retryable: v.Retryable,
		})
	}
	if len(denials) == 0 {
		return Outcome{Allowed: true}
	}
	retryable := true
	for _, d := range denials {
		if !d.Retryable {
			retryable = false
			break
		}
	}
	return Outcome{
		Denials:   denials,
		Primary:   denials[0],
		Allowed:   false,
		Retryable: retryable,
	}
}

// stageUnassembled 未装配集合时的虚拟环名（留痕里不能是空串）。
const stageUnassembled = "unassembled"

// runGuard 跑一环，把 panic 与返回错误都收成一条不可重试的拒绝。
//
// recover 在这里而不是散落各处，与 invoke.runStage 同一理由：守卫一旦被搬出
// 主流程（后台服务、worker、子进程），原本包在宿主进程里的 recover 就不在了。
func runGuard(ctx context.Context, g Guard, c invoke.Call) (v invoke.Verdict, ok bool) {
	if g.Check == nil {
		return invoke.Verdict{
			Allow:  false,
			Reason: "守卫 " + g.Name + " 未装配判定函数",
		}, false
	}
	defer func() {
		if r := recover(); r != nil {
			v = invoke.Verdict{Allow: false, Reason: "守卫 " + g.Name + " panic：" + panicText(r)}
			ok = false
		}
	}()
	v = g.Check(ctx, c)
	// 拒绝却不说理由的守卫无法合成指引：模型会收到一条没有内容的否定。
	if !v.Allow && strings.TrimSpace(v.Reason) == "" {
		v.Reason = "守卫 " + g.Name + " 拒绝且未给出理由"
	}
	return v, true
}

// panicText 把 panic 值转成可读文本（值未必是 error，可能是裸字符串或任意值）。
func panicText(r any) string {
	switch v := r.(type) {
	case error:
		return v.Error()
	case string:
		return v
	default:
		return "未知"
	}
}
