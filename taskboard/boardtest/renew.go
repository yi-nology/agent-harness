// renew.go —— 过期接管与租约续期的契约场景（issue #87）。
//
// 四条判据，两两成对：
//
//   - 过期接管后旧持有者提交被拒（迟到提交有判据可拒）↔ 长任务续期后可继续
//     持有（续期给出路）；
//   - 接管代次严格递增（代次是真判据不是装饰）↔ 旧令牌续期被拒
//     （假续期延长的是别人的闸）。
//
// 每条场景的判据核心是返回 error 的 checkXxx——变异验证（见 boardtest_test.go）
// 直接调核心，摘掉判据必红。
package boardtest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// runExpiredTakeover 租约过期可被接管（持有着崩了，不接管会留下永久占用）。
func runExpiredTakeover(t *testing.T, factory Factory) {
	t.Helper()
	b, clock := newBoard(t, factory)
	seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
	if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		t.Fatalf("首次认领应当成功: %v", err)
	}
	// 租约到期：持有着崩了，不接管会留下一把永久占用。
	clock.Advance(2 * time.Minute)
	if _, err := b.Claim(context.Background(), "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		t.Fatalf("过期租约必须可被接管（实得 %v）", err)
	}
}

// checkTakeoverRejectsStaleSubmit 过期接管后，旧持有者的释放与完成都必须被拒，
// 且**不波及**新持有者。
//
// 「旧令牌重用被拒」（token.go）钉的是「释放 → 再认领」这条 ABA 路径；
// 这里钉的是更危险的「过期 → 接管」路径：旧持有者只是 GC 停顿或网络分区，
// 恢复后手里还攥着旧令牌——它迟到的 Release 会放掉新持有者的闸、迟到的
// Complete 会把别人正在做的事记成自己做完。拒绝必须成对验证：
// 旧令牌被拒的同时，新持有者的一切照常。
func checkTakeoverRejectsStaleSubmit(ctx context.Context, b taskboard.Board, clock *FakeClock) error {
	if err := seedBoard(b, taskboard.Task{ID: "t1", State: taskboard.StateOpen}); err != nil {
		return err
	}
	first, err := b.Claim(ctx, "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 首次认领应当成功")
	}
	clock.Advance(2 * time.Minute)
	second, err := b.Claim(ctx, "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 过期租约必须可被接管")
	}
	if second.Lease.Token == first.Lease.Token {
		return errors.New("boardtest: 接管必须换新令牌（旧令牌与新一代并存等于没有换代）")
	}
	if err := b.Release(ctx, "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		return fmt.Errorf("boardtest: 过期持有者的迟到释放必须报 ErrLeaseLost（实得 %v）——"+
			"静默成功会放掉新持有者的闸", err)
	}
	if err := b.Complete(ctx, "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		return fmt.Errorf("boardtest: 过期持有者的迟到完成必须报 ErrLeaseLost（实得 %v）——"+
			"静默成功会把别人正在做的事记成自己做完", err)
	}
	if err := b.Complete(ctx, "t1", second.Lease.Token); err != nil {
		return errors.Wrap(err, "boardtest: 新持有者的完成应当成功（拒绝必须只打在旧令牌上）")
	}
	return nil
}

func runTakeoverRejectsStaleSubmit(t *testing.T, factory Factory) {
	t.Helper()
	b, clock := newBoard(t, factory)
	if err := checkTakeoverRejectsStaleSubmit(context.Background(), b, clock); err != nil {
		t.Fatal(err)
	}
}

// checkTakeoverFenceIncrements 接管代次严格递增（issue #87）。
//
// 两条认领路径都要验：过期接管（新代 > 旧代）与释放后再认领（同样换代）。
// 代次不动的实现，下游凭「代次比已见过的小」拒收迟到写入的栅栏就失效了。
func checkTakeoverFenceIncrements(ctx context.Context, b taskboard.Board, clock *FakeClock) error {
	if err := seedBoard(b, taskboard.Task{ID: "t1", State: taskboard.StateOpen}); err != nil {
		return err
	}
	first, err := b.Claim(ctx, "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 首次认领应当成功")
	}
	clock.Advance(2 * time.Minute)
	takeover, err := b.Claim(ctx, "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 过期租约必须可被接管")
	}
	if takeover.Lease.Fence <= first.Lease.Fence {
		return fmt.Errorf("boardtest: 接管后的代次必须严格大于旧代（实得 old=%d new=%d）——"+
			"代次不递增，下游凭代次拒收迟到写入的栅栏就失效了",
			first.Lease.Fence, takeover.Lease.Fence)
	}
	if err := b.Release(ctx, "t1", takeover.Lease.Token); err != nil {
		return errors.Wrap(err, "boardtest: 持有者释放应当成功")
	}
	reclaim, err := b.Claim(ctx, "t1", "a3", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 释放后应当可再认领")
	}
	if reclaim.Lease.Fence <= takeover.Lease.Fence {
		return fmt.Errorf("boardtest: 再认领后的代次必须严格递增（实得 old=%d new=%d）",
			takeover.Lease.Fence, reclaim.Lease.Fence)
	}
	return nil
}

func runTakeoverFenceIncrements(t *testing.T, factory Factory) {
	t.Helper()
	b, clock := newBoard(t, factory)
	if err := checkTakeoverFenceIncrements(context.Background(), b, clock); err != nil {
		t.Fatal(err)
	}
}

// checkRenewKeepsLongTask 长任务续期后可继续持有（issue #87）。
//
// 续期把到期时刻推后、代次与令牌**不变**（续期不是接管）；续期之后，
// 原本会到期的租约仍在保护持有者——别人认领会得到 ErrTaken。
// 时间轴：t0 认领（TTL 1min）→ t0+30s 续期（新到期 t0+90s）→ t0+80s
// （原始租约已过期、续期后的还在）→ 别人认领必须被拒，持有者完成应当成功。
func checkRenewKeepsLongTask(ctx context.Context, b taskboard.Board, clock *FakeClock) error {
	r, err := asRenewing(b)
	if err != nil {
		return err
	}
	if err := seedBoard(b, taskboard.Task{ID: "t1", State: taskboard.StateOpen}); err != nil {
		return err
	}
	first, err := b.Claim(ctx, "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 首次认领应当成功")
	}
	oldUntil, token, fence, seq := first.Lease.Until, first.Lease.Token, first.Lease.Fence, first.Seq

	clock.Advance(30 * time.Second)
	renewed, err := r.Renew(ctx, "t1", token, taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 持有者续期应当成功")
	}
	if renewed.Lease.Token != token {
		return errors.New("boardtest: 续期不得更换令牌（续期不是接管——换了令牌旧持有者的在途提交全被误拒）")
	}
	if renewed.Lease.Fence != fence {
		return fmt.Errorf("boardtest: 续期不得递增代次（实得 old=%d new=%d）——续期不是接管",
			fence, renewed.Lease.Fence)
	}
	if !renewed.Lease.Until.After(oldUntil) {
		return fmt.Errorf("boardtest: 续期必须把到期时刻推后（实得 old=%v new=%v）", oldUntil, renewed.Lease.Until)
	}
	if renewed.Seq <= seq {
		return fmt.Errorf("boardtest: 续期是任务的一次变更，Seq 必须前进（实得 old=%d new=%d）", seq, renewed.Seq)
	}

	// 原始租约的到期时刻已过：若续期没生效，这里会错误地允许接管。
	clock.Advance(50 * time.Second)
	if _, err := b.Claim(ctx, "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute}); !errors.Is(err, taskboard.ErrTaken) {
		return fmt.Errorf("boardtest: 续期后的租约仍在保护持有者，他人认领必须报 ErrTaken（实得 %v）——"+
			"这条红了说明续期是假的（没把到期时刻推后）", err)
	}
	if err := b.Complete(ctx, "t1", token); err != nil {
		return errors.Wrap(err, "boardtest: 长任务干完后持有者的完成应当成功")
	}
	return nil
}

func runRenewKeepsLongTask(t *testing.T, factory Factory) {
	t.Helper()
	b, clock := newBoard(t, factory)
	if err := checkRenewKeepsLongTask(context.Background(), b, clock); err != nil {
		t.Fatal(err)
	}
}

// checkRenewRejectsStaleToken 旧令牌续期被拒——三条路径都验（issue #87）。
//
//   - 接管后：旧令牌续期延长的是**新持有者的闸**（假续期最危险的形态）；
//   - 释放后：租约已还，续的是一段不存在持有权的租约；
//   - 过期后（还没被接管）：资格在到期时刻终止，想继续做走重新认领。
//
// 每条路径都要**不做任何修改**——拒绝之后现任持有者的租约必须原样。
func checkRenewRejectsStaleToken(ctx context.Context, b taskboard.Board, clock *FakeClock) error {
	r, err := asRenewing(b)
	if err != nil {
		return err
	}

	// 路径一：接管后旧令牌续期被拒，且新持有者的租约不被波及。
	if err := seedBoard(b, taskboard.Task{ID: "t1", State: taskboard.StateOpen}); err != nil {
		return err
	}
	first, err := b.Claim(ctx, "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 首次认领应当成功")
	}
	clock.Advance(2 * time.Minute)
	second, err := b.Claim(ctx, "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 过期租约必须可被接管")
	}
	if _, err := r.Renew(ctx, "t1", first.Lease.Token, taskboard.ClaimRequest{TTL: time.Minute}); !errors.Is(err, taskboard.ErrLeaseLost) {
		return fmt.Errorf("boardtest: 接管后旧令牌续期必须报 ErrLeaseLost（实得 %v）——"+
			"放行就是假续期：延长的别人的闸，旧持有者却以为延长的是自己的", err)
	}
	if _, err := b.Claim(ctx, "t1", "a3", taskboard.ClaimRequest{TTL: time.Minute}); !errors.Is(err, taskboard.ErrTaken) {
		return fmt.Errorf("boardtest: 旧令牌续期被拒后，新持有者的租约必须原样（实得 %v）", err)
	}

	// 路径二：释放后旧令牌续期被拒。
	if err := b.Release(ctx, "t1", second.Lease.Token); err != nil {
		return errors.Wrap(err, "boardtest: 持有者释放应当成功")
	}
	if _, err := r.Renew(ctx, "t1", second.Lease.Token, taskboard.ClaimRequest{TTL: time.Minute}); !errors.Is(err, taskboard.ErrLeaseLost) {
		return fmt.Errorf("boardtest: 释放后旧令牌续期必须报 ErrLeaseLost（实得 %v）", err)
	}

	// 路径三：过期后（没被接管）续期被拒——资格在到期时刻终止。
	if err := seedBoard(b, taskboard.Task{ID: "t2", State: taskboard.StateOpen}); err != nil {
		return err
	}
	third, err := b.Claim(ctx, "t2", "b1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: t2 首次认领应当成功")
	}
	clock.Advance(2 * time.Minute)
	if _, err := r.Renew(ctx, "t2", third.Lease.Token, taskboard.ClaimRequest{TTL: time.Minute}); !errors.Is(err, taskboard.ErrLeaseLost) {
		return fmt.Errorf("boardtest: 过期租约的续期必须报 ErrLeaseLost（实得 %v）——"+
			"过期后任务随时可能被接管，存储不能既承诺「随时可接管」又给死者续命", err)
	}
	return nil
}

func runRenewRejectsStaleToken(t *testing.T, factory Factory) {
	t.Helper()
	b, clock := newBoard(t, factory)
	if err := checkRenewRejectsStaleToken(context.Background(), b, clock); err != nil {
		t.Fatal(err)
	}
}
