package ptc

import (
	"context"
	"strings"
	"testing"
)

// sigBind 造一个带调用形态的绑定，并把收到的 JSON 文本记下来。
func sigBind(name, sig, echo string, got *string) Binding {
	return Binding{
		Name:      name,
		Signature: sig,
		Call: func(_ context.Context, argsJSON string) (string, error) {
			if got != nil {
				*got = argsJSON
			}
			return echo, nil
		},
	}
}

// TestSignatureBindingPacksNamedArgs 模型按位置传参，宿主收到的是**具名** JSON。
//
// 这是类型化绑定的全部价值：模型不必在程序里手写 JSON 文本，少一层引号转义、
// 少一份重复的参数名，宿主那侧也拿到结构化入参不必再解析一次。
func TestSignatureBindingPacksNamedArgs(t *testing.T) {
	var got string
	bind := []Binding{sigBind("get_logs", "get_logs(host, count)", `ok`, &got)}

	r := Run(context.Background(), `return host.get_logs("h1", 3600)`, bind, Options{})
	if !r.OK {
		t.Fatalf("应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if r.Value != `"ok"` {
		t.Fatalf("出参应原样透传，实得 %s", r.Value)
	}
	if !strings.Contains(got, `"host":"h1"`) || !strings.Contains(got, `"count":3600`) {
		t.Fatalf("宿主应收到具名参数，实得 %s", got)
	}
}

// TestSignatureZeroParamBinding 无参形态也要能挂上，且不打包出多余字段。
func TestSignatureZeroParamBinding(t *testing.T) {
	var got string
	bind := []Binding{sigBind("now", "now()", `"T0"`, &got)}
	r := Run(context.Background(), `return host.now()`, bind, Options{})
	if !r.OK {
		t.Fatalf("无参形态应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if got != "{}" {
		t.Fatalf("无参应传空对象，实得 %s", got)
	}
}

// TestSignatureArityMismatchIsCatchable 元数不符应给出**指向形参**的错误，
// 且不得触达绑定。
//
// 这条是「模型能不能自己改对」的关键：错误若指向宿主内部反序列化，模型会以为
// 宿主坏了而放弃；指向形参名与实参数，它下一次就能改对。
func TestSignatureArityMismatchIsCatchable(t *testing.T) {
	hits := 0
	bind := []Binding{{
		Name: "get_logs", Signature: "get_logs(host, count)",
		Call: func(context.Context, string) (string, error) { hits++; return "x", nil },
	}}
	// try/catch 后程序应当正常完成——元数错是**调用点**的错，不该终止整个程序。
	const prog = `try { host.get_logs("h1") } catch (e) { return "caught:" + e.message } return "未捕获"`
	r := Run(context.Background(), prog, bind, Options{})

	if !r.OK {
		t.Fatalf("try/catch 后应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.HasPrefix(r.Value, `"caught:`) {
		t.Fatalf("应被程序捕获，实得 %s", r.Value)
	}
	for _, want := range []string{"get_logs", "期望 2 个参数", "实得 1"} {
		if !strings.Contains(r.Value, want) {
			t.Fatalf("错误应含 %q（函数名/期望数/实得数），实得 %s", want, r.Value)
		}
	}
	if hits != 0 {
		t.Fatalf("元数不符不得触达绑定，实得 %d 次", hits)
	}
}

// TestSignatureInvalidRejectedAtAssembly 形态串非法应在装配期就拒，不进 VM。
//
// 放行的话，模型侧表现为「绑定了却怎么调都不对」——最难往形态串上想的那类故障。
func TestSignatureInvalidRejectedAtAssembly(t *testing.T) {
	cases := map[string]string{
		"缺括号":   "get_logs",
		"函数名非法": "9bad(a)",
		"形参非法":  "get_logs(a-b)",
		"形参重名":  "get_logs(x, x)",
	}
	for name, sig := range cases {
		t.Run(name, func(t *testing.T) {
			hits := 0
			bind := []Binding{{
				Name: "get_logs", Signature: sig,
				Call: func(context.Context, string) (string, error) { hits++; return "x", nil },
			}}
			r := Run(context.Background(), `return 1`, bind, Options{})
			if r.OK {
				t.Fatal("非法形态应在装配期被拒")
			}
			if r.ErrorKind != "binding_signature" {
				t.Fatalf("ErrorKind 应为 binding_signature，实得 %q", r.ErrorKind)
			}
			if hits != 0 {
				t.Fatalf("被拒的程序不得触达绑定，实得 %d 次", hits)
			}
		})
	}
}

// TestSignatureNameMismatchRejected 形态里的函数名必须等于绑定名净化后的标识符。
//
// 两边不一致时若静默取其一，模型调用的名字和实际挂载的名字会对不上，
// 而两侧都不报错——「怎么调都不对」是最难查的一类故障。
func TestSignatureNameMismatchRejected(t *testing.T) {
	r := Run(context.Background(), `return 1`, []Binding{{
		Name: "get-logs", Signature: "getLogs(host)",
		Call: func(context.Context, string) (string, error) { return "x", nil },
	}}, Options{})
	if r.OK || r.ErrorKind != "binding_signature" {
		t.Fatalf("形态名与绑定名不一致应被拒，实得 OK=%v kind=%q", r.OK, r.ErrorKind)
	}
}

// TestMixedSignatureAndPlainBindings 两种形态可在同一张表里混用。
func TestMixedSignatureAndPlainBindings(t *testing.T) {
	bind := []Binding{
		sigBind("get_logs", "get_logs(host)", `"structured"`, nil),
		okBind("echo", "plain"),
	}
	const prog = `return host.get_logs("h1") + "/" + host.echo("raw-json")`
	r := Run(context.Background(), prog, bind, Options{})
	if !r.OK {
		t.Fatalf("混用应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(r.Value, "structured") || !strings.Contains(r.Value, "plain") {
		t.Fatalf("两种形态都应可用，实得 %s", r.Value)
	}
}

// TestSignatureStillCountsAgainstCallBudget 结构化调用同样计预算。
//
// 三预算闸是按**调用次数与出参字节**计的，不因调用形态不同而漏记——否则多绕一层
// 壳就等于多一条绕过预算的路。
func TestSignatureStillCountsAgainstCallBudget(t *testing.T) {
	var got int
	bind := []Binding{{
		Name: "loop", Signature: "loop(n)",
		Call: func(context.Context, string) (string, error) { got++; return "ok", nil },
	}}
	r := Run(context.Background(), `for (let i=0;i<99;i++) host.loop(i); return 1`, bind, Options{MaxCalls: 3})
	if r.OK || r.ErrorKind != "budget_calls" {
		t.Fatalf("结构化调用同样应受调用次数闸约束，实得 OK=%v kind=%q", r.OK, r.ErrorKind)
	}
	if got != 3 {
		t.Fatalf("实际触达应恰好 3 次（闸在超限前收口），实得 %d", got)
	}
}

// ── 程序内工具发现 ──

// TestDiscoverExposedToProgram 装配了 Discoverer 就该能在程序里查到工具。
func TestDiscoverExposedToProgram(t *testing.T) {
	opts := Options{Discover: func(q string) []Discovered {
		if q == "重启" {
			return []Discovered{
				{Name: "restart_service", Signature: "restart_service(host)", Description: "重启服务"},
			}
		}
		return nil
	}}
	const prog = `const hits = discover("重启");
	 if (hits.length !== 1) return "数量不对:" + hits.length;
	 return hits[0].name + "/" + hits[0].signature`
	r := Run(context.Background(), prog, nil, opts)
	if !r.OK {
		t.Fatalf("发现入口应可用，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if r.Value != `"restart_service/restart_service(host)"` {
		t.Fatalf("应返回命中的工具与形态，实得 %s", r.Value)
	}
}

// TestDiscoverNotMountedWhenUnconfigured 未装配时**不存在**该全局。
//
// 挂一个永远返回空数组的空壳会让模型以为「没有别的工具了」而放弃检索——
// 那是比「函数不存在」更坏的失败：前者给出错误结论，后者给出可纠正的 ReferenceError。
func TestDiscoverNotMountedWhenUnconfigured(t *testing.T) {
	const prog = `try { discover("x"); return "竟然存在" } catch (e) { return "不存在" }`
	r := Run(context.Background(), prog, nil, Options{})
	if !r.OK || r.Value != `"不存在"` {
		t.Fatalf("未装配时不应挂出该全局，实得 OK=%v value=%s err=%q", r.OK, r.Value, r.Error)
	}
}

// TestDiscoverReceivesQueryUnified 查询参数与绑定参数同一套归一（字符串或对象都行）。
func TestDiscoverReceivesQueryUnified(t *testing.T) {
	var saw string
	opts := Options{Discover: func(q string) []Discovered {
		saw = q
		return nil
	}}
	r := Run(context.Background(), `discover({q:"服务"}); return 1`, nil, opts)
	if !r.OK {
		t.Fatalf("实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(saw, "服务") {
		t.Fatalf("对象入参应被归一为 JSON 文本后交给宿主，实得 %q", saw)
	}
}

// TestDiscoverNotCountedAsToolCall 发现不计入工具调用预算。
//
// 它是本地目录检索，不触达宿主工具；计入预算等于让模型「查一次工具表」也要交税，
// 那会让它在预算紧张时不敢检索——而检索正是它省调用的手段。
func TestDiscoverNotCountedAsToolCall(t *testing.T) {
	opts := Options{
		MaxCalls: 1,
		Discover: func(string) []Discovered { return nil },
	}
	const prog = `for (let i=0;i<50;i++) discover("x"); return 1`
	r := Run(context.Background(), prog, nil, opts)
	if !r.OK {
		t.Fatalf("发现不应消耗调用预算，实得 %q / %q", r.ErrorKind, r.Error)
	}
}
