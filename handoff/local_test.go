package handoff

import (
	"context"
	"sync"
	"testing"
	"time"

	kerrors "github.com/oekit/pkg/errors"
)

// fakeClock 手动推进的假时钟（过期语义确定性测，不靠 sleep）。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestLocalDeskLifecycle(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	ctx := context.Background()

	t.Run("挂出 → 领取 → 回执收口", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		tk, err := d.Offer(ctx, baseTicket())
		if err != nil {
			t.Fatal(err)
		}
		if tk.State != StateOffered || tk.Generation != 1 || tk.Deadline.IsZero() {
			t.Fatalf("首代单错误：%+v", tk)
		}
		h, err := d.Take(ctx, "step-7", "proc-b")
		if err != nil {
			t.Fatal(err)
		}
		if h.Fence != 1 || h.Token == "" || !h.Until.Equal(tk.Deadline) {
			t.Fatalf("handle 错误：%+v", h)
		}
		if err := d.Reply(ctx, "step-7", h.Token, Receipt{Status: ReceiptDone, Summary: "ok"}); err != nil {
			t.Fatal(err)
		}
		got, _ := d.Snapshot(ctx)
		if got[0].State != StateClosed || got[0].Holder != "proc-b" || got[0].ReceiptAt.IsZero() {
			t.Fatalf("收口单错误：%+v", got[0])
		}
	})

	t.Run("重放：同 Key 重复挂出返回原单", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		first, _ := d.Offer(ctx, baseTicket())
		again, err := d.Offer(ctx, baseTicket())
		if err != nil {
			t.Fatal(err)
		}
		if again.Generation != first.Generation || !again.Deadline.Equal(first.Deadline) {
			t.Fatalf("重放开出了新单：%+v vs %+v", again, first)
		}
	})

	t.Run("终态后重新挂出 = 新一代", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		if err := d.Cancel(ctx, "step-7", "proc-a"); err != nil {
			t.Fatal(err)
		}
		tk, err := d.Offer(ctx, baseTicket())
		if err != nil {
			t.Fatal(err)
		}
		if tk.Generation != 2 || tk.State != StateOffered {
			t.Fatalf("重新交接应开第二代：%+v", tk)
		}
	})

	t.Run("非指定接收方被拒（回执寻址的领取半边）", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Take(ctx, "step-7", "proc-c"); !kerrors.Is(err, ErrNotAddressee) {
			t.Fatalf("期望 ErrNotAddressee，得到 %v", err)
		}
	})

	t.Run("过期后领取与回执都拒收（懒式过期落账）", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		clock.Advance(2 * time.Minute)
		if _, err := d.Take(ctx, "step-7", "proc-b"); !kerrors.Is(err, ErrExpired) {
			t.Fatalf("期望 ErrExpired，得到 %v", err)
		}
		got, _ := d.Snapshot(ctx)
		if got[0].State != StateExpired {
			t.Fatalf("逾期单应落 expired：%+v", got[0])
		}
		if _, err := d.Offer(ctx, baseTicket()); err != nil { // 重新交接 = 新一代
			t.Fatal(err)
		}
		h, err := d.Take(ctx, "step-7", "proc-b")
		if err != nil {
			t.Fatal(err)
		}
		clock.Advance(2 * time.Minute)
		err = d.Reply(ctx, "step-7", h.Token, Receipt{Status: ReceiptDone, Summary: "ok"})
		if !kerrors.Is(err, ErrExpired) {
			t.Fatalf("迟到回执应被拒（ErrExpired），得到 %v", err)
		}
	})

	t.Run("回执凭令牌：错令牌拒收，凭内容认不了人", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		h, err := d.Take(ctx, "step-7", "proc-b")
		if err != nil {
			t.Fatal(err)
		}
		err = d.Reply(ctx, "step-7", h.Token+"-wrong", Receipt{Status: ReceiptDone, Summary: "ok"})
		if !kerrors.Is(err, ErrLeaseLost) {
			t.Fatalf("期望 ErrLeaseLost，得到 %v", err)
		}
	})

	t.Run("不合法回执不动状态", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		h, _ := d.Take(ctx, "step-7", "proc-b")
		if err := d.Reply(ctx, "step-7", h.Token, Receipt{Status: ReceiptDone}); err == nil {
			t.Fatal("缺结论的回执应被拒")
		}
		got, _ := d.Snapshot(ctx)
		if got[0].State != StateAccepted {
			t.Fatalf("被拒回执不得改变状态：%+v", got[0])
		}
	})

	t.Run("收回只归源方且只在待领取", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		if err := d.Cancel(ctx, "step-7", "proc-z"); !kerrors.Is(err, ErrNotSource) {
			t.Fatalf("期望 ErrNotSource，得到 %v", err)
		}
		if _, err := d.Take(ctx, "step-7", "proc-b"); err != nil {
			t.Fatal(err)
		}
		if err := d.Cancel(ctx, "step-7", "proc-a"); !kerrors.Is(err, ErrTaken) {
			t.Fatalf("已领取后收回应 ErrTaken，得到 %v", err)
		}
	})

	t.Run("终态动作全部拒收", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		if err := d.Cancel(ctx, "step-7", "proc-a"); err != nil {
			t.Fatal(err)
		}
		if err := d.Cancel(ctx, "step-7", "proc-a"); !kerrors.Is(err, ErrAlreadySettled) {
			t.Fatalf("期望 ErrAlreadySettled，得到 %v", err)
		}
		if _, err := d.Take(ctx, "step-7", "proc-b"); !kerrors.Is(err, ErrAlreadySettled) {
			t.Fatalf("期望 ErrAlreadySettled，得到 %v", err)
		}
		if _, err := d.Take(ctx, "nope", "proc-b"); !kerrors.Is(err, ErrUnknownKey) {
			t.Fatalf("期望 ErrUnknownKey，得到 %v", err)
		}
	})

	t.Run("快照确定性", func(t *testing.T) {
		d := NewLocalDesk(WithClock(clock.Now))
		for _, k := range []string{"s3", "s1", "s2"} {
			tk := baseTicket()
			tk.Key = Key(k)
			if _, err := d.Offer(ctx, tk); err != nil {
				t.Fatal(err)
			}
		}
		got, _ := d.Snapshot(ctx)
		if len(got) != 3 || got[0].Key != "s1" || got[2].Key != "s3" {
			t.Fatalf("快照应按 Key 升序：%+v", got)
		}
	})
}
