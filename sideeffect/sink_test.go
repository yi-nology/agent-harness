package sideeffect

import (
	"context"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"
)

// memSink 测试替身：一份按 FencedSink 契约实现的内存下游。
//
// # 它必须忠实模拟原子性，否则测试验证的是它自己
//
// 契约要求「比较与写入在同一次原子操作里完成」。本替身用一把 mutex
// 把「读已见最大 → 比较 → 写 → 更新已见最大」包在一起，
// 中间不让出——这正是契约要求的样子。
//
// 若实现写成三步（读 → 判断 → 写），两个持有者可以同时读到同一个旧值，
// 双双通过检查，然后先后写入——更旧的那次写入反而后落。
// 下面的 TestSinkRejectsConcurrentStaleWrite 就是钉这一条。
type memSink struct {
	mu sync.Mutex

	// seen 资源 → 已见过的最大令牌。
	seen map[string]Fence
	// writes 每个资源已发生的写入次数。
	writes map[string]int

	// failWrite 注入的写入故障。
	failWrite error
	// beforeWrite 写入前的钩子（用于构造并发窗口）。
	beforeWrite func()
}

func newMemSink() *memSink {
	return &memSink{
		seen:   map[string]Fence{},
		writes: map[string]int{},
	}
}

func (m *memSink) WriteFenced(_ context.Context, resource string, presented Fence, write func(ctx context.Context) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 比较与写入在同一次原子操作里——故整段持锁，中途不让出。
	if m.beforeWrite != nil {
		m.beforeWrite()
	}
	if presented < m.seen[resource] {
		// 陈旧写入：拒收，且**不得发生任何写入**。
		return errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代", "resource", resource, "presented", uint64(presented), "seen", uint64(m.seen[resource]))
	}
	if m.failWrite != nil {
		return m.failWrite
	}
	if write != nil {
		if err := write(context.Background()); err != nil {
			return err
		}
	}
	m.writes[resource]++
	if presented > m.seen[resource] {
		m.seen[resource] = presented
	}
	return nil
}

// seenOf 读某资源已见过的最大令牌。
func (m *memSink) seenOf(resource string) Fence {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[resource]
}

// writesOf 读某资源的写入次数。
func (m *memSink) writesOf(resource string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes[resource]
}

// fenceSink 确认 memSink 实现了契约。
var _ FencedSink = (*memSink)(nil)

// 未使用的 time 占位（保持 import 干净）。
var _ = time.Second

// racySink 模拟「读已见最大 → 判断 → 再写入」的**三步实现**：
// 比较与写入之间会让出锁，故两个持有者可能同时读到同一个旧值，
// 双双通过检查，然后先后写入——更旧的那次写入反而后落。
//
// 它是**反面教材**，用来证明「原子性」这条契约要求不是空话：
// 有了这个替身，就能证明 TestSinkRejectsConcurrentStaleWrite
// 真的能抓住「非原子实现会让旧写入后落覆盖新写入」。
type racySink struct {
	mu    sync.Mutex
	seen  map[string]Fence
	order []Fence // 实际落地的写入顺序

	// beforeRead 在「读已见最大」之后、「判断」之前调用。
	// 用它**确定性地**制造「两个持有者读到同一个旧值」的条件——
	// 靠并发时序去撞是不确定的，撞上了也证明不了什么。
	//
	// 实现方需保证它**可以安全地被多次调用**（本包测试用 sync.Once 包一层），
	// 故此处不做「只生效一次」的清零——那会引入数据竞争。
	beforeRead func()
}

func newRacySink() *racySink {
	return &racySink{seen: map[string]Fence{}}
}

func (r *racySink) WriteFenced(_ context.Context, resource string, presented Fence, write func(ctx context.Context) error) error {
	// 第一步：读已见最大（放锁）。
	r.mu.Lock()
	seen := r.seen[resource]
	r.mu.Unlock()

	// 确定性竞态窗口：此刻另一个持有者可以读到同一个 seen。
	if r.beforeRead != nil {
		r.beforeRead()
	}

	// 第二步：判断（放锁期间可能被别人抢先）。
	if presented < seen {
		return errors.Wrap(ErrFenceStale, "栅栏令牌已不是最新代", "resource", resource)
	}

	// 第三步：写入（再取锁）。
	r.mu.Lock()
	defer r.mu.Unlock()
	if write != nil {
		if err := write(context.Background()); err != nil {
			return err
		}
	}
	r.seen[resource] = presented
	r.order = append(r.order, presented)
	return nil
}
