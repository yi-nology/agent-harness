package ptc

import (
	"context"
	"strings"
	"testing"

	"github.com/oekit/pkg/errors"
)

// errHeavy 供「报错但带大载荷」的工具返回。
var errHeavy = errors.New("boom")

// TestByteBudgetCoversErrorPath 钉住字节闸必须盖住错误路径。
//
// 背景：res.Bytes += len(out) 对**报错调用**同样记账，但字节闸的判定原先排在
// 错误分支之后——而错误分支紧接着 panic 出去，判定永远执行不到。于是程序只要
// try/catch 掉每次报错，报错工具返回的载荷就能无限累加，MaxBytes 形同虚设，
// 只剩 MaxCalls 拦着。这违反本文件头注的「超限=中断收口，不给程序越限继续跑」。
func TestByteBudgetCoversErrorPath(t *testing.T) {
	const each = 8 << 10 // 8KB
	const maxBytes = 16 << 10

	// 报错但返回大载荷：走错误路径。若字节闸不在错误路径上，攒到超限也不中断。
	heavy := Binding{Name: "heavy", Call: func(_ context.Context, _ string) (string, error) {
		return strings.Repeat("x", each), errHeavy
	}}

	// 程序逐次 try/catch，把报错吞掉继续跑——正是绕过字节闸所需的动作。
	prog := `
		for (let i = 0; i < 50; i++) {
			try { await host.heavy(); } catch (e) {}
		}
		return "done";
	`
	r := Run(context.Background(), prog, []Binding{heavy}, Options{
		MaxCalls: 50,
		MaxBytes: maxBytes,
		Timeout:  0,
	})

	// 超限必须中断收口：要么报字节预算超限，要么整体失败，
	// 但绝不能「跑完了 50 次报错调用还返回 OK」。
	if r.OK && r.ErrorKind != "budget_bytes" {
		t.Fatalf("报错路径必须同样受字节闸约束，实得 OK=%v ErrorKind=%q Error=%q",
			r.OK, r.ErrorKind, r.Error)
	}
	if !r.OK && r.ErrorKind == "budget_bytes" {
		return // 预期路径：以字节预算超限收口
	}
	t.Fatalf("预期以字节预算超限中断，实得 OK=%v Value=%q ErrorKind=%q",
		r.OK, r.Value, r.ErrorKind)
}

// TestByteBudgetStillRecordsErrorCalls 确认改动没有牺牲留痕：
// 报错调用即便随后触发中断，CallLog 里也要留下这条记录。
func TestByteBudgetStillRecordsErrorCalls(t *testing.T) {
	const each = 8 << 10
	const maxBytes = 16 << 10

	heavy := Binding{Name: "heavy", Call: func(_ context.Context, _ string) (string, error) {
		return strings.Repeat("x", each), errHeavy
	}}
	prog := `
		for (let i = 0; i < 50; i++) {
			try { await host.heavy(); } catch (e) {}
		}
		return "done";
	`
	r := Run(context.Background(), prog, []Binding{heavy}, Options{MaxCalls: 50, MaxBytes: maxBytes})

	if len(r.CallLog) == 0 {
		t.Fatalf("报错调用也必须留痕（CallLog 为空即回归）")
	}
	for _, c := range r.CallLog {
		if c.Name != "heavy" {
			t.Fatalf("CallLog 混入了非目标工具: %+v", c)
		}
		if c.Status != "error" {
			t.Fatalf("报错调用的 CallRecord.Status 应为 error，实得 %q", c.Status)
		}
	}
}
