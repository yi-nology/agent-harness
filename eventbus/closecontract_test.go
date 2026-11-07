package eventbus

import (
	"context"
	"testing"
	"time"
)

// 本文件专钉「退订即关闭」这条 channel 契约。与 eventbus_test.go 分开放，
// 是因为它判的不是「事件对不对」而是**消费方的写法要求**：契约一旦回退，
// 「退订后不再收事件」会以最难查的方式失效（旧 channel 仍挂着、仍收事件、无报错）。

// T5 退订后不再收事件；cancel 可重复调用且 ctx 结束自动退订。
func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe(context.Background())
	b.Publish("s", "", nil, EventSessionCreated, "info", nil)
	if _, ok := waitReceive(ch); !ok {
		t.Fatal("退订前应能收到事件")
	}

	cancel()
	cancel() // 幂等，不应 panic
	b.Publish("s", "", nil, EventStepStarted, "info", nil)
	b.Publish("s", "", nil, "step_finished", "info", nil)
	if got := drainAll(ch); len(got) != 0 {
		t.Fatalf("退订后不应再收事件, got %v", got)
	}

	// ctx 取消 = 自动退订，退订完成的**判据是 channel 被关闭**（unsubscribe 与
	// close 在同一个 once 里、同锁完成）。旧写法只能「发一条看收不收得到」地轮询
	// ——那是「不关 channel」时代的唯一可用信号，现在有更硬的一条：关闭是**不可能
	// 被误读**的（缓冲里还有事件时 ok 仍为 true，故要先抽干再判关闭）。
	ctx, ctxCancel := context.WithCancel(context.Background())
	ch2, cancel2 := b.Subscribe(ctx)
	defer cancel2()
	ctxCancel()
	closed := false
	deadline := time.After(2 * time.Second)
	for !closed {
		select {
		case _, ok := <-ch2:
			closed = !ok
		case <-deadline:
			t.Fatal("ctx 取消后订阅 channel 未在 2s 内关闭（自动退订未生效）")
		}
	}
	// 关掉之后再发：已关闭的 channel 收不到任何东西（读到的是 ok=false 的零值）。
	b.Publish("s", "", nil, EventStepStarted, "info", nil)
	if _, ok := <-ch2; ok {
		t.Fatal("已关闭的订阅 channel 不应再收到事件")
	}
}

// TestSubscribeChannelClosesOnCancel 钉住「退订即关闭」这条契约本身。
//
// 为什么要单钉一次：这是一条**上游改过**的契约（runtime#20 永不关闭 → runtime#32
// 退订即关闭），而它改变的是消费方的写法要求。契约一旦静默回退，「退订后不再收
// 事件」这条承诺会以最难查的方式失效——旧 channel 仍挂着、仍收事件，且没有任何
// 报错。两条退订路径（手动 cancel / ctx 取消）各钉一次。
func TestSubscribeChannelClosesOnCancel(t *testing.T) {
	t.Run("手动 cancel", func(t *testing.T) {
		b := New()
		ch, cancel := b.Subscribe(context.Background())
		cancel()
		cancel() // 幂等：二次 cancel 不得 panic（once 里连 close 一起做）
		assertClosed(t, "手动 cancel", ch)
	})

	t.Run("ctx 取消", func(t *testing.T) {
		b := New()
		ctx, ctxCancel := context.WithCancel(context.Background())
		ch, _ := b.Subscribe(ctx)
		ctxCancel()
		assertClosed(t, "ctx 取消", ch)
	})
}

// assertClosed 等 channel 关闭（退订是异步的：unsubscribe 跑在监听 goroutine 里）。
//
// 关闭前可能还有已缓冲事件，故读到 ok==true 不算失败——只继续等到关闭。
func assertClosed(t *testing.T, name string, ch <-chan Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("%s: 退订后 channel 未在 2s 内关闭（消费方的 range 会永久阻塞）", name)
		}
	}
}
