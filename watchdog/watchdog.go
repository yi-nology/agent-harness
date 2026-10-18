// Package watchdog 被动配对不变量监视（spec 2026-09-15-tool-identity-otel-guard-design P3）：
// 订阅事件总线校验工具调用配对不变量——只告警，不裁决（热路径裁决在 IdentityGuard）。
// INV1 环节收口仍有未执行声明 → reason=declared_not_executed；
// INV2 结果无对应声明（重复/幽灵结果）→ reason=unpaired_result。
// spec §8 INV3（事件面 checksum 抽验）不实现：tool_result 事件载荷截断到 2000 字符，
// 无法重算 checksum——内容级校验由守卫在源头全量覆盖，此处不重复。
package watchdog

import (
	"context"
	"fmt"
	"time"

	"github.com/yi-nology/agent-harness/eventbus"
)

// Pub 告警发布（与调度器 pub 同一落库广播路径：repo.InsertEvent + Bus.Broadcast）。
type Pub func(sessionID, caseID string, stepID *uint64, typ, level string, payload map[string]any)

// 残留清扫参数：事件总线对慢订阅丢帧/环节未收口的异常流会让部分 session|step 条目
// 永远等不到 StepFinished（单协程消费，无人再删）——按最后活动时间过期（2h 无活动
// 视为泄漏），低频清扫（10min 一轮）防长跑进程缓慢泄漏。
const (
	openIdleTTL = 2 * time.Hour
	sweepEvery  = 10 * time.Minute
)

// Watcher 配对不变量监视器。Run 单协程消费事件通道，无需加锁。
type Watcher struct {
	pub        Pub
	open       map[string]map[string]string // key(session|step) → call_id → 工具名
	lastActive map[string]time.Time         // key → 最后活动时刻（TTL 清扫依据）
	lastSweep  time.Time
}

// New 构造（pub 必填；nil 时告警静默丢弃——不阻塞事件流）。
func New(pub Pub) *Watcher {
	return &Watcher{pub: pub, open: map[string]map[string]string{}, lastActive: map[string]time.Time{}}
}

// Run 消费事件流直到 ctx 取消（main 装配期后台 goroutine）。
func (w *Watcher) Run(ctx context.Context, bus *eventbus.Bus) {
	ch, cancel := bus.Subscribe(ctx)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			w.observe(ev)
		}
	}
}

func (w *Watcher) observe(ev eventbus.Event) {
	w.maybeSweep()
	switch ev.Type {
	case eventbus.EventToolCall:
		callID, _ := ev.Payload["call_id"].(string)
		if callID == "" {
			return
		}
		tool, _ := ev.Payload["tool"].(string)
		key := w.key(ev)
		if w.open[key] == nil {
			w.open[key] = map[string]string{}
		}
		w.open[key][callID] = tool
		w.lastActive[key] = time.Now()
	case eventbus.EventToolResult:
		callID, _ := ev.Payload["call_id"].(string)
		if callID == "" {
			return
		}
		key := w.key(ev)
		if _, ok := w.open[key][callID]; ok {
			delete(w.open[key], callID)
			w.lastActive[key] = time.Now()
			return
		}
		// 结果无对应 open 声明：重复回灌或事件流乱序丢声明——观测可信度告警
		tool, _ := ev.Payload["tool"].(string)
		w.alert(ev, "unpaired_result", callID, tool)
	case eventbus.EventStepFinished:
		key := w.key(ev)
		for callID, tool := range w.open[key] {
			w.alert(ev, "declared_not_executed", callID, tool)
		}
		delete(w.open, key)
		delete(w.lastActive, key)
	}
}

// maybeSweep 低频 TTL 清扫：单协程消费内联触发，无锁。
func (w *Watcher) maybeSweep() {
	now := time.Now()
	if now.Sub(w.lastSweep) < sweepEvery {
		return
	}
	w.lastSweep = now
	for key, last := range w.lastActive {
		if now.Sub(last) > openIdleTTL {
			delete(w.open, key)
			delete(w.lastActive, key)
		}
	}
}

func (w *Watcher) key(ev eventbus.Event) string {
	step := "0"
	if ev.StepID != nil {
		step = fmt.Sprintf("%d", *ev.StepID)
	}
	return ev.SessionID + "|" + step
}

func (w *Watcher) alert(ev eventbus.Event, reason, callID, tool string) {
	if w.pub == nil {
		return
	}
	w.pub(ev.SessionID, ev.CaseID, ev.StepID, eventbus.EventToolPairingLeak, "warn", map[string]any{
		"reason": reason, "call_id": callID, "tool": tool, "step_event": ev.Type,
	})
}
