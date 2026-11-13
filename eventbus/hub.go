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
// 投递语义的**缓冲面**与 progress.Bus 一致：订阅 channel 带缓冲，满即丢帧计数
// （不阻塞发布方——编排热路径不可被慢连接拖住）；丢帧由 SSE 的 DB 回放游标 + 全局
// tail 兜底补达，不丢语义。零订阅者的发布成本 = 一次 map 读 + 空 slice 遍历。
//
// # 与 eventbus.Bus 的**有意分叉**：退订时不关闭 channel
//
// 上游 runtime v0.10.0 把 progress.Bus 改成了「退订即 close」（runtime#32），
// eventbus.Bus 随之关闭；Hub **不**跟：
//
//   - Hub 的消费方是宿主的 SSE 循环，内核看不到那些循环有没有写 `v, ok := <-ch`；
//   - 关闭一个「按旧契约写」的 channel，后果是循环**空转**——CPU 打满、拿到的
//     全是零值事件、且一声不响。这是个失败形态**变绿**的改动，不该在看不见
//     消费方的那一层单方面做。
//
// 所以关闭的时机留给能看见宿主代码的那一层决定；本包把「不关」写成声明而不是
// 遗漏，由 TestHubSubscribeDoesNotCloseChannel 钉住。
//
// 消费方约定：用 `select` + 自己的 done 信号退出（**禁用 `for range ch`**，
// 它在本包永不返回）。
//
// # 跨实例面在 Relay 接缝之后（issue #145 多副本部署）
//
// 缺省无 Relay = 进程内限定（本页描述的语义一字不变）。多副本宿主装配
// Relay 把事件带出本进程（outbox 表 / 消息总线 / …），别处收到的事件经
// Inject 注回本进程订阅者——分发逻辑仍只有这一份，跨实例投递归宿主实现。
type Hub struct {
	mu      sync.RWMutex
	subs    map[string]map[chan Event]struct{}
	dropped atomic.Int64
	// relay 跨实例中继（nil = 进程内限定）。读写都在 mu 之下，
	// 但 Publish 的调用发生在解锁之后（见 Publish 注释的顺序说明）。
	relay Relay
}

// Relay 跨实例事件中继（宿主实现；issue #145）。
//
// 契约三条：
//   - **不得阻塞**发布热路径（编排热路径不可被跨实例投递拖住；需要缓冲/
//     重试的形态在实现内部做）；
//   - **失败语义自担**：Relay 没有返回值，投递失败由实现自己记账
//     （丢帧计数/告警），本包不重试不回滚——权威事件顺序与可靠面是宿主
//     落库游标（bq_event），Relay 只是尽力而为的实时面；
//   - 实现**可以回调** Hub.Inject（收到远端事件注回本地）——Publish 在
//     解锁后才调 Relay，回调不会死锁；但不得经 Relay 再次外发同一条事件
//     （回流即环，外发的去重是实现的责任）。
type Relay interface {
	// Publish 一条事件离开本进程（只在本地分发之后调用）。
	Publish(ev Event)
}

// hubBuf 单订阅者缓冲：SSE 消费速率远高于事件产生速率（前端逐帧渲染是快路径），
// 满缓冲即慢连接（浏览器挂起/网络停滞），丢帧合理。
const hubBuf = 256

// NewHub 构造。
func NewHub() *Hub {
	return &Hub{subs: map[string]map[chan Event]struct{}{}}
}

// SetRelay 装配跨实例中继（nil = 拆除，回到进程内限定）。并发安全。
func (h *Hub) SetRelay(r Relay) {
	h.mu.Lock()
	h.relay = r
	h.mu.Unlock()
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

// Publish 向会话订阅者投递（非阻塞；满缓冲丢帧计数），随后交给 Relay 外发。
// SessionID 为空的系统级事件（expert_toggled 等）无会话受众，直接忽略——
// 系统事件经 /audit 面，不走 SSE（Relay 同样不收：外发条件与本地受众一致）。
//
// 顺序：**本地先、Relay 后，且 Relay 在解锁之后调用**——本地分发是主契约
// （Relay 故障不牵连本地），解锁后调用让 Relay 可以安全回调 Inject。
// 代价是并发 Publish 的 Relay 外发顺序不作保证（权威顺序是宿主落库游标）。
func (h *Hub) Publish(ev Event) {
	h.mu.RLock()
	h.deliverLocked(ev)
	relay := h.relay
	h.mu.RUnlock()
	if relay != nil && ev.SessionID != "" {
		relay.Publish(ev)
	}
}

// Inject 把从别处收到的事件投给本进程订阅者（issue #145 跨实例回注）。
//
// 只做本地分发、**不回流 Relay**——回流即环（A 外发 → B 注入 → B 再外发
// → …）。与 Publish 同一分发语义（含空 SessionID 忽略、满缓冲丢帧计数）。
func (h *Hub) Inject(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	h.deliverLocked(ev)
}

// deliverLocked 向会话订阅者投递（调用方持 h.mu 读锁）。
func (h *Hub) deliverLocked(ev Event) {
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
