// assembly_gen_test.go —— 装配面的**代次**与**并发换分不丢更新**。
//
// # 为什么这两条要单独钉
//
// 代次回答审计要问的第一个问题之一：「**换过没有**」与「这次判定是按第几代做的」。
// 它必须**唯一、单调、不回卷**——代次一重复，「换过没有」就给不出答案
// （轮询方看到 6 → 6 以为没换过，而策略确实换了一次）。
//
// 「不丢更新」回答的是另一个问题：「两个并发撤销者会不会互相覆盖」。
// 这条是**真缺陷**的回归：旧形态是「读当前份 → 算新的 → 直接换指针」，
// 两个写者拿到同一份旧，后写者把先写者静默盖掉 ⇒ **撤销被撤销**
// （被撤掉的守卫悄悄复活，且没有任何东西会提示）。
//
// 构造方式一律**确定性**（通道握手 + CAS 内的嵌套换分），不靠时序去撞。

package guardset

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/oagentkit/guardx/invoke"
)

// setOf 造一份放行全部的守卫集合。
func setOf(t *testing.T, names ...string) *Set {
	t.Helper()
	gs := make([]Guard, 0, len(names))
	for _, n := range names {
		gs = append(gs, Guard{Name: n, Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return allow()
		}})
	}
	s, err := New(gs...)
	if err != nil {
		t.Fatalf("构造守卫集合失败: %v", err)
	}
	return s
}

// TestGenerationIsProjectionOfCurrent 钉住：代次是**当前份的投影**，不是另一个计数器。
//
// 判据不是「两者相等」——分开读会得到「集合是第 N 代、代次是第 N-1 代」这种
// 自相矛盾的一对（netguard 修掉的「两套真相」的可观测形态）。
// 正确的不变式是**一次读出**：`Snapshot()` 的两个字段来自同一次装载。
func TestGenerationIsProjectionOfCurrent(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := asm.Generation(); got != 0 {
		t.Fatalf("初始代次应为 0（从未换过），实得 %d", got)
	}
	snap := asm.Snapshot()
	if snap.Generation != 0 || snap.Set == nil {
		t.Fatalf("快照应同时给出集合与代次，实得 %+v", snap)
	}

	if err := asm.Swap(context.Background(), setOf(t, "g1", "g2")); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	snap = asm.Snapshot()
	if snap.Generation != 1 {
		t.Fatalf("换一次后代次应为 1，实得 %d", snap.Generation)
	}
	if strings.Join(snap.Set.Guards(), ",") != "g1,g2" {
		t.Fatalf("快照的集合应是换上去的那份，实得 %v", snap.Set.Guards())
	}
	if asm.Generation() != snap.Generation {
		t.Fatalf("全局代次应是快照代次的投影：快照 %d，全局 %d",
			snap.Generation, asm.Generation())
	}
}

// TestGenerationUniqueUnderConcurrentSwap 钉住：并发换分的代次必须**互不相同**。
//
// 判据与 netguard 同款：N 次成功换份后，代次必须恰好比起始值大 N。
// 若代次来自「另一个计数器 + 1」，并发写者会读到同一个旧值、各自算出同一个
// next，代次**重复**——于是「换过没有」给不出答案。
func TestGenerationUniqueUnderConcurrentSwap(t *testing.T) {
	// 集合要有 n+1 条守卫：每个并发者撤**各自那一条**。
	//
	// 两个坑都在这里：
	//   - 只留一条 ⇒ 撤销它会得到空集合 ⇒ 按「全部撤销是失守」被拒，
	//     代次自然不推进（那样测的就不是代次唯一性，而是空集合拒绝了）；
	//   - 都撤同一条 ⇒ 第二个人起就撤销了「不存在的守卫」⇒ 同样被拒。
	const n = 64
	names := make([]string, 0, n+1)
	for i := 0; i <= n; i++ {
		names = append(names, fmt.Sprintf("g%d", i))
	}
	asm, err := NewAssembly(setOf(t, names...))
	if err != nil {
		t.Fatal(err)
	}
	start := asm.Generation()

	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			_ = asm.SwapFunc(context.Background(), func(cur Snapshot) (*Set, error) {
				return cur.Set.Without(fmt.Sprintf("g%d", i))
			})
		}(i)
	}
	close(gate)
	wg.Wait()

	if got, want := asm.Generation(), start+n; got != want {
		t.Fatalf("并发换份 %d 次后代次应恰好推进 %d（每次 +1，代次唯一）；实得 %d（起始 %d）——"+
			"代次重复即「换过没有」给不出答案", n, want, got, start)
	}
}

// TestOutcomeCarriesGeneration 钉住：判定结论带**代次**。
//
// 它回答「这次判定是按第几代守卫做的」。撤销一条守卫后，早先已在途的判定
// 仍按旧份走（那是设计，见 assembly.go）——没有代次，这两件事在留痕里长得一样。
func TestOutcomeCarriesGeneration(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1"))
	if err != nil {
		t.Fatal(err)
	}
	out := asm.Evaluate(context.Background(), call())
	if out.Generation != 0 {
		t.Fatalf("初始代次下判定结论的代次应为 0，实得 %d", out.Generation)
	}
	if err := asm.Swap(context.Background(), setOf(t, "g1", "g2")); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	out = asm.Evaluate(context.Background(), call())
	if out.Generation != 1 {
		t.Fatalf("换一次后判定结论的代次应为 1，实得 %d", out.Generation)
	}
}

// TestConcurrentSwapFuncDoesNotLoseUpdates 钉住：**并发撤销不丢更新**。
//
// 这是**真缺陷**的回归。旧形态（读当前份 → 算新的 → 直接换指针）：
// 两个写者拿到同一份旧，后写者把先写者静默盖掉 ⇒ 被撤掉的守卫悄悄复活。
//
// 构造是确定性的：
//
//  1. A 的 mutate 进入后**阻塞**（此时它已观察到 base，但尚未发布）；
//  2. B 换分落地（撤掉 g2）——current 变成 {g1, g3}；
//  3. 放行 A：A 基于**旧观察**算出的 {g2, g3} 发布时会 CAS 失败 ⇒ 重试；
//     重试时 mutate 重新看到 {g1, g3} ⇒ 撤 g1 ⇒ {g3} 落地。
//
// 终态必须是 {g3}：g1 与 g2 **都**撤掉了。
// 旧形态下终态是 {g2, g3}——g1 复活了。
func TestConcurrentSwapFuncDoesNotLoseUpdates(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1", "g2", "g3"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	done := make(chan error, 1)

	go func() {
		done <- asm.SwapFunc(context.Background(), func(cur Snapshot) (*Set, error) {
			once.Do(func() { close(entered) })
			<-release
			return cur.Set.Without("g1")
		})
	}()

	<-entered // A 已观察到 base，尚未发布
	// B 此时换分：它看到的就是 base，撤掉 g2 后落地。
	if err := asm.SwapFunc(context.Background(), func(cur Snapshot) (*Set, error) {
		return cur.Set.Without("g2")
	}); err != nil {
		t.Fatalf("B 换分失败: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("A 换分失败: %v", err)
	}

	if got := strings.Join(asm.Current().Guards(), ","); got != "g3" {
		t.Fatalf("两次并发撤销都应生效（g1 与 g2 都撤掉），实得 %q——"+
			"后写者把先写者盖掉了（撤销被撤销）", got)
	}
}

// TestSwapFuncRetriesOnCasFailureAndSeesLatest 钉住：CAS 失败会**重试**，
// 且重试时 mutate 看到的是**最新的那一份**。
//
// 构造是确定性的：在 mutate 内部做一次嵌套换分 ⇒ 外层的 CAS 必然失败。
// 于是 mutate 被调用两次，第二次必须看到嵌套换分后的那份。
//
// 旧形态（不重试）只会调用一次 mutate ⇒ 外层用**过期观察**发布 ⇒ 覆盖嵌套换分。
func TestSwapFuncRetriesOnCasFailureAndSeesLatest(t *testing.T) {
	asm, err := NewAssembly(setOf(t, "g1", "g2"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	var seenSecond []string
	err = asm.SwapFunc(context.Background(), func(cur Snapshot) (*Set, error) {
		calls++
		if calls == 1 {
			// 内层换分：让外层的 CAS 必然失败。
			inner := setOf(t, "g2", "inner")
			if err := asm.SwapFunc(context.Background(), func(Snapshot) (*Set, error) {
				return inner, nil
			}); err != nil {
				return nil, err
			}
			// 基于**旧观察**算出的下一份——它必须被 CAS 失败挡掉。
			next, err := cur.Set.Without("g1")
			return next, err
		}
		seenSecond = cur.Set.Guards()
		next, err := cur.Set.Without("g2")
		return next, err
	})
	if err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	if calls != 2 {
		t.Fatalf("CAS 失败应触发重试（mutate 应被调用 2 次），实得 %d 次", calls)
	}
	if got := strings.Join(seenSecond, ","); got != "g2,inner" {
		t.Fatalf("重试时 mutate 必须看到**最新的那一份**（嵌套换分后的），实得 %q", got)
	}
	// 终态：第二次基于最新份撤掉 g2 ⇒ {inner}。
	// 若过期观察被发布出去，终态会是 {g2}（嵌套换分被覆盖）。
	if got := strings.Join(asm.Current().Guards(), ","); got != "inner" {
		t.Fatalf("终态应为 {inner}（过期观察被 CAS 挡掉），实得 %q", got)
	}
}
