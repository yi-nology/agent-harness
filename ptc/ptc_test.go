// PTC 执行器单测（批三百零五）：完成值 JSON 契约/失败是结果字段不是异常/三预算闸/
// 归并黄金形态（N 次绑定→小结果，程序把大输出「洗」小的账目在 CallLog 不缺）/
// JS 子集边界（对象参数序列化、标识符净化）。
package ptc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oekit/pkg/errors"
)

// echo 绑定：回显参数（记账形态断言用）。
func echo(name string) Binding {
	return Binding{Name: name, Call: func(_ context.Context, args string) (string, error) {
		return "OUT<" + args + ">", nil
	}}
}

func failing(name, msg string) Binding {
	return Binding{Name: name, Call: func(_ context.Context, _ string) (string, error) {
		return "", errors.New(msg)
	}}
}

func TestRunReturnContract(t *testing.T) {
	ctx := context.Background()
	// return 值=JSON 可序列化；undefined→null
	r := Run(ctx, `return 42`, nil, Options{})
	if !r.OK || r.Value != "42" {
		t.Fatalf("数字 return 不符: %+v", r)
	}
	r = Run(ctx, `return {a: 1, b: "x"}`, nil, Options{})
	if !r.OK || r.Value != `{"a":1,"b":"x"}` {
		t.Fatalf("对象 return 序列化不符: %+v", r)
	}
	r = Run(ctx, ``, nil, Options{})
	if !r.OK || r.Value != "null" {
		t.Fatalf("无 return 应为 null: %+v", r)
	}
	// 顶层 await 可用（async IIFE）
	r = Run(ctx, `const v = await host.echo("x"); return v`, []Binding{echo("echo")}, Options{})
	if !r.OK || r.Value != `"OUT<x>"` {
		t.Fatalf("await 绑定返回不符: %+v", r)
	}
}

// 多指标归并黄金形态（golden 对拍的微形）：5 次采集只回一个小结果——原始输出不过
// 模型面，CallLog 账目不缺（程序「洗」小结果的审计面）。
func TestRunAggregationGolden(t *testing.T) {
	var calls int
	big := Binding{Name: "metrics", Call: func(_ context.Context, args string) (string, error) {
		calls++
		return strings.Repeat("M", 4096) + args, nil // 大原始输出
	}}
	prog := `
let max = 0;
for (let i = 0; i < 5; i++) {
  const out = await host.metrics('{"host":"h' + i + '"}');
  const n = out.length;
  if (n > max) max = n;
}
return { hosts: 5, maxLen: max };`
	r := Run(context.Background(), prog, []Binding{big}, Options{})
	wantLen := 4096 + len(`{"host":"h0"}`) // big 绑定=4096 M + 参数原文
	if !r.OK || r.Value != fmt.Sprintf(`{"hosts":5,"maxLen":%d}`, wantLen) {
		t.Fatalf("归并结果不符: %+v", r)
	}
	if calls != 5 || r.Calls != 5 || len(r.CallLog) != 5 {
		t.Fatalf("调用账目不符: calls=%d r=%+v", calls, r)
	}
	if r.Bytes != 5*wantLen {
		t.Fatalf("字节账目不符: %d", r.Bytes)
	}
}

// 失败是结果字段不是异常：JS 抛错/绑定失败/拒绝——恒 Result 不 panic。
func TestRunFailureIsField(t *testing.T) {
	ctx := context.Background()
	r := Run(ctx, `throw new Error("boom")`, nil, Options{})
	if r.OK || r.ErrorKind != "runtime" || !strings.Contains(r.Error, "boom") {
		t.Fatalf("JS 抛错应落字段: %+v", r)
	}
	// 绑定失败可被程序捕获（try/catch 纪律）
	r = Run(ctx, `try { await host.bad("x"); return "caught"; } catch (e) { return "caught:" + String(e).slice(0, 6); }`,
		[]Binding{failing("bad", "kaboom")}, Options{})
	if !r.OK || !strings.Contains(r.Value, "caught") {
		t.Fatalf("绑定失败应可捕获: %+v", r)
	}
	// 未捕获=拒绝落字段
	r = Run(ctx, `await host.bad("x")`, []Binding{failing("bad", "kaboom")}, Options{})
	if r.OK || r.ErrorKind != "runtime" || !strings.Contains(r.Error, "kaboom") {
		t.Fatalf("未捕获绑定失败应落字段: %+v", r)
	}
}

// 三预算闸：调用次数/输出字节/墙钟 deadline 到点中断=结果字段（kind 精确）。
func TestRunBudgets(t *testing.T) {
	ctx := context.Background()
	r := Run(ctx, `for (let i = 0; i < 10; i++) await host.echo(String(i)); return "done"`,
		[]Binding{echo("echo")}, Options{MaxCalls: 3})
	if r.OK || r.ErrorKind != "budget_calls" {
		t.Fatalf("调用超限应 budget_calls: %+v", r)
	}

	r = Run(ctx, `await host.echo("0123456789"); return "done"`,
		[]Binding{echo("echo")}, Options{MaxBytes: 5})
	if r.OK || r.ErrorKind != "budget_bytes" {
		t.Fatalf("字节超限应 budget_bytes: %+v", r)
	}

	r = Run(ctx, `while (true) {}`, nil, Options{Timeout: 100 * time.Millisecond})
	if r.OK || r.ErrorKind != "deadline" {
		t.Fatalf("失控循环应 deadline 中断: %+v", r)
	}
}

// 永不落定的 await：VM 无定时器——如实判 pending 不挂死。
func TestRunPendingPromise(t *testing.T) {
	r := Run(context.Background(), `await new Promise(function(){}); return 1`, nil, Options{Timeout: 2 * time.Second})
	if r.OK || r.ErrorKind != "pending" {
		t.Fatalf("永不落定应 pending: %+v", r)
	}
}

// return 值不可 JSON 序列化=bad_result（契约「必须 JSON 可序列化」的显形）。
func TestRunBadResult(t *testing.T) {
	r := Run(context.Background(), `return function(){}`, nil, Options{})
	if r.OK || r.ErrorKind != "bad_result" {
		t.Fatalf("函数 return 应 bad_result: %+v", r)
	}
}

// 对象参数序列化+标识符净化：host.x({cmd:..}) 与 host["a-b"] 形态都能通。
func TestRunBindingArgsAndNames(t *testing.T) {
	var got string
	b := Binding{Name: "get-logs.host1", Call: func(_ context.Context, args string) (string, error) {
		got = args
		return "ok", nil
	}}
	r := Run(context.Background(), `await host.get_logs_host1({cmd: "uptime", n: 3}); return "x"`, []Binding{b}, Options{})
	if !r.OK {
		t.Fatalf("对象参数/净化名应可调: %+v", r)
	}
	if got != `{"cmd":"uptime","n":3}` && got != `{"n":3,"cmd":"uptime"}` {
		t.Fatalf("对象参数序列化不符: %q", got)
	}
}

func TestJsIdent(t *testing.T) {
	cases := map[string]string{
		"read_spill":     "read_spill",
		"get-logs.host1": "get_logs_host1",
		"9lives":         "_9lives",
		"-x-":            "x_",
		"":               "",
	}
	for in, want := range cases {
		if got := jsIdent(in); got != want {
			t.Fatalf("jsIdent(%q)=%q want %q", in, got, want)
		}
	}
}

// ── P2（批三百零七）─────────────────────────────────────────────────────────

// 阻塞绑定被墙钟断掉：模拟变更类工具审批等待（<-ctx.Done() 永不返回的绑定）——
// vm.Interrupt 打不断阻塞中的 Go 调用，runCtx 取消是唯一收刀（批三百零七核心修）。
// 断言：墙钟 100ms 后 Run 在 2s 内返回（不拖穿）、ErrorKind=deadline、CallLog 留痕。
func TestDeadlineCancelsBlockedBinding(t *testing.T) {
	block := Binding{
		Name: "run_approved_command",
		Call: func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done() // 审批等待形态：只有 ctx 能打断
			return "", ctx.Err()
		},
	}
	start := time.Now()
	res := Run(context.Background(), "return await host.run_approved_command('{}')",
		[]Binding{block}, Options{Timeout: 100 * time.Millisecond})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("阻塞绑定拖穿墙钟: %s", elapsed)
	}
	if res.OK {
		t.Fatal("deadline 到点不应成功")
	}
	if res.ErrorKind != "deadline" && res.ErrorKind != "runtime" {
		t.Fatalf("kind 应为 deadline/runtime，got %q（%s）", res.ErrorKind, res.Error)
	}
	if len(res.CallLog) != 1 || res.CallLog[0].Status != "error" {
		t.Fatalf("已执行步骤应留痕: %+v", res.CallLog)
	}
}

// 语法预检：syntax kind 带 goja 行号，不烧绑定预算（Calls 恒 0）。
func TestSyntaxPreCheck(t *testing.T) {
	res := Run(context.Background(), "return {{{", nil, Options{})
	if res.ErrorKind != "syntax" {
		t.Fatalf("kind 应为 syntax: %+v", res)
	}
	if !strings.Contains(res.Error, "程序语法错误") {
		t.Fatalf("错误文案应含语法标识: %s", res.Error)
	}
	if res.Calls != 0 {
		t.Fatal("语法预检不应烧绑定预算")
	}
}

// 对拍回归门（spec §4 P1 golden 对拍的确定性代理）：10×2KB 采集在程序内归并成小结果，
// 模型可见面=归并结果而非原始量——断言归并结果比逐调路径小两个数量级（值主张回归门：
// 若哪天绑定输出不经归并直通回包，本测即红）。
func TestMergeContextRegression(t *testing.T) {
	var bindings []Binding
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("get_metrics_%d", i)
		big := strings.Repeat("x", 2048)
		bindings = append(bindings, Binding{
			Name: name,
			Call: func(_ context.Context, _ string) (string, error) { return big, nil },
		})
	}
	prog := `var total = 0;
for (var i = 0; i < 10; i++) { total += host["get_metrics_" + i]("{}").length; }
return { merged: "ok", total: total };`
	res := Run(context.Background(), prog, bindings, Options{MaxBytes: 1 << 20})
	if !res.OK {
		t.Fatalf("归并程序应成功: %+v", res)
	}
	if res.Calls != 10 {
		t.Fatalf("应 10 次绑定调用: %d", res.Calls)
	}
	perCallVisible := 10 * 2048 // 逐调路径：全部原始量进上下文
	if len(res.Value)*100 > perCallVisible {
		t.Fatalf("归并结果应比逐调路径小两个数量级: merged=%d perCall=%d", len(res.Value), perCallVisible)
	}
}
