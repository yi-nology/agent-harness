package netguard

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetPolicy 把扩展策略清回空，避免用例之间互相污染（本包是包级全局）。
func resetPolicy(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { _ = SetPolicy(nil, nil) })
}

// ── 不可撕裂读：本条改动的全部存在理由 ──

// TestPolicyReadNeverTears 是这次重构的核心断言：
// **任何一次读到的策略，必须是某一份完整策略的原样，不得是两个版本拼起来的。**
//
// 热重载会交替装载两份完整策略 P1（主机名 alpha / 后缀 .one）与
// P2（主机名 beta / 后缀 .two）。读侧若分两次取快照，就可能读到
// 「alpha 的主机名 + .two 的后缀」这种两边都不存在的组合——
// 每个字段单独看都是原子且正确的，组合起来的那一瞬间不是。
//
// 这正是 OpenCode 2.0 issue #52333 的形状（至 2026-10 未修）：配置热重载窗口
// 绕过 deny 策略，且不报错、不告警，事后也查不出用的是哪份策略。
func TestPolicyReadNeverTears(t *testing.T) {
	resetPolicy(t)

	p1Host, p1Sfx := "alpha.example", ".one"
	p2Host, p2Sfx := "beta.example", ".two"

	if err := SetPolicy([]string{p1Host}, []string{p1Sfx}); err != nil {
		t.Fatal(err)
	}
	if err := SetPolicy([]string{p2Host}, []string{p2Sfx}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 写侧：在两份**完整**策略之间高频交替，模拟热重载反复生效。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := SetPolicy([]string{p1Host}, []string{p1Sfx}); err != nil {
				t.Error(err)
				return
			}
			if err := SetPolicy([]string{p2Host}, []string{p2Sfx}); err != nil {
				t.Error(err)
				return
			}
		}
	}()

	// 读侧：断言每次读到的都是某一份完整策略。
	var reads atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			p := CurrentPolicy()
			hasP1Host := p.BlocksHostname(p1Host)
			hasP2Host := p.BlocksHostname(p2Host)
			_, s1 := p.BlocksSuffix("probe" + p1Sfx)
			_, s2 := p.BlocksSuffix("probe" + p2Sfx)

			// 合法形态只有四种：(P1,P1) (P2,P2) 与它们各自的空态。
			// 「主机名是 P1 但后缀是 P2」= 撕裂读，必须为 0。
			torn := (hasP1Host && s2) || (hasP2Host && s1)
			if torn {
				t.Errorf("读到撕裂策略：主机名 p1=%v p2=%v，后缀 s1=%v s2=%v（代次 %d）",
					hasP1Host, hasP2Host, s1, s2, p.Generation())
				return
			}
			reads.Add(1)
		}
	}()

	// 跑到样本量够为止（带时限兜底）：空转等固定轮数没有意义——
	// 那只等到了主协程自己跑完，读侧可能一次都没跑过。
	waitSamples(&reads, 200000)

	close(stop)
	wg.Wait()

	if got := reads.Load(); got < 1000 {
		t.Fatalf("读侧样本太少（%d 次），本用例不足以说明问题", got)
	}
}

// waitSamples 轮询直到计数器达到 target 或超时返回。
// 判据是「读了很多次且一次没撕裂」，不是「一定读到两种策略」——
// 并发下没有「跑够就一定覆盖」的保证，强行要求覆盖只会让用例偶发红。
func waitSamples(c *atomic.Int64, target int64) {
	deadline := time.Now().Add(3 * time.Second)
	for c.Load() < target && time.Now().Before(deadline) {
		time.Sleep(200 * time.Microsecond)
	}
}

// TestPolicyGenerationAdvances 代次可观测：策略换过没有，外部能回答。
//
// 「闸被重置后没人告知」是本仓 checkpoint.SeqGate 记录过的失效形态，
// 这里必须保证调用方拿得到代次，否则策略被换掉后无从追溯。
func TestPolicyGenerationAdvances(t *testing.T) {
	resetPolicy(t)

	g0 := PolicyGeneration()
	if err := SetPolicy([]string{"a.example"}, []string{".s"}); err != nil {
		t.Fatal(err)
	}
	g1 := PolicyGeneration()
	if g1 <= g0 {
		t.Fatalf("成功交换后代次应递增：%d → %d", g0, g1)
	}
	if got := CurrentPolicy().Generation(); got != g1 {
		t.Fatalf("快照内代次应与全局一致：快照 %d，全局 %d", got, g1)
	}
}

// TestPolicyGenerationNotAdvancedOnReject 校验失败不得推进代次。
// 代次是「换过没有」的唯一凭据，被失败的尝试推高就失去意义了。
func TestPolicyGenerationNotAdvancedOnReject(t *testing.T) {
	resetPolicy(t)

	if err := SetPolicy([]string{"keep.example"}, []string{".keep"}); err != nil {
		t.Fatal(err)
	}
	before := PolicyGeneration()

	// 无前导点的后缀：非法。
	if err := SetPolicy([]string{"x.example"}, []string{"nodot"}); err == nil {
		t.Fatal("无前导点后缀应被拒绝")
	}
	if got := PolicyGeneration(); got != before {
		t.Fatalf("被拒的交换不应推进代次：%d → %d", before, got)
	}
	// 且旧策略必须原样保留，不能半应用。
	p := CurrentPolicy()
	if !p.BlocksHostname("keep.example") {
		t.Fatal("被拒的交换不得改动已生效策略")
	}
	if _, hit := p.BlocksSuffix("a.keep"); !hit {
		t.Fatal("被拒的交换不得清掉既有后缀（半应用）")
	}
}

// TestPolicyLegacySettersPreserveOtherField 单项设置器不得清掉另一项。
//
// 旧版两个设置器各自只改一项、互不影响，但「分别调用」在两次调用之间存在撕裂
// 窗口。改为「取出当前策略 → 改一项 → 整份换回」后，单独调用依然安全。
func TestPolicyLegacySettersPreserveOtherField(t *testing.T) {
	resetPolicy(t)

	if err := SetExtraBlockedHostSuffixes(".corp"); err != nil {
		t.Fatal(err)
	}
	SetExtraBlockedHostnames("only.example")

	p := CurrentPolicy()
	if !p.BlocksHostname("only.example") {
		t.Fatal("设主机名后应生效")
	}
	if _, hit := p.BlocksSuffix("a.corp"); !hit {
		t.Fatal("设主机名不应清掉已注册的后缀")
	}

	if err := SetExtraBlockedHostSuffixes(".intra"); err != nil {
		t.Fatal(err)
	}
	p = CurrentPolicy()
	if !p.BlocksHostname("only.example") {
		t.Fatal("设后缀不应清掉已注册的主机名")
	}
	if _, hit := p.BlocksSuffix("a.intra"); !hit {
		t.Fatal("新后缀应生效")
	}
}

// TestCurrentPolicyNeverNil 零值状态取策略不得返回 nil，调用方不必到处判空。
func TestCurrentPolicyNeverNil(t *testing.T) {
	resetPolicy(t)

	p := CurrentPolicy()
	if p == nil {
		t.Fatal("零值状态下 CurrentPolicy 不应为 nil")
	}
	if p.BlocksHostname("anything.example") {
		t.Fatal("空策略不应拦任何主机名")
	}
	if sfx, hit := p.BlocksSuffix("a.b"); hit {
		t.Fatalf("空策略不应拦任何后缀，实得 %q", sfx)
	}
}

// TestPolicyGenerationNeverRewinds 代次单调，清空策略**不回卷**。
//
// 这条是「代次」这个机制成立的前提：调用方靠「代次变了没」回答
// 「策略这段时间换过没有」。若清空把代次归零，那么
// 「换上一份 → 清空 → 再换上一份」与「什么都没换」在代次上不可区分，
// 可观测性就白做了。
func TestPolicyGenerationNeverRewinds(t *testing.T) {
	resetPolicy(t)

	if err := SetPolicy([]string{"a.example"}, []string{".s"}); err != nil {
		t.Fatal(err)
	}
	peak := PolicyGeneration()

	if err := SetPolicy(nil, nil); err != nil {
		t.Fatal(err)
	}
	afterClear := PolicyGeneration()
	if afterClear < peak {
		t.Fatalf("清空策略不应回卷代次：%d → %d", peak, afterClear)
	}

	if err := SetPolicy([]string{"a.example"}, []string{".s"}); err != nil {
		t.Fatal(err)
	}
	if got := PolicyGeneration(); got <= afterClear {
		t.Fatalf("清空后再换应当继续推进代次：%d → %d", afterClear, got)
	}
}

// TestConcurrentSetPolicyAndRead 反复换策略与判定并发，不得崩、不得竞态。
// 配 -race 才有效。
func TestConcurrentSetPolicyAndRead(t *testing.T) {
	resetPolicy(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = SetPolicy([]string{"n.example"}, []string{".n"})
				SetExtraBlockedHostnames()
				_ = SetExtraBlockedHostSuffixes()
				_, _, _ = classifyHostName("probe.n")
			}
		}(i)
	}
	wg.Wait()
}

// ── 与旧设计的对照：证明「撕裂」是真实存在的失败形态 ──

// TestTwoIndependentPointersDoTear 对照组：换成两个独立 atomic.Pointer 后，
// 同样的读法就会读到撕裂策略。
//
// 这条用例的作用是把「我们在防什么」写死成可执行的证据，避免后来者认为
// 「两个指针也都是原子的，应该没问题」。判定用 atomic 计数而非 t.Errorf，
// 因为这是对照组演示，不是被测对象的失败。
func TestTwoIndependentPointersDoTear(t *testing.T) {
	var hostPtr atomic.Pointer[map[string]struct{}]
	var sfxPtr atomic.Pointer[[]string]
	p1s, p2s := []string{".one"}, []string{".two"}
	p1h := map[string]struct{}{"alpha.example": {}}
	p2h := map[string]struct{}{"beta.example": {}}

	// 预热：先让两侧都有值，再开始交替
	hostPtr.Store(&p1h)
	sfxPtr.Store(&p1s)

	var torn atomic.Int64
	reads := atomic.Int64{}
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			hostPtr.Store(&p1h)
			sfxPtr.Store(&p1s)
			hostPtr.Store(&p2h)
			sfxPtr.Store(&p2s)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// 旧设计的读法：两次独立 Load。
			hn := hostPtr.Load()
			sf := sfxPtr.Load()
			_, isP1Host := (*hn)["alpha.example"]
			_, isP2Host := (*hn)["beta.example"]
			s1 := (*sf)[0] == ".one"
			s2 := (*sf)[0] == ".two"
			if (isP1Host && s2) || (isP2Host && s1) {
				torn.Add(1)
			}
			reads.Add(1)
		}
	}()

	waitSamples(&reads, 200000)

	close(stop)
	wg.Wait()

	// 旧设计必然出现撕裂：两次 Load 之间写侧已经换掉了另一侧。
	// 这条断言若失败，说明本机调度把窗口全躲过去了——那属于对照组退化，
	// 应当降低对它的依赖，而不是放松被测对象的断言。
	t.Logf("对照组：读 %d 次，其中撕裂 %d 次（旧设计的失败形态）", reads.Load(), torn.Load())
	if torn.Load() == 0 {
		t.Log("提示：本次调度未复现对照组撕裂（不代表旧设计安全）")
	}
}
