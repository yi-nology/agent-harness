package sidetest

import (
	"context"
	"sync"
	"testing"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// 栅栏套件的**负对照**：判据必须有分辨力——故意写坏的实现必须被跑红，
// 否则套件的绿什么都没证明（与结果判据一族的负对照同一纪律）。
//
// 两个坏实现各封一种最常见的误写形态：
//   - permissiveSink 从不拒收：陈旧写入照样落地（栅栏形同虚设）；
//   - noRollbackSink 写入体失败后照样保留预占与部分写（不得部分写被违反）。

type brokenSinkBase struct {
	mu      sync.Mutex
	seen    map[string]sideeffect.Fence
	markers map[string]string
}

func newBrokenSinkBase() brokenSinkBase {
	return brokenSinkBase{seen: map[string]sideeffect.Fence{}, markers: map[string]string{}}
}

func (b *brokenSinkBase) SeenFence(_ context.Context, resource string) (sideeffect.Fence, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen[resource], nil
}

func (b *brokenSinkBase) Marker(_ context.Context, resource string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.markers[resource], nil
}

// permissiveSink 陈旧写入一律放行（从不比令牌）。
type permissiveSink struct {
	brokenSinkBase
}

func (p *permissiveSink) WriteFenced(_ context.Context, resource string, presented sideeffect.Fence, write func(ctx context.Context) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if write != nil {
		if err := write(context.Background()); err != nil {
			return err
		}
	}
	if presented > p.seen[resource] {
		p.seen[resource] = presented
	}
	return nil
}

func (p *permissiveSink) WriteMarker(_ context.Context, resource string) error {
	// 不加锁：WriteMarker 只在 WriteFenced 的写入体里被调用（同一 goroutine，
	// 此时 WriteFenced 已持锁）；真实实现的标记写入走同一事务，替身没有事务。
	p.markers[resource] += "m"
	return nil
}

// noRollbackSink 比较/预占与写入体分两截提交：写入体失败后预占与部分写都留着。
type noRollbackSink struct {
	brokenSinkBase
}

func (n *noRollbackSink) WriteFenced(_ context.Context, resource string, presented sideeffect.Fence, write func(ctx context.Context) error) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if presented < n.seen[resource] {
		return sideeffect.ErrFenceStale
	}
	// 预占先落（错：这一步不该在写入体成功前提交）。
	n.seen[resource] = presented
	if write != nil {
		if err := write(context.Background()); err != nil {
			return err // 部分写与预占一并留下（错：不得部分写）
		}
	}
	return nil
}

func (n *noRollbackSink) WriteMarker(_ context.Context, resource string) error {
	// 不加锁：WriteMarker 只在 WriteFenced 的写入体里被调用（同一 goroutine，
	// 此时 WriteFenced 已持锁）；真实实现的标记写入走同一事务，替身没有事务。
	n.markers[resource] += "m"
	return nil
}

func TestSinkSuiteCatchesPermissiveSink(t *testing.T) {
	p := &permissiveSink{brokenSinkBase: newBrokenSinkBase()}
	if err := checkSinkStaleRejectedNoWrite(p); err == nil {
		t.Fatal("从不拒收的实现必须被 stale_rejected_no_write 判红")
	}
	if err := checkSinkConcurrentStaleRejected(p); err == nil {
		t.Fatal("从不拒收的实现必须被 concurrent_stale_rejected 判红")
	}
	if err := checkSinkStaleAfterFreshKeepsMax(p); err == nil {
		t.Fatal("从不拒收的实现必须被 stale_after_fresh_keeps_max 判红")
	}
}

func TestSinkSuiteCatchesNoRollbackSink(t *testing.T) {
	p := &noRollbackSink{brokenSinkBase: newBrokenSinkBase()}
	if err := checkSinkWriteErrorNoPartialWrite(p); err == nil {
		t.Fatal("写入体失败后保留预占的实现必须被 write_error_no_partial_write 判红")
	}
}

// compliantSink 一份最小但合规的内存实现（判据的「正对照」）：
// 整段持锁完成「比较 → 写入体 → 更新已见最大」，标记走暂存区模拟事务回滚。
type compliantSink struct {
	mu      sync.Mutex
	seen    map[string]sideeffect.Fence
	markers map[string]string
	pending *[]string
}

func newCompliantSink() *compliantSink {
	return &compliantSink{seen: map[string]sideeffect.Fence{}, markers: map[string]string{}}
}

func (c *compliantSink) WriteFenced(_ context.Context, resource string, presented sideeffect.Fence, write func(ctx context.Context) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if presented < c.seen[resource] {
		return sideeffect.ErrFenceStale
	}
	var pending []string
	c.pending = &pending
	defer func() { c.pending = nil }()
	if write != nil {
		if err := write(context.Background()); err != nil {
			return err
		}
	}
	for _, r := range pending {
		c.markers[r] += "m"
	}
	if presented > c.seen[resource] {
		c.seen[resource] = presented
	}
	return nil
}

func (c *compliantSink) SeenFence(_ context.Context, resource string) (sideeffect.Fence, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[resource], nil
}

func (c *compliantSink) WriteMarker(_ context.Context, resource string) error {
	if c.pending == nil {
		return sideeffect.ErrFenceStale // 不在写入体里调用即非法（判据不会走到）
	}
	*c.pending = append(*c.pending, resource)
	return nil
}

func (c *compliantSink) Marker(_ context.Context, resource string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.markers[resource], nil
}

// TestSinkSuitePassesCompliantDoubles 合格实现必须全绿：负对照之外，
// 再钉一次「套件不误伤」——判据红了必须是真的坏了，不是套件本身写反。
// 每条场景一份**全新**实现（与 run 包装的 factory 约定一致：场景之间不共状态）。
func TestSinkSuitePassesCompliantDoubles(t *testing.T) {
	for name, check := range map[string]func(SinkProbe) error{
		"fresh_accepted":      checkSinkFreshAccepted,
		"equal_accepted":      checkSinkEqualAccepted,
		"stale_rejected":      checkSinkStaleRejectedNoWrite,
		"keeps_max":           checkSinkStaleAfterFreshKeepsMax,
		"concurrent_rejected": checkSinkConcurrentStaleRejected,
		"no_partial_write":    checkSinkWriteErrorNoPartialWrite,
	} {
		if err := check(newCompliantSink()); err != nil {
			t.Fatalf("合格实现不该被 %s 判红: %v", name, err)
		}
	}
}
