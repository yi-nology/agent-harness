package sandbox

// 本文件补的是**转发壳的调用覆盖**——上一版只验了类型/登记簿/执行三项，
// 覆盖率实测 60.0%，缺的正是 `SupportsBasic` 与 `SupportsAll` 两个转发壳。
//
// # 为什么门面包的「语句覆盖率」不是形式主义
//
// 门面包的每条语句就是一句转发（`return upstream.X(...)`）。它对**转发目标**与
// **参数顺序**完全不设防：把 `SupportsAll` 转成 `SupportsBasic` 编译得过、
// 类型也合，症状却是「后端声称能兜内存、实际兜不住」——而这句话又只有
// `Supports*` 被真的调一次才暴露。故对门面包而言，「每个壳至少被调一次且
// 语义对得上」就是它唯一可能的缺陷面，覆盖率在这里恰好是恰当的度量。
//
// 与之相对，实现层的行为测试仍归上游（见 sandbox_test.go 头注），不在本仓重复。

import (
	"context"
	"testing"

	"github.com/oekit/pkg/errors"
)

// TestSupportsBasicCoversOnlyThreeGates 基本后端只兑现三闸，不兑现内存。
//
// 判据是**两侧**都要判：只判「要求内存时 false」会漏掉「零要求时也 false」
// （那会让所有基本后端在任何请求下都不可选）；只判「零要求时 true」则漏掉
// 前面那条。转发错了方向，两种错法各自表现为其中一侧。
func TestSupportsBasicCoversOnlyThreeGates(t *testing.T) {
	if !SupportsBasic(Budget{}) {
		t.Error("零要求（什么都没要）应报兜得住")
	}
	if !SupportsBasic(Budget{MaxCalls: 10, MaxBytes: 1 << 20}) {
		t.Error("只要求三闸时应报兜得住")
	}
	if SupportsBasic(Budget{MaxMemory: 1 << 20}) {
		t.Error("要求内存时基本后端应报兜不住——这是「漏声明 ⇒ 少能力」的方向")
	}
}

// TestSupportsAllAlwaysTrue 全支持后端对任何预算都应报兜得住。
//
// 「恒为真」的判据要拿**最刁的输入**去问：只喂零值 Budget 时，把
// `SupportsAll` 错实现成 `b.MaxMemory <= 0` 的假实现也会过。
func TestSupportsAllAlwaysTrue(t *testing.T) {
	for _, b := range []Budget{
		{},
		{MaxCalls: 1, MaxBytes: 1, MaxMemory: 1},
		{MaxMemory: 1 << 30},
	} {
		if !SupportsAll(b) {
			t.Errorf("SupportsAll(%+v) 应为真", b)
		}
	}
}

// TestCapableFuncAdaptsClosure CapableFunc 把普通函数适配成 Capable。
//
// 这条钉的是**别名转发后适配器仍可用**：宿主大量用 `CapableFunc(func(...) bool)`
// 现场声明能力面，别名若丢了方法集，这类调用点会编译不过。
func TestCapableFuncAdaptsClosure(t *testing.T) {
	var c Capable = CapableFunc(func(b Budget) bool { return b.MaxMemory == 0 })
	if !c.Supports(Budget{}) {
		t.Error("应支持零要求")
	}
	if c.Supports(Budget{MaxMemory: 1}) {
		t.Error("应不支持内存要求")
	}
}

// TestDuplicateProviderRejected 同标识重复登记即拒。
//
// 为什么这条重要到值得单独一个用例：重复标识时「选中哪一个」取决于登记顺序，
// 而选中哪个后端直接决定这次执行的实际隔离强度。晚一步发现就是装配期缺陷
// 变成执行期的不确定强度。
func TestDuplicateProviderRejected(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(stubProvider{id: "same", level: LevelProcess}); err != nil {
		t.Fatalf("首次登记应成功: %v", err)
	}
	err := reg.Register(stubProvider{id: "same", level: LevelOS})
	if err == nil {
		t.Fatal("同标识重复登记必须报错")
	}
	if !errors.Is(err, ErrDuplicateProvider) {
		t.Errorf("错误应可被 errors.Is(ErrDuplicateProvider) 认出，实得 %v", err)
	}
}

// TestExecuteRejectsBudgetBeyondCapability 兜不住预算的后端**不被选中**，
// 而不是「选中之后在执行期失败」。
//
// 两者对宿主的含义完全不同：前者在**选后端**这一步就说「这台机器上没有能
// 兑现这项要求的执行模型」；后者会先跑起来、再以一句执行错误告终，宿主容易
// 误判成「程序写错了」。故这条判的是**拒绝点在哪一步**。
func TestExecuteRejectsBudgetBeyondCapability(t *testing.T) {
	reg := NewRegistry()
	// stubProvider 不实现 Capable ⇒ 走 SupportsBasic 的缺省语义（不兑现内存）。
	if err := reg.Register(stubProvider{id: "basic", level: LevelProcess}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	_, err := Execute(context.Background(), reg, LevelProcess, Request{
		Program: "x",
		Budget:  Budget{MaxMemory: 64 << 20},
	})
	if err == nil {
		t.Fatal("要求内存而唯一的后端兜不住时，必须在选后端这步拒绝")
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("错误应可被 errors.Is(ErrNoProvider) 认出，实得 %v", err)
	}
}
