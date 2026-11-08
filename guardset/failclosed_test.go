package guardset

import (
	"context"
	"testing"

	"github.com/oagentkit/guardx/invoke"
)

// issue #43：零值 Set 与未装配 Assembly 不得 fail-open（allow-all）——
// New 在构造期拒绝空集合，「没人看着」的形态只剩零值/未装配，二者必须
// 按拒绝收口。
func TestZeroValueSetFailsClosed(t *testing.T) {
	var s Set
	out := s.Evaluate(context.Background(), invoke.Call{Name: "t"})
	if out.Allowed {
		t.Fatal("零值 Set 必须 fail-closed，不得放行")
	}
	if len(out.Denials) == 0 || out.Denials[0].Guard != "guardset" {
		t.Fatalf("拒绝必须可归因到 guardset, got %+v", out.Denials)
	}

	var p *Set // nil receiver：方法调用合法（Evaluate 首行判 nil）
	if p.Evaluate(context.Background(), invoke.Call{Name: "t"}).Allowed {
		t.Fatal("nil Set 必须 fail-closed")
	}
}

func TestUnassembledAssemblyFailsClosed(t *testing.T) {
	var a Assembly // 未 NewAssembly
	out := a.Evaluate(context.Background(), invoke.Call{Name: "t"})
	if out.Allowed {
		t.Fatal("未装配 Assembly 必须 fail-closed，不得放行")
	}

	// Stage() 接进 invoke 链同样拒绝（此前零值装配面 = 全放行链环）
	verdict, err := a.Stage().Check(context.Background(), invoke.Call{Name: "t"})
	if err != nil {
		t.Fatalf("拒绝不得以 error 收口: %v", err)
	}
	if verdict.Allow {
		t.Fatal("未装配 Assembly 的链环必须拒绝")
	}
}
