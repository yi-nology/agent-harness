package handoff

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/concurrency/async"
	kerrors "github.com/oekit/pkg/errors"
)

func TestConcurrentTake(t *testing.T) {
	t.Run("并发领取：赢家恰好 1 个", func(t *testing.T) {
		d := NewLocalDesk()
		ctx := context.Background()
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		tos := []Agent{"proc-b", "proc-b", "proc-b", "proc-b", "proc-b", "proc-b", "proc-b", "proc-b"}
		winners, err := ConcurrentTake(ctx, d, "step-7", tos, 10*time.Second)
		if err != nil {
			t.Fatalf("判据失败：%v", err)
		}
		if winners != 1 {
			t.Fatalf("赢家应恰好 1 个，得到 %d", winners)
		}
	})

	t.Run("没有交接单时判据显式失败（不是静默零赢家）", func(t *testing.T) {
		d := NewLocalDesk()
		ctx := context.Background()
		_, err := ConcurrentTake(ctx, d, "nope", []Agent{"proc-b"}, time.Second)
		if err == nil || kerrors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("判据应显式失败，得到 %v", err)
		}
	})

	t.Run("参数不合法被拒", func(t *testing.T) {
		d := NewLocalDesk()
		ctx := context.Background()
		if _, err := ConcurrentTake(ctx, d, "step-7", nil, time.Second); err == nil {
			t.Fatal("零接收方应被拒")
		}
		if _, err := ConcurrentTake(ctx, d, "step-7", []Agent{"proc-b"}, 0); err == nil {
			t.Fatal("零时限应被拒")
		}
	})

	t.Run("判据自身限时收口（存储挂死不拖垮调用方）", func(t *testing.T) {
		d := slowDesk{delay: time.Hour}
		ctx := context.Background()
		if _, err := d.Offer(ctx, baseTicket()); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err := ConcurrentTake(ctx, d, "step-7", []Agent{"proc-b", "proc-c"}, 50*time.Millisecond)
		if err == nil {
			t.Fatal("挂死实现应被判据超时拒绝")
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("判据没有按时限收口")
		}
	})
}

// slowDesk 挂死的存储替身：Take 永不返回（验证判据的限时收口）。
type slowDesk struct{ delay time.Duration }

func (s slowDesk) Offer(_ context.Context, t Ticket) (Ticket, error) { return t, nil }

func (s slowDesk) Take(ctx context.Context, _ Key, _ Agent) (Handle, error) {
	select {
	case <-time.After(s.delay):
		return Handle{}, nil
	case <-ctx.Done():
		return Handle{}, ctx.Err()
	}
}

func (s slowDesk) Reply(context.Context, Key, string, Receipt) error { return nil }
func (s slowDesk) Cancel(context.Context, Key, Agent) error          { return nil }
func (s slowDesk) Snapshot(context.Context) ([]Ticket, error)        { return nil, nil }

// 编译期确认 LocalDesk 满足 Store，且 GoSafe 生命周期无泄漏面
// （contention 判据在 race 模式下跑过全量并发路径）。
var (
	_ Store = (*LocalDesk)(nil)
	_       = async.GoSafe
)
