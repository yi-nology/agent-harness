package trajectory

import (
	"fmt"

	"github.com/yi-nology/agent-harness/eventbus"
)

// apply 投一条事件到投影器（调用方已持锁）。
//
// 事件类型取自本仓 eventbus 的词表——投影器吃本仓自己的事件，不另立事件模型：
// 另立一套事件词表，等于让「什么算一次工具调用」在两处各答一次，
// 答案迟早漂。
func (p *Projector) apply(ev eventbus.Event) {
	switch ev.Type {
	case eventbus.EventTurnStarted:
		p.openTurn(ev)
	case eventbus.EventTurnEnded:
		p.closeTurn(ev)
	case eventbus.EventStepStarted:
		p.openStep(ev)
	case eventbus.EventStepFinished:
		p.closeStep(ev)
	case eventbus.EventToolCall:
		p.openTool(ev)
	case eventbus.EventToolResult:
		p.closeTool(ev)
	default:
		// 其余事件类型不构成轨迹行（它们是过程的旁证，不是过程段本身）——
		// 不是缺口，不记。
	}
}

// openTurn 开一轮：新建轮次行。
//
// 上一轮若仍打开（没有 turn_ended），**不替它补一个结束时刻**——补了就是
// 伪造实测时长。它保持 open，Finalize 时作为未完成行报出。
func (p *Projector) openTurn(ev eventbus.Event) {
	p.turnNo++
	idx := p.append(Row{
		Kind:    KindTurn,
		ID:      fmt.Sprintf("turn#%d", p.turnNo),
		Turn:    p.turnNo,
		StartAt: ev.OccurredAt,
		Status:  StatusOpen,
	})
	p.turn = idx
}

// closeTurn 收一轮：收口当前轮次行；没有可收口的轮次行时落成孤儿行
// （只有结束事件、没有开始事件），不静默丢弃。
func (p *Projector) closeTurn(ev eventbus.Event) {
	if p.turn >= 0 && p.rows[p.turn].Status == StatusOpen {
		p.rows[p.turn].EndAt = ev.OccurredAt
		p.rows[p.turn].Status = StatusClosed
		return
	}
	p.append(Row{
		Kind:   KindTurn,
		ID:     seqID(KindTurn, p.seq+1),
		Turn:   p.currentTurn(),
		EndAt:  ev.OccurredAt,
		Status: StatusOrphan,
	})
}

// openStep 开一个步骤行。
//
// 环节号是步骤的自然键；没有环节号的步骤只能按先后顺序配对（见 matchOpenStep）。
func (p *Projector) openStep(ev eventbus.Event) {
	r := Row{
		Kind:    KindStep,
		Turn:    p.currentTurn(),
		StartAt: ev.OccurredAt,
		Status:  StatusOpen,
	}
	if id, ok := stepIDOf(ev); ok {
		r.HasStep = true
		r.StepID = id
		r.ID = fmt.Sprintf("step#%d", id)
	} else {
		r.ID = seqID(KindStep, p.seq+1)
	}
	idx := p.append(r)
	p.opens = append(p.opens, idx)
}

// closeStep 收一个步骤行；没有可收口的步骤行时落成孤儿行。
func (p *Projector) closeStep(ev eventbus.Event) {
	idx := p.matchOpenStep(ev)
	if idx < 0 {
		r := Row{
			Kind:   KindStep,
			Turn:   p.currentTurn(),
			EndAt:  ev.OccurredAt,
			Status: StatusOrphan,
		}
		if id, ok := stepIDOf(ev); ok {
			r.HasStep = true
			r.StepID = id
		}
		r.ID = seqID(KindStep, p.seq+1)
		p.append(r)
		return
	}
	p.rows[idx].EndAt = ev.OccurredAt
	p.rows[idx].Status = StatusClosed
	p.dropOpen(idx)
}

// matchOpenStep 找出可与该结束事件配对的步骤行。
//
// 带环节号：配**最近**一条同号且仍打开的步骤行——同一环节号被重新开过一次时
// （重试），后一条才是本次结束对应的。
// 不带环节号：配**最早**一条仍打开的无环节号步骤行（先进先出）。
// 都配不上时返回 -1（调用方落孤儿行）。
func (p *Projector) matchOpenStep(ev eventbus.Event) int {
	if id, ok := stepIDOf(ev); ok {
		for i := len(p.opens) - 1; i >= 0; i-- {
			idx := p.opens[i]
			if p.rows[idx].HasStep && p.rows[idx].StepID == id {
				return idx
			}
		}
		return -1
	}
	for _, idx := range p.opens {
		if !p.rows[idx].HasStep {
			return idx
		}
	}
	return -1
}

// dropOpen 从「仍打开的步骤行」清单里摘掉一行。
func (p *Projector) dropOpen(idx int) {
	for i, open := range p.opens {
		if open == idx {
			p.opens = append(p.opens[:i], p.opens[i+1:]...)
			return
		}
	}
}

// openTool 开一个工具调用行。
//
// 配对键是 call_id。同一 call_id 的第二次声明**不新建行**：第一次观测是真相，
// 改写起点等于伪造实测时长——只记一条 duplicate_call 缺口。
func (p *Projector) openTool(ev eventbus.Event) {
	callID := payloadString(ev, "call_id")
	tool := payloadString(ev, "tool")
	if callID != "" {
		if idx, ok := p.tools[callID]; ok {
			if p.rows[idx].Kind == KindToolResult {
				// 结果先到、调用后到：就地配对（起点取调用时刻）。
				// 事件顺序反了时两时刻会倒挂，Duration() 随之判定「无实测值」——
				// 那正是事实，不拿绝对值糊过去。
				p.rows[idx].Kind = KindToolCall
				p.rows[idx].StartAt = ev.OccurredAt
				p.rows[idx].Status = StatusClosed
				p.rows[idx].Pairing = PairingPaired
				p.clearIssue(PairingUnpairedResult, callID)
				return
			}
			p.issue(PairingDuplicateCall, callID, p.rows[idx].Seq)
			return
		}
	}
	r := Row{
		Kind:    KindToolCall,
		Turn:    p.currentTurn(),
		CallID:  callID,
		Tool:    tool,
		StartAt: ev.OccurredAt,
		Status:  StatusOpen,
		Pairing: PairingMissingResult,
	}
	if id, ok := stepIDOf(ev); ok {
		r.HasStep = true
		r.StepID = id
	}
	if callID == "" {
		r.ID = seqID(KindToolCall, p.seq+1)
	} else {
		r.ID = "tool#" + callID
	}
	idx := p.append(r)
	if callID != "" {
		p.tools[callID] = idx
	}
}

// closeTool 用结果收口工具调用行。
//
// 第一次配上的结果：结束时刻取结果时刻，时长随之成为实测值。
// 重复结果：**不重写结束时刻**（第一次观测是真相），只记缺口与计数。
// 结果没有对应调用：落成结果行 + unpaired_result 缺口，不静默丢弃。
func (p *Projector) closeTool(ev eventbus.Event) {
	callID := payloadString(ev, "call_id")
	tool := payloadString(ev, "tool")
	idx, ok := p.tools[callID]
	if !ok || callID == "" {
		// 结果没有对应调用：落成结果行 + unpaired_result 缺口，不静默丢弃。
		//
		// 结果事件是「一次工具调用的**结束**」，故时刻记在 EndAt、起点留空
		// （起点缺失 = StatusOrphan）。这样它后来若等到迟到的调用声明，
		// 可以就地补上起点完成配对，而不必搬动时刻。
		r := Row{
			Kind:    KindToolResult,
			Turn:    p.currentTurn(),
			CallID:  callID,
			Tool:    tool,
			EndAt:   ev.OccurredAt,
			Status:  StatusOrphan,
			Pairing: PairingUnpairedResult,
		}
		if id, has := stepIDOf(ev); has {
			r.HasStep = true
			r.StepID = id
		}
		if callID == "" {
			r.ID = seqID(KindToolResult, p.seq+1)
		} else {
			r.ID = "tool#" + callID
		}
		pi := p.append(r)
		if callID != "" {
			p.tools[callID] = pi
		}
		p.issue(PairingUnpairedResult, callID, p.rows[pi].Seq)
		return
	}
	row := &p.rows[idx]
	if row.EndAt.IsZero() {
		row.EndAt = ev.OccurredAt
		row.Status = StatusClosed
		row.Pairing = PairingPaired
		p.clearIssue(PairingMissingResult, callID)
		return
	}
	row.DupResults++
	if row.Kind == KindToolCall {
		// 已配上的行：状态改成「结果重复」。
		row.Pairing = PairingDuplicateResult
	}
	// 仍是孤儿结果行的（始终没有对应调用）：配对状态保持 unpaired_result——
	// 改成 duplicate_result 会把「压根没配对」这层事实盖掉。两件事都进缺口清单。
	p.issue(PairingDuplicateResult, callID, row.Seq)
}
