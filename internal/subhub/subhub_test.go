package subhub

import (
	"sync"
	"testing"
)

// TestZeroValueUsable 钉住「使用方不必构造函数」。
//
// 三条使用方一个声明成包级变量（netguard/pii）、一个挂在结构体字段上
// （guardset.Assembly）——若本包要求构造函数，那个结构体字段就得改成指针并在
// 每个 New 路径里记得初始化，漏一处就是「订阅表是 nil，Add 直接崩」。
func TestZeroValueUsable(t *testing.T) {
	var r Registry[int]
	var got []int
	if h := r.Add(func(v int) { got = append(got, v) }); h == 0 {
		t.Fatal("零值 Registry 的 Add 必须返回有效句柄（0 被保留为无效）")
	}
	r.Notify(7)
	if len(got) != 1 || got[0] != 7 {
		t.Fatalf("零值 Registry 应能通知，实得 %v", got)
	}
}

// TestHandleIsMonotonicAndUnique 钉住句柄唯一且单调。
//
// 句柄是「这次注销是不是我登记的那一次」的唯一凭据。若发号出现重复，
// Remove 会误放掉**别人**的订阅——而失效形态是「某个订阅者悄悄不再收到通知」，
// 不红不报。
func TestHandleIsMonotonicAndUnique(t *testing.T) {
	var r Registry[int]
	seen := map[Handle]bool{}
	prev := Handle(0)
	for i := 0; i < 100; i++ {
		h := r.Add(func(int) {})
		if h == 0 {
			t.Fatal("Add 不得返回 0（0 表示无效句柄）")
		}
		if seen[h] {
			t.Fatalf("句柄 %d 重复发放", h)
		}
		if h <= prev {
			t.Fatalf("句柄必须单调递增：%d 之后发出 %d", prev, h)
		}
		seen[h] = true
		prev = h
	}
}

// TestRemoveOnlyItsOwnHandle 钉住注销只认句柄、不认内容。
//
// 这是本包存在的并发正确性核心：若 Remove 靠「回调指针相同」或「序号相同」认人，
// 同名分支/同一份回调被登记两次时就会放掉错的那一个。
func TestRemoveOnlyItsOwnHandle(t *testing.T) {
	var r Registry[int]
	var got []string
	fn := func(int) { got = append(got, "x") }
	h1 := r.Add(fn)
	h2 := r.Add(fn) // 逐字相同的回调，两个不同的订阅

	r.Remove(h1)
	r.Notify(1)
	if len(got) != 1 {
		t.Fatalf("去掉一个应只剩一个订阅者，实得 %d 次调用", len(got))
	}

	// 重复注销/从未登记过的句柄：静默空操作，不得影响在册的那一个。
	r.Remove(h1)
	r.Remove(Handle(9999))
	r.Remove(0)
	if r.Len() != 1 {
		t.Fatalf("无效注销不得改动订阅表，在册数应为 1，实得 %d", r.Len())
	}

	r.Remove(h2)
	if r.Len() != 0 {
		t.Fatalf("全部注销后在册数应为 0，实得 %d", r.Len())
	}
}

// TestNotifyUsesOneSnapshot 钉住「遍历的是一次快照」。
//
// 回调里增删订阅者不得改变**本次**通知的名单：否则通知名单随遍历进度漂移，
// 同一个变更对部分订阅者送达、对另一部分静默不送达，且没有任何信号。
func TestNotifyUsesOneSnapshot(t *testing.T) {
	var r Registry[int]
	var mu sync.Mutex
	var order []string

	var hB Handle
	added := false
	r.Add(func(int) {
		mu.Lock()
		order = append(order, "A")
		// 回调里：注销 B、登记 C——两者都不该影响本次通知。
		if !added {
			added = true
			r.Remove(hB)
			r.Add(func(int) {
				mu.Lock()
				defer mu.Unlock()
				order = append(order, "C")
			})
		}
		mu.Unlock()
	})
	hB = r.Add(func(int) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, "B")
	})

	r.Notify(1)
	if len(order) != 2 || order[0] != "A" || order[1] != "B" {
		t.Fatalf("本次通知应按快照送出 A、B，实得 %v", order)
	}

	// 下一次通知才反映新的名单：B 已注销，C 已登记。
	order = nil
	r.Notify(2)
	if len(order) != 2 || order[0] != "A" || order[1] != "C" {
		t.Fatalf("下一次通知应反映新名单 A、C，实得 %v", order)
	}
}

// TestPanicIsolated 钉住「一个回调 panic 不影响其余回调、不外溢」。
//
// 调用点全都在「变更已经落地」之后。让其中一个订阅者把整次变更说成失败，
// 等于把已发生的事实改写成没发生——那正是这层机制要消灭的失效。
func TestPanicIsolated(t *testing.T) {
	var r Registry[int]
	called := 0
	r.Add(func(int) { panic("订阅者自己炸了") })
	r.Add(func(int) { called++ })
	r.Add(func(int) { panic("第二个也炸了") })
	r.Add(func(int) { called++ })

	r.Notify(1) // 不得 panic（panic 一旦外溢，本用例会以整个测试二进制崩溃收场）
	if called != 2 {
		t.Fatalf("panic 的订阅者不得影响其余订阅者，应有 2 次成功调用，实得 %d", called)
	}
}

// TestDoSwallowsPanic 钉住无载荷回调的同一条纪律（留痕/通知类副作用）。
func TestDoSwallowsPanic(t *testing.T) {
	Do(func() { panic("留痕回调炸了") })
	ran := false
	Do(func() { ran = true })
	if !ran {
		t.Fatal("Do 应正常执行回调")
	}
}

// TestPayloadIsSameValueForAll 钉住「同一次变更对每个订阅者送出同一个 v」。
//
// 载荷若按订阅者各造一份，订阅者之间的视图就会出现「同一次换份、两种内容」——
// 与「不可撕裂读」正好相反。
func TestPayloadIsSameValueForAll(t *testing.T) {
	var r Registry[[]string]
	var a, b []string
	r.Add(func(v []string) { a = v })
	r.Add(func(v []string) { b = v })
	payload := []string{"g1", "g2"}
	r.Notify(payload)
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("两个订阅者都应收到载荷，实得 %v / %v", a, b)
	}
	// 同一个底层数组：改 a 的元素能看见 b 的变化。
	a[0] = "changed"
	if b[0] != "changed" {
		t.Fatal("同一次通知必须给每个订阅者同一份载荷（不是各造一份）")
	}
}

// TestConcurrentAddRemoveNotify 并发下的唯一断言是「不崩、不丢已注销的、不炸竞态」。
//
// 用 -race 跑（make race）时它同时验证 copy-on-write 发布没有数据竞争。
func TestConcurrentAddRemoveNotify(t *testing.T) {
	var r Registry[int]
	const workers = 8
	const rounds = 50

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				h := r.Add(func(int) {})
				r.Notify(j)
				r.Remove(h)
			}
		}()
	}
	wg.Wait()

	if r.Len() != 0 {
		t.Fatalf("全部注销后在册数应为 0，实得 %d", r.Len())
	}
}

// TestConcurrentRemoveSameHandle 钉住「多个 goroutine 同时注销同一句柄」不丢别的订阅。
//
// CAS 失败必须重试而不是放弃：放弃会让「同时注销」变成「一个成功、其余静默无效」，
// 而真正的失效是——它们本来要删的那个还在册（于是回调泄漏），或者重试时错删了别人。
func TestConcurrentRemoveSameHandle(t *testing.T) {
	var r Registry[int]
	var mu sync.Mutex
	keptCalls := 0
	r.Add(func(int) {
		mu.Lock()
		defer mu.Unlock()
		keptCalls++
	})
	shared := r.Add(func(int) {})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Remove(shared)
		}()
	}
	wg.Wait()

	if r.Len() != 1 {
		t.Fatalf("只剩一个在册订阅，实得 %d", r.Len())
	}
	r.Notify(1)
	mu.Lock()
	defer mu.Unlock()
	if keptCalls != 1 {
		t.Fatalf("留下的必须是没被注销的那一个，实得 %d 次调用", keptCalls)
	}
}
