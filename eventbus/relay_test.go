package eventbus

// Relay 接缝（issue #145 多副本部署）的判据：
//   - 装配 Relay 后 Publish 本地先、外发后；不装 = 进程内限定（现状）；
//   - Inject 只本地分发、不回流 Relay（回流即环）；
//   - Relay 可以回调 Inject（解锁后调用，不许死锁——这条是顺序设计的判据）；
//   - 空 SessionID 系统事件不外发（外发条件与本地受众一致）。

import (
	"sync"
	"testing"
	"time"
)

// captureRelay 记录外发事件的替身。
type captureRelay struct {
	mu  sync.Mutex
	got []Event
}

func (c *captureRelay) Publish(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ev)
}

func (c *captureRelay) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.got))
	copy(out, c.got)
	return out
}

func TestHubRelay外发与本地分发(t *testing.T) {
	h := NewHub()
	rel := &captureRelay{}
	h.SetRelay(rel)
	ch, stop := h.Subscribe("sess-a")
	defer stop()

	h.Publish(Event{ID: 1, SessionID: "sess-a", Type: "x"})
	assertRecv(t, ch, 1)
	got := rel.snapshot()
	if len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("Relay 应收到该事件，实得 %+v", got)
	}

	// 空 SessionID 系统事件：无本地受众，也不外发（条件一致）。
	h.Publish(Event{ID: 2, Type: "expert_toggled"})
	if n := len(rel.snapshot()); n != 1 {
		t.Fatalf("系统事件不应外发（实得 %d 条）", n)
	}

	// 拆除 Relay = 回到进程内限定。
	h.SetRelay(nil)
	h.Publish(Event{ID: 3, SessionID: "sess-a", Type: "x"})
	assertRecv(t, ch, 3)
	if n := len(rel.snapshot()); n != 1 {
		t.Fatalf("拆除后不应再外发（实得 %d 条）", n)
	}
}

func TestHubInject只本地不回流(t *testing.T) {
	h := NewHub()
	rel := &captureRelay{}
	h.SetRelay(rel)
	ch, stop := h.Subscribe("sess-a")
	defer stop()

	// 别处收到的事件注回：本地订阅者拿到，Relay 不再收到（回流即环）。
	h.Inject(Event{ID: 7, SessionID: "sess-a", Type: "remote"})
	assertRecv(t, ch, 7)
	if n := len(rel.snapshot()); n != 0 {
		t.Fatalf("Inject 不得回流 Relay（实得 %d 条）", n)
	}
	// 与 Publish 同一分发语义：空 SessionID 忽略。
	h.Inject(Event{ID: 8, Type: "system"})
}

// TestHubRelay可回调Inject不死锁 —— Publish 在解锁后才调 Relay 的设计判据：
// Relay 实现收到事件后把「另一实例来的事件」注回同一 Hub，若 Publish 持锁
// 调 Relay，这里会死锁（RWMutex 不可重入）。
func TestHubRelay可回调Inject不死锁(t *testing.T) {
	h := NewHub()
	ch, stop := h.Subscribe("sess-a")
	defer stop()

	done := make(chan struct{})
	h.SetRelay(relayFunc(func(ev Event) {
		// 模拟宿主的跨实例回注（B 副本收到 A 的外发后注入本地订阅者）。
		h.Inject(Event{ID: ev.ID + 100, SessionID: ev.SessionID, Type: "echo"})
		close(done)
	}))

	h.Publish(Event{ID: 1, SessionID: "sess-a", Type: "x"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Relay 回调 Inject 死锁——Publish 必须在解锁后调用 Relay")
	}
	assertRecv(t, ch, 1)
	assertRecv(t, ch, 101)
}

// relayFunc 把函数变成 Relay（轻量替身，免再定义一个结构体）。
type relayFunc func(Event)

func (f relayFunc) Publish(ev Event) { f(ev) }

// TestHubRelay并发装配与发布 race 判据（-race 下跑）：SetRelay 与 Publish
// 并发不撕裂、不漏锁。
func TestHubRelay并发装配与发布(t *testing.T) {
	h := NewHub()
	rel := &captureRelay{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				h.Publish(Event{ID: uint64(n*100 + j), SessionID: "sess-x", Type: "x"})
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			h.SetRelay(rel)
			h.SetRelay(nil)
		}
	}()
	wg.Wait()
}
