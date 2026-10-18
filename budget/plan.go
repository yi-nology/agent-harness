package budget

import (
	"fmt"

	"github.com/oekit/pkg/errors"
)

// 压实下界（拒绝「看起来能跑、实际没干活」的操作）。
//
// 三条都是**空操作守卫**：压实在边界上会退化成什么都不做，而「什么都不做却报成功」
// 比报错更糟——上下文照旧超限，宿主还以为已经处置过了。
const (
	// ForceKeepItems 强制保留尾部条数（压实的**设计基线**）。
	//
	// 数值取自参考实现：强制压实时至少逐字留下这么多条。内核取这个值不是因为
	// 「6」有魔法，而是因为它同时是**第一条守卫的推导来源**——见 MinEffectiveItems。
	ForceKeepItems = 6

	// MinSummarizeItems 最小的待摘要批量。摘要的价值在**跨条目归纳**，
	// 只摘要 1 条拿不到归纳，只会产出一段更长的复述。
	MinSummarizeItems = 2
)

// MinEffectiveItems 压实所需的最少有效条目数 = ForceKeepItems + MinSummarizeItems。
// 对齐参考实现的 MIN_EFFECTIVE_MESSAGES = FORCE_KEEP_MESSAGES + 2。
//
// # 为什么阈值必须挂在保留尾部基线上，而不是一个独立常量
//
// 参考实现没有取「至少 2 条」这种独立常量，而是由**强制保留的尾部条数**加上待摘要
// 下限推出来。这不是写法风格，而是防绕过：独立常量下，调用方把 keepTail 调成 1 就能
// 把守卫拆掉——3 条历史压成 1 条摘要，看起来是在处置，实际只是把内容换了个说法再赔上
// 一份摘要的 token。阈值挂在保留尾部基线上，把 keepTail 调小**不会**让门槛跟着降，
// 守卫就再也绕不过去。
//
// # 为什么不是「按调用方的 keepTail 派生」
//
// 刻意**不**收调用方的 keepTail 参数：门槛一旦跟着调用方的 keepTail 走（哪怕取下限），
// 「过了门槛」就恒等价于「cutoff >= 2」，下面两条守卫会变成**数学上不可达的死代码**——
// 而留着死守卫等于给「越界不会被发现」一个假的安全感。参考实现同样把两者分开：门槛取
// 模块常量 FORCE_KEEP_MESSAGES，而 cutoff 取当次实际算出的位置（自动压实会按比例取更
// 大的保留量），正因如此 cutoff<=0 与「待摘要不足 2 条」才仍是**活的**兜底。
//
// 这里的 + MinSummarizeItems 也正是这两条活守卫能存在的余地。
const MinEffectiveItems = ForceKeepItems + MinSummarizeItems

// 压实拒绝原因。调用方应按 errors.Is 判定并把 Detail 呈给用户，
// 而不是把它当布尔吞掉——「为什么不压实」和「压实了」是两种不同的现场。
var (
	// ErrTooFewItems 有效条目数不足，连一次压实都撑不起来。
	ErrTooFewItems = errors.New("压实拒绝：有效条目数低于最小压实体量")

	// ErrCutoffTooLow 截断下标非正，即保留的尾部会吞掉整段会话。
	ErrCutoffTooLow = errors.New("压实拒绝：截断下标非正，保留尾部会吞掉整段会话")

	// ErrTooFewToSummarize 待摘要条目数不足最小摘要批量。
	ErrTooFewToSummarize = errors.New("压实拒绝：待摘要条目数低于最小摘要批量")
)

// PlanInput 压实计划入参。
type PlanInput struct {
	// Items 会话段当前条目数（逐条可寻址的单位：一条消息 / 一次工具往返）。
	Items int
	// KeepTail 尾部逐字保留条数；负值按 0 视作「不保留尾部」。
	KeepTail int
	// ItemTokens 单条估算 token（用于估 GrossRemovedTokens）；<=0 则不估。
	ItemTokens int
}

// Plan 压实计划：保留尾部逐字内容、领先条目进摘要，并给出压实前移除量的估算。
//
// 计划是**纯计算产物**，不含内容也不做 I/O：宿主拿它决定要不要压实、
// 按什么次序执行（先归档后摘要，见 Apply）。
type Plan struct {
	// Items 计划基于的条目总数（宿主据此判计划是否已过期）。
	Items int
	// Cutoff 截断下标：下标 < Cutoff 的条目进摘要，[Cutoff, Items) 逐字保留。
	Cutoff int
	// SummarizeItems 进摘要的条目数（等于 Cutoff）。
	SummarizeItems int
	// KeepTailItems 逐字保留的条目数（等于 Items-Cutoff）。
	KeepTailItems int
	// GrossRemovedTokens 压实时被逐字移除的领先条目估算 token；无 ItemTokens 时为 0。
	//
	// 字段名里的 Gross 是**硬要求**，不是修饰词：参考实现里同名的 removed_tokens 是
	// **净值** max(0, 移除量 - 摘要量)，而这里是扣掉摘要之前的**毛移除量**。同名不同义，
	// 宿主按名字对齐两份实现时会静默错账——账面凭空少掉一份摘要的量，且方向恰好是
	// 「看起来更省了」。要净额请用 NetRemovedTokens，不要改名字求和。
	GrossRemovedTokens int
}

// BuildPlan 由会话段条目数与保留尾部算出压实计划。纯函数。
//
// # 三条空操作守卫（按序判定，各自可独立触发）
//
//  1. ErrTooFewItems：有效条目数 < MinEffectiveItems。整段没到能压的体量。
//     阈值挂在保留尾部基线上，见该常量注释——这是「不能被 keepTail 绕过」的那一条。
//  2. ErrCutoffTooLow：截断下标 <= 0。保留的尾部吃掉整段，压实后仍是满的，
//     只是把「超时」换成了「更慢地超时」。
//  3. ErrTooFewToSummarize：待摘要条目数 < MinSummarizeItems。宁可不动，
//     也不做「摘要 1 条」这种看起来在工作、实则是复述的假处置。
//
// # 为什么返回原因而不是 bool
//
// 三条守卫都指向同一个表面症状（压不下去了），处置却不同：等一轮能压、
// 调小保留尾部、还是这条会话已经不值得压。布尔会把三种病因压成一种。
func BuildPlan(in PlanInput) (Plan, error) {
	keep := in.KeepTail
	if keep < 0 {
		keep = 0
	}
	need := MinEffectiveItems
	if in.Items < need {
		return Plan{}, fmt.Errorf("%w: items=%d < %d (keep_tail=%d)", ErrTooFewItems, in.Items, need, keep)
	}
	cutoff := in.Items - keep
	if cutoff <= 0 {
		return Plan{}, fmt.Errorf("%w: items=%d keep_tail=%d → cutoff=%d",
			ErrCutoffTooLow, in.Items, keep, cutoff)
	}
	if cutoff < MinSummarizeItems {
		return Plan{}, fmt.Errorf("%w: summarize=%d < %d",
			ErrTooFewToSummarize, cutoff, MinSummarizeItems)
	}
	p := Plan{
		Items:          in.Items,
		Cutoff:         cutoff,
		SummarizeItems: cutoff,
		KeepTailItems:  in.Items - cutoff,
	}
	if in.ItemTokens > 0 {
		p.GrossRemovedTokens = cutoff * in.ItemTokens
	}
	return p, nil
}
