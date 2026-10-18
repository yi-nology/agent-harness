package tooldisclose

// 执行门禁的行为钉住：两类失败必须严格分开，且重试性相反。

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/oekit/pkg/errors"
)

// TestGateNotLoadedIsRetryableAndActivates 目录内但未取回 → 可重试，且**已作为副作用激活**。
// 这是本包的核心：模型凭占位猜出的调用不必为「取 schema」再绕一圈，重试即可成。
func TestGateNotLoadedIsRetryableAndActivates(t *testing.T) {
	c := NewCatalog(tools())
	g := NewGate(c)

	err := g.Check("alpha_lookup")
	if err == nil {
		t.Fatal("未加载的调用必须被拒")
	}
	if !errors.Is(err, ErrNotLoaded) {
		t.Fatalf("应命中 ErrNotLoaded 哨兵，实得 %v", err)
	}
	if errors.Is(err, ErrNotInCatalog) {
		t.Fatal("未加载不得被混成「不在目录」：那是不可重试的另一种结局")
	}
	if !Retryable(err) {
		t.Fatal("未加载是可恢复的，必须标为可重试")
	}
	if !c.IsLoaded("alpha_lookup") {
		t.Fatal("门禁必须把该工具作为副作用激活")
	}
	ge := gateErr(t, err)
	if !ge.Activated {
		t.Fatal("结构化错误应记录激活副作用")
	}
	if ge.Name != "alpha_lookup" {
		t.Fatalf("结构化错误应带工具名，实得 %q", ge.Name)
	}
	if !ge.Retryable() {
		t.Fatal("结构化错误自报的重试性应与 Retryable 一致")
	}
	// 激活是副作用而非放行：这次调用照样被拒，但重试必成。
	if err := g.Check("alpha_lookup"); err != nil {
		t.Fatalf("激活后重试应放行，实得 %v", err)
	}
}

// TestGateNotInCatalogIsNotRetryable 压根不在目录 → 不可重试：
// 不存在的东西不会因为重试而出现，退避只会把一次策略否定变成无穷重试。
func TestGateNotInCatalogIsNotRetryable(t *testing.T) {
	c := NewCatalog(tools())
	g := NewGate(c)

	err := g.Check("epsilon_missing")
	if err == nil {
		t.Fatal("目录外的调用必须被拒")
	}
	if !errors.Is(err, ErrNotInCatalog) {
		t.Fatalf("应命中 ErrNotInCatalog 哨兵，实得 %v", err)
	}
	if errors.Is(err, ErrNotLoaded) {
		t.Fatal("不在目录不得被混成「未加载」")
	}
	if Retryable(err) {
		t.Fatal("不在目录是不可恢复的，必须标为不可重试")
	}
	ge := gateErr(t, err)
	if ge.Retryable() || ge.Activated {
		t.Fatalf("目录外既不可重试也不该有激活副作用，实得 %+v", ge)
	}
	// 重试多少次都是同一个结局：重试不应改变任何状态。
	for i := 0; i < 3; i++ {
		if err := g.Check("epsilon_missing"); !errors.Is(err, ErrNotInCatalog) || Retryable(err) {
			t.Fatalf("第 %d 次重试结果被改动: %v", i, err)
		}
	}
	if len(c.Loaded()) != 0 {
		t.Fatalf("目录外的名字不得进入已加载集，实得 %v", c.Loaded())
	}
}

// TestGateSentinelsAreDistinct 两个哨兵互不命中（分支可靠的前提）。
func TestGateSentinelsAreDistinct(t *testing.T) {
	g := NewGate(NewCatalog(tools()))
	notLoaded := g.Check("alpha_lookup")
	notInCat := NewGate(NewCatalog(tools())).Check("epsilon_missing")

	if !errors.Is(notLoaded, ErrNotLoaded) || errors.Is(notLoaded, ErrNotInCatalog) {
		t.Fatalf("未加载分支判定错位: %v", notLoaded)
	}
	if !errors.Is(notInCat, ErrNotInCatalog) || errors.Is(notInCat, ErrNotLoaded) {
		t.Fatalf("不在目录分支判定错位: %v", notInCat)
	}
	if errors.Is(notLoaded, notInCat) {
		t.Fatal("两类结局不得互判")
	}
	// 包一层 fmt.Errorf 后仍可分支：上层加上下文不应毁掉判定。
	wrapped := fmt.Errorf("第 3 轮调用失败: %w", notLoaded)
	if !errors.Is(wrapped, ErrNotLoaded) || !Retryable(wrapped) {
		t.Fatalf("包装后应仍可分支判定: %v", wrapped)
	}
}

// TestGateLoadedPasses 已加载 → 直接放行，不产生错误。
func TestGateLoadedPasses(t *testing.T) {
	c := NewCatalog(tools())
	c.Load("beta_report")
	if err := NewGate(c).Check("beta_report"); err != nil {
		t.Fatalf("已加载的工具应放行，实得 %v", err)
	}
	if err := NewGate(c).Check("beta_report"); err != nil {
		t.Fatalf("重复放行应稳定，实得 %v", err)
	}
}

// TestGateEmptyNameRejected 空名按「不在目录」处置：门禁不做乐观放行。
func TestGateEmptyNameRejected(t *testing.T) {
	err := NewGate(NewCatalog(tools())).Check("")
	if !errors.Is(err, ErrNotInCatalog) || Retryable(err) {
		t.Fatalf("空名应按不可重试的「不在目录」处置，实得 %v", err)
	}
}

// TestGateFailsClosed 无目录 / 空门禁一律按「不在目录」拒绝，不放行。
func TestGateFailsClosed(t *testing.T) {
	if err := NewGate(nil).Check("alpha_lookup"); !errors.Is(err, ErrNotInCatalog) || Retryable(err) {
		t.Fatalf("空目录门禁应拒绝且不可重试，实得 %v", err)
	}
	if err := NewGate(NewCatalog(nil)).Check("alpha_lookup"); !errors.Is(err, ErrNotInCatalog) {
		t.Fatalf("空目录应拒绝，实得 %v", err)
	}
	var nilGate *Gate
	if err := nilGate.Check("alpha_lookup"); !errors.Is(err, ErrNotInCatalog) || Retryable(err) {
		t.Fatalf("零值门禁应 fail-closed，实得 %v", err)
	}
}

// TestRetryableUnknownErrorFailClosed 非门禁错误一律不可重试（认不出就不重试）。
func TestRetryableUnknownErrorFailClosed(t *testing.T) {
	for _, err := range []error{nil, errors.New("随手一个错误"), fmt.Errorf("包装: %w", errors.New("底层"))} {
		if Retryable(err) {
			t.Fatalf("认不出的错误不应被判可重试: %v", err)
		}
	}
}

// TestGateErrorMessage 带工具名，留痕时不必再自己拼。
func TestGateErrorMessage(t *testing.T) {
	g := NewGate(NewCatalog(tools()))
	for _, tc := range []struct {
		name string
		want string
	}{
		{"alpha_lookup", ErrNotLoaded.Error()},
		{"epsilon_missing", ErrNotInCatalog.Error()},
	} {
		err := g.Check(tc.name)
		if !containsAll(err.Error(), tc.want, tc.name) {
			t.Fatalf("错误信息应含原因与工具名，实得 %q", err.Error())
		}
	}
}

// TestGateConcurrentCheck 门禁并发判定：同一工具被激活后即放行，不会反复报「未加载」。
func TestGateConcurrentCheck(t *testing.T) {
	c := NewCatalog(tools())
	g := NewGate(c)
	c.Load("gamma_send")

	var (
		mu         sync.Mutex
		notLoaded  int
		unexpected []error
		wg         sync.WaitGroup
	)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := g.Check("gamma_send"); {
			case err == nil:
			case errors.Is(err, ErrNotLoaded):
				mu.Lock()
				notLoaded++
				mu.Unlock()
				if !c.IsLoaded("gamma_send") {
					t.Error("未加载分支必须留下激活痕迹")
				}
			default:
				mu.Lock()
				unexpected = append(unexpected, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(unexpected) != 0 {
		t.Fatalf("已加载的工具不应被其他理由拒绝: %v", unexpected[0])
	}
	if notLoaded > 1 {
		t.Fatalf("激活后不应反复报未加载，实得 %d 次", notLoaded)
	}
	if got := c.Loaded(); !reflect.DeepEqual(got, []string{"gamma_send"}) {
		t.Fatalf("已加载集应只含一项，实得 %v", got)
	}
}

// GateError 必须满足通用退避层问的那条判据：方法集一改，这里立刻编译失败。
//
// 这条断言是跨包契约的编译期锚点——门禁与分类器之间只靠这一个方法相连，
// 它一旦对不上，两边就退回各判一套（而症状是「可恢复的失败被判成永久失败」，
// 静默漂移，测试全绿）。
var _ interface{ Retryable() bool } = (*GateError)(nil)

// TestRetryableTypedNilGateErrorFailClosed 链上挂着 typed-nil *GateError → false，不得 panic。
//
// typed-nil 不是「没有错误」，而是一个**非 nil 的 error 接口包着 nil 指针**：
// errors.As 判定命中并把目标置为 nil。读字段就是空指针解引用，而 panic 会杀掉
// 整个编排进程，且只在这条罕见链路上出现——所以必须在测试里钉死。
func TestRetryableTypedNilGateErrorFailClosed(t *testing.T) {
	var typedNil *GateError
	wrapped := fmt.Errorf("外层上下文: %w", error(typedNil))

	// 前置条件：确认它确实命中 As（否则本测试只是绕开了缺陷而不是钉住它）。
	var ge *GateError
	if !errors.As(wrapped, &ge) || ge != nil {
		t.Fatalf("前置不成立：包装过的 typed-nil 应命中 As 且取出 nil，实得 %v", ge)
	}
	if Retryable(wrapped) {
		t.Fatal("链上 typed-nil 门禁错误不得判为可重试")
	}
	if Retryable(typedNil) {
		t.Fatal("直接传入 typed-nil 也不得判为可重试")
	}
	// 自报方法被别处当接口调用时同样不得解引用 nil 接收者。
	if typedNil.Retryable() {
		t.Fatal("nil 接收者的自报结论应是 false")
	}
	// 同一条链上的另两个出口同样不许炸：Error 进日志链路，Unwrap 进 errors.Is 遍历。
	if typedNil.Error() != "<nil>" || typedNil.Unwrap() != nil {
		t.Fatal("nil 接收者的文本与解包应安全且为空")
	}
	if errors.Is(wrapped, ErrNotLoaded) || errors.Is(wrapped, ErrNotInCatalog) {
		t.Fatal("typed-nil 不应冒充任何一个门禁哨兵")
	}
	if errors.Unwrap(wrapped) != error(typedNil) {
		t.Fatalf("外层包装应原样透出内层错误, 实得 %v", errors.Unwrap(wrapped))
	}
	if got := wrapped.Error(); !strings.Contains(got, "<nil>") {
		t.Fatalf("外层文案应引用内层的缺省串而不 panic, 实得 %q", got)
	}
}

func gateErr(t *testing.T, err error) *GateError {
	t.Helper()
	var ge *GateError
	if !errors.As(err, &ge) || ge == nil {
		t.Fatalf("门禁错误应可 As 成非 nil 的 *GateError，实得 %v: %v", ge, err)
	}
	return ge
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
