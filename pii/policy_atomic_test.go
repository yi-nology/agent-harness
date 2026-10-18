package pii

// 策略面「不可撕裂读」的护栏与对照组。
//
// # 这组用例补的是哪条缝
//
// snapshot_test.go 已经证明**单文本**路径（Apply）会整份快照策略。它没有覆盖
// **多叶载荷**路径（ApplyValue），而后者才是宿主处理工具返回的主要入口——
// issue 里单列的「工具返回与模型输出不是一个风险面」指的就是它。
//
// ApplyValue 原实现把闭包交给递归：`func(t string) { return Apply(s, t) }`，
// 于是**每个字符串叶子都重新走一遍 Apply，各读一次全局策略**。一次遍历跨越
// 一次热重载，载荷内就会同时出现两种处置形态：前半按 mask 打码、后半换占位，
// 而 blocked 由 OR 累加得出，两者不同源。这正是 applySpansAs 注释里声明要
// 消除的现象，只不过那条保证止步于单文本，复合载荷从旁边绕了过去。
//
// 判据沿用 snapshot_test.go 那张互斥表（只在 {redact, block} 集合内成立：
// mask 与 block 处置形态相同，从文本上分不开）：
//
//	出现 redact 占位 ⇒ 快照不是 block ⇒ blocked 必为 false
//	同一载荷内      ⇒ 形态必须单一，混不得

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestApplyValueMultiLeafStaysSelfConsistent 多叶载荷在宿主并发换策略时，
// 必须整体按同一份快照处置，且 blocked 与处置形态同源。
func TestApplyValueMultiLeafStaysSelfConsistent(t *testing.T) {
	restore(t)
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatal(err)
	}

	// 64 个叶子：遍历窗口足够宽，逐叶读的缺陷才有机会显形。
	payload := make(map[string]string, 64)
	for i := 0; i < 64; i++ {
		payload["k"+strconv.Itoa(i)] = synMobile
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = SetStrategy(StrategyRedact)
			_ = SetStrategy(StrategyBlock)
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	for i := 0; i < 300; i++ {
		out, blocked := ApplyValue(SurfaceToolResults, payload)
		leaves, ok := out.(map[string]string)
		if !ok {
			t.Fatalf("第 %d 次：载荷结构被破坏，实得 %T", i, out)
		}
		placeholder := 0
		for _, v := range leaves {
			if strings.Contains(v, redactedPlaceholder) {
				placeholder++
			}
		}
		if placeholder != 0 && placeholder != len(leaves) {
			t.Fatalf("第 %d 次：同一载荷内出现混合处置形态（%d/%d 个 redact 占位，其余按 block 打码）——"+
				"说明逐叶重读了全局策略", i, placeholder, len(leaves))
		}
		if placeholder > 0 && blocked {
			t.Fatalf("第 %d 次：按 redact 形态处置却回报阻断（快照与信号不同源）", i)
		}
	}
}

// ── 与旧设计的对照：证明「撕裂」是真实存在的失败形态 ──

// TestThreeIndependentAtomicsDoTear 对照组：enabled / surfaces / strategy 三份
// 状态各自原子，整体替换时读侧分三次取，就会读到**从未被任何写侧设置过**的组合。
//
// 这条用例的作用是把「我们在防什么」写死成可执行的证据，避免后来者认为
// 「三个都是 atomic.Bool/Uint32，应该没问题」。判定用 atomic 计数而非 t.Errorf，
// 因为这是对照组演示，不是被测对象的失败。
func TestThreeIndependentAtomicsDoTear(t *testing.T) {
	// 两份完整且合法的策略：
	//   p1 = 开启 + 三面全开 + mask
	//   p2 = 开启 + 三面全关 + block（宿主「这个面不脱敏了」的合法意图）
	const full, maskCode, blockCode = uint32(SurfaceAll), uint32(1), uint32(4)

	var en atomic.Bool
	var sf atomic.Uint32
	var st atomic.Uint32
	en.Store(true)
	sf.Store(full)
	st.Store(maskCode)

	// 合法组合只有两种：全部取自 p1，或全部取自 p2。
	// 「全开 + block」「全关 + mask」都不对应任何一次真实设置。
	legal := func(e bool, s, g uint32) bool {
		return e && ((s == full && g == maskCode) || (s == 0 && g == blockCode))
	}

	var torn atomic.Int64
	var reads atomic.Int64
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
			// 整体替换 p2：三次 Store 之间读侧可以插进来
			en.Store(true)
			sf.Store(0)
			st.Store(blockCode)
			// 整体替换回 p1
			en.Store(true)
			sf.Store(full)
			st.Store(maskCode)
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
			for i := 0; i < 200; i++ {
				e := en.Load()
				s := sf.Load()
				g := st.Load()
				reads.Add(1)
				if !legal(e, s, g) {
					torn.Add(1)
				}
			}
		}
	}()

	time.Sleep(400 * time.Millisecond)
	close(stop)
	wg.Wait()

	if torn.Load() == 0 {
		t.Fatal("对照组失效：跑满 400ms 一次撕裂都没读到，说明读法没制造出窗口——" +
			"本用例失去了证据价值（要么并发度不足，要么写侧顺序变了）")
	}
	t.Logf("对照成立：%d 次读取中 %d 次读到非法组合", reads.Load(), torn.Load())
}
