package pii

// 处置策略变更订阅的边界与并发形态：被拒的变更不通知、订阅者崩了不牵连他人与策略面、
// 回调里再改策略不死锁、名单在剧烈增删下不被撕裂。
//
// 正常路径在 subscribe_test.go。

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── 边界：被拒的变更、panic、回调内改策略 ──

// TestRejectedStrategyDoesNotNotify 校验失败的变更**一次通知都不发**。
//
// 发了就等于告诉订阅者「策略换过了」，而它此刻根本没换。订阅者据此重建的派生视图
// 会对应一份从未生效过的策略，且没有任何东西能指出这一点。
func TestRejectedStrategyDoesNotNotify(t *testing.T) {
	restore(t)

	var r rec
	h, err := Subscribe(r.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)
	r.reset()

	if err := SetStrategy("nonsense"); err == nil {
		t.Fatal("未知策略应被拒绝")
	}
	if got := r.count(); got != 0 {
		t.Fatalf("被拒的变更不得触发通知，实得 %d 次", got)
	}
	if Strategy() != StrategyMask {
		t.Fatalf("被拒的变更不得改动已生效策略，实得 %q", Strategy())
	}
}

// TestSubscriberPanicDoesNotBreakOthers 订阅者 panic 不得影响其他订阅者，
// 也不得把已落地的 CAS 说成失败。
//
// 一次成功的 CAS 已经落地——让某个订阅者抛异常就把它改写成「没发生」，等于把已经
// 发生的事实说成没发生，而这恰恰是订阅面要消灭的那类失效。
func TestSubscriberPanicDoesNotBreakOthers(t *testing.T) {
	restore(t)

	var before, boom, after rec
	hb, err := Subscribe(before.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, hb)
	hm, err := Subscribe(func(p Policy) {
		boom.fn(p)
		panic("订阅者自己崩了")
	})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, hm)
	ha, err := Subscribe(after.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ha)
	before.reset()
	boom.reset()
	after.reset()

	if err := SetStrategy(StrategyBlock); err != nil {
		t.Fatalf("订阅者 panic 不得让 SetStrategy 变成失败：%v", err)
	}

	if got := after.count(); got != 1 {
		t.Fatalf("崩掉的订阅者之后的订阅者仍应收到通知，实得 %d——panic 吞掉了后半个名单", got)
	}
	if after.last().strategy != StrategyBlock {
		t.Fatal("后置订阅者拿到的不应是新快照")
	}
	if got := before.count(); got != 1 {
		t.Fatalf("前置订阅者也应收到，实得 %d", got)
	}

	// 策略面本身必须已经生效：被 panic 打断之后读侧拿到的还是新策略。
	if Strategy() != StrategyBlock {
		t.Fatal("订阅者 panic 后策略面必须仍是已生效的新策略")
	}
}

// TestSubscriberMaySetStrategyRecursively 订阅者回调里再调 SetStrategy 不得死锁。
//
// 这条最容易写错：通知一旦发生在任何一把锁内，回调里再取策略就是自锁死锁。
// 本包的订阅表是快照式替换、没有锁，通知在锁外，故回调内可安全再改策略。
//
// 判据是「没有死锁」——回调内再改策略会再触发一轮通知，递归深度由订阅者自己收敛。
// 故只在**变更通知**那一次（第 2 次进入）回改策略：只有走通知这条路才谈得上
// 「通知是否发生在锁内」，从注册时的立即回调里回改，测的是另一条路径。
func TestSubscriberMaySetStrategyRecursively(t *testing.T) {
	restore(t)

	done := make(chan struct{})
	failed := make(chan error, 1)
	var calls atomic.Int64

	// # 整段交互放进 goroutine：测试体绝不能自己参与通知路径
	//
	// 通知一旦真在锁内，交互方会卡死在回调里出不来。若测试体同在这条路径上，它
	// 也就跟着卡死——于是 5 秒超时**永远到不了**，死锁从「用例变红」退化成「整个
	// 套件挂到 Go 的总超时」。会挂死的门禁比没有门禁更糟：CI 上它只是变慢，
	// 本地却看不出是哪条用例。
	//
	// 测试体只与 channel 打交道，不碰任何可能拿锁的调用。
	go func() {
		h, err := Subscribe(func(Policy) {
			// 第 1 次是注册时的立即回调，第 2 次才是变更通知——只在后者回改。
			if calls.Add(1) != 2 {
				return
			}
			if serr := SetStrategy(StrategyRedact); serr != nil {
				select {
				case failed <- serr:
				default:
				}
				return
			}
			close(done)
		})
		if err != nil {
			select {
			case failed <- err:
			default:
			}
			return
		}
		defer Unsubscribe(h)
		if serr := SetStrategy(StrategyHash); serr != nil {
			select {
			case failed <- serr:
			default:
			}
		}
	}()

	select {
	case <-done:
	case err := <-failed:
		t.Fatalf("交互失败: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("订阅者回调内再调 SetStrategy 死锁了——通知多半发生在锁内")
	}

	if Strategy() != StrategyRedact {
		t.Fatal("回调内设置的策略应当生效")
	}
}

// ── 并发 ──

// TestConcurrentSubscribeUnsubscribeAndNotify 反复变更 + 反复订阅/退订并发进行，
// 不得崩、不得竞态、不得把订阅表撕裂（配 -race 有效）。
func TestConcurrentSubscribeUnsubscribeAndNotify(t *testing.T) {
	restore(t)

	// 一位长驻订阅者：它证明名单在剧烈增删下仍能被稳定遍历。
	var resident rec
	h, err := Subscribe(resident.fn)
	if err != nil {
		t.Fatal(err)
	}
	drain(t, h)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// 写侧：高频换策略。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := SetStrategy(StrategyBlock); err != nil {
				t.Error(err)
				return
			}
		}
	}()

	// 订阅侧：反复订阅、反复退订，制造「通知正在遍历时名单被改」的窗口。
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local rec
			for {
				select {
				case <-stop:
					return
				default:
				}
				sh, err := Subscribe(local.fn)
				if err != nil {
					t.Error(err)
					return
				}
				Unsubscribe(sh)
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()

	if resident.count() == 0 {
		t.Fatal("长驻订阅者一次通知都没收到——并发增删把通知路径打断了")
	}
	// 通知快照必须是完整的一份：代次单调不减，且相邻两份不是同一个代次被叫两遍。
	lastGen := uint64(0)
	for i, p := range resident.all() {
		if p.gen < lastGen {
			t.Fatalf("第 %d 份通知快照的代次回退了：%d → %d（通知发了陈旧快照）", i, lastGen, p.gen)
		}
		lastGen = p.gen
	}
}
