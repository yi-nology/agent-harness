package eventbus

// Hub 按会话键控分发语义回归（SSE 单路桥接）：只投本会话、零订阅零成本、慢订阅丢帧计数。

import (
	"sync"
	"testing"
	"time"
)

func TestHubPublishesOnlyToSessionSubscribers(t *testing.T) {
	h := NewHub()
	chA, stopA := h.Subscribe("sess-a")
	defer stopA()
	chB, stopB := h.Subscribe("sess-b")
	defer stopB()

	h.Publish(Event{ID: 1, SessionID: "sess-a", Type: "x"})
	assertRecv(t, chA, 1)
	select {
	case ev := <-chB:
		t.Fatalf("sess-b 不应收到 sess-a 的事件: %+v", ev)
	default:
	}
	// 无订阅会话：发布零副作用不 panic。
	h.Publish(Event{ID: 2, SessionID: "sess-nobody", Type: "x"})
	// 系统级事件（空 session）无会话受众，忽略。
	h.Publish(Event{ID: 3, Type: "expert_toggled"})
}

func TestHubUnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch, stop := h.Subscribe("sess-a")
	stop()
	stop() // 幂等
	h.Publish(Event{ID: 1, SessionID: "sess-a"})
	select {
	case ev := <-ch:
		t.Fatalf("退订后不应再收到事件: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	// 重订阅收新帧。
	ch2, stop2 := h.Subscribe("sess-a")
	defer stop2()
	h.Publish(Event{ID: 2, SessionID: "sess-a"})
	assertRecv(t, ch2, 2)
}

func TestHubSlowSubscriberDropsAndCounts(t *testing.T) {
	h := NewHub()
	ch, stop := h.Subscribe("sess-a")
	defer stop()
	for i := 1; i <= hubBuf+10; i++ { // 不消费塞满缓冲再溢出
		h.Publish(Event{ID: uint64(i), SessionID: "sess-a"})
	}
	if got := h.Dropped(); got != 10 {
		t.Fatalf("溢出 %d 帧应计数 10, got %d", 10, got)
	}
	// 消费侧仍按序拿到前 hubBuf 帧。
	first := <-ch
	if first.ID != 1 {
		t.Fatalf("缓冲帧应保序, 首帧 ID=%d", first.ID)
	}
}

// TestHubSubscribeDoesNotCloseChannel 钉住 Hub 的「退订不关闭」声明。
//
// 这条与 TestSubscribeChannelClosesOnCancel（Bus 关）成对存在：两个投递面在这里
// **有意分叉**，各自被钉住才叫声明；只钉一侧的话，另一侧哪天跟着上游改了也没人
// 知道——而那个改动会让宿主按旧契约写的 SSE 循环开始空转（零值事件 + CPU 打满，
// 且不报错）。
func TestHubSubscribeDoesNotCloseChannel(t *testing.T) {
	h := NewHub()
	ch, stop := h.Subscribe("sess-a")
	stop()

	h.Publish(Event{ID: 1, SessionID: "sess-a"})
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("Hub 退订后不应关闭 channel（关闭会让未查 ok 的宿主循环空转）")
		}
		t.Fatal("Hub 退订后不应再收到事件")
	case <-time.After(50 * time.Millisecond):
		// 既没读到事件、也没读到关闭 —— 正是本包声明的那一态。
	}
}

func TestHubConcurrentPublishSubscribe(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup
	stopFns := make([]func(), 0, 8)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		ch, stop := h.Subscribe("sess-x")
		stopFns = append(stopFns, stop)
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Hub 退订**不**关 channel——与 eventbus.Bus（退订即 close）有意分叉，
			// 理由见 hub.go 包注释：宿主侧 SSE 循环未必查 ok，内核在这里单方面
			// 关闭会让它们空转。消费者经 done 信号退出，禁用 range ch。
			// 该分叉由 TestHubSubscribeDoesNotCloseChannel 钉住。
			for {
				select {
				case <-ch:
				case <-done:
					return
				}
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		h.Publish(Event{ID: uint64(i), SessionID: "sess-x"})
	}
	close(done)
	for _, stop := range stopFns {
		stop()
	}
	wg.Wait() // -race 下验证无数据竞争
}

func assertRecv(t *testing.T, ch <-chan Event, wantID uint64) {
	t.Helper()
	select {
	case ev := <-ch:
		if ev.ID != wantID {
			t.Fatalf("应收到 ID=%d, got %d", wantID, ev.ID)
		}
	case <-time.After(time.Second):
		t.Fatalf("未收到 ID=%d", wantID)
	}
}
