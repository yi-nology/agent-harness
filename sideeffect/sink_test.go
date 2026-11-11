// 本文件的测试替身在**外部测试包**（sideeffect_test）里：
// 契约判据已收编进共享套件 sidetest.RunSinkContract（判据只有一份），
// 本包的用例变成「内存替身接套件」的接线 + 替身专属的反面教材
// （三步非原子实现被确定性抓红——它依赖替身内部钩子，收不进通用套件）。
package sideeffect_test

import (
	"context"
	"sync"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/sideeffect"
	"github.com/yi-nology/goagent-kernel/sidetest"
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
// 共享套件的 concurrent_stale_rejected 场景就是钉这一条。
type memSink struct {
	mu sync.Mutex

	// seen 资源 → 已见过的最大令牌。
	seen map[string]sideeffect.Fence
	// writes 每个资源已发生的写入次数。
	writes map[string]int
	// markers 已落盘的参考业务写入（供共享套件判「不得部分写」）。
	markers map[string]string

	// pending 写入体执行期间的标记暂存区：模拟事务语义——整次写入成功
	// 才落盘，写入体报错即整批丢弃。真实实现（examples/pgstore）用真正的
	// 数据库事务做到同一件事。写入体与 WriteFenced 同步执行（同一 goroutine
	// 且全程持锁），故这个字段不需要自己的锁。
	pending *[]string

	// failWrite 注入的写入故障。
	failWrite error
	// beforeWrite 写入前的钩子（用于构造并发窗口）。
	beforeWrite func()
}

func newMemSink() *memSink {
	return &memSink{
		seen:    map[string]sideeffect.Fence{},
		writes:  map[string]int{},
		markers: map[string]string{},
	}
}

func (m *memSink) WriteFenced(_ context.Context, resource string, presented sideeffect.Fence, write func(ctx context.Context) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 比较与写入在同一次原子操作里——故整段持锁，中途不让出。
	if m.beforeWrite != nil {
		m.beforeWrite()
	}
	if presented < m.seen[resource] {
		// 陈旧写入：拒收，且**不得发生任何写入**。
		return errors.Wrap(sideeffect.ErrFenceStale, "栅栏令牌已不是最新代", "resource", resource, "presented", uint64(presented), "seen", uint64(m.seen[resource]))
	}
	if m.failWrite != nil {
		return m.failWrite
	}
	var pending []string
	m.pending = &pending
	defer func() { m.pending = nil }()
	if write != nil {
		if err := write(context.Background()); err != nil {
			return err
		}
	}
	m.writes[resource]++
	for _, r := range pending {
		m.markers[r] += "m"
	}
	if presented > m.seen[resource] {
		m.seen[resource] = presented
	}
	return nil
}

// SeenFence 读某资源已见过的最大令牌（共享套件的观测面）。
func (m *memSink) SeenFence(_ context.Context, resource string) (sideeffect.Fence, error) {
	return m.seenOf(resource), nil
}

// WriteMarker 在写入体里执行一次参考业务写入（暂存，整次写入成功才落盘）。
//
// 只能在 WriteFenced 的写入体里调用：替身没有事务，用暂存区模拟「失败即回滚」。
func (m *memSink) WriteMarker(_ context.Context, resource string) error {
	if m.pending == nil {
		return errors.New("参考业务写入只能在 WriteFenced 的写入体里执行（替身以暂存区模拟事务）")
	}
	*m.pending = append(*m.pending, resource)
	return nil
}

// Marker 读已落盘的参考业务写入。
func (m *memSink) Marker(_ context.Context, resource string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.markers[resource], nil
}

// seenOf 读某资源已见过的最大令牌（负面教材用例仍要用它）。
func (m *memSink) seenOf(resource string) sideeffect.Fence {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[resource]
}

// memSink 满足契约与共享套件的全部观测面（编译期确认）。
var _ sidetest.SinkProbe = (*memSink)(nil)

// racySink 模拟「读已见最大 → 判断 → 再写入」的**三步实现**：
// 比较与写入之间会让出锁，故两个持有者可能同时读到同一个旧值，
// 双双通过检查，然后先后写入——更旧的那次写入反而后落。
//
// 它是**反面教材**，用来证明「原子性」这条契约要求不是空话：
// 有了这个替身，就能证明共享套件的并发场景
// 真的能抓住「非原子实现会让旧写入后落覆盖新写入」。
type racySink struct {
	mu    sync.Mutex
	seen  map[string]sideeffect.Fence
	order []sideeffect.Fence // 实际落地的写入顺序

	// beforeRead 在「读已见最大」之后、「判断」之前调用。
	// 用它**确定性地**制造「两个持有者读到同一个旧值」的条件——
	// 靠并发时序去撞是不确定的，撞上了也证明不了什么。
	//
	// 实现方需保证它**可以安全地被多次调用**（调用方用原子标志包一层），
	// 故此处不做「只生效一次」的清零——那会引入数据竞争。
	beforeRead func()
}

func newRacySink() *racySink {
	return &racySink{seen: map[string]sideeffect.Fence{}}
}

func (r *racySink) WriteFenced(_ context.Context, resource string, presented sideeffect.Fence, write func(ctx context.Context) error) error {
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
		return errors.Wrap(sideeffect.ErrFenceStale, "栅栏令牌已不是最新代", "resource", resource)
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
