package eventbus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// drainAll 非阻塞抽干通道，返回当前已缓冲的全部事件。
func drainAll(ch <-chan Event) []Event {
	var got []Event
	for {
		select {
		case e := <-ch:
			got = append(got, e)
		default:
			return got
		}
	}
}

// waitReceive 带超时收一条事件；超时返回 ok=false（可在任意 goroutine 使用）。
func waitReceive(ch <-chan Event) (Event, bool) {
	select {
	case e := <-ch:
		return e, true
	case <-time.After(2 * time.Second):
		return Event{}, false
	}
}

// waitConsumed 轮询等待订阅者消费到 target 条（-race 下读 goroutine 可能长停顿，
// 发布端按批等消费保证"零丢弃"断言确定成立）。
func waitConsumed(t *testing.T, ctr *atomic.Int64, target int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for ctr.Load() < target {
		if time.Now().After(deadline) {
			t.Fatalf("订阅者消费停滞: %d/%d", ctr.Load(), target)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// T1 Publish 赋 ID/时间戳/级别缺省：New 从 1 起连续递增，空 level 归一 info。
func TestPublishAssignsFields(t *testing.T) {
	b := New()
	step := uint64(3)
	e1 := b.Publish("s1", "c1", &step, EventStepStarted, "", map[string]any{"k": "v"})
	e2 := b.Publish("s1", "c1", nil, "step_finished", "warn", nil)

	if e1.ID != 1 || e2.ID != 2 {
		t.Fatalf("序号应从 1 连续递增, got %d,%d", e1.ID, e2.ID)
	}
	if e1.SessionID != "s1" || e1.CaseID != "c1" || *e1.StepID != 3 {
		t.Fatalf("字段透传失真: %+v", e1)
	}
	if e1.Level != "info" {
		t.Fatalf("空 level 应归一 info, got %q", e1.Level)
	}
	if e2.Level != "warn" {
		t.Fatalf("显式 level 不应被覆盖, got %q", e2.Level)
	}
	if e1.OccurredAt.IsZero() || e2.OccurredAt.Before(e1.OccurredAt) {
		t.Fatalf("OccurredAt 应非零且单调: %v -> %v", e1.OccurredAt, e2.OccurredAt)
	}
	if e1.Payload["k"] != "v" {
		t.Fatalf("payload 透传失真: %v", e1.Payload)
	}
	if e2.Payload != nil {
		t.Fatalf("nil payload 应保持 nil, got %v", e2.Payload)
	}
}

// T2 Broadcast 透传落库 ID：生产路径先 repo.InsertEvent 拿 bq_event 自增 ID 再广播，
// 总线不得重编号——SSE after 游标与回放 ID 同一单调空间；空 level 归一 info。
func TestBroadcastPassthroughAndSeqContinuity(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe(context.Background())
	defer cancel()

	// 显式 ID 原样透传（模拟 bq_event 自增水位 4096 起）。
	prev := uint64(4095)
	for i := 0; i < 3; i++ {
		b.Broadcast(Event{ID: prev + 1, SessionID: "s", Type: EventStepStarted})
		e, ok := waitReceive(ch)
		if !ok {
			t.Fatal("2s 内未收到广播事件")
		}
		if e.ID != prev+1 {
			t.Fatalf("Broadcast 应透传落库 ID: 期望 %d, got %d", prev+1, e.ID)
		}
		if e.Level != "info" {
			t.Fatalf("空 level 应归一 info, got %q", e.Level)
		}
		prev = e.ID
	}

	// 显式 level 不被覆盖。
	b.Broadcast(Event{ID: prev + 1, SessionID: "s", Type: "step_finished", Level: "warn"})
	e2, ok := waitReceive(ch)
	if !ok {
		t.Fatal("2s 内未收到广播事件")
	}
	if e2.Level != "warn" {
		t.Fatalf("显式 level 不应被覆盖, got %q", e2.Level)
	}

	// Publish 临时序号（未落库路径）与 Broadcast 的透传 ID 互不干扰、各自连续。
	e := b.Publish("s", "", nil, EventIntentRouted, "", nil)
	if e.ID != 1 {
		t.Fatalf("Publish 临时序号应从 1 起, got %d", e.ID)
	}
	if e3 := b.Publish("s", "", nil, EventIntentRouted, "", nil); e3.ID != 2 {
		t.Fatalf("Publish 临时序号应连续, got %d", e3.ID)
	}
}

// T3 多订阅者扇出：各自按发布顺序收到同一事件序列（SSE 与 bq_event writer 双写保序）。
func TestFanOutPreservesOrder(t *testing.T) {
	b := New()
	ctx := context.Background()
	const n = 20
	subs := make([]<-chan Event, 3)
	cancels := make([]func(), 3)
	for i := range subs {
		subs[i], cancels[i] = b.Subscribe(ctx)
		defer cancels[i]()
	}

	var want []uint64
	for i := 0; i < n; i++ {
		want = append(want, b.Publish("s", "", nil, EventStepStarted, "info", map[string]any{"i": i}).ID)
	}
	for i, ch := range subs {
		got := drainAll(ch)
		if len(got) != n {
			t.Fatalf("订阅者 %d 应收满 %d 条, got %d", i, n, len(got))
		}
		for j, e := range got {
			if e.ID != want[j] || e.Payload["i"] != j {
				t.Fatalf("订阅者 %d 第 %d 条失序: got ID=%d 期望 %d", i, j, e.ID, want[j])
			}
		}
	}
	if b.Dropped() != 0 {
		t.Fatalf("缓冲未满不应丢弃, dropped=%d", b.Dropped())
	}
}

// T4 慢订阅丢弃有损语义：慢订阅者不拖慢发布、只丢掉缓冲溢出部分（Dropped 可归因），
// 消费及时的订阅者一条不丢且保序（发布端按批等待消费，保证该断言确定成立）。
func TestSlowSubscriberDropDoesNotAffectOthers(t *testing.T) {
	b := New()
	slow, cancelSlow := b.Subscribe(context.Background())
	defer cancelSlow()
	fast, cancelFast := b.Subscribe(context.Background())
	defer cancelFast()

	const total, bufCap, batch = 100, 64, 25
	var (
		fastGot []Event
		mu      sync.Mutex
		ctr     atomic.Int64
	)
	fastDone := make(chan struct{})
	go func() { // 快订阅者收包循环：收满哨兵为止
		defer close(fastDone)
		for {
			e, ok := waitReceive(fast)
			if !ok {
				t.Error("快订阅者 2s 内未收到事件")
				return
			}
			if e.Type == "done" {
				return
			}
			mu.Lock()
			fastGot = append(fastGot, e)
			mu.Unlock()
			ctr.Add(1)
		}
	}()

	// 分批发布（批 < 缓冲上限），批间等快订阅者消化完——其永远不会因积压被丢。
	sent := 0
	for sent < total {
		end := sent + batch
		if end > total {
			end = total
		}
		for ; sent < end; sent++ {
			b.Publish("s", "", nil, EventStepStarted, "info", map[string]any{"i": sent})
		}
		waitConsumed(t, &ctr, int64(sent))
	}
	b.Publish("s", "", nil, "done", "info", nil) // 结束哨兵（对慢订阅者也计一次丢弃）
	select {
	case <-fastDone:
	case <-time.After(2 * time.Second):
		t.Fatal("快订阅者未收齐")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(fastGot) != total {
		t.Fatalf("快订阅者应收全 %d 条（慢订阅不影响他人）, got %d", total, len(fastGot))
	}
	for i, e := range fastGot {
		if e.ID != uint64(i+1) || e.Payload["i"] != i {
			t.Fatalf("快订阅者第 %d 条失序/失真: %+v", i, e)
		}
	}

	// 慢订阅者无人消费：恰留缓冲上限条最早事件，保序。
	slowGot := drainAll(slow)
	if len(slowGot) != bufCap {
		t.Fatalf("慢订阅者应恰收缓冲上限 %d 条, got %d", bufCap, len(slowGot))
	}
	for i, e := range slowGot {
		if e.ID != uint64(i+1) {
			t.Fatalf("慢订阅者应保留最早 %d 条且保序: 第 %d 条 ID=%d", bufCap, i, e.ID)
		}
	}
	if got := b.Dropped(); got != int64(total+1-bufCap) {
		t.Fatalf("丢弃计数应 %d, got %d", total+1-bufCap, got)
	}
}

// T5 退订后不再收事件；cancel 可重复调用且 ctx 结束自动退订。
func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe(context.Background())
	b.Publish("s", "", nil, EventSessionCreated, "info", nil)
	if _, ok := waitReceive(ch); !ok {
		t.Fatal("退订前应能收到事件")
	}

	cancel()
	cancel() // 幂等，不应 panic
	b.Publish("s", "", nil, EventStepStarted, "info", nil)
	b.Publish("s", "", nil, "step_finished", "info", nil)
	if got := drainAll(ch); len(got) != 0 {
		t.Fatalf("退订后不应再收事件, got %v", got)
	}

	// ctx 取消 = 自动退订（由监听 goroutine 异步删订阅，轮询至生效为止）。
	ctx, ctxCancel := context.WithCancel(context.Background())
	ch2, cancel2 := b.Subscribe(ctx)
	defer cancel2()
	ctxCancel()
	unsubscribed := false
	for i := 0; i < 100 && !unsubscribed; i++ {
		b.Publish("s", "", nil, EventStepStarted, "info", nil)
		select {
		case <-ch2: // 退订未生效，稍候重试
			time.Sleep(time.Millisecond)
		default:
			// Publish 同步投递/丢弃，返回后仍收不到 = 已注销。
			unsubscribed = true
		}
	}
	if !unsubscribed {
		t.Fatal("ctx 取消后仍在收事件（自动退订未生效）")
	}
}

// T6 并发发布不竞争不丢不重：多 goroutine 并发 Publish（批内真并发），
// 两路订阅者收满、无重复 ID、字段完整（-race 下运行）。
func TestConcurrentPublishNoRaceNoDup(t *testing.T) {
	b := New()
	ch1, c1 := b.Subscribe(context.Background())
	ch2, c2 := b.Subscribe(context.Background())
	defer c1()
	defer c2()

	const writers, per, batch = 8, 48, 4 // 每轮 8*4=32 条 < 缓冲 64，读端零丢弃
	total := writers * per

	type sink struct {
		ctr  atomic.Int64
		mu   sync.Mutex
		got  []Event
		stop chan struct{}
		done chan struct{}
	}
	collect := func(ch <-chan Event) *sink {
		s := &sink{stop: make(chan struct{}), done: make(chan struct{})}
		go func() {
			defer close(s.done)
			for {
				select {
				case <-s.stop:
					return
				case e := <-ch:
					s.mu.Lock()
					s.got = append(s.got, e)
					s.mu.Unlock()
					s.ctr.Add(1)
				}
			}
		}()
		return s
	}
	s1, s2 := collect(ch1), collect(ch2)

	var wg sync.WaitGroup
	for round := 0; round < per/batch; round++ {
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w, round int) {
				defer wg.Done()
				for i := 0; i < batch; i++ {
					b.Publish("s", "", nil, EventStepStarted, "info",
						map[string]any{"w": w, "i": round*batch + i})
				}
			}(w, round)
		}
		wg.Wait()
		waitConsumed(t, &s1.ctr, int64((round+1)*batch*writers))
		waitConsumed(t, &s2.ctr, int64((round+1)*batch*writers))
	}

	// 收尾：停掉收包循环（HB 由 done 关闭建立），再断言。
	close(s1.stop)
	close(s2.stop)
	<-s1.done
	<-s2.done

	seen := make(map[uint64]bool, total)
	for _, s := range []*sink{s1, s2} {
		// 注意：ID 在进入广播锁之前由 seq.Add 分配，多发布者并发下各订阅者
		// 收到的 ID 不保证单调（单发布者保序由 T3/T4 覆盖）——只断言不丢不重。
		s.mu.Lock()
		got := s.got
		s.mu.Unlock()
		if len(got) != total {
			t.Fatalf("应收满 %d 条, got %d", total, len(got))
		}
		subSeen := make(map[uint64]bool, total)
		for _, e := range got {
			if subSeen[e.ID] {
				t.Fatalf("同一事件应只送达一次: ID=%d 重复", e.ID)
			}
			subSeen[e.ID] = true
			if e.SessionID != "s" || e.Payload == nil {
				t.Fatalf("事件字段失真: %+v", e)
			}
		}
	}
	for _, e := range s1.got {
		seen[e.ID] = true
	}
	if len(seen) != total {
		t.Fatalf("并发发布 ID 应唯一不重不漏, 去重后 %d", len(seen))
	}
	if b.Dropped() != 0 {
		t.Fatalf("收满 total 要求零丢弃, dropped=%d", b.Dropped())
	}
}

// T7 无订阅者发布是安全 no-op（engine 先于 SSE 建连发布不炸、不计数）。
func TestPublishWithoutSubscribers(t *testing.T) {
	b := New()
	for i := 0; i < 5; i++ {
		e := b.Publish("s", "", nil, EventIntentRouted, "info", nil)
		if e.ID != uint64(i+1) {
			t.Fatalf("无订阅者也应正常赋号, got %d", e.ID)
		}
	}
	if b.Dropped() != 0 {
		t.Fatalf("无订阅者不应计丢弃, dropped=%d", b.Dropped())
	}
}
