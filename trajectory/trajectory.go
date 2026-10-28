// Package trajectory 事件流 → 轨迹行的投影器（机制面）。
//
// 「这一轮到底发生了什么、每段花了多久」这件事，宿主此前只能自己从事件流里拼：
// 起止时刻靠猜，工具调用与结果按 call_id 手工配对，配对缺了没人知道。各宿主
// 各拼一套，于是同一个问题的答案会漂——尤其是**时长这一个数**：有人拿载荷
// 体积估、有人拿 token 数除、有人按事件时刻相减，三种口径混进同一张表，
// 谁也说不清哪一列是实测、哪一列是猜的。
//
// 本包把这件事收成一条机制面：**吃本仓 eventbus 的事件，产出轨迹行**。
//
// 三条纪律：
//
//  1. **实测与估算不得混同**（本包存在的核心理由）。行的时长**只有一个源**：
//     起止时刻相减。Row 里**不存**时长字段——时长是 Duration() 现算的。
//     于是「按载荷体积估一个时长」这件事**无处安放**：不是靠纪律约束，是
//     结构上装不进去。没有实测值就是没有实测值（Duration 返回 ok=false），
//     不拿 0 冒充「瞬时完成」，也不拿估算值填进实测字段。
//
//  2. **配对不完整要显形**。工具调用与结果按 call_id 配对；只有调用没有结果、
//     结果重复、结果没有对应调用、同号重复声明——四种缺口都进 Issues()，
//     不静默丢弃。
//
//  3. **未完成不是完成**。只有开始没有结束的行，状态是 open（未收口），
//     不是「时长 0 的已完成行」。Finalize() 把「不会再有新事件」落成显式事实，
//     未收口的行原样报出。
//
// 事件流是追加式的，故投影是**增量**的：先喂一半再喂后半，既有行被就地更新
// （补上结束时刻、时长随之变成实测），而不是追加出第二条行。
//
// 边界：本包是机制面，不含任何宿主领域知识；**不做前端渲染**——各宿主技术栈
// 不同，轨迹行是数据不是视图（要文案就用 Options.Summarize 注入）。
package trajectory

import (
	"fmt"
	"sync"
	"time"

	"github.com/yi-nology/agent-harness/eventbus"
)

// Kind 轨迹行的类目。
type Kind string

const (
	// KindTurn 轮次行：一轮 turn 的起止（turn_started / turn_ended）。
	KindTurn Kind = "turn"
	// KindStep 步骤行：一个环节的起止（step_started / step_finished）。
	KindStep Kind = "step"
	// KindToolCall 工具调用行：调用时刻为起，配对到的结果时刻为止。
	KindToolCall Kind = "tool_call"
	// KindToolResult 工具结果行：**只在结果没有对应调用时**出现——
	// 配对缺口要显形，不能因为「没有调用行可挂」就把结果丢掉。
	KindToolResult Kind = "tool_result"
)

// Status 轨迹行的收口状态。
type Status string

const (
	// StatusOpen 已开始、未收口：缺结束事件（可能仍在途，也可能是缺口）。
	StatusOpen Status = "open"
	// StatusClosed 起止齐备：行已收口。
	StatusClosed Status = "closed"
	// StatusOrphan 只有结束事件、没有开始事件：起点缺失，配不出时长。
	StatusOrphan Status = "orphan"
)

// Pairing 工具调用与结果的配对状态（非工具行恒为 PairingNone）。
type Pairing string

const (
	// PairingNone 不适用（非工具行）。
	PairingNone Pairing = ""
	// PairingPaired 调用与结果已配对：起止齐备，时长为实测。
	PairingPaired Pairing = "paired"
	// PairingMissingResult 尚未观察到结果。可能仍在途；投影收口后仍如此，
	// 即确认为「等了也没等到」。
	PairingMissingResult Pairing = "missing_result"
	// PairingDuplicateResult 同一 call_id 收到第二个及以后的结果。
	PairingDuplicateResult Pairing = "duplicate_result"
	// PairingUnpairedResult 结果没有对应的调用。
	PairingUnpairedResult Pairing = "unpaired_result"
	// PairingDuplicateCall 同一 call_id 收到第二个调用声明。
	PairingDuplicateCall Pairing = "duplicate_call"
)

// Row 轨迹行：一轮 turn 里的一段有起止的过程。
//
// 行里**没有**时长字段——时长由 Duration() 从起止时刻现算（见包头纪律 1）。
// 存一个时长字段就会多出第二个真相：事件时刻与存下来的时长各说各话，
// 而估算值也就能混进同一个字段冒充实测。
type Row struct {
	// Seq 行序号（1 起，按创建顺序）：宿主据此排序，投影内稳定。
	Seq int
	// Kind 行类目。
	Kind Kind
	// ID 行标识：轮次行 "turn#1"；带环节号的步骤行 "step#7"；
	// 工具行 "tool#<call_id>"；无自然键的行用 "step#-<seq>" 形式兜底
	// （"-" 前缀表示「这个标识是投影生成的，不是业务自然键」）。
	ID string
	// Turn 所属轮次序号（1 起）；轮次之外的事件为 0。
	Turn int
	// HasStep 是否带环节号（环节号 0 是合法值，故用独立标志位表达「有没有」）。
	HasStep bool
	// StepID 所属环节号（HasStep 为真时有效）。
	StepID uint64
	// CallID 工具调用标识（仅工具行；配对键）。
	CallID string
	// Tool 工具名（仅工具行）。
	Tool string
	// StartAt 起始时刻（取自事件时刻；缺失时零值）。
	StartAt time.Time
	// EndAt 结束时刻（取自事件时刻；未收口时零值）。
	EndAt time.Time
	// Status 收口状态。
	Status Status
	// Pairing 配对状态（仅工具行）。
	Pairing Pairing
	// DupResults 重复结果计数（同一 call_id 收到第二个及以后的结果时累加）。
	DupResults int
	// Summary 行摘要（默认通用标签；宿主可用 Options.Summarize 覆盖）。
	Summary string
}

// Duration 返回实测耗时与「是否实测」。
//
// 时长只有这一个源：起止时刻相减。行内不存时长——存了就会有两个真相，
// 而估算值也就能混进同一个字段冒充实测。没有实测值时返回 ok=false：
// 不拿 0 冒充「瞬时完成」，更不拿载荷体积估一个填进来。
//
// 结束早于开始（事件时刻本身不自洽，例如结果先于调用到达）时同样返回
// ok=false：负时长不是实测值。
func (r Row) Duration() (time.Duration, bool) {
	if r.StartAt.IsZero() || r.EndAt.IsZero() {
		return 0, false
	}
	d := r.EndAt.Sub(r.StartAt)
	if d < 0 {
		return 0, false
	}
	return d, true
}

// Complete 行是否已收口（起止齐备）。
//
// 与 Duration 的区别：orphan 行（只有结束没有开始）同样没有实测时长，
// 但它的缺口不是「还在等」，而是「起点从未有过」——两件事要分开看。
func (r Row) Complete() bool {
	return !r.StartAt.IsZero() && !r.EndAt.IsZero()
}

// Issue 配对缺口（可观测面）：配对不完整时显式报出，不静默丢弃。
type Issue struct {
	// Pairing 缺口类目（missing_result / duplicate_result / unpaired_result /
	// duplicate_call）。
	Pairing Pairing
	// CallID 相关工具调用标识（无关联调用时为空）。
	CallID string
	// Seq 相关行的序号。
	Seq int
}

// Options 投影选项。
type Options struct {
	// Summarize 摘要注入点：宿主自定义行标签（展示文案归宿主，内核不认识
	// 任何领域词）。返回空串时沿用本包的通用标签；nil 时全程用通用标签。
	//
	// 只在行创建时调用一次——行的可读字段（类目、标识、工具名、时刻）在创建
	// 后不再变化，故摘要不需要跟着配对状态刷新。
	Summarize func(Row) string
}

// Projector 增量投影器：事件流 → 轨迹行。
//
// 并发安全：事件流可能由多个生产者投递，故 Apply 内部加锁。
type Projector struct {
	mu   sync.Mutex
	opts Options

	rows   []Row          // 按创建顺序追加；下标 + 1 即行序号
	tools  map[string]int // call_id → 行下标（一个 call_id 最多一行）
	opens  []int          // 仍打开的步骤行下标（按创建顺序）
	turn   int            // 当前轮次行下标（-1 = 尚未开轮）
	turnNo int            // 已开轮次计数
	seq    int            // 行序号计数
	issues []Issue
}

// NewProjector 构造投影器。
func NewProjector(opts Options) *Projector {
	return &Projector{
		opts:  opts,
		tools: map[string]int{},
		turn:  -1,
	}
}

// Apply 投一条事件（增量投影）。
//
// 已经被投过的事件不会被重复计入：后续事件只会**更新**既有行（补结束时刻、
// 把配对状态从「未配对」改成「已配对」），不会追加出第二条行。
func (p *Projector) Apply(ev eventbus.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.apply(ev)
}

// ApplyAll 依次投多条事件（增量投影；与逐条 Apply 等价）。
func (p *Projector) ApplyAll(evs []eventbus.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ev := range evs {
		p.apply(ev)
	}
}

// Rows 当前轨迹行快照（按行序号升序）。
//
// 返回拷贝：调用方改动拿到的切片不影响投影器内部状态。
func (p *Projector) Rows() []Row {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Row, len(p.rows))
	copy(out, p.rows)
	return out
}

// Len 当前轨迹行数。
func (p *Projector) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.rows)
}

// Issues 配对缺口清单（累计）。
//
// 口径：只有「当前仍成立」的缺口会被后来的事件清掉（缺结果等到了结果、
// 无对应的结果等到了调用）；「发生过」的缺口（重复结果、重复声明）永远留在
// 清单里。同一 (类目, call_id) 只记一条——重复次数由行上的 DupResults 承载。
func (p *Projector) Issues() []Issue {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Issue, len(p.issues))
	copy(out, p.issues)
	return out
}

// Finalize 收口投影：把「不会再有新事件」落成显式事实。
//
// 返回仍未收口的行（未完成行）。这些行是 open 态：它们有起点、缺终点，
// 不是「时长 0 的已完成行」。工具调用行若此时仍无结果，会被记成
// missing_result 缺口——到这一步才知道「等不到」是真的等不到。
//
// 不锁死投影器：宿主若继续投事件，投影照常更新，相应缺口也会被清掉。
func (p *Projector) Finalize() []Row {
	p.mu.Lock()
	defer p.mu.Unlock()
	var open []Row
	for i := range p.rows {
		r := p.rows[i]
		if r.Status != StatusOpen {
			continue
		}
		open = append(open, r)
		if r.Kind == KindToolCall && r.Pairing == PairingMissingResult {
			p.issue(PairingMissingResult, r.CallID, r.Seq)
		}
	}
	return open
}

// append 追加一行（分配序号、填默认摘要）。
func (p *Projector) append(r Row) int {
	p.seq++
	r.Seq = p.seq
	r.Summary = defaultSummary(r)
	if p.opts.Summarize != nil {
		if s := p.opts.Summarize(r); s != "" {
			r.Summary = s
		}
	}
	p.rows = append(p.rows, r)
	return len(p.rows) - 1
}

// issue 记一条配对缺口（同一类目 + call_id 只记一条）。
func (p *Projector) issue(kind Pairing, callID string, seq int) {
	for _, is := range p.issues {
		if is.Pairing == kind && is.CallID == callID {
			return
		}
	}
	p.issues = append(p.issues, Issue{Pairing: kind, CallID: callID, Seq: seq})
}

// clearIssue 清掉不再成立的缺口（缺结果等到了结果 / 无对应的结果等到了调用）。
func (p *Projector) clearIssue(kind Pairing, callID string) {
	kept := p.issues[:0]
	for _, is := range p.issues {
		if is.Pairing == kind && is.CallID == callID {
			continue
		}
		kept = append(kept, is)
	}
	p.issues = kept
}

// currentTurn 当前轮次序号（尚未开轮时为 0）。
func (p *Projector) currentTurn() int {
	if p.turn < 0 {
		return 0
	}
	return p.rows[p.turn].Turn
}

// defaultSummary 默认行摘要：通用标签，不含任何领域词。
func defaultSummary(r Row) string {
	switch r.Kind {
	case KindTurn, KindStep:
		return r.ID
	case KindToolCall, KindToolResult:
		if r.Tool == "" {
			return string(r.Kind)
		}
		return string(r.Kind) + ":" + r.Tool
	default:
		return string(r.Kind)
	}
}

// payloadString 取事件载荷里的字符串键（缺失或类型不符时为空串）。
func payloadString(ev eventbus.Event, key string) string {
	if ev.Payload == nil {
		return ""
	}
	s, _ := ev.Payload[key].(string)
	return s
}

// stepIDOf 取事件的环节号。
func stepIDOf(ev eventbus.Event) (uint64, bool) {
	if ev.StepID == nil {
		return 0, false
	}
	return *ev.StepID, true
}

// seqID 生成无自然键的行的标识（"-" 前缀 = 投影生成，不是业务自然键）。
func seqID(kind Kind, seq int) string {
	return fmt.Sprintf("%s#-%d", kind, seq)
}
