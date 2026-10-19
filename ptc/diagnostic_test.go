package ptc

import (
	"context"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/oagentkit/guardx/invoke"
	"github.com/oekit/pkg/errors"
)

// TestSyntaxErrorLocationPointsIntoUserProgram 语法错误的位置必须是**程序内**行号，
// 不是包裹后（含 IIFE 头）的行号。
//
// 这条是整个诊断面的地基：包裹多了一行，折算少减一次，报出来的就是差一行的行号。
// 而差一行的行号比没有行号更坏——模型会照着去改一行根本没问题的代码。
func TestSyntaxErrorLocationPointsIntoUserProgram(t *testing.T) {
	const prog = "let a = 1;\nlet b = 2;\nlet c = (;"
	r := Run(context.Background(), prog, nil, Options{})

	if r.OK || r.ErrorKind != "syntax" {
		t.Fatalf("应有语法错误，实得 OK=%v kind=%q", r.OK, r.ErrorKind)
	}
	if !r.Location.Valid() {
		t.Fatalf("语法错误应带位置，实得零值（Error=%q）", r.Error)
	}
	// 第 3 行是 `let c = (;`
	if r.Location.Line != 3 {
		t.Fatalf("位置应指向程序第 3 行（%q），实得第 %d 行", strings.SplitN(prog, "\n", 3)[2], r.Location.Line)
	}
}

// TestSyntaxErrorOnFirstLine 第一行出错时折算不能把行号推到 0。
func TestSyntaxErrorOnFirstLine(t *testing.T) {
	r := Run(context.Background(), `let x = (;`, nil, Options{})
	if r.Location.Line != 1 {
		t.Fatalf("首行语法错误应报第 1 行，实得第 %d 行（折算把首行减没了？）", r.Location.Line)
	}
}

// TestRuntimeRejectionDoesNotFabricatePosition 运行期异常**不得**编造位置。
//
// 程序体是 async IIFE：函数体内抛出的异常变成 Promise rejection，RunProgram 本身
// 正常返回，rejection 原因只是一个值——goja 没有提供「rejection 处回溯调用栈」的
// 公开面。所以这条路径上位置是拿不到的。
//
// 这里钉的是**诚实**：位置不可得就留零值。编一个位置出来会让模型去改一行根本没
// 问题的代码，比没有位置更坏。
func TestRuntimeRejectionDoesNotFabricatePosition(t *testing.T) {
	const prog = "const x = 1;\nconst y = null;\nreturn x.missing.deep;"
	r := Run(context.Background(), prog, nil, Options{})

	if r.OK || r.ErrorKind != "runtime" {
		t.Fatalf("应产生 runtime 错误，实得 OK=%v kind=%q Error=%q", r.OK, r.ErrorKind, r.Error)
	}
	if r.Location.Valid() {
		t.Fatalf("rejection 路径上 goja 拿不到程序栈，位置应为零值，实得 %+v（说明是编的）", r.Location)
	}
}

// TestLocationOfExceptionWorksWhenAvailable 直接给 locationOfException 一个真实异常，
// 钉住「能拿到时确实取到了 program 帧」。
//
// 与上一条并看：能力边界是**工具的**，不是实现的偷懒——同步可得的路径确实work。
func TestLocationOfExceptionWorksWhenAvailable(t *testing.T) {
	vm := goja.New()
	const prog = "const a = 1;\nconst b = null;\nb.missing.deep;"
	// 必须用 Compile 而非 RunString：helper 按源名 program 过滤帧，RunString 的
	// 源名是 "<eval>"，那属于「源名不匹配 → 不报」而不是「位置不可得」。
	p, err := goja.Compile(programSourceName, prog, false)
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := vm.RunProgram(p)
	if runErr == nil {
		t.Skip("该 goja 版本未在 RunProgram 上抛异常，跳过")
	}
	loc := locationOfException(runErr, 0)
	if !loc.Valid() {
		t.Fatalf("同步异常应能取到 program 帧，实得零值（err=%v）", runErr)
	}
	if loc.Line != 3 {
		t.Fatalf("应指向第 3 行（%q），实得第 %d 行", "b.missing.deep;", loc.Line)
	}
}

// TestLocationOfExceptionIgnoresMismatchedSource 源名不匹配的帧一律不报。
//
// 宿主绑定抛出的错误栈顶是 native 帧，报它等于让模型去改一个它根本改不了的地方；
// RunString 产生的 <eval> 帧同理。取不到就返回零值，不硬凑。
func TestLocationOfExceptionIgnoresMismatchedSource(t *testing.T) {
	vm := goja.New()
	p, err := goja.Compile("别的源名", "throw new Error('x')", false)
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := vm.RunProgram(p)
	if runErr == nil {
		t.Skip("该 goja 版本未在 RunProgram 上抛异常，跳过")
	}
	if loc := locationOfException(runErr, 0); loc.Valid() {
		t.Fatalf("源名不匹配的帧不该被当成程序位置，实得 %+v", loc)
	}
}

// TestLocationOfExceptionOnNonJSError 非 JS 错误不得被硬凑出位置。
func TestLocationOfExceptionOnNonJSError(t *testing.T) {
	if loc := locationOfException(errors.New("就是个普通 Go 错误"), 0); loc.Valid() {
		t.Fatalf("普通错误不该有程序位置，实得 %+v", loc)
	}
}

// TestErrorStringCarriesPosition 错误串本身也要带位置——只读 Error 字段的调用方
// （不看结构化字段的那类）才是多数。
func TestErrorStringCarriesPosition(t *testing.T) {
	r := Run(context.Background(), "let a = 1;\nlet b = (;", nil, Options{})
	if !strings.Contains(r.Error, "第 2 行") {
		t.Fatalf("错误串应含行号位置，实得 %q", r.Error)
	}
}

// TestSuggestionsPresentForEveryFailureKind 每种失败都要给模型可执行的建议。
//
// 「建议表」容易腐化的方式不是整条丢失，而是某个类别忘了加——所以这里逐类点。
func TestSuggestionsPresentForEveryFailureKind(t *testing.T) {
	cases := map[string]Result{
		"deadline":          Run(context.Background(), "while(true){}", nil, Options{Timeout: 30_000_000}),
		"budget_calls":      Run(context.Background(), "for(let i=0;i<99;i++) host.echo(String(i));", []Binding{okBind("echo", "x")}, Options{MaxCalls: 2}),
		"budget_bytes":      Run(context.Background(), "return host.big();", []Binding{{Name: "big", Call: func(context.Context, string) (string, error) { return strings.Repeat("x", 64), nil }}}, Options{MaxBytes: 8}),
		"pending":           Run(context.Background(), "await new Promise(function(){}); return 1;", nil, Options{}),
		"bad_result":        Run(context.Background(), "return function(){}", nil, Options{}),
		"syntax":            Run(context.Background(), "let c = (;", nil, Options{}),
		"binding_collision": Run(context.Background(), "return 1", []Binding{okBind("a-b", "1"), okBind("a_b", "2")}, Options{}),
		"runtime":           Run(context.Background(), "const y = null; return y.x.y;", nil, Options{}),
		"stage_denied": Run(context.Background(), `host.t("x")`,
			[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
			Options{Stages: []invoke.Stage{invoke.StageFunc{StageName: "审计门", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
				return invoke.Verdict{Allow: false, Reason: "变更须走方案审批"}, nil
			}}}}),
		"stage_failed": Run(context.Background(), `host.t("x")`,
			[]Binding{{Name: "t", Call: func(context.Context, string) (string, error) { return "x", nil }}},
			Options{Stages: []invoke.Stage{invoke.StageFunc{StageName: "crasher", Fn: func(context.Context, invoke.Call) (invoke.Verdict, error) {
				panic("环里炸了")
			}}}}),
	}

	for kind, r := range cases {
		t.Run(kind, func(t *testing.T) {
			if r.OK {
				t.Fatalf("该样本应产生失败")
			}
			if r.ErrorKind != kind {
				t.Fatalf("ErrorKind 应为 %s，实得 %q（Error=%q）", kind, r.ErrorKind, r.Error)
			}
			if len(r.Suggestions) == 0 {
				t.Fatalf("类别 %s 必须给出面向模型的修复建议", kind)
			}
			for _, s := range r.Suggestions {
				if strings.TrimSpace(s) == "" {
					t.Fatalf("建议不得为空串：%v", r.Suggestions)
				}
			}
		})
	}
}

// TestSuccessHasNoSuggestions 成功不该带任何建议——诊断是失败面，成功时出现它
// 说明有分支漏判。
func TestSuccessHasNoSuggestions(t *testing.T) {
	r := Run(context.Background(), `return 1`, nil, Options{})
	if !r.OK {
		t.Fatalf("应成功，实得 %q", r.Error)
	}
	if r.Suggestions != nil {
		t.Fatalf("成功不应带建议，实得 %v", r.Suggestions)
	}
	if r.Location.Valid() {
		t.Fatalf("成功不应带位置，实得 %+v", r.Location)
	}
}

// TestUnknownKindHasNoSuggestion 未知类别返回 nil 而不是编一句建议。
// 猜出来的建议对模型有害：它会照着一条不对的路走。
func TestUnknownKindHasNoSuggestion(t *testing.T) {
	if got := suggestionsFor("some_kind_we_never_defined"); got != nil {
		t.Fatalf("未知类别不应编造建议，实得 %v", got)
	}
}

// TestSuggestionsAreIndependentSlices 每次调用拿到独立切片。
// 共享底层数组会让某一处 append 污染后续所有调用的返回值。
func TestSuggestionsAreIndependentSlices(t *testing.T) {
	a := suggestionsFor("deadline")
	a[0] = "被改了"
	b := suggestionsFor("deadline")
	if b[0] == "被改了" {
		t.Fatal("建议切片被共享：一次调用污染了另一次")
	}
}
