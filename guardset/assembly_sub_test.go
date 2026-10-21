// assembly_sub_test.go —— 装配面的**订阅**与**通知期的 panic 兜底**。
//
// # 为什么订阅要单独一组用例
//
// 代次只是**信号**：调用方得自己起一个轮询循环，前后各取一次代次比对。
// 轮询有三件它解决不了的事：
//
//   - 空转期仍在读全局状态，每个订阅者一份重复读取；
//   - 两次轮询之间若换了两次，只能看出「变了」，看不出「变成了什么」；
//   - 代次单调只保证「不漏掉发生过变更」，不保证「每一代都被谁记下过」。
//
// 订阅把这件事一次做完：变更发生的**那一刻**由写侧直接把新记录推给订阅者。
//
// # 为什么通知期的 panic 要单独钉
//
// 一次成功的换份**已经落地**。此时让某个订阅者抛异常就把整次变更说成「换份失败」，
// 等于把已经发生的事实改写成没发生——而这恰恰是本包要消灭的那类失效。
// 故逐个 recover：让其余订阅者继续收到通知。

package guardset

import (
	"context"
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

// TestSubscribeReceivesChangeAtSwapTime 钉住：订阅者在**换份那一刻**收到记录，
// 且记录带齐四个问题要答的东西（换了几次 / 换走了什么 / 换上了什么 / 第几代）。
func TestSubscribeReceivesChangeAtSwapTime(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "old_guard"))
	if err != nil {
		t.Fatal(err)
	}
	var got []SwapRecord
	h, err := asm.Subscribe(func(r SwapRecord) { got = append(got, r) })
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	if h == 0 {
		t.Fatal("订阅句柄不得为零（零表示无效）")
	}

	if err := asm.Swap(context.Background(), setOf(t, "new_guard")); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("订阅者应收到 1 条换份记录，实得 %d", len(got))
	}
	r := got[0]
	if r.Seq != 1 || r.Generation != 1 {
		t.Fatalf("换份序号与代次都应为 1，实得 Seq=%d Generation=%d", r.Seq, r.Generation)
	}
	if strings.Join(r.From, ",") != "old_guard" {
		t.Fatalf("From 应是换走的那份，实得 %v", r.From)
	}
	if strings.Join(r.To, ",") != "new_guard" {
		t.Fatalf("To 应是换上来的那份，实得 %v", r.To)
	}
	if r.Set == nil || strings.Join(r.Set.Guards(), ",") != "new_guard" {
		t.Fatalf("记录应带上换完之后当前那一份，实得 %v", r.Set)
	}
}

// TestUnsubscribeStopsDelivery 钉住：注销后不再收到通知。
//
// 运行期增删订阅者要**可能**（不是要求重建装配面）——
// 重建装配面等于把整份换掉，那不是「退订」。
func TestUnsubscribeStopsDelivery(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	h, err := asm.Subscribe(func(SwapRecord) { count++ })
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	if err := asm.Swap(context.Background(), setOf(t, "g2")); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("注销前应收到 1 次，实得 %d", count)
	}
	asm.Unsubscribe(h)
	if err := asm.Swap(context.Background(), setOf(t, "g3")); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("注销后不应再收到通知，实得累计 %d 次", count)
	}
}

// TestSubscriberPanicDoesNotFailSwap 钉住：订阅者 panic **不得**把已落地的换份说成失败，
// 且**其余订阅者**仍要收到通知。
//
// 这正是「把已经发生的事实改写成没发生」——本包要消灭的那类失效。
func TestSubscriberPanicDoesNotFailSwap(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := asm.Subscribe(func(SwapRecord) { panic("订阅者自己坏了") }); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	after := 0
	if _, err := asm.Subscribe(func(SwapRecord) { after++ }); err != nil {
		t.Fatalf("订阅失败: %v", err)
	}

	if err := asm.Swap(context.Background(), setOf(t, "g2")); err != nil {
		t.Fatalf("订阅者 panic 不得让换份失败: %v", err)
	}
	if after != 1 {
		t.Fatalf("前一个订阅者 panic 后，其余订阅者仍应收到通知，实得 %d 次", after)
	}
	// 且换份确实落地了（当前份已换）。
	if got := strings.Join(asm.Current().Guards(), ","); got != "g2" {
		t.Fatalf("换份应已落地，实得当前份 %q", got)
	}
}

// TestRecorderPanicDoesNotFailSwap 钉住：留痕回调 panic 同样不得掀翻换份。
//
// 与订阅者同一条纪律：换份已经落地，回调抛异常不能把它说成失败。
func TestRecorderPanicDoesNotFailSwap(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"), WithSwapRecorder(func(SwapRecord) {
		panic("留痕回调自己坏了")
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := asm.Swap(context.Background(), setOf(t, "g2")); err != nil {
		t.Fatalf("留痕回调 panic 不得让换份失败: %v", err)
	}
	if got := strings.Join(asm.Current().Guards(), ","); got != "g2" {
		t.Fatalf("换份应已落地，实得当前份 %q", got)
	}
}

// TestSubscribeRejectsNilCallback 钉住：订阅回调为 nil ⇒ 报错并返回**零句柄**。
//
// 登记一个必然 panic 的订阅者毫无意义。调用方要能**凭返回值**知道订阅没生效，
// 而不是等到第一次变更时才发现「原来一直没跟上」。
func TestSubscribeRejectsNilCallback(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := asm.Subscribe(nil)
	if err == nil {
		t.Fatal("nil 订阅回调必须被拒绝")
	}
	if h != 0 {
		t.Fatalf("被拒绝的订阅必须返回零句柄（零表示无效），实得 %d", h)
	}
}

// TestSwapFuncNilMutateRejected 钉住：改写函数为 nil ⇒ 报错。
//
// 传 nil 进来只能说明调用方漏了判断，而漏判断的换份会「什么也不做」——
// 静默无动作比报错更难查。
func TestSwapFuncNilMutateRejected(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := asm.SwapFunc(context.Background(), nil); err == nil {
		t.Fatal("nil 改写函数必须被拒绝")
	}
	if got := strings.Join(asm.Current().Guards(), ","); got != "g1" {
		t.Fatalf("被拒绝的换份不得动当前份，实得 %q", got)
	}
}

// TestSwapFuncMutateErrorKeepsCurrent 钉住：mutate 返回错误 ⇒ 换份放弃，
// 当前份**保持不变**。
func TestSwapFuncMutateErrorKeepsCurrent(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"))
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("宿主拒绝这次撤销")
	if err := asm.SwapFunc(context.Background(), func(Snapshot) (*Set, error) {
		return nil, sentinel
	}); err != sentinel {
		t.Fatalf("mutate 的错误必须原样透传，实得 %v", err)
	}
	if got := strings.Join(asm.Current().Guards(), ","); got != "g1" {
		t.Fatalf("换份放弃后当前份必须保持不变，实得 %q", got)
	}
}
