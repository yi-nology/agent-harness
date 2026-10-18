package pii

// 处置策略变更订阅的正常路径：通知发出、拿到的是新快照、注册即得当前值、注销即失效。
//
// # 这组用例补的是哪条缝
//
// policy_cas_test.go / policy_atomic_test.go 证明了策略「不可撕裂读」，policy_test.go
// 证明了写侧拒绝时保持原值。但那些都只管**读侧自洽**：Gen() 能回答「换过没有」，
// 回答不了「换成了什么」，而策略注入之后没有任何东西会去响应它——宿主只能自己起轮询，
// 两次比对之间换了两次就只说得出「变了」。
//
// 边界形态（被拒不通知、订阅者 panic、回调内再改策略）与并发形态在
// subscribe_edge_test.go。

import (
	"sync"
	"testing"
)

// rec 订阅者收到的快照序列（测试侧收集器，带锁以配 -race）。
type rec struct {
	mu    sync.Mutex
	snaps []Policy
}

func (r *rec) fn(p Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = append(r.snaps, p)
}

func (r *rec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.snaps)
}

func (r *rec) last() Policy {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.snaps) == 0 {
		return Policy{}
	}
	return r.snaps[len(r.snaps)-1]
}

func (r *rec) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = nil
}

func (r *rec) all() []Policy {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Policy(nil), r.snaps...)
}

// drain 把本用例登记的订阅者在退出时摘掉。不摘会让订阅被后续用例的
// SetStrategy 顺手叫醒——那些闭包持有本用例的 t。
func drain(t *testing.T, hs ...Subscription) {
	t.Helper()
	t.Cleanup(func() {
		for _, h := range hs {
			Unsubscribe(h)
		}
	})
}

// ── 正常路径 ──

// TestSubscribeReceivesCurrentSnapshot 注册即得当前快照，且拿到的就是**当前**那份。
//
// 不立即回调的话，订阅者要等到下一次变更才知道现在是什么——而「很久没有下一次变更」
// 正是策略注入在长生命周期进程里的典型形态，于是它带着一个从没填过的视图上线，
// 却以为自己已经跟上了。
func TestSubscribeReceivesCurrentSnapshot(t *testing.T) {
	restore(t)
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatal(err)
	}
	SetSurfaces(surfaceInputBit)

	var r rec
	h, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)

	if got := r.count(); got != 1 {
		t.Fatalf("注册时应立即回调一次，实得 %d 次", got)
	}
	snap := r.last()
	if snap.strategy != StrategyRedact {
		t.Fatalf("初始快照应为当前生效的 redact，实得 %q", snap.strategy)
	}
	if !snap.surfaces.Has(SurfaceInput) || snap.surfaces.Has(SurfaceOutput) {
		t.Fatalf("初始快照面集合应为「仅 input」，实得 %d", snap.surfaces)
	}
	if snap.gen != Gen() {
		t.Fatalf("初始快照代次应与全局一致：快照 %d，全局 %d", snap.gen, Gen())
	}
}

// TestChangeNotifiesWithNewSnapshot 变更触发通知，且拿到的是**新**快照。
func TestChangeNotifiesWithNewSnapshot(t *testing.T) {
	restore(t)

	var r rec
	h, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)
	r.reset()

	if err := SetStrategy(StrategyBlock); err != nil {
		t.Fatal(err)
	}

	if got := r.count(); got != 1 {
		t.Fatalf("一次成功变更应回调一次，实得 %d 次", got)
	}
	snap := r.last()
	if snap.strategy != StrategyBlock {
		t.Fatalf("通知必须送出新快照（block），实得 %q", snap.strategy)
	}
	if snap.gen != Gen() {
		t.Fatalf("新快照代次应与全局一致：快照 %d，全局 %d", snap.gen, Gen())
	}

	// 三个写侧都必须触发通知——漏掉任何一个都是一条静默失效的变更通道。
	r.reset()
	SetEnabled(false)
	if got := r.count(); got != 1 {
		t.Fatalf("SetEnabled 应触发通知，实得 %d", got)
	}
	r.reset()
	SetSurfaces(surfaceOutputBit)
	if got := r.count(); got != 1 {
		t.Fatalf("SetSurfaces 应触发通知，实得 %d", got)
	}
}

// TestAllSubscribersNotified 多订阅者全部收到——不能只通知第一个（漏通知的那位
// 会带着旧视图继续跑，且没有任何信号）。
func TestAllSubscribersNotified(t *testing.T) {
	restore(t)

	var a, b, c rec
	h1, err := Subscribe(a.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h1)
	h2, err := Subscribe(b.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h2)
	h3, err := Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h3)

	for i, r := range []*rec{&a, &b, &c} {
		if got := r.count(); got != 1 {
			t.Fatalf("第 %d 位订阅者注册时应立即收到一次，实得 %d", i+1, got)
		}
		r.reset()
	}

	if err := SetStrategy(StrategyHash); err != nil {
		t.Fatal(err)
	}
	for i, r := range []*rec{&a, &b, &c} {
		if got := r.count(); got != 1 {
			t.Fatalf("第 %d 位订阅者应各收到一次通知，实得 %d", i+1, got)
		}
		if r.last().strategy != StrategyHash {
			t.Fatalf("第 %d 位订阅者拿到的不是新快照：%q", i+1, r.last().strategy)
		}
	}
}

// TestUnsubscribeStopsNotifications 注销后不再收到。
//
// 没有注销这条腿，长期跑的宿主会把订阅闭包连同它捕获的一切永久留在表里——
// 而这条泄漏不会自己显形。
func TestUnsubscribeStopsNotifications(t *testing.T) {
	restore(t)

	var r rec
	h, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)
	r.reset()

	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatal(err)
	}
	if got := r.count(); got != 1 {
		t.Fatalf("注销前应收到通知，实得 %d", got)
	}

	Unsubscribe(h)
	r.reset()

	if err := SetStrategy(StrategyHash); err != nil {
		t.Fatal(err)
	}
	if got := r.count(); got != 0 {
		t.Fatalf("注销后不应再收到通知，实得 %d 次——订阅表没摘干净", got)
	}
}

// TestUnsubscribeTwiceSafe 重复注销是空操作，不得影响仍在册的订阅者。
func TestUnsubscribeTwiceSafe(t *testing.T) {
	restore(t)

	var a, b rec
	h1, err := Subscribe(a.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h1)
	h2, err := Subscribe(b.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h2)

	Unsubscribe(h1)
	Unsubscribe(h1) // 重复注销：不得误伤 h2
	a.reset()
	b.reset()

	if err := SetStrategy(StrategyBlock); err != nil {
		t.Fatal(err)
	}
	if got := a.count(); got != 0 {
		t.Fatalf("已注销者不应再收到，实得 %d", got)
	}
	if got := b.count(); got != 1 {
		t.Fatalf("在册订阅者应仍收到，实得 %d——重复注销误伤了他人", got)
	}
}

// TestSubscribeNilCallbackRejected 订阅失败必须有信号：返回零句柄 + 错误，
// 不得把一个必然 panic 的回调登记进去。
func TestSubscribeNilCallbackRejected(t *testing.T) {
	restore(t)

	h, err := Subscribe(nil)
	if err == nil {
		t.Fatal("空回调应报错")
	}
	if h != 0 {
		t.Fatalf("订阅失败应返回零句柄，实得 %d", h)
	}

	// 失败不得留下任何在册订阅者：此刻换策略不应叫醒任何人。
	var r rec
	good, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, good)
	r.reset()

	if err := SetStrategy(StrategyHash); err != nil {
		t.Fatal(err)
	}
	if got := r.count(); got != 1 {
		t.Fatalf("只有合法订阅者应收通知，实得 %d", got)
	}
}
