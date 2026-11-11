// token.go —— 令牌铸造与「只认 token」的契约场景。
//
// 令牌由存储铸造（v0.33.0），释放/完成只认 token 不认内容。本文件的场景
// 钉四条：铸造、非持有者被拒、旧令牌重用被拒（ABA）、**跨任务与并发的
// 令牌复用被拒**（issue #97 新增的两条——各拿各的令牌的并发认领撞不出
// 「校验与写入分离」，必须让同一令牌的使用者正面相撞）。
package boardtest

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/taskboard"
)

// runTokenMintedByStorage 令牌由存储铸造。
func runTokenMintedByStorage(t *testing.T, factory Factory) {
	t.Helper()
	b, _ := newBoard(t, factory)
	seed(t, b,
		taskboard.Task{ID: "t1", State: taskboard.StateOpen},
		taskboard.Task{ID: "t2", State: taskboard.StateOpen},
	)
	// 认领请求里**没有令牌**——令牌只能是存储铸出来的。
	t1, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		t.Fatalf("首次认领应当成功: %v", err)
	}
	if t1.Lease.Token == "" {
		t.Fatal("认领必须返回存储铸造的令牌（否则释放与完成无从验证持有者）")
	}
	// 不同次认领的令牌必须不同：否则「上一轮的迟到释放」会认成这一轮的。
	t2, err := b.Claim(context.Background(), "t2", "a2", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		t.Fatalf("另一任务的认领应当成功: %v", err)
	}
	if t1.Lease.Token == t2.Lease.Token {
		t.Fatalf("不同次认领的令牌必须不同，实得同一个 %q", t1.Lease.Token)
	}
}

// runNonHolderReleaseRejected 非持有者（拿着编造令牌）释放被拒。
func runNonHolderReleaseRejected(t *testing.T, factory Factory) {
	t.Helper()
	b, _ := newBoard(t, factory)
	seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
	if _, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute}); err != nil {
		t.Fatalf("首次认领应当成功: %v", err)
	}
	// **同一个 agent 名**也能被误认：这正是「凭 token 不凭内容」的理由。
	err := b.Release(context.Background(), "t1", "不是这一次的令牌")
	if !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("非持有者释放必须报 ErrLeaseLost（实得 %v）——静默成功会让输家以为自己的释放生效了", err)
	}
}

// runStaleTokenReuseRejected 释放后重认领必须换新令牌，旧令牌的释放与完成都被拒。
func runStaleTokenReuseRejected(t *testing.T, factory Factory) {
	t.Helper()
	b, _ := newBoard(t, factory)
	seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
	first, err := b.Claim(context.Background(), "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		t.Fatalf("首次认领应当成功: %v", err)
	}
	if err := b.Release(context.Background(), "t1", first.Lease.Token); err != nil {
		t.Fatalf("持有者释放应当成功: %v", err)
	}
	// 重新认领：令牌必须换新（否则旧令牌还能用 ⇒ ABA）。
	second, err := b.Claim(context.Background(), "t1", "a2", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		t.Fatalf("释放后应当可再认领: %v", err)
	}
	if second.Lease.Token == first.Lease.Token {
		t.Fatalf("重新认领必须换新令牌，实得仍是 %q（否则上一轮的迟到释放会放掉这一轮的闸）", first.Lease.Token)
	}
	// 上一轮的令牌：**释放**与**完成**都必须被拒。
	if err := b.Release(context.Background(), "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("旧令牌释放必须报 ErrLeaseLost（实得 %v）", err)
	}
	if err := b.Complete(context.Background(), "t1", first.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		t.Fatalf("旧令牌完成必须报 ErrLeaseLost（实得 %v）", err)
	}
	// 当前持有者的令牌仍然有效。
	if err := b.Complete(context.Background(), "t1", second.Lease.Token); err != nil {
		t.Fatalf("当前持有者的完成应当成功: %v", err)
	}
}

// runCrossTaskTokenRejected 跨任务令牌复用被拒（issue #97）。
//
// 一个任务上铸造的令牌对**另一个任务**无效——令牌标识的是「同一次占用」，
// 不是「同一个持有者的任意动作」。接受它的实现会把 A 任务的凭证当成
// B 任务的钥匙。
func checkCrossTaskTokenRejected(ctx context.Context, b taskboard.Board) error {
	if err := seedBoard(b,
		taskboard.Task{ID: "t1", State: taskboard.StateOpen},
		taskboard.Task{ID: "t2", State: taskboard.StateOpen},
	); err != nil {
		return err
	}
	t1, err := b.Claim(ctx, "t1", "a1", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 认领 t1 应当成功")
	}
	t2, err := b.Claim(ctx, "t2", "a2", taskboard.ClaimRequest{TTL: time.Minute})
	if err != nil {
		return errors.Wrap(err, "boardtest: 认领 t2 应当成功")
	}
	if t1.Lease.Token == t2.Lease.Token {
		return errors.New("boardtest: 不同任务的令牌撞号——释放与完成会把两个任务混成一份状态")
	}
	if err := b.Release(ctx, "t2", t1.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		return errors.Errorf("boardtest: t1 的令牌拿去释放 t2 必须报 ErrLeaseLost（实得 %v）", err)
	}
	if err := b.Complete(ctx, "t2", t1.Lease.Token); !errors.Is(err, taskboard.ErrLeaseLost) {
		return errors.Errorf("boardtest: t1 的令牌拿去完成 t2 必须报 ErrLeaseLost（实得 %v）", err)
	}
	// 对照：t2 自己的令牌仍然有效——上面的拒绝是因为令牌不匹配，不是 t2 坏了。
	if err := b.Complete(ctx, "t2", t2.Lease.Token); err != nil {
		return errors.Wrap(err, "boardtest: 真持有者的完成应当成功（拒绝必须是因为令牌不匹配）")
	}
	return nil
}

func runCrossTaskTokenRejected(t *testing.T, factory Factory) {
	t.Helper()
	b, _ := newBoard(t, factory)
	if err := checkCrossTaskTokenRejected(context.Background(), b); err != nil {
		t.Fatal(err)
	}
}

// runDuplicateTokenOneWinner 重复令牌并发提交恰好一人成功（issue #97）。
//
// 判据本体在 taskboard.DuplicateTokenSubmit（判据长在包里，宿主不必接套件
// 也能用）：校验与写入分离的实现会让同一令牌的两个并发提交**都**成功。
func runDuplicateTokenOneWinner(t *testing.T, factory Factory) {
	t.Helper()
	b, _ := newBoard(t, factory)
	seed(t, b, taskboard.Task{ID: "t1", State: taskboard.StateOpen})
	won, err := taskboard.DuplicateTokenSubmit(context.Background(), b, "t1", 2, time.Minute)
	if err != nil {
		t.Fatalf("判据无法给出结论: %v", err)
	}
	if won != 1 {
		t.Fatalf("同一令牌的 2 个并发提交，成功者 = %d，契约要求恰好 1 个（>1 说明 "+
			"Complete 的校验与写入不是原子的；0 说明连真持有者都被拒了）", won)
	}
}
