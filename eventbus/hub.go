package eventbus

import (
	"sync"
	"sync/atomic"
)

// Hub 按会话键控的事件分发器（SSE 单路桥接，2026-10-03 会话平面审计）：
// 订阅时绑定 session_id，发布只投递该会话的订阅者——SSE 连接不再为无关会话的事件
// 做逐条过滤（旧形态每连接收全实例事件再自行丢弃，成本 = 连接数 × 全局事件数），
// 分发成本随活跃会话数而非连接总数增长。
//
// 投递语义与 progress.Bus 一致：订阅 channel 带缓冲，满即丢帧计数（不阻塞发布方——
// 编排热路径不可被慢连接拖住）；丢帧由 SSE 的 DB 回放游标 + 全局 tail 兜底补达，
// 不丢语义。零订阅者的发布成本 = 一次 map 读 + 空 slice 遍历。
type Hub struct {
	mu      sync.RWMutex
	subs    map[string]map[chan Event]struct{}
	dropped atomic.Int64
}

// hubBuf 单订阅者缓冲：SSE 消费速率远高于事件产生速率（前端逐帧渲染是快路径），
// 满缓冲即慢连接（浏览器挂起/网络停滞），丢帧合理。
const hubBuf = 256

// NewHub 构造。
func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan Event]struct{}{}}
}

// Subscribe 订阅指定会话的事件流（返回取消订阅函数，幂等安全）。
func (h *Hub) Subscribe(sessionID string) (<-chan Event, func()) {
	ch := make(chan Event, hubBuf)
	h.mu.Lock()
	if h.subs[sessionID] == nil {
		h.subs[sessionID] = map[chan Event]struct{}{}
	}
	h.subs[sessionID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if set := h.subs[sessionID]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.subs, sessionID)
			}
		}
		h.mu.Unlock()
	}
}

// Publish 向会话订阅者投递（非阻塞；满缓冲丢帧计数）。SessionID 为空的系统级事件
// （expert_toggled 等）无会话受众，直接忽略——系统事件经 /audit 面，不走 SSE。
func (h *Hub) Publish(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[ev.SessionID] {
		select {
		case ch <- ev:
		default:
			h.dropped.Add(1)
		}
	}
}

// Dropped 慢订阅丢帧累计（可观测）。
func (h *Hub) Dropped() int64 { return h.dropped.Load() }
