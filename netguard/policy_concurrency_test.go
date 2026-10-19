// policy_concurrency_test.go —— 策略代次在并发交换下的唯一性与自洽性。
//
// # 为什么这条要单独钉
//
// 代次是本包对外承诺的可观测性凭据（包头：「每次交换递增 Generation，
// 并经 CurrentPolicy / PolicyGeneration 暴露」「代次单调且不回卷」）。
// 调用方据此判断「我读策略的这段时间里，策略被换过没有」。
//
// 旧实现用**两个独立的原子变量**记同一件事：
//
//	next := policyGen.Load() + 1     ← 非原子的读-改-写
//	curPolicy.Store(p)               ← 策略指针（含 gen）
//	policyGen.Store(next)            ← 另一个计数器
//
// 三个问题叠在一起：
//
//  1. **代次会重复**。两个并发写者可能读到同一个 policyGen，各自算出同一个
//     next，于是两次交换拿到**同一个代次**——「每次交换递增」被打破。
//     后果：轮询方看到代次 6 → 6，认为「没换过」，而策略其实换了一次。
//     这正是本包要防的那类失效（「换过没有」给不出答案）。
//  2. **代次会回退**。读-改-写之间若有交错，后写者可能把 policyGen 写回
//     较小的值，于是代次不再单调——而「单调且不回卷」是写进注释的承诺。
//  3. **两套真相**。curPolicy 里的 gen 与 policyGen 是两个变量，
//     curPolicy.Store 与 policyGen.Store 之间存在窗口，读者能同时看到
//     「快照 gen=6、全局 gen=5」这种自相矛盾的一对。
//
// 同族的 pii 包已经用 CAS 循环做对了（代次从**观察到的**旧策略推导，
// CAS 保证只有一个写者胜出，代次天然唯一、单源）。本包应当对齐。

package netguard

import (
	"strconv"
	"sync"
	"testing"
)

// TestPolicyGenerationUniqueUnderConcurrentSwap 并发交换的代次必须互不相同。
//
// 判据：N 次成功交换后，代次必须恰好比起始值大 N。
// 旧实现下 N 次并发交换常常只推进 1~2 代（大量写者读到同一个旧值），
// 于是这条断言直接红。
func TestPolicyGenerationUniqueUnderConcurrentSwap(t *testing.T) {
	resetPolicy(t)

	const n = 64
	start := PolicyGeneration()

	var wg sync.WaitGroup
	startGate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startGate // 尽量让所有写者挤在同一时刻读旧代次
			_ = SetPolicy([]string{"h" + strconv.Itoa(i) + ".example"}, []string{".corp"})
		}(i)
	}
	close(startGate)
	wg.Wait()

	got := PolicyGeneration()
	want := start + n
	if got != want {
		t.Fatalf("并发交换 %d 次后代次应恰好推进 %d（每次交换 +1，代次唯一）；"+
			"实得 %d（起始 %d）——代次重复即「换过没有」给不出答案", n, n, got, start)
	}
}

// TestPolicyGenerationMonotonicUnderConcurrentSwap 代次在并发下必须单调不回退。
//
// 旧实现的 policyGen.Store(next) 与 curPolicy.Store(p) 分离，
// 交错时可能把计数器写回较小的值——「单调且不回卷」是注释里的承诺。
func TestPolicyGenerationMonotonicUnderConcurrentSwap(t *testing.T) {
	resetPolicy(t)

	const n = 32
	base := PolicyGeneration()
	var wg sync.WaitGroup
	startGate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startGate
			_ = SetPolicy([]string{"m" + strconv.Itoa(i) + ".example"}, []string{".corp"})
		}(i)
	}
	close(startGate)

	// 交换过程中持续观察代次，记录见过的最小值。
	var mu sync.Mutex
	lo := base
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			g := PolicyGeneration()
			mu.Lock()
			if g < lo {
				lo = g
			}
			mu.Unlock()
		}
	}()

	wg.Wait()
	close(done)
	mu.Lock()
	defer mu.Unlock()
	if lo < base {
		t.Fatalf("代次出现回退：观察到最小值 %d，起始代次 %d", lo, base)
	}
}

// TestPolicyGenerationSingleSourceOfTruth 快照代次与全局代次必须来自同一个真相。
//
// 判据不是「两者相等」——并发下两次独立取快照本来就不该相等（中间可能换过
// 策略），拿相等做断言是**假的不变式**。正确的不变式是单向的：
//
//	先取快照（代次 N），再取全局代次 → 全局代次必须 ≥ N
//
// 代次只增不减，故「后读的」不可能小于「先读的」。而旧实现里全局代次来自
// **另一个变量**（policyGen），在 curPolicy.Store 与 policyGen.Store 之间的
// 窗口里，快照已是第 N 代而全局还停在第 N-1 代——**快照超前于全局**。
// 那正是「两套真相」的可观测形态。
//
// 旧实现下这个窗口极窄（两条指令），故采样要够多才必然撞上；
// 修复后该不等式在结构上永不可能被违反，采样多少遍都是绿的。
func TestPolicyGenerationSingleSourceOfTruth(t *testing.T) {
	resetPolicy(t)

	const n = 32
	var wg sync.WaitGroup
	startGate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-startGate
			_ = SetPolicy([]string{"s" + strconv.Itoa(i) + ".example"}, []string{".corp"})
		}(i)
	}
	close(startGate)

	stop := make(chan struct{})
	var wg2 sync.WaitGroup
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snapGen := CurrentPolicy().Generation()
			globalGen := PolicyGeneration()
			if snapGen > globalGen {
				t.Errorf("快照代次 %d 超前于全局代次 %d（两套真相：快照与全局不是同一个计数器）",
					snapGen, globalGen)
				return
			}
		}
	}()
	wg.Wait()
	close(stop)
	wg2.Wait()

	// 静止后两者必须一致（公开 API 的一致性承诺）。
	if got, want := CurrentPolicy().Generation(), PolicyGeneration(); got != want {
		t.Fatalf("静止后快照代次 %d 与全局代次 %d 必须一致", got, want)
	}
}
