package ptc

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/invoke"
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
