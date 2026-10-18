package watchdog

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yi-nology/agent-harness/eventbus"
)

func stepPtr(id uint64) *uint64 { return &id }

// rec 收集提示（模拟 repo+bus 落库广播路径）。
type rec struct {
	mu  sync.Mutex
	got []eventbus.Event
}

func (r *rec) pub(sid, cid string, stepID *uint64, typ, level string, payload map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, eventbus.Event{SessionID: sid, CaseID: cid, StepID: stepID, Type: typ, Level: level, Payload: payload})
}

func (r *rec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func (r *rec) first() eventbus.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[0]
}

func callEvent(callID string) eventbus.Event {
	return eventbus.Event{SessionID: "s1", CaseID: "c1", StepID: stepPtr(7),
		Type: eventbus.EventToolCall, Payload: map[string]any{"call_id": callID, "tool": "use_skill"}}
}

func resultEvent(callID string) eventbus.Event {
	return eventbus.Event{SessionID: "s1", CaseID: "c1", StepID: stepPtr(7),
		Type: eventbus.EventToolResult, Payload: map[string]any{"call_id": callID, "tool": "use_skill"}}
}

func finishEvent() eventbus.Event {
	return eventbus.Event{SessionID: "s1", CaseID: "c1", StepID: stepPtr(7),
		Type: eventbus.EventStepFinished, Payload: map[string]any{"agent": "x", "status": "succeeded"}}
}

func TestWatchdogLeakDetected(t *testing.T) {
	// INV1：声明后无结果、环节收口 → declared_not_executed 提示
	r := &rec{}
	w := New(r.pub)
	w.observe(callEvent("t1"))
	w.observe(finishEvent())

	if r.count() != 1 {
		t.Fatalf("应有一条泄漏提示: %d", r.count())
	}
	got := r.first()
	if got.Type != eventbus.EventToolPairingLeak || got.Level != "warn" {
		t.Fatalf("事件类型/级别不符: %+v", got)
	}
	if got.Payload["reason"] != "declared_not_executed" || got.Payload["call_id"] != "t1" {
		t.Fatalf("payload 不符: %v", got.Payload)
	}
}

func TestWatchdogDuplicateResult(t *testing.T) {
	// INV2：同一 call_id 结果回灌两次 → unpaired_result 提示
	r := &rec{}
	w := New(r.pub)
	w.observe(callEvent("t1"))
	w.observe(resultEvent("t1"))
	w.observe(resultEvent("t1"))

	if r.count() != 1 {
		t.Fatalf("应有一条 unpaired 提示: %d", r.count())
	}
	if r.first().Payload["reason"] != "unpaired_result" {
		t.Fatalf("reason 应为 unpaired_result: %v", r.first().Payload)
	}
}

func TestWatchdogCleanPairing(t *testing.T) {
	// 正常配对：声明→结果→收口，零提示；收口后状态清理，同号复跑仍零提示
	r := &rec{}
	w := New(r.pub)
	for i := 0; i < 2; i++ {
		w.observe(callEvent("t1"))
		w.observe(resultEvent("t1"))
		w.observe(finishEvent())
	}
	if r.count() != 0 {
		t.Fatalf("正常配对不应提示: %+v", r.got)
	}
}

func TestWatchdogNoPub(t *testing.T) {
	// pub 未装配：提示静默丢弃，不 panic（不阻塞事件流语义）
	w := New(nil)
	w.observe(callEvent("t1"))
	w.observe(finishEvent())
	w.observe(resultEvent("t1"))
}

func TestWatchdogRunSmoke(t *testing.T) {
	// Run 订阅消费链路冒烟：订阅注册存在微小窗口（goroutine 启动 vs 首条发布），
	// 轮询重发直到提示到达——泄漏判定语义本身已被上面的同步用例覆盖。
	bus := eventbus.New()
	r := &rec{}
	w := New(r.pub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, bus)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		bus.Publish("s1", "c1", stepPtr(7), eventbus.EventToolCall, "info",
			map[string]any{"call_id": "t9", "tool": "use_skill"})
		bus.Publish("s1", "c1", stepPtr(7), eventbus.EventStepFinished, "info",
			map[string]any{"agent": "x", "status": "succeeded"})
		if r.count() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Run 订阅链路应在超时前收到事件并提示")
}
