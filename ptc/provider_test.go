// ptc 适配器单测：把本包的执行面接进 sandbox.Provider 协议。
//
// # 为什么这些用例不在 sandbox 包里
//
// 适配器要调本包的 Run（真实 goja 执行），而 sandbox 包不认识任何后端。
// 把用例放这边，钉的是**整条链**：登记 → 按要求档位选后端 → 执行 → 结果形状。
// 只测 sandbox 侧（协议对不对）与只测 ptc 侧（程序跑不跑得通）都钉不住
// 「两侧漂移时两边都绿」那个夹缝。
package ptc

import (
	"context"
	"testing"

	"github.com/yi-nology/agent-harness/sandbox"
)

func TestProviderDeclaresProcessLevel(t *testing.T) {
	p := Provider{}
	if p.ID() != ProviderID {
		t.Fatalf("后端标识不符: %q", p.ID())
	}
	if p.Level() != sandbox.LevelProcess {
		t.Fatalf("档位应为 process，实得 %v", p.Level())
	}
}

func TestProviderExecRunsProgram(t *testing.T) {
	bind := Binding{Name: "echo", Call: func(_ context.Context, args string) (string, error) {
		return "OUT<" + args + ">", nil
	}}
	reg := sandbox.NewRegistry()
	if err := reg.Register(Provider{Bindings: []Binding{bind}}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	out, err := sandbox.Execute(context.Background(), reg, sandbox.LevelProcess,
		sandbox.Request{Program: `const v = await host.echo("x"); return v`})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !out.OK {
		t.Fatalf("执行应当成功: %+v", out)
	}
	if out.Value != `"OUT<x>"` {
		t.Fatalf("结果文本不符: %q", out.Value)
	}
	// Detail 载本包的执行明细，调用方按类型断言取用。
	res, ok := out.Detail.(Result)
	if !ok {
		t.Fatalf("Detail 应为本包 Result，实得 %T", out.Detail)
	}
	if res.Calls != 1 {
		t.Fatalf("绑定应被调用一次，实得 %d", res.Calls)
	}
}

// 失败是结果字段不是异常，且失败分类要能穿过适配器到达调用方。
func TestProviderExecFailureCarriesKind(t *testing.T) {
	reg := sandbox.NewRegistry()
	if err := reg.Register(Provider{}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	out, err := sandbox.Execute(context.Background(), reg, sandbox.LevelProcess,
		sandbox.Request{Program: `return (`})
	if err != nil {
		t.Fatalf("执行层不应抛异常: %v", err)
	}
	if out.OK {
		t.Fatal("语法错误的程序不应成功")
	}
	f, ok := out.Err.(*sandbox.Failure)
	if !ok {
		t.Fatalf("失败应是 sandbox.Failure，实得 %T", out.Err)
	}
	if f.Kind != "syntax" {
		t.Fatalf("失败分类应为 syntax，实得 %q", f.Kind)
	}
}

// 要求高于本包提供的档位时，登记簿必须拒绝执行而不是悄悄跑在弱档位上。
func TestRegistryRefusesStrongerLevelThanProvided(t *testing.T) {
	reg := sandbox.NewRegistry()
	if err := reg.Register(Provider{}); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if _, err := sandbox.Execute(context.Background(), reg, sandbox.LevelOS,
		sandbox.Request{Program: `return 1`}); err == nil {
		t.Fatal("要求 os 档但只有 process 后端，应当拒绝执行")
	}
}
