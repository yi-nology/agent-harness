package ptc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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

// ── issue #67：RunProgram 返回后的收口窗口在 VM 上跑用户 JS——三个可稳定
// 复现的缺陷形态（对修复前代码实测钉住）──
//
// 旧收口对被拒值做 fmt.Sprintf("%v", p.Result())——*goja.Object 的 String()
// 会执行 JS 的 toString/valueOf（用户代码）。三个暴露形态：
//
//	旧缺陷 A（中断竞态误归因，实测）：敌意 toString 死循环 + 墙钟到点 ⇒
//	  Interrupt 的 panic 在 String 方法里炸开、被 fmt 的 catchPanic 吞成
//	  "%!v(PANIC=String method: ptc deadline exceeded ...)" 文本——墙钟事件
//	  被报成「程序拒绝」+ panic 渲染残渣，deadline 归因丢失；
//	旧缺陷 B（锚定也在跑 JS，实测 5.0s）：载体被程序原样重抛但 toString
//	  被下毒 ⇒ 连**锚定比较与消息渲染**都要跑敌意 JS，烧完整幅墙钟才收口，
//	  分类还拿不到；
//	旧缺陷 C（渲染异常变残渣，实测）：toString 自己 throw ⇒ 错误文案变成
//	  "程序拒绝: %!v(PANIC=...)"，模型可见消息不可读。
//
// 修法：锚定改**对象身份**（指针相等，不进 VM）；失配分支的文本渲染收在
// renderRejection 的保护下（中断 → deadline/中断归类；异常 → 非执行形态退文本）。

// TestRejectedRenderInterruptClassifiedAsDeadline 旧缺陷 A 的稳定暴露：
// toString 死循环让「渲染窗口」宽到秒级，墙钟 60ms 必落在其中——旧代码
// 稳定复现「runtime + %!v(PANIC=String method: ptc deadline exceeded ...)」
// （墙钟事件被吞成程序拒绝），新代码必须归因 deadline。
func TestRejectedRenderInterruptClassifiedAsDeadline(t *testing.T) {
	res := Run(context.Background(),
		`throw {toString: function(){ while (true) {} }}`,
		nil, Options{Timeout: 60 * time.Millisecond})
	if res.ErrorKind != "deadline" {
		t.Fatalf("渲染窗口被墙钟打断必须归因 deadline（不是 host_panic），got %q（Error=%q）",
			res.ErrorKind, res.Error)
	}
}

// TestRejectedCarrierWithPoisonedToStringStillClassified 旧缺陷 B 的稳定暴露：
// 程序接住判定环载体、给它的 toString 下毒再原样重抛——身份锚定不进 VM，
// 分类必须**立刻**拿到（不等墙钟、不受敌意代码拖延）。
//
// 旧代码在锚定与消息两处都要渲染被拒值 ⇒ 死循环烧完 5s 墙钟才收口（实测
// 5.001s）且分类丢失；新代码无渲染路径，瞬间返回 stage_denied。elapsed 上界
// 把「回归到渲染路径」钉死在时间维度上。
func TestRejectedCarrierWithPoisonedToStringStillClassified(t *testing.T) {
	deny := invoke.StageFunc{StageName: "审计门",
		Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
			return invoke.Verdict{Allow: false, Reason: "策略否定"}, nil
		}}
	start := time.Now()
	res := Run(context.Background(),
		`try { await host.t() } catch (e) { e.toString = function(){ while (true) {} }; throw e; }`,
		[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
		Options{Stages: []invoke.Stage{deny}, Timeout: 5 * time.Second})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("身份锚定不进 VM，不得被敌意 toString 拖住：耗时 %v", elapsed)
	}
	if res.ErrorKind != "stage_denied" {
		t.Fatalf("原样重抛（含 toString 被下毒）仍须归因 stage_denied，got %q（Error=%q）",
			res.ErrorKind, res.Error)
	}
}

// TestRejectedHostileRenderFallsBackSafely 旧缺陷 C：toString 自己抛异常，
// 旧代码把 fmt 的 panic 残渣（%!v(PANIC=...)）当错误文案；新代码渲染退到
// 非执行形态的占位文本（runtime），文案可读。
func TestRejectedHostileRenderFallsBackSafely(t *testing.T) {
	res := Run(context.Background(),
		`throw {toString: function(){ throw new Error("炸渲染") }}`,
		nil, Options{Timeout: 5 * time.Second})
	if res.OK {
		t.Fatal("被拒程序不得成功")
	}
	if res.ErrorKind != "runtime" {
		t.Fatalf("渲染被敌意对象打断应退 runtime（不是 host_panic），got %q（Error=%q）",
			res.ErrorKind, res.Error)
	}
	if !strings.Contains(res.Error, "不可渲染") {
		t.Fatalf("错误文案应说明被拒值不可渲染，got %q", res.Error)
	}
}

// TestDeadlineDuringProgramBodyStaysDeadline 对照组：墙钟打断的是**程序本体**
// （非收口窗口）——该路径一直归因 deadline，修 #67 不得让它回归。
func TestDeadlineDuringProgramBodyStaysDeadline(t *testing.T) {
	res := Run(context.Background(), `while (true) {}`, nil, Options{Timeout: 60 * time.Millisecond})
	if res.ErrorKind != "deadline" {
		t.Fatalf("程序本体被墙钟打断必须 deadline，got %q（Error=%q）", res.ErrorKind, res.Error)
	}
}

// TestFulfilledExportInterruptNotHostPanic issue #67 安全网（Run 的收口 recover）：
// 墙钟打断的也可以是 **fulfilled 值的导出**（getter 里死循环）——这条路径没有
// fmt 兜底，InterruptedError panic 直达 Run 的 recover：旧代码一律收成
// host_panic（「宿主侧要修的」），安全网按中断哨兵归因 deadline。
func TestFulfilledExportInterruptNotHostPanic(t *testing.T) {
	res := Run(context.Background(),
		`return { get x(){ while (true) {} } }`,
		nil, Options{Timeout: 60 * time.Millisecond})
	if res.ErrorKind != "deadline" {
		t.Fatalf("fulfilled 值导出被墙钟打断必须归因 deadline（不是 host_panic），got %q（Error=%q）",
			res.ErrorKind, res.Error)
	}
}
