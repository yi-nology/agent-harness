package pii

// 写侧护栏：三个写侧并发时不得丢更新。
//
// # 为什么这一条要特殊构造
//
// 策略换成单个 atomic.Pointer[Policy] 之后，写侧从「各写各的原子变量」变成了
// 「读当前 → 改一个字段 → 写回」。这一步把「并发读线程安全」换成了「并发写
// 会丢更新」的风险：宿主刚 SetEnabled(false)，并发进来的 SetSurfaces 若用
// 改前的旧副本写回，enabled 就被悄悄翻回 true——宿主明确关闭了脱敏，它却仍在
// 生效。updatePolicy 的 CAS 循环就是为这件事存在的。
//
// **但朴素写法抓不住它。** 曾先写过一版：64 轮 × 3 goroutine 各调公开的
// SetEnabled/SetSurfaces/SetStrategy，注入掉 CompareAndSwap 后跑——绿；
// 提到 20000 轮，仍绿。原因是丢更新的窗口在 mutate 结束到 Store 之间，只有
// 几纳秒，比 Go 的调度粒度还窄，只能靠 async preemption 恰好落在那几纳秒上。
// 轮次加多少都是概率，抓不住就是抓不住。
//
// 现在这版用两步把窗口撑到确定可撞：
//  1. **barrier 同步起跑**：两个写侧都就位后才一起进入 updatePolicy，
//     排除 goroutine 启动延迟这个干扰因子；
//  2. **放慢 mutate**：把耗时插在 Load 之后、Store 之前，窗口从纳秒级撑到
//     毫秒级，两边都完成 Load 而未 Store 变成必然而非巧合。
//
// 于是无 CAS 版必然丢更新（先 Store 的被后 Store 的整体覆盖），有 CAS 版
// 必然重试收敛。已反向验证：抽掉 CompareAndSwap 后本用例转红。

import (
	"sync"
	"testing"
	"time"
)

// TestConcurrentSettersDoNotLoseUpdates 两个写侧并发改不同字段，最终两个字段
// 都必须生效。丢更新的表现是：先写回的那份快照把后写者的修改整份覆盖掉，
// 后者等于没写过。
func TestConcurrentSettersDoNotLoseUpdates(t *testing.T) {
	restore(t)

	// 放慢 mutate：把窗口插在 Load 之后、Store 之前。
	slow := func(fn func(*Policy)) func(*Policy) {
		return func(p *Policy) {
			time.Sleep(2 * time.Millisecond)
			fn(p)
		}
	}

	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		ready.Done()
		<-start
		updatePolicy(slow(func(p *Policy) { p.enabled = false }))
	}()
	go func() {
		defer wg.Done()
		ready.Done()
		<-start
		updatePolicy(slow(func(p *Policy) { p.surfaces = 0 }))
	}()

	ready.Wait()
	close(start)
	wg.Wait()

	p := CurrentPolicy()
	if p.enabled {
		t.Error("enabled 被并发写覆盖回旧值——读改写丢了更新（CAS 失效）")
	}
	if p.surfaces != 0 {
		t.Errorf("surfaces=%v，被并发写覆盖回旧值——读改写丢了更新（CAS 失效）", p.surfaces)
	}
}

// TestPolicySnapshotIsObservable Gen 与 Enabled 是宿主唯一的可观测入口：
// Policy 的字段都是私有的，宿主拿不到快照里的原始值，只能走这两个访问器。
// 留着它们却不测，等于留了没人验证过的公开 API。
func TestPolicySnapshotIsObservable(t *testing.T) {
	restore(t)

	if !Enabled() {
		t.Fatal("缺省应为启用（secure by default）")
	}
	before := Gen()
	if before == 0 {
		t.Fatal("代次缺省应为 1")
	}

	SetEnabled(false)
	if Enabled() {
		t.Fatal("SetEnabled(false) 后 Enabled 仍为 true")
	}
	if Gen() <= before {
		t.Fatalf("代次应随替换递增：before=%d after=%d", before, Gen())
	}

	SetEnabled(true)
	if !Enabled() {
		t.Fatal("SetEnabled(true) 后 Enabled 仍为 false")
	}
	if Gen() <= before {
		t.Fatalf("代次应随替换递增：before=%d after=%d", before, Gen())
	}
}
