// Package delta 增量节流器（真流式观测面）。
//
// 节流窗口 / 总量帽 / seq 单调 / 权威收口语义的**基础机制在 oagentkit/runtime/pacer**；
// 本包提供其上的编排层：
//   - (kind, iteration) 键控：同一轮内的 reasoning / text 各自成流；
//   - 惰性建流（构造后可注入时钟，建流待首次使用）；
//   - 收口扫序：轮升序 × reasoning→text，保证事件落序确定性。
//
// 语义约定（同轮权威快照优先）：权威整段事件到达即 absorb 缓冲（回放/终稿以权威
// 为准）；调用轮没有权威正文快照，残尾只能靠 FlushAll 冲刷落地。
//
// 单 goroutine 契约：调用方的 OnEvent 在 drain 循环内同步回调（不阻塞不并发），
// 与 pairer/obs 同一约束面，故不加锁——**换宿主时须确认该契约仍成立**。
//
// # 生命周期是「单个环节」，不是「整个进程」
//
// entries 按 (kind, iteration) 惰性建流且**从不回收**， FlushAll 的扫序上界又是
// `max(iteration)`——这两处单独看都像缺陷（前者无界增长，后者按 max 空转）。
// 它们安全**只因为一个外部不变量**：宿主每个 LLM 环节新建一个 Pacer，在该环节的
// defer 里 FlushAll 收口后整体丢弃；iteration 是该环节内的局部计数器，不是
// 模型可控值、也不跨环节累加（见 bianque `steps_llm.go` 的 `round`）。
//
// 所以本包**不能**被改成长生命周期对象（如「整个会话一个 Pacer」）而无人察觉：
// 那会同时引爆上面两条，且症状是「跑久了内存涨 / 收口卡住」，极难归因。
// 改宿主接线前先看这一段。
package delta

import (
	"strconv"
	"time"

	"github.com/oagentkit/runtime/pacer"
)

const (
	// FlushInterval 节流窗口：距上次发布 ≥ 该值或缓冲 ≥ FlushBytes 才发布。
	FlushInterval = 400 * time.Millisecond
	// FlushBytes 缓冲字节下限（达到即发，不必等窗口）。
	FlushBytes = 200
	// CapBytes 单轮单类增量发布总量帽（深度思考可极长），超帽静默停发。
	CapBytes = 64 << 10
)

// Pub 一次待发布增量：seq 在 (kind, iteration) 内单调自 1 递增，text 为合并分片
// （非累积快照——消费方按序追加，避免长文本 O(n²) 重发）。
type Pub struct {
	Kind      string
	Iteration int
	Seq       int
	Text      string
}

type entry struct {
	kind      string
	iteration int
	stream    *pacer.Stream
}

// Pacer 按 (kind, iteration) 键控的增量节流集合。
type Pacer struct {
	entries map[string]*entry
	now     func() time.Time
}

// New 构造节流器。测试可在构造后替换内部时钟。
func New() *Pacer {
	return &Pacer{entries: map[string]*entry{}, now: time.Now}
}

func key(kind string, iteration int) string {
	return kind + ":" + strconv.Itoa(iteration)
}

func (p *Pacer) ent(kind string, iteration int) *entry {
	k := key(kind, iteration)
	e := p.entries[k]
	if e == nil {
		e = &entry{kind: kind, iteration: iteration,
			stream: pacer.NewStream(FlushInterval, FlushBytes, CapBytes, pacer.WithClock(p.now))}
		p.entries[k] = e
	}
	return e
}

// Offer 接收一个增量分片：按节流窗口判定是否发布；done（权威已到）后的迟到分片丢弃。
func (p *Pacer) Offer(kind string, iteration int, chunk string) (Pub, bool) {
	d, ok := p.ent(kind, iteration).stream.Offer(chunk)
	return Pub{Kind: kind, Iteration: iteration, Seq: d.Seq, Text: d.Text}, ok
}

// Flush 立即发布该轮缓冲残尾（帽内）；空缓冲/已收口不动序号。
func (p *Pacer) Flush(kind string, iteration int) (Pub, bool) {
	e, ok := p.entries[key(kind, iteration)]
	if !ok {
		return Pub{}, false
	}
	d, ok := e.stream.Flush()
	return Pub{Kind: kind, Iteration: iteration, Seq: d.Seq, Text: d.Text}, ok
}

// Absorb 权威快照到达：该轮增量缓冲作废并收口（消费方以权威整段替换增量累积）。
func (p *Pacer) Absorb(kind string, iteration int) {
	if e, ok := p.entries[key(kind, iteration)]; ok {
		e.stream.Absorb()
	}
}

// FlushAll 环节收口：按轮序冲刷所有残尾（调用轮正文无权威快照，只在此落地）。
// 顺序确定性：轮升序 × reasoning→text，保证事件落序可回放。
func (p *Pacer) FlushAll() []Pub {
	max := 0
	for _, e := range p.entries {
		if !e.stream.Sealed() && e.iteration > max {
			max = e.iteration
		}
	}
	var out []Pub
	for it := 1; it <= max; it++ {
		for _, kind := range []string{"reasoning", "text"} {
			if pub, ok := p.Flush(kind, it); ok {
				out = append(out, pub)
			}
		}
	}
	return out
}
