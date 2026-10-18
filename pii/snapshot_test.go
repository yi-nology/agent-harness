package pii

// 策略快照的并发护栏：一次处置 = 一个策略。
//
// 这里不放合成词表以外的东西：样本仍是 policy_test.go 里的合成手机号与身份证，
// 夹具带领域味就等于把契约钉死在某个域上。

import (
	"strings"
	"sync"
	"testing"
)

// TestApplySpansSnapshotSurvivesConcurrentStrategyFlip 宿主在重配期间并发换策略时，
// 一次处置必须**整体**按同一个策略完成。
//
// # 断言为什么只覆盖 redact / block 两档
//
// 这两档的处置形态**互不相同**（占位 vs 保长打码），所以「文本形态」与「blocked」
// 之间存在可判定的互斥关系：
//
//   - blocked=false ⇒ 快照不是 block ⇒ 全文不得出现 mask 的星号；
//   - 出现占位（redact 快照）⇒ blocked 必为 false。
//
// 逐段读全局的实现会在这里翻车：前半段按 mask 打码、后半段换占位，blocked 又按
// 末次读到的策略算，于是「有星号却不报阻断」或「出现占位却报阻断」当场成立。
// 快照实现无论怎么翻转都恒满足上表，所以这条用例不是概率性断言。
func TestApplySpansSnapshotSurvivesConcurrentStrategyFlip(t *testing.T) {
	restore(t)
	// 多段文本：翻转窗口足够宽，逐段读的缺陷才有机会显形。
	txt := strings.Repeat("a "+synMobile+" b ", 32) + synID
	// 先落进翻转集合再开跑：缺省策略是 mask，而上表的互斥关系只在 {redact, block}
	// 这个集合内成立（mask 与 block 的处置形态相同，从文本上分不开）。
	if err := SetStrategy(StrategyRedact); err != nil {
		t.Fatal(err)
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

	for i := 0; i < 500; i++ {
		out, blocked := Apply(SurfaceOutput, txt)
		if strings.Contains(out, redactedPlaceholder) && blocked {
			t.Fatalf("第 %d 次：按 redact 形态处置却回报阻断（快照与信号不同源）", i)
		}
		if !blocked && strings.Contains(out, "*") {
			t.Fatalf("第 %d 次：出现 mask 形态却不报阻断——说明处置与 blocked 取自两次不同的策略", i)
		}
	}
}
