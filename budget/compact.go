package budget

import (
	"context"
	"fmt"

	"github.com/oekit/pkg/errors"
)

// Item 会话段条目引用：内核只认**标识与规模**，不携带内容。
//
// 内容归宿主（必要时凭 Locator 取回）。内核搬运内容就等于替宿主选定了存储形态，
// 那正是本仓不做的事——与 contract.Fields 开放键、checkpoint.Fields 同一条纪律。
type Item struct {
	// ID 条目标识（宿主分配，须能回到原始内容）。
	ID string
	// Tokens 该条估算 token。
	Tokens int
}

// Summary 摘要结果。Tokens 由宿主如实填写：内核只把它加回账，不核实、不修正。
type Summary struct {
	Text   string
	Tokens int
}

// Locator 归档定位符（取回句柄）。格式归宿主，内核只当不透明串传递。
type Locator string

// Summarizer 摘要生成出口（宿主实现）。
//
// 内核**不调 LLM**：「编排是代码，LLM 只做单环节」——摘要是一次 LLM 调用，
// 属于模型面，不属于编排面。内核给计划、宿主给摘要，接口边界即纪律边界。
type Summarizer interface {
	// Summarize 把领先条目压成一条摘要。leading 已由 Apply 校验过条数与计划一致。
	Summarize(ctx context.Context, leading []Item) (Summary, error)
}

// Archive 原文归档出口（宿主实现）。
//
// 压实是不可逆的逐字丢弃，所以原文必须先落地：摘要是**有损**的，事后发现摘要
// 丢了关键信息时，唯一能救回来的地方是这份归档。内核不碰存储，只定义这次接缝。
type Archive interface {
	// Archive 落盘压实前的原文并返回取回句柄。
	Archive(ctx context.Context, items []Item) (Locator, error)
}

// SummaryItemID 压实后摘要条目在会话段中的 ID。
const SummaryItemID = "summary"

// 压实执行期的拒绝原因。
var (
	// ErrPlanMismatch 入参条目与计划对不上（计划已过期或调用方算错了下标）。
	// 宁可不压：按过期计划摘要，会把该保留的尾部内容一起吞掉。
	ErrPlanMismatch = errors.New("压实拒绝：入参条目与计划不符（计划已失效）")

	// ErrNotWired 执行压实所需接口未装配。
	ErrNotWired = errors.New("压实拒绝：归档 / 摘要接口未装配")
)

// Result 压实执行结果。
type Result struct {
	// Segment 压实后的会话段：一条摘要 + 逐字保留的尾部。
	Segment []Item
	// Summary 摘要原文（宿主决定是否回填到展示层）。
	Summary Summary
	// Locator 原文归档句柄（供后续取回）。
	Locator Locator
	// GrossRemovedTokens 本次逐字移除的估算量（取自计划），**未扣掉摘要**。
	//
	// 与参考实现的 removed_tokens（净值）同名不同义，务必走 NetRemovedTokens 取净额。
	GrossRemovedTokens int
}

// NetRemovedTokens 压实的**净**移除量 = max(0, 毛移除量 - 摘要量)，对齐参考实现的
// removed_tokens 口径。
//
// # 为什么这个换算是显式函数而不是字段上的减法
//
// 宿主在拿到 Result 之前**没有**摘要量（摘要由宿主自己生成），所以毛移除量与净移除量
// 不可能同时存在于结构体里。把换算写成一个函数，是为了让「毛」与「净」在类型和字段名
// 上就分得开——同名字段分处两地时，按名字映射两份实现的宿主会静默错账，方向恰好是
// 「看起来多省了一份摘要」，而多省出来的量根本不存在。
//
// 钳位在 0：摘要比被移除的原文还长时，净移除量是 0 而不是负数。负数会让调用方
// 反向加账（凭空给上下文添 token），而压实本来只可能让上下文变小。
func NetRemovedTokens(gross, summaryTokens int) int {
	if n := gross - summaryTokens; n > 0 {
		return n
	}
	return 0
}

// SnapshotDelta 内存快照的**带符号修正量**（加到已记录的会话段 token 上）。
//
// 要「从快照里减掉多少」请直接用 NetRemovedTokens —— 本方法给的是带符号值，
// 摘要比原文还长时为正（表示要加回去），方向与「压实」直觉相反，容易用错。
// 压实后的正确用法：m.Shrink(段, NetRemovedTokens(res.GrossRemovedTokens, res.Summary.Tokens))。
func (r Result) SnapshotDelta() int {
	return r.Summary.Tokens - r.GrossRemovedTokens
}

// Apply 按计划执行压实：先归档原文，再生成摘要，最后拼出压实后的会话段。
//
// # 顺序是刻意定的：归档必须先于摘要
//
// 摘要失败（超时、限流、模型返回空）时原文已经落盘，可以直接重试；反序则可能出现
// 「摘要已丢弃原文、摘要本身又没拿到」的净损失。不可逆操作之后的那一步，必须最便宜。
//
// # 内核做什么、不做什么
//
// 编排（校验 → 归档 → 摘要 → 拼接 → 给修正量）是代码，所以归内核；
// 生成摘要与落盘是能力，所以归宿主（Summarizer / Archive）。本函数里没有任何
// 模型调用与文件操作，这正是本包存在的意义。
func Apply(ctx context.Context, p Plan, leading, tail []Item, arc Archive, sum Summarizer) (Result, error) {
	if arc == nil || sum == nil {
		return Result{}, ErrNotWired
	}
	// 计划是一张对下标的承诺：入参条数与计划不符说明计划已过期。
	if len(leading) != p.SummarizeItems || len(tail) != p.KeepTailItems {
		return Result{}, fmt.Errorf("%w: plan(items=%d summarize=%d keep_tail=%d) vs 入参(leading=%d tail=%d)",
			ErrPlanMismatch, p.Items, p.SummarizeItems, p.KeepTailItems, len(leading), len(tail))
	}

	loc, err := arc.Archive(ctx, leading)
	if err != nil {
		return Result{}, fmt.Errorf("原文归档失败（压实中止，原文未丢弃）: %w", err)
	}
	sm, err := sum.Summarize(ctx, leading)
	if err != nil {
		// 原文已归档，此处不回收：调用方直接重试即可，重跑归档是幂等的。
		return Result{Locator: loc}, fmt.Errorf("摘要生成失败（原文已归档于 %q，可直接重试）: %w", loc, err)
	}

	seg := make([]Item, 0, 1+len(tail))
	seg = append(seg, Item{ID: SummaryItemID, Tokens: sm.Tokens})
	seg = append(seg, tail...) // 尾部逐字保留：新切片，不与入参共享底层数组
	return Result{
		Segment:            seg,
		Summary:            sm,
		Locator:            loc,
		GrossRemovedTokens: p.GrossRemovedTokens,
	}, nil
}
