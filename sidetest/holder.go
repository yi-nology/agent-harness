package sidetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 本文件是「持有者身份」一族的契约场景（v0.33.0 新增）。
//
// # 这一族判的是什么
//
// 判的是**三项联查**里令牌盖不住的那两项：
//
//  1. 持有者身份由存储侧**铸造**、随授予返回——调用方自选的身份等于没有
//     身份（与 taskboard「令牌由存储铸造」同一条纪律）；
//  2. 出示的凭据不是本次授予铸造的那份 → ErrNotHolder——栅栏令牌会经下游
//     流转（FencedSink 的写入参数就是它），见过的组件拿着令牌就能冒充
//     持有者；持有者身份不经下游流转，冒充者拿不出它；
//  3. A 资源的租约被拿去做 B 资源的操作 → ErrResourceMismatch——租约与
//     动作各说各话时，两个「真相」拼不出一个合规。
//
// 失效形态：只比令牌的实现，拦不住「偷到令牌的组件在持有者还在场时冒充它」
// 与「凭 A 的租约动 B」这两条路。

// checkHolderMintedByStore 钉住：Lease.Holder 由存储铸造（非空、授予间不重合）。
func checkHolderMintedByStore(st sideeffect.Store) error {
	ctx := context.Background()

	_, lease1, _, err := st.Acquire(ctx, act("k-holder-1", "res-holder-1"), time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	if lease1.Holder == "" {
		return fmt.Errorf("授予时必须铸造持有者身份（Lease.Holder 为空 = 凭据不存在，冒充无从拦截）")
	}
	_, lease2, _, err := st.Acquire(ctx, act("k-holder-2", "res-holder-2"), time.Minute)
	if err != nil {
		return fmt.Errorf("二次 Acquire 失败: %w", err)
	}
	if lease2.Holder == "" {
		return fmt.Errorf("第二次授予未铸造持有者身份")
	}
	if lease1.Holder == lease2.Holder {
		return fmt.Errorf("两次授予的持有者身份重合（铸造不可预测、不重合是冒充拦截的前提）")
	}
	return nil
}

// checkNonHolderRejected 钉住：非持有者拿（偷到的）令牌冒充持有者被拒。
//
// 场景里「另一组件」只偷到了令牌（Fence 经下游流转可见），拿不出铸造的
// 持有者身份——Complete / Renew 用它的伪造凭据必须被拒为 ErrNotHolder。
func checkNonHolderRejected(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-not-holder", "res-not-holder")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	// 伪造：令牌是真的（下游见过它），身份是假的（不经下游流转）。
	forged := lease
	forged.Holder = "stolen-component-impersonating"

	if _, err := st.Renew(ctx, a, forged, time.Minute); !errors.Is(err, sideeffect.ErrNotHolder) {
		return fmt.Errorf("非持有者 Renew 应被拒为 ErrNotHolder（只比令牌的实现拦不住冒充），实得 %v", err)
	}
	if _, err := st.Complete(ctx, a, forged, sideeffect.Result{Payload: []byte("forged")}); !errors.Is(err, sideeffect.ErrNotHolder) {
		return fmt.Errorf("非持有者 Complete 应被拒为 ErrNotHolder（迟到冒充结果不得覆盖真持有者），实得 %v", err)
	}
	// 真持有者不受影响：冒充被拒后，凭据本身仍有效。
	if _, err := st.Renew(ctx, a, lease, time.Minute); err != nil {
		return fmt.Errorf("被拒的冒充不该影响真持有者的凭据: %w", err)
	}
	return nil
}

// checkResourceMismatchRejected 钉住：A 资源的租约用于 B 资源的操作被拒。
func checkResourceMismatchRejected(st sideeffect.Store) error {
	ctx := context.Background()
	a := act("k-mismatch", "res-mismatch-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		return fmt.Errorf("首次 Acquire 失败: %w", err)
	}
	// 同一份凭据，对准另一个资源：租约说 A，动作说 B。
	b := act("k-mismatch-b", "res-mismatch-b")

	if _, err := st.Renew(ctx, b, lease, time.Minute); !errors.Is(err, sideeffect.ErrResourceMismatch) {
		return fmt.Errorf("跨资源凭据（租约与动作的资源不一致）应被拒为 ErrResourceMismatch，实得 %v", err)
	}
	if err := st.Release(ctx, b, lease); !errors.Is(err, sideeffect.ErrResourceMismatch) {
		return fmt.Errorf("跨资源 Release 应被拒为 ErrResourceMismatch（否则 A 的闸被 B 的调用放掉），实得 %v", err)
	}
	// 被拒的跨资源调用不得影响两个资源各自的状态：A 仍被持有（contended），
	// B 仍可被授予。
	if outcome, _, _, err := st.Acquire(ctx, a, time.Minute); err != nil || outcome != sideeffect.OutcomeContended {
		return fmt.Errorf("被拒的跨资源调用不得放掉 A 的闸，实得 outcome=%v err=%v", outcome, err)
	}
	if outcome, _, _, err := st.Acquire(ctx, b, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		return fmt.Errorf("另一资源应不受影响可被授予，实得 outcome=%v err=%v", outcome, err)
	}
	return nil
}

// ── 场景入口（供 Run 调用）────────────────────────────────────────────

func runHolderMintedByStore(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkHolderMintedByStore(st); err != nil {
		t.Fatalf("违反「持有者身份由存储铸造」契约: %v", err)
	}
}

func runNonHolderRejected(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkNonHolderRejected(st); err != nil {
		t.Fatalf("违反「非持有者冒充被拒」契约: %v", err)
	}
}

func runResourceMismatchRejected(t *testing.T, factory Factory) {
	t.Helper()
	st, _ := newStore(t, factory)
	if err := checkResourceMismatchRejected(st); err != nil {
		t.Fatalf("违反「跨资源凭据被拒」契约: %v", err)
	}
}
