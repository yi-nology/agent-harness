// assembly_test.go —— 装配面（撤销的归属与机械路径）的行为钉住。
//
// # 这组用例钉的到底是什么
//
// 不是「能不能撤销」，而是**撤销的语义**：
//
//   - 撤销是**换整份**，不是改其中一条（改其中一条 ⇒ 「当前有哪些守卫」有两个真相）；
//   - 换份是**原子**的：在途判定跑在旧份上，下一次判定跑在新份上；
//   - 换份后**无需重接链**（链上那一环指向装配面，不指向某一份）；
//   - 「全部撤销」不是降级，是失守（换上去的那份永远非空）。
//
// 夹具用合成守卫名（old_guard / new_guard / old_marker / blocker），
// 不带领域味。

package guardset

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oekit/pkg/errors"
)

// allowAll 造一份放行全部的守卫集合。
func allowAll(t *testing.T, names ...string) *Set {
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

// TestAssemblySwapTakesEffectWithoutRewiring 钉住：换份后**不用重接链**。
//
// 这是装配面存在的理由——若换份要求重建 invoke.Chain，撤销就还是「改代码 +
// 重启」，只是把重启换成了重接链。
//
// 断言语义更强：换份前后用的是**同一个 stage 变量**，判定结果却变了。
// 若实现把 stage 绑在初始份上（很自然的写法：`asm.Stage()` 捕获当时的 set），
// 本用例会立刻红。
func TestAssemblySwapTakesEffectWithoutRewiring(t *testing.T) {
	oldSet, err := New(Guard{Name: "old_guard", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
		return deny("旧守卫拒绝", false)
	}})
	if err != nil {
		t.Fatal(err)
	}
	newSet := allowAll(t, "new_guard")

	asm, err := NewAssembly(oldSet)
	if err != nil {
		t.Fatal(err)
	}
	stage := asm.Stage()

	v, err := stage.Check(context.Background(), call())
	if err != nil {
		t.Fatalf("判定不该返回 error: %v", err)
	}
	if v.Allow {
		t.Fatal("换份前旧守卫在拒，不该放行")
	}

	if err := asm.Swap(context.Background(), newSet); err != nil {
		t.Fatalf("换份失败: %v", err)
	}

	v, err = stage.Check(context.Background(), call())
	if err != nil {
		t.Fatalf("判定不该返回 error: %v", err)
	}
	if !v.Allow {
		t.Fatalf("换份后新守卫放行，同一个 stage 仍按旧份拒（= stage 绑死了初始份）: %+v", v)
	}
}

// TestAssemblySwapNilKeepsCurrent 钉住：换上去的目标为 nil ⇒ 拒绝换份，
// **当前份保持不变**。
//
// fail-closed：撤销失败不等于「换上一份没人管的东西」。
// 「全部撤销」是失守，不是降级。
func TestAssemblySwapNilKeepsCurrent(t *testing.T) {
	oldSet, err := New(Guard{Name: "old_guard", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
		return deny("旧守卫拒绝", false)
	}})
	if err != nil {
		t.Fatal(err)
	}
	asm, err := NewAssembly(oldSet)
	if err != nil {
		t.Fatal(err)
	}

	if err := asm.Swap(context.Background(), nil); err == nil {
		t.Fatal("换上一份未装配的集合必须被拒绝")
	}
	if got := asm.Current(); got != oldSet {
		t.Fatalf("换份被拒后当前份必须保持不变，实得 %v", got.Guards())
	}
	// 且判定仍按旧份走。
	if asm.Evaluate(context.Background(), call()).Allowed {
		t.Fatal("换份被拒后判定仍应按旧份拒绝")
	}
}

// TestAssemblySwapGateRejectsKeepsCurrent 钉住：换份闸拒绝 ⇒ 不换份，
// 错误透传给调用方。
//
// 这一条是「谁在什么条件下可以换整份」的落点：授权归宿主，本包只提供接入点。
func TestAssemblySwapGateRejectsKeepsCurrent(t *testing.T) {
	oldSet, err := New(Guard{Name: "old_guard", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
		return deny("旧守卫拒绝", false)
	}})
	if err != nil {
		t.Fatal(err)
	}
	gateErr := errors.New("闸门：本次换份未获批准")
	var sawNext string
	asm, err := NewAssembly(oldSet, WithSwapGate(func(_ context.Context, next *Set) error {
		sawNext = strings.Join(next.Guards(), ",")
		return gateErr
	}))
	if err != nil {
		t.Fatal(err)
	}

	err = asm.Swap(context.Background(), allowAll(t, "new_guard"))
	if !errors.Is(err, gateErr) {
		t.Fatalf("闸门的错误必须透传给调用方，实得 %v", err)
	}
	if sawNext != "new_guard" {
		t.Fatalf("闸门必须看到**即将换上去的那一份**，实得 %q", sawNext)
	}
	if got := asm.Current(); got != oldSet {
		t.Fatalf("闸门拒绝后当前份必须保持不变，实得 %v", got.Guards())
	}
}

// TestAssemblySwapRecordsAuditTrail 钉住：换份留痕能回答
// 「换了几次 / 换走了什么 / 换上了什么」。
func TestAssemblySwapRecordsAuditTrail(t *testing.T) {
	oldSet, err := New(Guard{Name: "old_guard", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
		return allow()
	}})
	if err != nil {
		t.Fatal(err)
	}
	var recs []SwapRecord
	asm, err := NewAssembly(oldSet, WithSwapRecorder(func(r SwapRecord) { recs = append(recs, r) }))
	if err != nil {
		t.Fatal(err)
	}

	if err := asm.Swap(context.Background(), allowAll(t, "new_guard", "new_guard2")); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("应有 1 条换份留痕，实得 %d", len(recs))
	}
	r := recs[0]
	if r.Seq != 1 {
		t.Fatalf("换份序号应从 1 起，实得 %d", r.Seq)
	}
	if strings.Join(r.From, ",") != "old_guard" {
		t.Fatalf("留痕的 From 应是换走的那份，实得 %v", r.From)
	}
	if strings.Join(r.To, ",") != "new_guard,new_guard2" {
		t.Fatalf("留痕的 To 应是换上来的那份，实得 %v", r.To)
	}
}

// TestWithoutReturnsNewSetAndLeavesOriginal 钉住：`Without` 是**纯函数**——
// 返回新的一份，**原集合不变**。
//
// 这是「撤销靠换整份，不靠改旧份」的落点：若 Without 能改到原集合，
// 那就出现了两个真相，且并发读它就是竞态。
func TestWithoutReturnsNewSetAndLeavesOriginal(t *testing.T) {
	s, err := New(
		Guard{Name: "g1", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }},
		Guard{Name: "g2", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }},
	)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Without("g1")
	if err != nil {
		t.Fatalf("撤销 g1 失败: %v", err)
	}
	if strings.Join(next.Guards(), ",") != "g2" {
		t.Fatalf("新集合应只剩 g2，实得 %v", next.Guards())
	}
	if strings.Join(s.Guards(), ",") != "g1,g2" {
		t.Fatalf("原集合必须不变（改其中一条 = 两个真相），实得 %v", s.Guards())
	}
}

// TestWithoutUnknownGuardRejected 钉住：撤销**不存在**的守卫必须被拒绝，
// 不静默忽略。
//
// 静默忽略会让宿主以为「撤掉了某条守卫」，实际那份集合里根本没有它——
// 撤销的是个不存在的东西，而宿主以为降级生效了。
func TestWithoutUnknownGuardRejected(t *testing.T) {
	s, err := New(Guard{Name: "g1", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Without("nope"); !errors.Is(err, ErrUnknownGuard) {
		t.Fatalf("撤销不存在的守卫必须被拒绝（ErrUnknownGuard），实得 %v", err)
	}
}

// TestWithoutLastGuardRejected 钉住：去掉**最后一条**必须被拒绝
// （`ErrEmptySet`）。
//
// 「全部撤销」不是降级，是失守——没人看着的调用不该看起来能用。
func TestWithoutLastGuardRejected(t *testing.T) {
	s, err := New(Guard{Name: "only", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Without("only"); !errors.Is(err, ErrEmptySet) {
		t.Fatalf("去掉最后一条必须被拒绝（ErrEmptySet），实得 %v", err)
	}
}

// TestSwapIsAtomicForInflightEvaluation 钉住：换份是**原子**的。
//
// 构造方式是**确定性的**（通道握手），不靠时序去撞：
//
//  1. 第一条守卫「blocker」进入后阻塞（信号 entered）；
//  2. 主协程换份；
//  3. 放行 blocker，让在途判定跑完。
//
// 在途判定必须**全程走旧份**：两条拒绝都来自旧份，且不含新份的任何痕迹。
//
// 若换份是「原地覆盖旧份的内容」，在途判定会看到新份 ⇒ 本用例红。
func TestSwapIsAtomicForInflightEvaluation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	oldSet, err := New(
		Guard{Name: "blocker", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
			close(entered)
			<-release
			return deny("旧份第一条", false)
		}},
		Guard{Name: "old_marker", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict {
			return deny("旧份第二条", false)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	newSet, err := New(Guard{Name: "new_marker", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict {
		return deny("新份痕迹", false)
	}})
	if err != nil {
		t.Fatal(err)
	}

	asm, err := NewAssembly(oldSet)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan Outcome, 1)
	go func() { done <- asm.Evaluate(context.Background(), call()) }()

	<-entered // 确定性：第一条守卫已进，此时装载的必然是旧份
	if err := asm.Swap(context.Background(), newSet); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	close(release)

	out := <-done
	reason := out.Reason()
	if !strings.Contains(reason, "旧份第一条") || !strings.Contains(reason, "旧份第二条") {
		t.Fatalf("在途判定必须全程走旧份（两条拒绝都来自旧份），实得 %q", reason)
	}
	if strings.Contains(reason, "新份痕迹") {
		t.Fatalf("在途判定不得混入换上去的新份（= 换份不是原子的），实得 %q", reason)
	}

	// 且下一次判定走新份。
	next := asm.Evaluate(context.Background(), call())
	if !strings.Contains(next.Reason(), "新份痕迹") {
		t.Fatalf("换份后的下一次判定必须走新份，实得 %q", next.Reason())
	}
}

// TestSwapLeavesPreviousSetUnmodified 钉住：换份**不修改旧份**。
//
// 旧份可能正被在途判定读着（见 TestSwapIsAtomicForInflightEvaluation），
// 所以「换份」必须是**换指针**，不能是「把旧份的内容改成新的」。
//
// 后者有一个很隐蔽的表现：Go 的 `for _, g := range s.guards` 在**循环开始**
// 就固定了切片头，所以在途判定看到的仍是旧的那一批守卫——**看起来没坏**；
// 但旧份对象的内容已经被改了，而它可能正被别的持有者当「上一份」读着
// （留痕、对比、审计）。故这条要单独钉：换份后旧份的守卫名单必须原样。
func TestSwapLeavesPreviousSetUnmodified(t *testing.T) {
	oldSet, err := New(
		Guard{Name: "old_a", Rank: 1, Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }},
		Guard{Name: "old_b", Rank: 2, Check: func(context.Context, invoke.Call) invoke.Verdict { return allow() }},
	)
	if err != nil {
		t.Fatal(err)
	}
	asm, err := NewAssembly(oldSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := asm.Swap(context.Background(), allowAll(t, "new_a")); err != nil {
		t.Fatalf("换份失败: %v", err)
	}
	if got := strings.Join(oldSet.Guards(), ","); got != "old_a,old_b" {
		t.Fatalf("换份不得修改旧份（旧份可能正被在途判定读着），实得 %q", got)
	}
}

// TestNewAssemblyRejectsNilSet 钉住：装配面不接受 nil 初始集合。
//
// 装配面一开始就没有守卫，那不是「还没装」，是「没人看着」。
func TestNewAssemblyRejectsNilSet(t *testing.T) {
	if _, err := NewAssembly(nil); !errors.Is(err, ErrUnassembled) {
		t.Fatalf("nil 初始集合必须被拒绝，实得 %v", err)
	}
}
