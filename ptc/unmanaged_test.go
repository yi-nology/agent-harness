package ptc

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oekit/pkg/errors"
)

// 本文件钉住 v0.32.0 新增的「安全默认装配」语义。
//
// # 这组用例在钉什么
//
// 钉的是一条默认位的红线：**装配了工具绑定却没装判定环 ⇒ 拒绝执行**。
//
// 失效形态（此前真实存在）：宿主接了 PTC，却忘了接判定环。于是程序里的
// host.<tool>(...) 直达宿主工具——披露门不判、审计门不判、权限门不判，
// 而**没有任何信号**：调用方拿到的是一个正常的执行结果。
// 这正是「信任边界移动了，沙箱没跟着移动」——而本包恰是对外推荐的那条路，
// 于是看起来更安全的路径反而最不安全。
//
// 两条配套纪律：
//   - 逃逸口（AllowUnmanaged）必须**显式**，且生产代码里不得用（门禁判）；
//   - 纯计算（不装配绑定）不需要判定环——风险在**能力**，不在程序。

// TestRunRefusesUnmanagedExecution 钉住核心：装了绑定却没装判定环 ⇒ 拒绝，
// 且**动作体一次都不该被调用**。
func TestRunRefusesUnmanagedExecution(t *testing.T) {
	hits := 0
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) {
		hits++
		return "x", nil
	}}
	r := Run(context.Background(), `return host.t("x")`, []Binding{bind}, Options{})
	if r.OK {
		t.Fatal("未装配判定环的执行必须被拒（它是整条链上唯一能批量调工具的路径）")
	}
	if r.ErrorKind != ErrorKindUnmanaged {
		t.Fatalf("ErrorKind 应为 %q，实得 %q", ErrorKindUnmanaged, r.ErrorKind)
	}
	if hits != 0 {
		t.Fatalf("被拒的执行不得触达动作体，实得 %d 次", hits)
	}
	if !strings.Contains(r.Error, "判定环") {
		t.Fatalf("拒绝理由应点名缺判定环，实得 %q", r.Error)
	}
}

// TestRunAllowsPureComputationWithoutStages 钉住另一半：纯计算程序
// （不装配任何绑定）不需要判定环，照常执行。
//
// 这条是「按能力判」而不是「按形式判」：没有绑定就没有可绕过的判定，
// 把它也堵上只会逼宿主装一条毫无作用的环。
func TestRunAllowsPureComputationWithoutStages(t *testing.T) {
	r := Run(context.Background(), `return 1 + 1`, nil, Options{})
	if !r.OK {
		t.Fatalf("纯计算程序不该被拒，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if r.Value != "2" {
		t.Fatalf("结果应为 2，实得 %s", r.Value)
	}
}

// TestRunAllowsExplicitUnmanagedEscape 钉住逃逸口：显式声明 AllowUnmanaged
// 时放行——但**必须显式**，沉默不放行。
func TestRunAllowsExplicitUnmanagedEscape(t *testing.T) {
	hits := 0
	bind := Binding{Name: "t", Call: func(context.Context, string) (string, error) {
		hits++
		return "x", nil
	}}
	r := Run(context.Background(), `return host.t("x")`, []Binding{bind}, Options{AllowUnmanaged: true})
	if !r.OK {
		t.Fatalf("显式声明逃逸口后应放行，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if hits != 1 {
		t.Fatalf("动作体应被调用 1 次，实得 %d", hits)
	}
}

// TestProviderRefusesUnmanagedExecution 钉住登记簿路径同样 fail-closed：
// 装进 sandbox 登记簿的后端，若没有判定环 ⇒ 执行被拒。
//
// 这一条比裸入口那条更要紧：裸入口已被门禁（harness-sandbox-execution-path）
// 拦在生产代码之外，而登记簿路径是**正规执行路径**——它当时是敞开的。
func TestProviderRefusesUnmanagedExecution(t *testing.T) {
	hits := 0
	p := Provider{
		Bindings: []Binding{{Name: "t", Call: func(context.Context, string) (string, error) {
			hits++
			return "x", nil
		}}},
	}
	out, err := p.Exec(context.Background(), sandbox.Request{Program: `return host.t("x")`})
	if err != nil {
		t.Fatalf("失败分类应走 Outcome.Err（Provider 的失败形态），实得 err=%v", err)
	}
	if out.OK {
		t.Fatal("登记簿路径同样必须拒绝未受管的执行")
	}
	if out.Err == nil {
		t.Fatal("被拒的执行必须带失败分类（Outcome.Err），实得 nil")
	}
	f, ok := out.Err.(*sandbox.Failure)
	if !ok {
		t.Fatalf("失败分类应是 *sandbox.Failure，实得 %T", out.Err)
	}
	// sandbox.Failure.Kind 统一走 toolfail 词表（issue #151）：
	// kindForErrorKind(unmanaged) = unknown（不可分类 = 不可重试，保守侧）；
	// 诊断口径 unmanaged 留在 Detail.Result.ErrorKind。
	if f.Kind != kindForErrorKind(ErrorKindUnmanaged) {
		t.Fatalf("失败分类应为 %q（toolfail 词表），实得 %q", kindForErrorKind(ErrorKindUnmanaged), f.Kind)
	}
	if hits != 0 {
		t.Fatalf("被拒的执行不得触达动作体，实得 %d 次", hits)
	}
}

// TestSubprocessProviderRefusesUnmanagedExecution 钉住子进程后端同样 fail-closed。
//
// 判在**起子进程之前**：子进程起来之后，工具调用的判定在父侧做——父侧此刻
// 不判，这次执行就已经把「无人判定」变成既成事实。
func TestSubprocessProviderRefusesUnmanagedExecution(t *testing.T) {
	p := NewSubprocessProvider(SubprocessOptions{
		Program:  `return await host.echo("hi");`,
		Bindings: []Binding{{Name: "echo", Call: func(context.Context, string) (string, error) { return "OUT", nil }}},
	})
	_, err := p.Exec(context.Background(), sandbox.Request{})
	if err == nil {
		t.Fatal("子进程后端同样必须拒绝未受管的执行")
	}
	var f *sandbox.Failure
	if !asFailure(err, &f) {
		t.Fatalf("失败应以 sandbox.Failure 形态返回，实得 %T", err)
	}
	// 与进程内 Provider 同词表（issue #151）：unmanaged → unknown。
	if f.Kind != kindForErrorKind(ErrorKindUnmanaged) {
		t.Fatalf("失败分类应为 %q（toolfail 词表），实得 %q", kindForErrorKind(ErrorKindUnmanaged), f.Kind)
	}
}

// asFailure 把错误取成 *sandbox.Failure（errors.As 的窄化封装）。
func asFailure(err error, target **sandbox.Failure) bool {
	return errors.As(err, target)
}
