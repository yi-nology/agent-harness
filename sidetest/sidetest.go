// Package sidetest 副作用闸（sideeffect.Store）的**共享契约测试套件**。
//
// # 为什么需要它
//
// sideeffect.Store 的正确性无法靠「看起来对」保证：它要跨进程、靠一次原子
// 操作维持三个判据的联合，而**每一家宿主都会自己写一遍存储实现**。
// 各写一遍必然各判一套，语义漂移开到没人发现为止——这正是本仓
// 「跨包契约的规则只能有一份，且要有端到端用例」那条纪律要防的事。
//
// 套件把「宿主实现必须满足什么」固化成**一组可复跑的子测试**：
// 宿主写完存储实现后，把本套件接进自己的测试里跑一遍，
// 违反哪条契约就红在哪一条，不必等人肉 review 发现。
//
// 范式沿用上游 `oekit/pkg/cache/cachetest` 与 `pkg/singleflighttest`
// （同一家族的做法）：共享套件 + **硬失败，不静默跳过**。
//
// # 为什么是「硬失败」
//
// 套件入口一律 `t.Fatalf` 而不是 `t.Skip`。理由：调用方**显式接入**套件
// 就是在声明「我这个实现要满足副作用闸的契约」——此时跳过一个场景，
// 等于把「我没测过」伪装成「我通过了」。这与本仓反复出现的教训同型：
// **静默跳过是最贵的失败形态**，因为它让未验证的东西看起来像已验证。
//
// # 覆盖了什么
//
// 套件覆盖的是**跨实现必须一致**的语义（每条子测试对应一个真实失效形态）：
//
//  1. apply_once            同一 Key 第二次 Acquire → replayed，且取回同一结果
//  2. completed_wins        已完成优先于「资源正忙」（不因占用而看不到已完成）
//  3. contended_while_held  租约未过期时，第二个持有者拿到 contended
//  4. takeover_after_expiry 租约过期后可被接管，且新令牌更大
//  5. fence_monotonic       同一 Resource 上令牌单调递增，不回退
//  6. stale_complete_rejected 过期持有者的 Complete 被拒（ErrFenceStale）
//  7. stale_release_rejected  过期持有者的 Release 被拒（不能放掉新持有者的闸）
//  8. release_does_not_done  Release 不把 Key 标记为已完成
//
// 另有两场景在 **renew.go**（续期一族，与「结果判据」是不同语义）：
//
//  9. renew_extends_lease             Renew 延长有效期且不换令牌
//  10. renew_after_takeover_rejected   闸已易主后 Renew 被拒（假续期的封口）
//     10b. renew_after_release_rejected   闸已释放后 Renew 被拒（只比令牌会漏掉的那条）
//
// 「结果未知」一族五场景在 **uncertain.go**（v0.32.0 新增，判失败路径上的
// 「至多一次」是否真的成立）：
//
//  11. uncertain_blocks_execution      结果未知的 Key 不得再被授予执行权
//  12. resolve_not_applied_allows_retry 裁决「未生效」后该 Key 重新可执行
//  13. resolve_applied_records_result  裁决「已生效」后该 Key 重放既有结果
//  14. resolve_first_wins              同一 Key 的多次裁决先到者为准
//  15. stale_fence_cannot_mark         过期持有者不得标记未知态
//
// 「执行意图」一族六场景在 **intent.go**（v0.33.0 新增，判「进程死在施加
// 成功之后、记账之前」那条窗口是否真被封住）：
//
//  16. intent_blocks_reacquire        未裁决的尝试不得被重新授予执行权
//  17. expired_intent_requires_reconcile 租约过期且未裁决 ⇒ 转入需对账态
//  18. expired_intent_safe_may_retry  声明了可安全重试 ⇒ 过期后可重新授予
//  19. abandon_allows_retry           显式放弃后该 Key 重新可执行
//  20. release_keeps_intent           只放闸不撤销意图 ⇒ 仍不得重新执行
//  21. abandon_requires_owner         放弃只认本次尝试的令牌
//
// 「持有者身份」一族三场景在 **holder.go**（v0.33.0 新增，判三项联查里
// 令牌盖不住的那两项）：
//
//  22. holder_minted_by_store         持有者身份由存储铸造（非空、不重合）
//  23. non_holder_rejected            偷到令牌的组件冒充持有者被拒（ErrNotHolder）
//  24. resource_mismatch_rejected     A 资源的租约用于 B 资源被拒（ErrResourceMismatch）
//
// 「标记失败」一族一场景在 **markfail.go**（v0.33.0 新增，套件内置故障
// 注入装饰器模拟存储侧不可用）：
//
//  25. mark_failure_blocks_acquire    标记写失败后同 Key 不得被授予执行权
//     （意图必须还在挡着）
//
// 「重试安全三档」一族两场景在 **unsafe.go**（v0.33.0 新增，判 Unsafe 与
// Unknown 在裁决后的分道）：
//
//  26. unsafe_resolve_stays_blocked   Unsafe 裁决「未生效」仍阻塞（ErrUnsafeNotRetryable）
//  27. unsafe_force_retryable_explicit 显式决策（ForceRetryable）才放行
//
// 「对账占用」一族两场景在 **reconcile.go**（v0.33.0 新增，判对账权认领）：
//
//  28. reconcile_taken_while_active   占用未过期时第二个对账者被拒；裁决清除占用
//  29. reconcile_reentrant_after_expiry 占用过期可重新认领；过期 ≠ 放行
//
// 「下游栅栏」一族六场景在 **sink.go**（v0.33.0 新增，RunSinkContract，判
// FencedSink 的比较口径与原子性）：
//
//  30. fresh_accepted                令牌更大 → 接受写入
//  31. equal_accepted                等于已见最大 → 接受（只有更小才是陈旧）
//  32. stale_rejected_no_write       陈旧拒收为 ErrFenceStale，且零写入
//  33. stale_after_fresh_keeps_max   新先旧后：已见最大不被陈旧写污染
//  34. concurrent_stale_rejected     并发下更旧的那次不能后落覆盖
//  35. write_error_no_partial_write  写入体失败 → 预占与部分写一起回滚
//
// 判据收编自 sideeffect 包内对内存替身的用例（判据只有一份）：内存替身与
// PostgreSQL 参考实现（examples/pgstore）跑同一段断言，宿主的 FencedSink
// 实现也接它，不另造一套。
//
// # 时钟
//
// 租约过期需要可控时钟，否则只能靠 sleep（慢且 flaky）。
// 套件提供 FakeClock；实现须通过 ClockedStore 接口暴露「用哪个时钟」，
// 且**过期判定必须读这个时钟**（不是 time.Now）——
// 否则套件的过期场景测的是真实时间，恒不触发，套件就成了一组空断言。
package sidetest

import (
	"sync"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// FakeClock 套件提供的假时钟（并发安全；测试内手动推进）。
//
// 取非零真实量级时刻，避开零值 time.Time 的「无过期」歧义。
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock 创建起点为 2026-01-01T00:00:00Z 的假时钟。
func NewFakeClock() *FakeClock {
	return &FakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Now 返回当前假时刻。
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance 把假时钟前进 d。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ClockedStore 实现须提供的能力：把时钟注入到存储实现里。
//
// **为什么单独一个接口**：过期判定是存储实现内部的事（它自己知道
// 「租约还在不在」）。套件不能替它判，只能要求它**用可注入的时钟**。
// 没实现这个接口 → 套件硬失败（而不是跳过过期场景——跳过等于假绿）。
type ClockedStore interface {
	sideeffect.Store
	// UseClock 指定存储实现读时钟的方式（通常是注入一个 func() time.Time）。
	UseClock(now func() time.Time)
}

// Factory 构造被测存储实现。
//
// factory 每个子测试调用一次，构造一份全新实现，并把套件的假时钟注入它。
// 实现的后台资源由 factory 经 t.Cleanup 清理。
type Factory func(t *testing.T, clock *FakeClock) ClockedStore

// Run 跑完整契约场景（29 个子测试，任何一个失败即实现违反契约）。
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("apply_once", func(t *testing.T) { runApplyOnce(t, factory) })
	t.Run("completed_wins", func(t *testing.T) { runCompletedWins(t, factory) })
	t.Run("contended_while_held", func(t *testing.T) { runContendedWhileHeld(t, factory) })
	t.Run("takeover_after_expiry", func(t *testing.T) { runTakeoverAfterExpiry(t, factory) })
	t.Run("fence_monotonic", func(t *testing.T) { runFenceMonotonic(t, factory) })
	t.Run("stale_complete_rejected", func(t *testing.T) { runStaleCompleteRejected(t, factory) })
	t.Run("stale_release_rejected", func(t *testing.T) { runStaleReleaseRejected(t, factory) })
	t.Run("release_does_not_done", func(t *testing.T) { runReleaseDoesNotDone(t, factory) })
	t.Run("renew_extends_lease", func(t *testing.T) { runRenewExtendsLease(t, factory) })
	t.Run("renew_after_takeover_rejected", func(t *testing.T) { runRenewAfterTakeoverRejected(t, factory) })
	t.Run("renew_after_release_rejected", func(t *testing.T) { runRenewAfterReleaseRejected(t, factory) })
	t.Run("uncertain_blocks_execution", func(t *testing.T) { runUncertainBlocksExecution(t, factory) })
	t.Run("resolve_not_applied_allows_retry", func(t *testing.T) { runResolveNotAppliedAllowsRetry(t, factory) })
	t.Run("resolve_applied_records_result", func(t *testing.T) { runResolveAppliedRecordsResult(t, factory) })
	t.Run("resolve_first_wins", func(t *testing.T) { runResolveFirstWins(t, factory) })
	t.Run("stale_fence_cannot_mark", func(t *testing.T) { runStaleFenceCannotMarkUncertain(t, factory) })
	t.Run("intent_blocks_reacquire", func(t *testing.T) { runIntentBlocksReacquire(t, factory) })
	t.Run("expired_intent_requires_reconcile", func(t *testing.T) { runExpiredIntentRequiresReconcile(t, factory) })
	t.Run("expired_intent_safe_may_retry", func(t *testing.T) { runExpiredIntentSafeMayRetry(t, factory) })
	t.Run("abandon_allows_retry", func(t *testing.T) { runAbandonAllowsRetry(t, factory) })
	t.Run("release_keeps_intent", func(t *testing.T) { runReleaseKeepsIntent(t, factory) })
	t.Run("abandon_requires_owner", func(t *testing.T) { runAbandonRequiresOwner(t, factory) })
	t.Run("holder_minted_by_store", func(t *testing.T) { runHolderMintedByStore(t, factory) })
	t.Run("non_holder_rejected", func(t *testing.T) { runNonHolderRejected(t, factory) })
	t.Run("resource_mismatch_rejected", func(t *testing.T) { runResourceMismatchRejected(t, factory) })
	t.Run("mark_failure_blocks_acquire", func(t *testing.T) { runMarkFailureBlocksAcquire(t, factory) })
	t.Run("unsafe_resolve_stays_blocked", func(t *testing.T) { runUnsafeResolveStaysBlocked(t, factory) })
	t.Run("unsafe_force_retryable_explicit", func(t *testing.T) { runUnsafeForceRetryableExplicit(t, factory) })
	t.Run("reconcile_taken_while_active", func(t *testing.T) { runReconcileTakenWhileActive(t, factory) })
	t.Run("reconcile_reentrant_after_expiry", func(t *testing.T) { runReconcileReentrantAfterExpiry(t, factory) })
}

// newStore 构造被测实现并注入假时钟。
func newStore(t *testing.T, factory Factory) (sideeffect.Store, *FakeClock) {
	t.Helper()
	clock := NewFakeClock()
	st := factory(t, clock)
	if st == nil {
		t.Fatalf("factory 返回了 nil 实现")
	}
	st.UseClock(clock.Now)
	return st, clock
}

// act 构造一个测试动作（合成数据，不含任何领域词面）。
func act(key, resource string) sideeffect.Action {
	return sideeffect.Action{Key: sideeffect.Key(key), Resource: resource, Payload: []byte("p")}
}
