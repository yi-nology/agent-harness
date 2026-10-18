package ptc

import (
	"context"
	"strings"
	"testing"
)

// optBind 造一个带可选形参的绑定，并记录宿主收到的 JSON 文本。
func optBind(name, sig, echo string, got *string) Binding {
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

// TestOptionalParamOmitted 少传可选形参时，该键**不应出现在 JSON 里**。
//
// 这是本条改动的全部理由。此前位置绑定没有可选表达，工具一带可选参数，模型就只能
// 二选一：为不填的槽位瞎填一个值，或者干脆不用 Signature 退回手写 JSON。
//
// 关键在「不出现」而不是「为 null」：宿主收到 {"count":null} 与「没给 count」是
// 两件事，前者可能走进显式的空值分支，后者才是走默认值。
func TestOptionalParamOmitted(t *testing.T) {
	var got string
	bind := []Binding{optBind("get_logs", "get_logs(host, count?)", `ok`, &got)}

	r := Run(context.Background(), `return host.get_logs("h1")`, bind, Options{})
	if !r.OK {
		t.Fatalf("少传可选形参应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(got, `"host":"h1"`) {
		t.Fatalf("必填形参应在，实得 %s", got)
	}
	if strings.Contains(got, "count") {
		t.Fatalf("未提供的可选形参不应出现在 JSON 里（null 与缺失不是一回事），实得 %s", got)
	}
}

// TestOptionalParamProvided 传了可选形参就该带上，且值正确。
func TestOptionalParamProvided(t *testing.T) {
	var got string
	bind := []Binding{optBind("get_logs", "get_logs(host, count?)", `ok`, &got)}

	r := Run(context.Background(), `return host.get_logs("h1", 3600)`, bind, Options{})
	if !r.OK {
		t.Fatalf("实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(got, `"count":3600`) {
		t.Fatalf("已提供的可选形参应带上，实得 %s", got)
	}
}

// TestExplicitUndefinedTreatedAsAbsent 显式传 undefined 等同于不传。
//
// 模型写 `host.get_logs("h1", undefined)` 是很自然的写法（它不知道省略参数在 JS 里
// 要靠实参个数），若把它当成「提供了一个 undefined 值」就会发出 {"count":null}。
func TestExplicitUndefinedTreatedAsAbsent(t *testing.T) {
	var got string
	bind := []Binding{optBind("get_logs", "get_logs(host, count?)", `ok`, &got)}

	r := Run(context.Background(), `return host.get_logs("h1", undefined)`, bind, Options{})
	if !r.OK {
		t.Fatalf("实得 %q / %q", r.ErrorKind, r.Error)
	}
	if strings.Contains(got, "count") {
		t.Fatalf("显式 undefined 应视同未提供，实得 %s", got)
	}
}

// TestOptionalStillArityBounded 少传**必填**形参仍须被拒——可选不是「随便传」。
func TestOptionalStillArityBounded(t *testing.T) {
	hits := 0
	bind := []Binding{{
		Name: "get_logs", Signature: "get_logs(host, count?)",
		Call: func(context.Context, string) (string, error) { hits++; return "x", nil },
	}}
	r := Run(context.Background(), `return host.get_logs()`, bind, Options{})
	if r.OK {
		t.Fatal("必填形参缺失应被拒")
	}
	if hits != 0 {
		t.Fatalf("元数不符不得触达绑定，实得 %d 次", hits)
	}
}

// TestOptionalOutOfOrderRejected 可选形参后面还有必填时判非法。
//
// 那种形态下，模型为了传后面的必填项就得先把前面的空位填上——而填进去的值是编的。
// 与其让它看起来能用，不如装配期直接拒。
func TestOptionalOutOfOrderRejected(t *testing.T) {
	r := Run(context.Background(), `return 1`, []Binding{{
		Name: "t", Signature: "t(a?, b)",
		Call: func(context.Context, string) (string, error) { return "x", nil },
	}}, Options{})
	if r.OK || r.ErrorKind != "binding_signature" {
		t.Fatalf("可选形参排在必填之前应被拒，实得 OK=%v kind=%q", r.OK, r.ErrorKind)
	}
}

// TestAllRequiredKeepsStrictArity 全必填形态的元数下限不能塌成 0。
//
// 这条是回归护栏：必填数曾在「没有可选形参」这条最常见的路径上留在零值，
// 于是元数下限形同不存在，少传参数照样放行——而全必填恰恰是绝大多数工具的样子。
func TestAllRequiredKeepsStrictArity(t *testing.T) {
	hits := 0
	bind := []Binding{{
		Name: "t", Signature: "t(a, b)",
		Call: func(context.Context, string) (string, error) { hits++; return "x", nil },
	}}
	const prog = `try { host.t("x") } catch (e) { return "caught" } return "未拦截"`
	r := Run(context.Background(), prog, bind, Options{})

	if !r.OK {
		t.Fatalf("try/catch 后应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if r.Value != `"caught"` {
		t.Fatalf("全必填形态少传参数必须被拦下，实得 %s", r.Value)
	}
	if hits != 0 {
		t.Fatalf("被拦下的调用不得触达绑定，实得 %d 次", hits)
	}
}

// TestArityMessageOmitsDegenerateRange 全必填时的报错不打印「2-2」这种退化区间。
func TestArityMessageOmitsDegenerateRange(t *testing.T) {
	bind := []Binding{{
		Name: "t", Signature: "t(a, b)",
		Call: func(context.Context, string) (string, error) { return "x", nil },
	}}
	const prog = `try { host.t("x") } catch (e) { return e.message } return "未拦截"`
	r := Run(context.Background(), prog, bind, Options{})
	if !r.OK {
		t.Fatalf("实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(r.Value, "期望 2 个参数") {
		t.Fatalf("全必填应打印「期望 2 个参数」，实得 %s", r.Value)
	}
	if strings.Contains(r.Value, "2-2") {
		t.Fatalf("不应打印退化区间，实得 %s", r.Value)
	}
}
