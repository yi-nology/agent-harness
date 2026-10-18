package netguard

// 策略变更订阅的正常路径：通知发出、拿到的是新快照、注册即得当前值、注销即失效。
//
// # 这组用例补的是哪条缝
//
// policy_test.go 证明了「换过没有、换成什么」可查询（Generation 递增、被拒不推进）。
// 但那只是**信号**：没有任何东西在策略真换掉时去响应它。宿主只能自己起轮询，
// 而两次轮询之间若换了两次，代次只能说明「变了」，说不出「变成了什么」。
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
	snaps []*Policy
}

func (r *rec) fn(p *Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = append(r.snaps, p)
}

func (r *rec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.snaps)
}

func (r *rec) last() *Policy {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.snaps) == 0 {
		return nil
	}
	return r.snaps[len(r.snaps)-1]
}

func (r *rec) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = nil
}

// drain 把本用例自己登记的订阅者在退出时摘掉。
// 不摘会让订阅被后续用例的 SetPolicy 顺手叫醒——那些闭包持有本用例的 t。
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
// 不立即回调的话，订阅者要等到下一次变更才知道现在是什么——而「很久没有下一次
// 变更」正是热重载在长生命周期进程里的典型形态，于是它带着一个从没填过的视图上线，
// 却以为自己已经跟上了。
func TestSubscribeReceivesCurrentSnapshot(t *testing.T) {
	resetPolicy(t)
	if err := SetPolicy([]string{"seed.example"}, []string{".seed"}); err != nil {
		t.Fatal(err)
	}

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
	if snap == nil {
		t.Fatal("未收到初始快照")
	}
	if !snap.BlocksHostname("seed.example") {
		t.Fatal("初始快照必须是当前生效的那份（seed.example 应命中）")
	}
	if snap.Generation() != PolicyGeneration() {
		t.Fatalf("初始快照代次应与全局一致：快照 %d，全局 %d",
			snap.Generation(), PolicyGeneration())
	}
}

// TestChangeNotifiesWithNewSnapshot 变更触发通知，且拿到的是**新**快照。
func TestChangeNotifiesWithNewSnapshot(t *testing.T) {
	resetPolicy(t)

	var r rec
	h, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)
	r.reset()

	if err := SetPolicy([]string{"next.example"}, []string{".next"}); err != nil {
		t.Fatal(err)
	}

	if got := r.count(); got != 1 {
		t.Fatalf("一次成功变更应回调一次，实得 %d 次", got)
	}
	snap := r.last()
	if !snap.BlocksHostname("next.example") {
		t.Fatal("通知必须送出新快照（next.example 应命中）")
	}
	if _, hit := snap.BlocksSuffix("a.next"); !hit {
		t.Fatal("通知必须送出新快照的后缀（.next 应命中）")
	}
	if snap.Generation() != PolicyGeneration() {
		t.Fatalf("新快照代次应与全局一致：快照 %d，全局 %d",
			snap.Generation(), PolicyGeneration())
	}
}

// TestLegacySettersNotify 两个兼容入口同样触发通知——它们内部走 SetPolicy，
// 宿主若改用旧入口却收不到通知，等于新增了一条静默失效的变更通道。
func TestLegacySettersNotify(t *testing.T) {
	resetPolicy(t)

	var r rec
	h, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)
	r.reset()

	SetExtraBlockedHostnames("legacy.example")
	if got := r.count(); got != 1 {
		t.Fatalf("SetExtraBlockedHostnames 应触发通知一次，实得 %d", got)
	}
	if !r.last().BlocksHostname("legacy.example") {
		t.Fatal("通知快照应含新主机名")
	}

	if err := SetExtraBlockedHostSuffixes(".legacy"); err != nil {
		t.Fatal(err)
	}
	if got := r.count(); got != 2 {
		t.Fatalf("SetExtraBlockedHostSuffixes 应再触发一次，实得共 %d", got)
	}
	if _, hit := r.last().BlocksSuffix("a.legacy"); !hit {
		t.Fatal("通知快照应含新后缀")
	}
}

// TestAllSubscribersNotified 多订阅者全部收到——不能只通知第一个（漏通知的那位
// 会带着旧视图继续跑，且没有任何信号）。
func TestAllSubscribersNotified(t *testing.T) {
	resetPolicy(t)

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

	// 各自注册时的立即回调各一次。
	for i, r := range []*rec{&a, &b, &c} {
		if got := r.count(); got != 1 {
			t.Fatalf("第 %d 位订阅者注册时应立即收到一次，实得 %d", i+1, got)
		}
		r.reset()
	}

	if err := SetPolicy([]string{"all.example"}, []string{".all"}); err != nil {
		t.Fatal(err)
	}
	for i, r := range []*rec{&a, &b, &c} {
		if got := r.count(); got != 1 {
			t.Fatalf("第 %d 位订阅者应各收到一次通知，实得 %d", i+1, got)
		}
		if !r.last().BlocksHostname("all.example") {
			t.Fatalf("第 %d 位订阅者拿到的不是新快照", i+1)
		}
	}
}

// TestUnsubscribeStopsNotifications 注销后不再收到。
//
// 没有注销这条腿，长期跑的宿主会把订阅闭包连同它捕获的一切永久留在表里——
// 而这条泄漏不会自己显形。
func TestUnsubscribeStopsNotifications(t *testing.T) {
	resetPolicy(t)

	var r rec
	h, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)
	r.reset()

	if err := SetPolicy([]string{"before.example"}, []string{".before"}); err != nil {
		t.Fatal(err)
	}
	if got := r.count(); got != 1 {
		t.Fatalf("注销前应收到通知，实得 %d", got)
	}

	Unsubscribe(h)
	r.reset()

	if err := SetPolicy([]string{"after.example"}, []string{".after"}); err != nil {
		t.Fatal(err)
	}
	if got := r.count(); got != 0 {
		t.Fatalf("注销后不应再收到通知，实得 %d 次——订阅表没摘干净", got)
	}
}

// TestUnsubscribeTwiceSafe 重复注销是空操作，不得影响仍在册的订阅者。
func TestUnsubscribeTwiceSafe(t *testing.T) {
	resetPolicy(t)

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

	if err := SetPolicy([]string{"still.example"}, []string{".still"}); err != nil {
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
	resetPolicy(t)

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

	if err := SetPolicy([]string{"x.example"}, []string{".x"}); err != nil {
		t.Fatal(err)
	}
	if got := r.count(); got != 1 {
		t.Fatalf("只有合法订阅者应收通知，实得 %d", got)
	}
}
