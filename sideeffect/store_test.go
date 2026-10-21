package sideeffect

import (
	"context"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// act 构造一个测试动作（合成数据，不含任何领域词面）。
func act(key, resource string) Action {
	return Action{Key: Key(key), Resource: resource, Payload: []byte("p")}
}

// 本文件钉住**存储侧（Store）的三条判据**：
// 判据的顺序、令牌的单调性、令牌的过期拒收。
// 「编排层」语义（Runner 的流程与失败形态）在 runner_test.go。

// // 本文件钉住**存储侧（Store）的三条判据**：
// 判据的顺序、令牌的单调性、令牌的过期拒收。
// 「编排层」语义（Runner 的流程与失败形态）在 runner_test.go。

func TestFenceIsMonotonicPerResource(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	// 三轮「占用 → 过期 → 再占用」，令牌须严格递增。
	var prev Fence
	for i := range 3 {
		outcome, lease, _, err := st.Acquire(ctx, a, time.Minute)
		if err != nil {
			t.Fatalf("第 %d 轮 Acquire 失败: %v", i, err)
		}
		if outcome != OutcomeApplied {
			t.Fatalf("第 %d 轮应授予执行权，实得 %v", i, outcome)
		}
		if lease.Fence <= prev {
			t.Fatalf("第 %d 轮令牌应严格递增：prev=%d cur=%d", i, prev, lease.Fence)
		}
		prev = lease.Fence
		// 让本次持有过期，下一轮才能重新拿到。
		st.advance(2 * time.Minute)
	}

	// 不同资源各自独立计数（互不影响，但各自单调）。
	_, leaseB, _, err := st.Acquire(ctx, act("k2", "res-b"), time.Minute)
	if err != nil {
		t.Fatalf("另一资源 Acquire 失败: %v", err)
	}
	if leaseB.Fence != 1 {
		t.Fatalf("另一资源令牌应独立从 1 起，实得 %d", leaseB.Fence)
	}
}

// TestStaleLeaseCannotComplete 钉住栅栏的**核心作用**：
// 过期持有者的迟到 Complete 被拒，新持有者的结果不被覆盖。
//
// 这是「更新的状态被旧状态覆盖」那道洞的直接封口。

func TestStaleLeaseCannotComplete(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	// 第一代持有。
	_, lease1, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// 它过期了。
	st.advance(2 * time.Minute)
	// 第二代接管。
	_, lease2, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("接管 Acquire 失败: %v", err)
	}
	if lease2.Fence <= lease1.Fence {
		t.Fatalf("接管令牌应更大：old=%d new=%d", lease1.Fence, lease2.Fence)
	}

	// 第一代迟到 Complete → 必须被拒。
	_, err = st.Complete(ctx, a, lease1, Result{Payload: []byte("stale")})
	if !errors.Is(err, ErrFenceStale) {
		t.Fatalf("过期持有者的 Complete 应被拒为 ErrFenceStale，实得 %v", err)
	}

	// 第二代 Complete → 接受。
	res, err := st.Complete(ctx, a, lease2, Result{Payload: []byte("fresh")})
	if err != nil {
		t.Fatalf("当前持有者的 Complete 应被接受: %v", err)
	}
	if string(res.Payload) != "fresh" {
		t.Fatalf("结果应为 fresh，实得 %q", res.Payload)
	}
}

// TestStaleLeaseCannotRelease 钉住释放凭令牌、不认内容：
// 过期持有者迟到的 Release 不得放掉新持有者的闸。
//
// 与 commitgate「凭唯一令牌释放」同源：认内容会放错闸。

func TestStaleLeaseCannotRelease(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease1, _, _ := st.Acquire(ctx, a, time.Minute)
	st.advance(2 * time.Minute)
	_, lease2, _, _ := st.Acquire(ctx, a, time.Minute)

	if err := st.Release(ctx, a, lease1); !errors.Is(err, ErrFenceStale) {
		t.Fatalf("过期持有者的 Release 应被拒，实得 %v", err)
	}
	// 新持有者的闸仍在（未被放掉）：再 Acquire 应是 Contended 而非 Applied。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != OutcomeContended {
		t.Fatalf("新持有者的闸应仍在，实得 %v", outcome)
	}
	_ = lease2
}

// TestCompletedBeatsContended 钉住 Acquire 内部两条判据的**优先级**：
// 动作已完成时，即使资源正被他人持有，也应返回 Replayed 而非 Contended。
//
// 误用形态：优先级写反 → 调用方把「早就做完了」误判成「没轮上我」，
// 于是重试或上报「未执行」，而真相是「早就做完了」。

func TestCompletedBeatsContended(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	// 先完成动作。
	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if _, err := st.Complete(ctx, a, lease, Result{Payload: []byte("v")}); err != nil {
		t.Fatalf("Complete 失败: %v", err)
	}
	// 资源随后被他人占用。
	st.mu.Lock()
	st.holders["res-a"] = fakeHold{
		lease:   Lease{Resource: "res-a", Fence: lease.Fence + 100, TTL: 30 * time.Second},
		expires: st.now.Add(30 * time.Second),
	}
	st.mu.Unlock()

	outcome, _, res, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != OutcomeReplayed {
		t.Fatalf("已完成应优先返回 Replayed，实得 %v", outcome)
	}
	if string(res.Payload) != "v" {
		t.Fatalf("应取回既有结果 v，实得 %q", res.Payload)
	}
}

// TestNoStoreFailsLoudly 钉住「没接存储也返回 nil」是最坏的一种失败。

func TestReleaseDoesNotMarkDone(t *testing.T) {
	st := newFakeStore()
	ctx := context.Background()
	a := act("k1", "res-a")

	_, lease, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if err := st.Release(ctx, a, lease); err != nil {
		t.Fatalf("Release 失败: %v", err)
	}
	if len(st.done) != 0 {
		t.Fatalf("释放不该标记完成，实得 done=%v", st.done)
	}

	// 动作仍可被施加。
	outcome, _, _, err := st.Acquire(ctx, a, time.Minute)
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if outcome != OutcomeApplied {
		t.Fatalf("释放后应仍能获得执行权，实得 %v", outcome)
	}
}

// TestUnknownOutcomeFailsLoudly 钉住「存储侧没说清楚」必须是可观测状态：
// 内核不猜、不给默认结论，直接显式失败。
