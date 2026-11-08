package ptc

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oagentkit/guardx/toolfail"
)

// #44：判定环拒绝被程序捕获后再抛自己的错误——ErrorKind 必须 runtime
// （改程序），不得被陈旧的 stageErr 单槽毒化成 stage_denied（去质问守卫）。
func TestStageDenialCatchThenThrowStaysRuntime(t *testing.T) {
	deny := invoke.StageFunc{StageName: "审计门",
		Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			return invoke.Verdict{Allow: false, Reason: "策略否定", Retryable: false}, nil
		}}
	program := `try { host.t() } catch (e) { /* 吞掉判定环拒绝 */ }
throw new Error("boom: program's own bug")`

	res := Run(context.Background(), program,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
		Options{Stages: []invoke.Stage{deny}})
	if res.ErrorKind != "runtime" {
		t.Fatalf("程序自己的错误必须 runtime, got %q (Error=%q)", res.ErrorKind, res.Error)
	}
	if !strings.Contains(res.Error, "boom") {
		t.Fatalf("错误文案必须是程序自己的, got %q", res.Error)
	}
}

// 对照：未捕获的判定环拒绝仍归 stage_denied（判定环前缀保留）。
func TestStageDenialUncaughtStillClassified(t *testing.T) {
	deny := invoke.StageFunc{StageName: "审计门",
		Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			return invoke.Verdict{Allow: false, Reason: "策略否定", Retryable: false}, nil
		}}
	res := Run(context.Background(), `host.t()`,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
		Options{Stages: []invoke.Stage{deny}})
	if res.ErrorKind != "stage_denied" {
		t.Fatalf("未捕获拒绝必须 stage_denied, got %q (Error=%q)", res.ErrorKind, res.Error)
	}
	if !strings.Contains(res.Error, "判定环拒绝") {
		t.Fatalf("判定环前缀必须保留, got %q", res.Error)
	}
}

// 普通绑定失败（无 toolfail 分类）未捕获仍 runtime（既有语义不回退）。
func TestPlainBindingErrorStaysRuntime(t *testing.T) {
	res := Run(context.Background(), `host.bad()`,
		[]Binding{{Name: "bad", Call: func(context.Context, string) (string, error) {
			return "", context.DeadlineExceeded
		}}}, Options{})
	// context.DeadlineExceeded 无 Kind() 接口 ⇒ 无分类 ⇒ runtime（非 deadline）
	if res.ErrorKind != "runtime" {
		t.Fatalf("无分类的绑定失败保持 runtime, got %q", res.ErrorKind)
	}
}

// TestToolfailFailureClassified 规范失败类型 *toolfail.Failure 的分类必须被
// 进程内路径认出（issue #58）。
//
// 此前 rejectionKindOf 用裸断言探 Kind() 接口，而 *Failure 没有该方法——宿主
// 用规范类型表态（标准动作）反而取不到分类，permission_denied 降级成
// runtime→transient，「重试也不会满足」的策略否定被报成「值得重试」。
func TestToolfailFailureClassified(t *testing.T) {
	cases := []struct {
		name  string
		class toolfail.Class
	}{
		{"permission_denied", toolfail.ClassPermissionDenied},
		{"not_found", toolfail.ClassNotFound},
		{"rate_limited", toolfail.ClassRateLimited},
		{"transient", toolfail.ClassTransient},
	}
	for _, c := range cases {
		res := Run(context.Background(), `await host.t()`,
			[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) {
				return "", toolfail.New(c.class, "", "宿主拒绝")
			}}}, Options{})
		if res.ErrorKind != string(c.class) {
			t.Fatalf("%s: ErrorKind 应为规范分类 %q, got %q (Error=%q)",
				c.name, c.class, res.ErrorKind, res.Error)
		}
	}
}

// TestToolfailFailureClassSurvivesWrap 同一失败套一层 %w 分类不得丢（issue #58
// 钉「链感知」）：裸类型断言不穿包装，规范类型包一层上下文就探不到。
func TestToolfailFailureClassSurvivesWrap(t *testing.T) {
	res := Run(context.Background(), `await host.t()`,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) {
			return "", fmt.Errorf("工具装饰链: %w", toolfail.New(toolfail.ClassPermissionDenied, "", "宿主拒绝"))
		}}}, Options{})
	if res.ErrorKind != "permission_denied" {
		t.Fatalf("包装后的规范分类不得丢, got %q (Error=%q)", res.ErrorKind, res.Error)
	}
}

// customKindError 宿主自定义的分类错误（Kind() 接口，非 toolfail 类型）。
type customKindError struct{ msg, kind string }

func (e *customKindError) Error() string { return e.msg }
func (e *customKindError) Kind() string  { return e.kind }

// TestCustomKindErrorNotStagePrefixed 自定义 Kind() 且与判定环无关的宿主拒绝：
// 分类照采，但消息**不得**冠「判定环拒绝」（issue #59）。
//
// 此前 stageOutcome 把一切非 ErrStageFailed 错误都当判定环拒绝——宿主审计门/
// 审批门/工具自身的权限拒绝被归因到判定环，排障的人去查根本没参与的守卫配置；
// 消息会进模型上下文与调用账，误导传染给模型的自纠判断。
func TestCustomKindErrorNotStagePrefixed(t *testing.T) {
	res := Run(context.Background(), `await host.t()`,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) {
			return "", &customKindError{msg: "自定义宿主拒绝：策略不允许", kind: "permission_denied"}
		}}}, Options{})
	if res.ErrorKind != "permission_denied" {
		t.Fatalf("ErrorKind 仍是该 kind, got %q", res.ErrorKind)
	}
	if strings.Contains(res.Error, "判定环") {
		t.Fatalf("与判定环无关的拒绝不得冠判定环前缀, got %q", res.Error)
	}
}

// TestToolfailFailureNotStagePrefixed toolfail 规范失败走收口 else 分支：
// 无判定环前缀、分类原样（issue #59 静态判据：else 分支必须可达——当前形态
// 曾是死代码）。
func TestToolfailFailureNotStagePrefixed(t *testing.T) {
	res := Run(context.Background(), `await host.t()`,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) {
			return "", toolfail.New(toolfail.ClassPermissionDenied, "", "宿主拒绝")
		}}}, Options{})
	if res.ErrorKind != "permission_denied" || strings.Contains(res.Error, "判定环") {
		t.Fatalf("规范失败应走 else 分支（无前缀、分类原样）, got kind=%q Error=%q",
			res.ErrorKind, res.Error)
	}
}

// TestRewrittenRejectionStaysRuntime 程序改写异常消息后重抛不得继承 stage
// 分类（issue #57）。
//
// 此前锚定用单向 Contains：catch(e){ throw new Error("failed: " + e.message) }
// 的被拒值包含载体消息 ⇒ 被判 stage_denied，Suggestions 按「换工具/走审批」
// 引导——而真实问题是程序自己抛错。现在锚定要求被拒值逐字等于载体未捕获时
// 的渲染（含每次抛出随机的 nonce），改写即失配。
func TestRewrittenRejectionStaysRuntime(t *testing.T) {
	deny := invoke.StageFunc{StageName: "审计门",
		Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			return invoke.Verdict{Allow: false, Reason: "策略否定", Retryable: false}, nil
		}}
	cases := []string{
		`try { await host.t() } catch (e) { throw new Error("failed: " + e.message) }`,
		`try { await host.t() } catch (e) { throw new Error(e.message) }`, // 逐字复制消息也不行
		`try { await host.t() } catch (e) { throw "字符串: " + e.message }`,
	}
	for _, prog := range cases {
		res := Run(context.Background(), prog,
			[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
			Options{Stages: []invoke.Stage{deny}})
		if res.ErrorKind != "runtime" {
			t.Fatalf("%s\n改写后的拒绝必须 runtime, got %q (Error=%q)", prog, res.ErrorKind, res.Error)
		}
		if strings.Contains(res.Error, "判定环拒绝") {
			t.Fatalf("%s\n改写后的拒绝不得带判定环前缀, got %q", prog, res.Error)
		}
	}
}

// TestRethrownCarrierStillClassified 程序原样重抛捕获到的载体（throw e）：
// 是同一次拒绝的再抛，分类照采、锚标记剥离（精确锚定的正对照，#57）。
func TestRethrownCarrierStillClassified(t *testing.T) {
	deny := invoke.StageFunc{StageName: "审计门",
		Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			return invoke.Verdict{Allow: false, Reason: "策略否定", Retryable: false}, nil
		}}
	res := Run(context.Background(), `try { await host.t() } catch (e) { throw e }`,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
		Options{Stages: []invoke.Stage{deny}})
	if res.ErrorKind != "stage_denied" {
		t.Fatalf("原样重抛仍应归因 stage_denied, got %q (Error=%q)", res.ErrorKind, res.Error)
	}
	if !strings.Contains(res.Error, "判定环拒绝") {
		t.Fatalf("判定环前缀必须保留, got %q", res.Error)
	}
	if strings.Contains(res.Error, "[ptc#") {
		t.Fatalf("锚标记是 Go 侧机械标记，不得进模型可见消息, got %q", res.Error)
	}
}

// TestStageFailureStillPrefixed 判定环自身故障（ErrStageFailed）仍是
// 「判定环故障」+ stage_failed（#59 正对照：防止为修归因把真判定环文案改掉）。
func TestStageFailureStillPrefixed(t *testing.T) {
	broken := invoke.StageFunc{StageName: "审计门",
		Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			return invoke.Verdict{}, fmt.Errorf("守卫存储不可用")
		}}
	res := Run(context.Background(), `await host.t()`,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
		Options{Stages: []invoke.Stage{broken}})
	if res.ErrorKind != "stage_failed" {
		t.Fatalf("判定环自身故障应 stage_failed, got %q (Error=%q)", res.ErrorKind, res.Error)
	}
	if !strings.Contains(res.Error, "判定环故障") {
		t.Fatalf("判定环故障前缀必须保留, got %q", res.Error)
	}
}
