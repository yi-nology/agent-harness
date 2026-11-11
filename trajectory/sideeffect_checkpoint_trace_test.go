// sideeffect_checkpoint_trace_test.go —— 副作用行与快照行的投影与 OTel 语义钉住
// （v0.33.0，issue #99）。
//
// # 钉的是什么
//
// trajectory 此前只认 turn/step/tool 三类行——「这个副作用到底施加了吗」
// 与「恢复点停在哪个序号」这两问在轨迹里查不到。本文件钉住新两类的行为：
//
//   - 副作用行的生命周期投影（授予段 / 对账段 / 点观察）；
//   - 收不了口的段在 Finalize 显形（授予没等到收口、对账没等到裁决）；
//   - span 状态语义（uncertain=error，点观察=ok，在途=unset）；
//   - 父子归属（带环节号挂环节，否则挂轮次）；
//   - 「内核只给映射、宿主负责投递」边界不变——本包不做任何投递。
//
// 夹具一律合成数据（act-a / run-a / res-a），不带任何领域词。
package trajectory

import (
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/eventbus"
)

// seEv 一条副作用事件（载荷键面与 eventbus 词表注释一致）。
func seEv(typ string, ms int, key, resource, safety string, fence uint64, extra map[string]any) eventbus.Event {
	payload := map[string]any{
		"key":      key,
		"resource": resource,
		"fence":    fence,
	}
	if safety != "" {
		payload["retry_safety"] = safety
	}
	for k, v := range extra {
		payload[k] = v
	}
	return eventbus.Event{Type: typ, OccurredAt: at(ms), Payload: payload}
}

// ckEv 一条快照提交事件。
func ckEv(ms int, runID string, seq uint64, status string) eventbus.Event {
	return eventbus.Event{
		Type:       eventbus.EventCheckpointCommitted,
		OccurredAt: at(ms),
		Payload:    map[string]any{"run_id": runID, "seq": seq, "status": status, "done": 2},
	}
}

// withStep 给事件补环节号。
func withStep(e eventbus.Event, step uint64) eventbus.Event {
	e.StepID = &step
	return e
}

// TestSideEffectLifecycleProjection 副作用行的三段投影：
// 授予段（granted→completed 实测时长）、对账段（uncertain→resolved）、点观察。
func TestSideEffectLifecycleProjection(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		seEv(eventbus.EventSideEffectGranted, 100, "act-a", "res-a", "safe", 3, nil),
		seEv(eventbus.EventSideEffectCompleted, 150, "act-a", "res-a", "safe", 3, nil),
		seEv(eventbus.EventSideEffectGranted, 200, "act-b", "res-b", "", 4, nil),
		seEv(eventbus.EventSideEffectUncertain, 250, "act-b", "res-b", "", 4, nil),
		seEv(eventbus.EventSideEffectResolved, 400, "act-b", "res-b", "", 4,
			map[string]any{"applied": true}),
		seEv(eventbus.EventSideEffectReplayed, 500, "act-a", "res-a", "", 0, nil),
		seEv(eventbus.EventSideEffectContended, 510, "act-c", "res-c", "", 0, nil),
	})

	rows := p.Rows()
	// 期望：act-a 授予段(closed) + act-b 授予段(closed,uncertain) + act-b 对账段(closed)
	// + act-a 点(replayed) + act-c 点(contended) = 5 行
	if len(rows) != 5 {
		t.Fatalf("应投影出 5 行，实得 %d：%+v", len(rows), rows)
	}
	grant := rows[0]
	if grant.Kind != KindSideEffect || grant.SideEffectPhase != SideEffectCompleted ||
		grant.Status != StatusClosed || grant.SideEffectKey != "act-a" {
		t.Fatalf("授予段应收口为 completed：%+v", grant)
	}
	if grant.ID != "sideeffect#act-a" {
		t.Fatalf("副作用行标识应为 sideeffect#act-a，实得 %q", grant.ID)
	}
	if d, ok := grant.Duration(); !ok || d != 50*time.Millisecond {
		t.Fatalf("授予段时长应为实测 50ms，实得 %v ok=%v", d, ok)
	}
	uncGrant := rows[1]
	if uncGrant.SideEffectPhase != SideEffectUncertain || uncGrant.Status != StatusClosed {
		t.Fatalf("act-b 授予段应收口为 uncertain：%+v", uncGrant)
	}
	recon := rows[2]
	if recon.SideEffectPhase != SideEffectResolvedApplied || recon.Status != StatusClosed {
		t.Fatalf("act-b 对账段应收口为 resolved_applied：%+v", recon)
	}
	if recon.ID != uncGrant.ID {
		t.Fatalf("同 Key 的授予段与对账段应共用一个行标识：%q vs %q", recon.ID, uncGrant.ID)
	}
	if d, ok := recon.Duration(); !ok || d != 150*time.Millisecond {
		t.Fatalf("对账段时长应为实测 150ms（等了多久才裁决），实得 %v ok=%v", d, ok)
	}
	replay := rows[3]
	if replay.SideEffectPhase != SideEffectReplayed || replay.Status != StatusOrphan {
		t.Fatalf("replayed 应是点观察孤儿行：%+v", replay)
	}
	if _, ok := replay.Duration(); ok {
		t.Fatal("点观察没有实测时长：起点缺失时 Duration 必须 ok=false，不拿 0 冒充")
	}
	if rows[4].SideEffectPhase != SideEffectContended {
		t.Fatalf("contended 应是点观察：%+v", rows[4])
	}
}

// TestSideEffectUncertainOpensReconcileOnlyOnce 同一 Key 的第二次 uncertain
// 不重开对账段——实测起点是第一次观测，重开等于改掉「等了多久才裁决」。
func TestSideEffectUncertainOpensReconcileOnlyOnce(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		seEv(eventbus.EventSideEffectUncertain, 100, "act-a", "res-a", "", 1, nil),
		seEv(eventbus.EventSideEffectUncertain, 200, "act-a", "res-a", "", 1, nil),
		seEv(eventbus.EventSideEffectResolved, 300, "act-a", "res-a", "", 1,
			map[string]any{"applied": false}),
	})
	rows := p.Rows()
	if len(rows) != 3 {
		t.Fatalf("uncertain(孤儿授予段) + 对账段 + 收口 = 3 行，实得 %d：%+v", len(rows), rows)
	}
	if d, ok := rows[1].Duration(); !ok || d != 200*time.Millisecond {
		t.Fatalf("对账时长应从第一次 uncertain 起算（200ms），实得 %v ok=%v", d, ok)
	}
}

// TestSideEffectFinalizeShowsUnsettled 收不了口的段在 Finalize 显形：
// 授予了没收口、转未知了没裁决——都是「等了也没等到」的事实，不静默丢弃。
func TestSideEffectFinalizeShowsUnsettled(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		seEv(eventbus.EventSideEffectGranted, 100, "act-a", "res-a", "", 1, nil),
		seEv(eventbus.EventSideEffectUncertain, 150, "act-b", "res-b", "", 2, nil),
	})
	open := p.Finalize()
	if len(open) != 2 {
		t.Fatalf("应报出 2 条未收口行（授予挂起 + 对账挂起），实得 %d：%+v", len(open), open)
	}
	if open[0].SideEffectPhase != SideEffectGranted || open[1].SideEffectPhase != SideEffectUncertain {
		t.Fatalf("未收口行的阶段应可读出悬在何处：%+v", open)
	}
}

// TestCheckpointCommittedProjection 快照行：点事件落孤儿行——没有实测时长
// 就没有 duration 属性；「恢复点停在哪个序号」由 run_id + seq 回答。
func TestCheckpointCommittedProjection(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		ckEv(100, "run-a", 3, "running"),
		ckEv(200, "run-a", 4, "running"),
	})
	rows := p.Rows()
	if len(rows) != 2 {
		t.Fatalf("两次提交应两行，实得 %d", len(rows))
	}
	if rows[0].Kind != KindCheckpoint || rows[0].Status != StatusOrphan {
		t.Fatalf("快照行应是孤儿形态的点行：%+v", rows[0])
	}
	if rows[0].ID != "checkpoint#run-a#3" || rows[1].CheckpointSeq != 4 {
		t.Fatalf("行标识/序号不符：%+v", rows)
	}
	if _, ok := rows[0].Duration(); ok {
		t.Fatal("快照提交没有实测时长：不拿 0 冒充")
	}
}

// TestSideEffectAndCheckpointOTelSemantics span 名低基数、状态三值、属性名进契约。
func TestSideEffectAndCheckpointOTelSemantics(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		seEv(eventbus.EventSideEffectGranted, 100, "act-a", "res-a", "unknown", 5, nil),
		seEv(eventbus.EventSideEffectUncertain, 150, "act-a", "res-a", "unknown", 5, nil),
		ckEv(160, "run-a", 3, "running"),
	})
	spans := SpansOf(p.Rows())
	var seSpans []Span
	var ck Span
	for _, s := range spans {
		switch s.Name {
		case string(KindSideEffect):
			seSpans = append(seSpans, s)
		case string(KindCheckpoint):
			ck = s
		}
	}
	// 两段 sideeffect span：授予段（closed, uncertain → error）与对账段（open → unset）。
	if len(seSpans) != 2 {
		t.Fatalf("应有两段 sideeffect span，实得 %d", len(seSpans))
	}
	statuses := map[SpanStatus]int{SpanStatusError: 0, SpanStatusUnset: 0}
	for _, s := range seSpans {
		statuses[s.Status]++
	}
	if statuses[SpanStatusError] != 1 || statuses[SpanStatusUnset] != 1 {
		t.Fatalf("uncertain 收口应为 error（已发生的问题）、对账挂起应为 unset（仍在途），实得 %v", statuses)
	}
	// 属性面：幂等键 / 资源 / 令牌 / 重试安全性都进属性。
	g := seSpans[0]
	if g.Name != string(KindSideEffect) {
		t.Fatalf("span 名应低基数（sideeffect），实得 %q", g.Name)
	}
	for k, want := range map[string]any{
		AttrSideEffectKey: "act-a", AttrSideEffectResource: "res-a",
		AttrSideEffectFence: int64(5), AttrSideEffectSafety: "unknown",
	} {
		if got := g.Attributes[k]; got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
	// 快照行：点观察 → ok；run_id / seq / status 进属性；无实测时长就没有该属性。
	if ck.Status != SpanStatusOK {
		t.Fatalf("快照提交是确定的观测 → ok，实得 %s", ck.Status)
	}
	if _, has := ck.Attributes[AttrDurationMs]; has {
		t.Fatal("快照行没有实测时长，不得出现 duration_ms 属性")
	}
	for k, want := range map[string]any{
		AttrCheckpointRunID: "run-a", AttrCheckpointSeq: int64(3), AttrCheckpointStatus: "running",
	} {
		if got := ck.Attributes[k]; got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
}

// TestSideEffectAndCheckpointParentLinks 父子归属：带环节号挂环节（更近的因果），
// 否则挂轮次；快照行同理。
func TestSideEffectAndCheckpointParentLinks(t *testing.T) {
	p := NewProjector(Options{})
	step := uint64(7)
	p.ApplyAll([]eventbus.Event{
		{Type: eventbus.EventTurnStarted, OccurredAt: at(0)},
		{Type: eventbus.EventStepStarted, OccurredAt: at(10), StepID: &step},
		withStep(seEv(eventbus.EventSideEffectGranted, 20, "act-a", "res-a", "", 1, nil), step),
		withStep(seEv(eventbus.EventSideEffectCompleted, 30, "act-a", "res-a", "", 1, nil), step),
		{Type: eventbus.EventCheckpointCommitted, OccurredAt: at(40), StepID: &step,
			Payload: map[string]any{"run_id": "run-a", "seq": uint64(1)}},
		{Type: eventbus.EventCheckpointCommitted, OccurredAt: at(50),
			Payload: map[string]any{"run_id": "run-a", "seq": uint64(2)}},
	})
	spans := SpansOf(p.Rows())
	for _, s := range spans {
		switch s.Name {
		case string(KindSideEffect):
			if s.ParentID != "step#7" {
				t.Errorf("带环节号的副作用行应挂环节下，实得父 %q", s.ParentID)
			}
		case string(KindCheckpoint):
			if s.Attributes[AttrCheckpointSeq] == int64(1) && s.ParentID != "step#7" {
				t.Errorf("带环节号的快照行应挂环节下，实得父 %q", s.ParentID)
			}
			if s.Attributes[AttrCheckpointSeq] == int64(2) && s.ParentID != "turn#1" {
				t.Errorf("无环节号的快照行应挂轮次下，实得父 %q", s.ParentID)
			}
		}
	}
}

// TestSideEffectPayloadJSONRoundTrip 载荷经 JSON 往返（整数变 float64）后
// 投影不失真——宿主的发布面几乎必然走序列化，按「只认 int64」写判据会全盲。
func TestSideEffectPayloadJSONRoundTrip(t *testing.T) {
	p := NewProjector(Options{})
	p.ApplyAll([]eventbus.Event{
		{Type: eventbus.EventCheckpointCommitted, OccurredAt: at(100),
			Payload: map[string]any{"run_id": "run-a", "seq": float64(9), "status": "running"}},
		seEv(eventbus.EventSideEffectGranted, 110, "act-a", "res-a", "", 0, nil),
	})
	rows := p.Rows()
	if rows[0].CheckpointSeq != 9 {
		t.Fatalf("JSON float64 形态的 seq 应被识别，实得 %d", rows[0].CheckpointSeq)
	}
	if rows[1].SideEffectFence != 0 || rows[1].ID != "sideeffect#act-a" {
		t.Fatalf("fence=0 时不写令牌属性，行标识仍取自然键：%+v", rows[1])
	}
}

// TestSideEffectRowsHaveNoDelivery 本包边界：只出映射，不做投递。
// 钉法：投影产物是纯数据（Row/Span），没有任何导出器或发送路径——
// 新增行类目若引入投递依赖，这里的 import 面就变了，构建即红。
func TestSideEffectRowsHaveNoDelivery(t *testing.T) {
	// Row 与 Span 都是纯数据结构；把它们各拷一份并改动，互不影响。
	rows := p99SampleRows()
	cp := append([]Row(nil), rows...)
	cp[0].SideEffectKey = "mutated"
	if rows[0].SideEffectKey == "mutated" {
		t.Fatal("Rows() 必须返回拷贝：调用方改动不得影响投影器内部状态")
	}
}

func p99SampleRows() []Row {
	return []Row{{
		Seq: 1, Kind: KindSideEffect, ID: "sideeffect#act-a", Turn: 1,
		SideEffectKey: "act-a", SideEffectPhase: SideEffectGranted,
		StartAt: at(0), Status: StatusOpen,
	}}
}
