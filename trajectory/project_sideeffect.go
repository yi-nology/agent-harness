// project_sideeffect.go —— 副作用行与快照行的**投影**（v0.33.0，issue #99）。
//
// 排障两问的轨迹面：「这个副作用到底施加了吗」（授予 / 施加 / 对账 / 裁决）
// 与「恢复点停在哪个序号」（快照提交）。单成文件是**重新分组**，不是拆行数：
// 副作用投影有自己的配对语义（幂等键配对、授予段→对账段接续、点观察），
// 与工具行的 call_id 配对、轮次/步骤的括号段是三套不同的账。
//
// 形态上是包级函数而不是 Projector 的方法：投影器的行为面按行族分组，
// 每族一组函数——「这个投影器支持哪些行」由 apply 的分发面回答，
// 不靠方法数堆在类型上。
package trajectory

import (
	"github.com/yi-nology/goagent-kernel/eventbus"
)

// ── 副作用行（v0.33.0，issue #99）────────────────────────────────────
//
// 排障第一问「这个副作用到底施加了吗」的轨迹面：授予（granted）→ 施加/放弃/
// 转未知（completed / abandoned / uncertain），uncertain 之后还有对账段
// （→ resolved_applied / resolved_cleared）。点观察（replayed / contended）
// 无括号段，落成孤儿行——没有实测时长就不写时长，不拿 0 冒充。
//
// 配对键是幂等键。同一 Key 的授予段、对账段、点观察共用一个行标识（它们是
// 同一个逻辑动作的先后两段/侧面），跨 span join 时应聚到一起。

// sideEffectPayload 从事件载荷取副作用行的公共字段。
func sideEffectPayload(ev eventbus.Event) (key, resource, safety string, fence uint64) {
	key = payloadString(ev, "key")
	resource = payloadString(ev, "resource")
	safety = payloadString(ev, "retry_safety")
	fence = payloadUint(ev, "fence")
	return
}

// openSideEffect 闸授予执行权：开一个副作用授予段（granted，打开中）。
//
// 同一 Key 已有打开中的授予段时**不收口旧行**：那是「上次授予没等到收口」的
// 事实（可能是缺口，也可能宿主没发布收口事件），改写或补收口都是伪造——
// 旧行保持打开，Finalize 时作为未完成行显形。
func openSideEffect(p *Projector, ev eventbus.Event) {
	key, resource, safety, fence := sideEffectPayload(ev)
	r := Row{
		Kind:               KindSideEffect,
		Turn:               p.currentTurn(),
		SideEffectKey:      key,
		SideEffectPhase:    SideEffectGranted,
		SideEffectResource: resource,
		SideEffectSafety:   safety,
		SideEffectFence:    fence,
		StartAt:            ev.OccurredAt,
		Status:             StatusOpen,
	}
	if key == "" {
		r.ID = seqID(KindSideEffect, p.seq+1)
	} else {
		r.ID = sideeffectRowID(key)
	}
	if id, ok := stepIDOf(ev); ok {
		r.HasStep = true
		r.StepID = id
	}
	idx := p.append(r)
	if key != "" {
		p.grants[key] = idx
	}
}

// settleSideEffect 授予段收口：completed / abandoned。
//
// 没有打开中的授予段（授予发生在别的进程 / 宿主没发布授予事件）时落成孤儿行
// （只有终点的行）——结论要显形，不能因为没有可收口的行就丢掉。
func settleSideEffect(p *Projector, ev eventbus.Event, phase SideEffectPhase) {
	key, resource, safety, fence := sideEffectPayload(ev)
	if idx, ok := p.grants[key]; ok && key != "" {
		delete(p.grants, key)
		row := &p.rows[idx]
		row.EndAt = ev.OccurredAt
		row.Status = StatusClosed
		row.SideEffectPhase = phase
		if resource != "" {
			row.SideEffectResource = resource
		}
		if safety != "" {
			row.SideEffectSafety = safety
		}
		return
	}
	appendSideEffectOrphan(p, ev, key, resource, safety, fence, phase)
}

// uncertainSideEffect 结果未知：收口授予段（phase=uncertain），并开启同 Key
// 的对账段（打开中，phase=uncertain，等 resolved 收口）。
//
// 两个动作出自同一事件：转未知既是「上次尝试的结论」也是「对账的开始理由」。
// 没有可收口的授予段时只开对账段（授予段从未被观测到，不伪造它）。
func uncertainSideEffect(p *Projector, ev eventbus.Event) {
	key, resource, safety, fence := sideEffectPayload(ev)
	if idx, ok := p.grants[key]; ok && key != "" {
		delete(p.grants, key)
		row := &p.rows[idx]
		row.EndAt = ev.OccurredAt
		row.Status = StatusClosed
		row.SideEffectPhase = SideEffectUncertain
		if resource != "" {
			row.SideEffectResource = resource
		}
		if safety != "" {
			row.SideEffectSafety = safety
		}
	} else {
		appendSideEffectOrphan(p, ev, key, resource, safety, fence, SideEffectUncertain)
	}
	openReconcile(p, ev, key, resource, safety, fence)
}

// openReconcile 开一个对账段（打开中，phase=uncertain）。
//
// 同 Key 已有打开中的对账段时**不重开**：第二次 uncertain 只是同一场对账的
// 又一条理由，重开等于把「等了多久才裁决」的实测起点改掉——第一次观测是真相。
func openReconcile(p *Projector, ev eventbus.Event, key, resource, safety string, fence uint64) {
	if key == "" {
		return // 无键无处挂对账段；点语义已由孤儿行承载
	}
	if _, ok := p.reconciles[key]; ok {
		return
	}
	r := Row{
		Kind:               KindSideEffect,
		Turn:               p.currentTurn(),
		SideEffectKey:      key,
		SideEffectPhase:    SideEffectUncertain,
		SideEffectResource: resource,
		SideEffectSafety:   safety,
		SideEffectFence:    fence,
		StartAt:            ev.OccurredAt,
		Status:             StatusOpen,
		ID:                 sideeffectRowID(key),
	}
	if id, ok := stepIDOf(ev); ok {
		r.HasStep = true
		r.StepID = id
	}
	idx := p.append(r)
	p.reconciles[key] = idx
}

// resolveSideEffect 对账裁决落定：收口对账段（resolved_applied / resolved_cleared）。
//
// 没有打开中的对账段时落成孤儿行（对账发生在别的进程 / 事件流只有尾部）。
func resolveSideEffect(p *Projector, ev eventbus.Event) {
	key, resource, safety, fence := sideEffectPayload(ev)
	phase := SideEffectResolvedCleared
	if payloadBool(ev, "applied") {
		phase = SideEffectResolvedApplied
	}
	if idx, ok := p.reconciles[key]; ok && key != "" {
		delete(p.reconciles, key)
		row := &p.rows[idx]
		row.EndAt = ev.OccurredAt
		row.Status = StatusClosed
		row.SideEffectPhase = phase
		if resource != "" {
			row.SideEffectResource = resource
		}
		return
	}
	appendSideEffectOrphan(p, ev, key, resource, safety, fence, phase)
}

// pointSideEffect 点观察（replayed / contended）：无括号段，落成孤儿行。
//
// 「动作早已完成」「正被别人持有」都是**观测到的结论**，时刻记在 EndAt、
// 起点留空：没有实测时长，Duration() 返回 ok=false——不拿 0 冒充。
func pointSideEffect(p *Projector, ev eventbus.Event, phase SideEffectPhase) {
	key, resource, safety, fence := sideEffectPayload(ev)
	appendSideEffectOrphan(p, ev, key, resource, safety, fence, phase)
}

// appendSideEffectOrphan 追加一条孤儿副作用行（只有终点 / 点观察）。
func appendSideEffectOrphan(p *Projector, ev eventbus.Event, key, resource, safety string, fence uint64, phase SideEffectPhase) {
	r := Row{
		Kind:               KindSideEffect,
		Turn:               p.currentTurn(),
		SideEffectKey:      key,
		SideEffectPhase:    phase,
		SideEffectResource: resource,
		SideEffectSafety:   safety,
		SideEffectFence:    fence,
		EndAt:              ev.OccurredAt,
		Status:             StatusOrphan,
	}
	if key == "" {
		r.ID = seqID(KindSideEffect, p.seq+1)
	} else {
		r.ID = sideeffectRowID(key)
	}
	if id, ok := stepIDOf(ev); ok {
		r.HasStep = true
		r.StepID = id
	}
	p.append(r)
}

// checkpointCommitted 快照提交（点事件）：落成孤儿行——一次提交没有括号段，
// 「提交耗时多久」从未被观测，不拿 0 冒充实测。
func checkpointCommitted(p *Projector, ev eventbus.Event) {
	runID := payloadString(ev, "run_id")
	seq := payloadUint(ev, "seq")
	r := Row{
		Kind:             KindCheckpoint,
		Turn:             p.currentTurn(),
		CheckpointRunID:  runID,
		CheckpointSeq:    seq,
		CheckpointStatus: payloadString(ev, "status"),
		EndAt:            ev.OccurredAt,
		Status:           StatusOrphan,
	}
	if runID == "" || seq == 0 {
		r.ID = seqID(KindCheckpoint, p.seq+1)
	} else {
		r.ID = checkpointRowID(runID, seq)
	}
	if id, ok := stepIDOf(ev); ok {
		r.HasStep = true
		r.StepID = id
	}
	p.append(r)
}
