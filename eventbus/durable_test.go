package eventbus

// DurablePublisher 次序不变量回归（issue #160）：正常序（落库行==广播帧==返回值）、
// insert 失败不广播、onDrop 留痕、并发发布广播序==游标序。
//
// 命名延续本包测试的 T 编号习惯，D 前缀标记 durable 面。

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// sameEvent 事件同值判定（Event 含 map 字段不可 ==，逐字段比）。
func sameEvent(a, b Event) bool {
	if a.ID != b.ID || a.SessionID != b.SessionID || a.CaseID != b.CaseID ||
		a.Type != b.Type || a.Level != b.Level || !a.OccurredAt.Equal(b.OccurredAt) {
		return false
	}
	if (a.StepID == nil) != (b.StepID == nil) {
		return false
	}
	if a.StepID != nil && *a.StepID != *b.StepID {
		return false
	}
	return reflect.DeepEqual(a.Payload, b.Payload)
}

// boomStore 固定失败的 Store：Insert 恒返回 err（模拟 DB 故障/ctx 超时取消）。
type boomStore struct{ err error }

func (s boomStore) Insert(_ context.Context, _ Event) (uint64, error) { return 0, s.err }

// dropLog onDrop 留痕收集（onDrop 在发布锁内同步调用，写侧天然串行；
// 读侧在 Publish 返回之后，靠 mu 把 happens-before 写明白而非靠推导）。
type dropLog struct {
	mu    sync.Mutex
	evs   []Event
	cuses []error
}

func (l *dropLog) onDrop(ev Event, cause error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.evs = append(l.evs, ev)
	l.cuses = append(l.cuses, cause)
}

func (l *dropLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.evs)
}

// TD1 正常序：insert→取 ID→broadcast。表驱动三类入参：
//
//   - 携带陈旧 ID 的入参：ID 输入侧一律忽略（游标由存储铸造，不伪造）；
//   - 空 Level / 零 OccurredAt：归一发生在 Insert **之前**——落库行与广播帧
//     同一内容（归一若在广播侧做，落库行就缺省值，帧与行漂移）；
//   - 显式 Level / OccurredAt：不被覆盖。
//
// 每行同时断言：返回值 == 广播帧 == MemoryStore 落库行（三者同一事件），
// 游标从 1 连续递增（MemoryStore 语义）。
func TestDurablePublishPersistsThenBroadcasts(t *testing.T) {
	b := New()
	store := NewMemoryStore()
	p := NewDurablePublisher(b, store)
	ch, cancel := b.Subscribe(context.Background())
	defer cancel()

	base := time.Now()
	cases := []struct {
		name    string
		in      Event
		wantLvl string
		wantOcc time.Time // 零值 = 只断言非零
	}{
		{
			name:    "陈旧ID被忽略+零值归一",
			in:      Event{ID: 4096, SessionID: "s1", Type: EventStepStarted, Payload: map[string]any{"k": "v"}},
			wantLvl: "info",
			wantOcc: time.Time{},
		},
		{
			name:    "显式Level与OccurredAt不覆盖",
			in:      Event{SessionID: "s1", Type: EventStepFinished, Level: "warn", OccurredAt: base},
			wantLvl: "warn",
			wantOcc: base,
		},
		{
			name:    "nilPayload保持nil",
			in:      Event{SessionID: "s1", Type: EventIntentRouted},
			wantLvl: "info",
			wantOcc: time.Time{},
		},
	}

	prevID := uint64(0)
	for i, tc := range cases {
		out, err := p.Publish(context.Background(), tc.in)
		if err != nil {
			t.Fatalf("用例 %q 正常路径不应报错: %v", tc.name, err)
		}
		if out.ID != prevID+1 {
			t.Fatalf("用例 %q 游标应从 %d 连续递增, got %d", tc.name, prevID+1, out.ID)
		}
		prevID = out.ID
		if out.Level != tc.wantLvl {
			t.Fatalf("用例 %q Level 归一口径错: want %q, got %q", tc.name, tc.wantLvl, out.Level)
		}
		if tc.wantOcc.IsZero() {
			if out.OccurredAt.IsZero() {
				t.Fatalf("用例 %q 零值 OccurredAt 应归一为当前时刻", tc.name)
			}
		} else if !out.OccurredAt.Equal(tc.wantOcc) {
			t.Fatalf("用例 %q 显式 OccurredAt 不应被覆盖: want %v, got %v", tc.name, tc.wantOcc, out.OccurredAt)
		}
		if !reflect.DeepEqual(out.Payload, tc.in.Payload) {
			t.Fatalf("用例 %q payload 应原样透传: in=%v out=%v", tc.name, tc.in.Payload, out.Payload)
		}

		// 广播帧 == 返回值（订阅者看到的就是持久化的那行）。
		frame, ok := waitReceive(ch)
		if !ok {
			t.Fatalf("用例 %q 2s 内未收到广播帧", tc.name)
		}
		if !sameEvent(frame, out) {
			t.Fatalf("用例 %q 广播帧应与返回值同一事件:\n frame=%+v\n out  =%+v", tc.name, frame, out)
		}

		// 落库行 == 返回值（ID 以存储铸造值为准；行内不含陈旧 ID 4096）。
		store.mu.Lock()
		row := store.evs[i]
		store.mu.Unlock()
		if !sameEvent(row, out) {
			t.Fatalf("用例 %q 落库行应与返回值同一事件:\n row=%+v\n out=%+v", tc.name, row, out)
		}
	}
}

// TD2 insert 失败 ⇒ 不广播 + onDrop 留痕（表驱动 onDrop 缺省与注入两态）。
// 「宁可 SSE 少一帧，不伪造游标 ID」：失败帧不得出现在任何订阅者面前，
// 也不得烧掉游标（后续成功发布续用下一号，不跳号不重号）。
func TestDurablePublishInsertFailureDropsWithoutBroadcast(t *testing.T) {
	boom := errors.New("bq_event insert 失败")
	cases := []struct {
		name      string
		withHook  bool
		wantDrops int
	}{
		{name: "onDrop缺省nil静默丢弃", withHook: false, wantDrops: 0},
		{name: "onDrop注入留痕", withHook: true, wantDrops: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			var log dropLog
			p := NewDurablePublisher(b, boomStore{err: boom})
			if tc.withHook {
				p = NewDurablePublisher(b, boomStore{err: boom}, WithOnDrop(log.onDrop))
			}
			ch, cancel := b.Subscribe(context.Background())
			defer cancel()

			in := Event{SessionID: "s1", Type: EventStepStarted, Payload: map[string]any{"k": "v"}}
			out, err := p.Publish(context.Background(), in)
			if !errors.Is(err, boom) {
				t.Fatalf("应透传 insert 错误, got %v", err)
			}
			if !sameEvent(out, Event{}) {
				t.Fatalf("失败路径应返回零值事件, got %+v", out)
			}
			if got := log.count(); got != tc.wantDrops {
				t.Fatalf("onDrop 应留痕 %d 次, got %d", tc.wantDrops, got)
			}
			if tc.withHook {
				log.mu.Lock()
				dropped, cause := log.evs[0], log.cuses[0]
				log.mu.Unlock()
				if dropped.ID != 0 || dropped.SessionID != "s1" || dropped.Type != EventStepStarted {
					t.Fatalf("onDrop 应收到归一后的事件（ID 已清零）: %+v", dropped)
				}
				if !errors.Is(cause, boom) {
					t.Fatalf("onDrop 应收到 insert 错误, got %v", cause)
				}
			}
			// 失败帧不广播：订阅者一无所获。
			select {
			case ev := <-ch:
				t.Fatalf("insert 失败不得广播, 订阅者收到 %+v", ev)
			case <-time.After(50 * time.Millisecond):
			}

			// 恢复后续用下一号：失败的发布不烧游标、不留半条事件。
			good := NewDurablePublisher(b, NewMemoryStore())
			out2, err := good.Publish(context.Background(), Event{SessionID: "s1", Type: EventStepFinished})
			if err != nil || out2.ID != 1 {
				t.Fatalf("失败后成功发布应拿游标 1, got id=%d err=%v", out2.ID, err)
			}
			frame, ok := waitReceive(ch)
			if !ok || frame.ID != 1 {
				t.Fatalf("恢复后应收到 ID=1 的广播帧, got %+v ok=%v", frame, ok)
			}
		})
	}
}

// TD3 并发发布游标单调（比宿主两份拷贝强的一档）：发布全程持锁 ⇒ 广播序 ==
// 游标序，每个订阅者看到的 ID 严格递增；无丢帧、无重号、无漏号（与
// TestConcurrentPublishNoRaceNoDup 的「并发下 ID 不保证单调」注释对照——
// 那条说的是 Bus.Publish 的临时序号，durable 面的契约更强）。
func TestDurableConcurrentPublishCursorMonotonic(t *testing.T) {
	b := New()
	p := NewDurablePublisher(b, NewMemoryStore())

	const writers, per = 8, 24 // 总量 192，分批节奏见发布侧注释
	total := writers * per

	// 双订阅者并发收集（收包循环，与 eventbus_test T6 同款握手收尾；
	// ctr 供发布结束后等收满再关停——关早了会把缓冲里未收的帧落下）。
	type sink struct {
		mu   sync.Mutex
		got  []Event
		ctr  atomic.Int64
		stop chan struct{}
		done chan struct{}
	}
	collect := func() *sink {
		ch, cancel := b.Subscribe(context.Background())
		s := &sink{stop: make(chan struct{}), done: make(chan struct{})}
		go func() {
			defer cancel()
			defer close(s.done)
			for {
				select {
				case <-s.stop:
					return
				case e, ok := <-ch:
					if !ok {
						return
					}
					s.mu.Lock()
					s.got = append(s.got, e)
					s.mu.Unlock()
					s.ctr.Add(1)
				}
			}
		}()
		return s
	}
	s1, s2 := collect(), collect()

	// 发布侧：并发 Publish（分批节奏，与 eventbus_test T6 同款）——批内真并发，
	// 批间等收满，批总量 < 订阅缓冲 64 ⇒ 零丢帧，单调断言确定成立
	// （不垫节奏的话发布端跑赢收包端会真丢帧，那是 Bus 的有损语义，不是
	// durable 面要证的契约）。收集返回的游标（onDrop 不设——正常路径无 drop）。
	var (
		wg     sync.WaitGroup
		pubIDs []uint64
		pubMu  sync.Mutex
	)
	const batch = 3 // 每轮 writers*batch=24 条 < 缓冲 64
	for round := 0; round < per/batch; round++ {
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w, round int) {
				defer wg.Done()
				for i := 0; i < batch; i++ {
					out, err := p.Publish(context.Background(), Event{
						SessionID: "s1",
						Type:      EventStepStarted,
						Payload:   map[string]any{"w": w, "i": round*batch + i},
					})
					if err != nil {
						t.Errorf("并发发布不应失败: %v", err)
						return
					}
					pubMu.Lock()
					pubIDs = append(pubIDs, out.ID)
					pubMu.Unlock()
				}
			}(w, round)
		}
		wg.Wait()
		waitConsumed(t, &s1.ctr, int64((round+1)*batch*writers))
		waitConsumed(t, &s2.ctr, int64((round+1)*batch*writers))
	}
	close(s1.stop)
	close(s2.stop)
	<-s1.done
	<-s2.done

	// 游标面：存储侧连续 1..total，发布侧返回值唯一不重。
	store := p.store.(*MemoryStore)
	if got := store.seq; got != uint64(total) {
		t.Fatalf("存储游标应推进到 %d, got %d", total, got)
	}
	seen := make(map[uint64]bool, total)
	for _, id := range pubIDs {
		if seen[id] {
			t.Fatalf("发布返回游标重复: %d", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Fatalf("发布返回游标应恰 %d 个不重值, got %d", total, len(seen))
	}

	// 广播面：每个订阅者收满 total、无重号、ID 严格递增（广播序==游标序）；
	// 全程零丢帧（节奏垫批 + 收满再续）。
	for si, s := range []*sink{s1, s2} {
		s.mu.Lock()
		got := s.got
		s.mu.Unlock()
		if len(got) != total {
			t.Fatalf("订阅者 %d 应收满 %d 条（零丢帧）, got %d", si, total, len(got))
		}
		for j, e := range got {
			if e.ID != uint64(j+1) {
				t.Fatalf("订阅者 %d 第 %d 条应见游标 %d（广播序==游标序）, got %d", si, j, j+1, e.ID)
			}
		}
	}
	if dropped := b.Dropped(); dropped != 0 {
		t.Fatalf("节奏垫批下应零丢帧, dropped=%d", dropped)
	}
}

// TD4 装配点 fail fast：nil bus / nil store 在构造时 panic——伪造游标的事
// 不得留到运行时才暴露。
func TestDurablePublisherConstructorRejectsNil(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"nilBus", func() { NewDurablePublisher(nil, NewMemoryStore()) }},
		{"nilStore", func() { NewDurablePublisher(New(), nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("%s 应在构造时 panic", tc.name)
				}
			}()
			tc.fn()
		})
	}
}
