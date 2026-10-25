// Package budget 上下文预算：输入侧的**分段计量**、窗口上限计算与压实计划骨架。
//
// # 为什么需要这个包
//
// 本仓已有 spill，但它管的是**输出**超预算（生成结果太长怎么办）。输入侧此前完全
// 缺位：下一轮要重发的会话历史、系统提示、工具面各占多少，没有任何一个包回答。
// 缺的不是「一个总数」，而是三件分别的事：
//
//  1. **分段**：只给总数时超预算就只有一个动作——砍历史。而工具面膨胀、规则膨胀
//     与历史膨胀是三种不同的病，处置完全不同。
//  2. **上限**：窗口不是全归输入的，输出得先占位，否则下一次请求生下来就已截断。
//  3. **处置**：到了上限要做什么。本包只给**压实**（保留语义、丢弃逐字）的计划
//     骨架，摘要怎么生成、原文往哪落盘各留一个接口由宿主实现。
//
// # 纪律
//
// 内核**不调 LLM、不碰存储、不猜 tokenizer**：本包只做确定性计量与计划，
// 摘要生成走 Summarizer、原文归档走 Archive，宿主实现。
//
// 与 spill 的分工：spill 管输出溢出后的截断与取回，本包管输入面计量与压实，
// 语义不同不互相替代——本包不提供任何溢出文本的落盘能力。
//
// # 压实之后怎么把账改回来
//
// 压实改写了会话段，但**没有产生模型调用**，所以下一轮到来之前没有任何新报数可依。
// 三步走完整闭环，全部在本包内：
//
//	m := budget.Meter{}; m.Add(budget.SegmentConversation, 60000)
//	p, _ := budget.BuildPlan(budget.PlanInput{Items: 10, KeepTail: 4, ItemTokens: 2500})
//	res, _ := budget.Apply(ctx, p, leading, tail, arc, sum)
//	m.Shrink(budget.SegmentConversation,
//	    budget.NetRemovedTokens(res.GrossRemovedTokens, res.Summary.Tokens))
//
// 毛移除量与净移除量必须分开取（NetRemovedTokens 有专门注释）：按名字对齐参考实现的
// 同名字段会静默错账，方向恰好是「看起来更省了」。
//
// # 一条不可让步的度量纪律
//
// 分段估算**永不回填** provider 报数。理由：provider 报的是总数，回填到分段就等于
// 让 tokenizer 误差披上分段精确度的外衣——UI 上「工具面 3.2k」看起来是真的，
// 实际是「总数减其余各段」的残差。压实计划恰恰要按段分配裁剪，喂残差等于喂假账。
package budget

import (
	"sort"
	"sync"
)

// Segment 上下文分段种类。
//
// 取值是 string 而非闭合枚举：**宿主自定义分段直接用自己的键**即可，内核不解释、
// 不校验、不假设它们存在（与 contract.Fields 的开放键同口径）。
// 下列常量是内核认得的**规范分段**——内核对它们的处理是通用的，但给名字便于
// 不同宿主对齐口径，也便于测试夹具对齐语义。
type Segment string

const (
	// SegmentSystem 系统提示段（每轮固定重发）。
	SegmentSystem Segment = "system"
	// SegmentTools 工具定义面（本轮实际可调用的工具声明）。
	SegmentTools Segment = "tools"
	// SegmentRules 规则 / 约束段（每轮注入的行为约束）。
	SegmentRules Segment = "rules"
	// SegmentSkills 技能说明段。
	SegmentSkills Segment = "skills"
	// SegmentMCP MCP 服务面（远端工具服务的能力声明）。
	SegmentMCP Segment = "mcp"
	// SegmentConversation 会话历史段（唯一随轮次持续增长的段）。
	SegmentConversation Segment = "conversation"
)

// canonicalSegments 规范分段的固定上报顺序：定性面在前、随轮增长的会话段在后。
// 固定顺序只为让报告能稳定 diff（跨宿主对齐时「顺序变了」不该被误读成「量变了」）。
var canonicalSegments = []Segment{
	SegmentSystem, SegmentTools, SegmentRules,
	SegmentSkills, SegmentMCP, SegmentConversation,
}

// SegmentUsage 单段计量结果。
type SegmentUsage struct {
	Segment Segment
	Tokens  int
}

// Meter 分段计量器。零值可用，并发安全（编排可能从多个环节同时计量）。
type Meter struct {
	mu  sync.Mutex
	seg map[Segment]int
}

// Add 累加一段的估算 token。
//
// 负值按 0 计入：估算器给出负数是**估算侧的 bug**，把它原样累加等于允许一次
// 负计量悄悄抹掉别段的量，而总数偏低时没人看得出。宁可少算也不要假装精确。
//
// 空段名不做特殊处理：照样计量、照样出现在报告里。总数因此永远不会「少算」，
// 一个无名分段出现在报告尾部是诚实的信号，吞掉它才是假的。
func (m *Meter) Add(seg Segment, tokens int) {
	if tokens < 0 {
		tokens = 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seg == nil {
		m.seg = make(map[Segment]int, len(canonicalSegments))
	}
	m.seg[seg] += tokens
}

// Shrink 从一段**就地减去** tokens 个 token，返回实际扣掉的量。对齐参考实现的
// shrink_context_usage：压实改写了下一轮提示但**没有产生新的模型调用**，所以在下一
// 轮到来之前不会有任何新报数回来，持有快照的一方若不减，账面会一直停在压实前的旧值。
//
// # 为什么必须存在这条路径
//
// 压实计划给出的是净移除量（见 NetRemovedTokens），没有对应的减法入口就等于让调用方
// 把减法做在别处：或另记一份账（两份真相必然漂移），或干脆不减（快照虚高，且虚高的
// 方向恰好让界面显示「还在紧张」，指向一个已经不存在的现场）。
//
// # 钳位与可观测性
//
// 单段扣减按**该段实有量**钳位，不会倒扣到别的段：把会话段压过头不应该连带抹掉
// 系统提示段的计量。返回的**实际**扣减量因此可能小于入参——返回值 < 入参即为钳位，
// 调用方据此能分辨「账上就这么多」与「真扣了这么多」，而不用自己去比 Total。
//
// 非正入参是空操作并返回 0：扣除负数等于反向加账。
func (m *Meter) Shrink(seg Segment, tokens int) int {
	if tokens <= 0 {
		return 0 // 非正入参：空操作，不反向加账
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	have := m.seg[seg]
	if have <= 0 {
		return 0 // 该段本就没有量：无可扣，也不制造负值
	}
	removed := min(tokens, have) // 钳位到本段实有量，不外溢到别的段
	m.seg[seg] = have - removed
	return removed
}

// Tokens 单段当前值；未计量过返回 0。
func (m *Meter) Tokens(seg Segment) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seg[seg]
}

// Total 各段求和（内核唯一的输入真相）。
func (m *Meter) Total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int
	for _, v := range m.seg {
		n += v
	}
	return n
}

// Segments 按固定顺序列出：规范分段在前，未知（宿主）分段按名字排序在后。
//
// 未计量过时返回的是**规范分段的零值列表**（六条 Tokens=0），不是空切片——
// 调用方遍历不必判空，且「这一段存在但量为 0」本身就是一句诚实的陈述。
// 快照同理（见 Snapshot）：规范段永远出现，未计量就是 0。
func (m *Meter) Segments() []SegmentUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SegmentUsage, 0, len(m.seg))
	seen := make(map[Segment]bool, len(canonicalSegments))
	for _, s := range canonicalSegments {
		seen[s] = true
		out = append(out, SegmentUsage{Segment: s, Tokens: m.seg[s]})
	}
	extra := make([]Segment, 0, len(m.seg))
	for s := range m.seg {
		if !seen[s] {
			extra = append(extra, s)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, s := range extra {
		out = append(out, SegmentUsage{Segment: s, Tokens: m.seg[s]})
	}
	return out
}

// Reset 清零（新一轮计量）。保留 map 复用——计量每轮都调，不必每次重新分配。
func (m *Meter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for s := range m.seg {
		m.seg[s] = 0
	}
}

// Display 展示口径。
//
// 结构上只有 Input 与 Cap 两个来源字段，Used 由二者派生而**不可直接赋值**——
// 这样「used = min(input, cap)」就不可能与 input/cap 对不上。
type Display struct {
	// Input 未钳位的输入量（分段估算或 provider 报数，取决于哪个 Display）。
	Input int
	// Cap 展示用上限；<=0 表示**未知上限**，此时 Used 直接返回 Input。
	Cap int
}

// Used 展示值 = min(Input, Cap)。未设上限时返回 Input。
func (d Display) Used() int {
	if d.Cap <= 0 {
		return d.Input
	}
	return min(d.Input, d.Cap)
}

// Overflow 输入是否已越界（仅在设了上限时有意义）。
func (d Display) Overflow() bool {
	return d.Cap > 0 && d.Input > d.Cap
}

// Report 一次计量报告：分段估算、provider 报数、展示口径三者**分列**。
//
// 刻意不给「用 provider 报数覆盖 Input」的入口：分段估算一旦被回填，
// 压实计划按段分配裁剪就建立在残差上（见包注释的度量纪律）。
type Report struct {
	// Segments 分段明细，规范分段在前。
	Segments []SegmentUsage
	// Input 各段求和。
	Input int
	// Cap 展示用上限（见 MaxInput）；<=0 = 未知上限。
	Cap int
	// Provider provider 自报的输入总量；<=0 表示本次无报数。
	Provider int
}

// Snapshot 取报告快照。cap 传 0 表示「本轮没有窗口约束」，展示口径按未知上限处理。
func (m *Meter) Snapshot(cap int) Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	rep := Report{Input: 0, Cap: cap}
	seen := make(map[Segment]bool, len(canonicalSegments))
	for _, s := range canonicalSegments {
		seen[s] = true
		rep.Segments = append(rep.Segments, SegmentUsage{Segment: s, Tokens: m.seg[s]})
		rep.Input += m.seg[s]
	}
	extra := make([]Segment, 0, len(m.seg))
	for s := range m.seg {
		if !seen[s] {
			extra = append(extra, s)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, s := range extra {
		rep.Segments = append(rep.Segments, SegmentUsage{Segment: s, Tokens: m.seg[s]})
		rep.Input += m.seg[s]
	}
	return rep
}

// Display 分段估算的展示口径（钳位在显示层做，估算值原样保留）。
func (r Report) Display() Display { return Display{Input: r.Input, Cap: r.Cap} }

// ProviderDisplay provider 报数的展示口径；本次无报数时退回分段估算口径，
// 免得 UI 在无报数时显示一个凭空而来的 0。
func (r Report) ProviderDisplay() Display {
	if r.Provider <= 0 {
		return r.Display()
	}
	return Display{Input: r.Provider, Cap: r.Cap}
}

// Drift provider 报数与分段估算之差（正 = 估算偏低）。只用于**观测**估算器的偏差，
// 不参与任何裁剪决策。
func (r Report) Drift() int {
	if r.Provider <= 0 {
		return 0
	}
	return r.Provider - r.Input
}
