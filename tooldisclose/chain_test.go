package tooldisclose

import (
	"context"
	"testing"

	"github.com/oagentkit/guardx/invoke"
	"github.com/oagentkit/toolgate/policy"
)

// 本文件是**跨包协同**的集成测试：披露门（harness/tooldisclose）与审计门
//（toolgate/policy）装进同一条 invoke 链，验证二者协同时的结论。
//
// 单包各自的正确性由各自的用例钉；这里钉的是「协同起来之后仍然绕不过去」。

// chain 装一条两环链，返回执行体触达次数的取指器。
func chain(t *testing.T, cat *Catalog, g *policy.Gate, op policy.Op) (*invoke.Chain, *int) {
	t.Helper()
	hits := 0
	c, err := invoke.NewChain(
		func(context.Context, invoke.Call) (invoke.Result, error) {
			hits++
			return invoke.Result{Text: "ok"}, nil
		},
		[]invoke.Stage{Stage(cat), policy.Stage(g, op, nil)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return c, &hits
}

func catalogOf(names ...string) *Catalog {
	tools := make([]Tool, 0, len(names))
	for _, n := range names {
		tools = append(tools, Tool{Name: n, Description: n + " 的说明"})
	}
	return NewCatalog(tools)
}

// TestChainBothGatesPass 已加载 + 只读：两环都放行，执行体触达。
func TestChainBothGatesPass(t *testing.T) {
	cat := catalogOf("read_logs")
	cat.Load("read_logs")
	c, hits := chain(t, cat, policy.NewGate(nil, nil), policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto})

	if _, err := c.Do(context.Background(), invoke.Call{Name: "read_logs"}); err != nil {
		t.Fatalf("两环应放行，实得 %v", err)
	}
	if *hits != 1 {
		t.Fatalf("应触达执行体一次，实得 %d", *hits)
	}
}

// TestChainNotLoadedIsRetryable 未取回 schema → 披露门拒绝但**可重试**。
//
// 这是本包二分里最容易被抹掉的一条：链路一「顺畅」，就有人把可重试也一并抹平，
// 于是模型为取 schema 得多绕一圈，渐进披露的收益当场归零。
func TestChainNotLoadedIsRetryable(t *testing.T) {
	cat := catalogOf("read_logs") // 未 Load
	c, hits := chain(t, cat, policy.NewGate(nil, nil), policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto})

	_, err := c.Do(context.Background(), invoke.Call{Name: "read_logs"})
	if err == nil {
		t.Fatal("未取回 schema 应拒绝")
	}
	if !invoke.Retryable(err) {
		t.Fatal("目录内未取回应判为可重试（Check 已激活，重试即可成）")
	}
	if *hits != 0 {
		t.Fatalf("被拒调用不得触达执行体，实得 %d 次", *hits)
	}
	// 重试即可成——这是「可重试」的实际含义，不是标签。
	if _, err := c.Do(context.Background(), invoke.Call{Name: "read_logs"}); err != nil {
		t.Fatalf("重试应成功（Check 已激活），实得 %v", err)
	}
	if *hits != 1 {
		t.Fatalf("重试成功后应触达执行体一次，实得 %d", *hits)
	}
}

// TestChainNotInCatalogNotRetryable 不在目录 → 拒绝且不可重试。
//
// 重试不会让不存在的东西出现；判成可重试就是把一次策略否定变成退避风暴。
func TestChainNotInCatalogNotRetryable(t *testing.T) {
	cat := catalogOf("read_logs")
	c, hits := chain(t, cat, policy.NewGate(nil, nil), policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto})

	_, err := c.Do(context.Background(), invoke.Call{Name: "unknown_tool"})
	if err == nil {
		t.Fatal("不在目录的工具应拒绝")
	}
	if invoke.Retryable(err) {
		t.Fatal("不在目录应判为不可重试")
	}
	if *hits != 0 {
		t.Fatalf("被拒调用不得触达执行体，实得 %d 次", *hits)
	}
}

// TestChainAuditGateBlocksAfterDisclosurePass 披露门放行但审计门拒绝时，
// 整体仍必须拒绝——证明第二环不是摆设，且披露门放行不构成任何豁免。
func TestChainAuditGateBlocksAfterDisclosurePass(t *testing.T) {
	cat := catalogOf("restart_service")
	cat.Load("restart_service")
	// 审计门装配为变异工具必拒；披露门已放行。
	c, hits := chain(t, cat, policy.NewGate(nil, nil),
		policy.Op{Type: policy.OpToolCall, Mode: policy.ModeFull, Mutating: true})

	_, err := c.Do(context.Background(), invoke.Call{Name: "restart_service"})
	if err == nil {
		t.Fatal("审计门拒绝时整体必须拒绝")
	}
	if invoke.Retryable(err) {
		t.Fatal("策略否定不可重试")
	}
	if *hits != 0 {
		t.Fatalf("被拒调用不得触达执行体，实得 %d 次", *hits)
	}
}

// TestChainNilCatalogFailsClosed 未装配目录应拒绝，不得因缺配置而放行。
//
// 「没配置就当放行」是这个面最危险的默认：空目录会让全部门禁消失，且无任何信号。
func TestChainNilCatalogFailsClosed(t *testing.T) {
	c, hits := chain(t, nil, policy.NewGate(nil, nil), policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto})

	_, err := c.Do(context.Background(), invoke.Call{Name: "read_logs"})
	if err == nil {
		t.Fatal("未装配目录应 fail-closed 拒绝")
	}
	if invoke.Retryable(err) {
		t.Fatal("不在目录不可重试")
	}
	if *hits != 0 {
		t.Fatalf("不得触达执行体，实得 %d 次", *hits)
	}
}

// TestChainRecordsEveryOutcome 任何结局都恰好留一条痕，跨两环同样成立。
//
// 审计面不对称是本次改动的起因之一：能力更强的路径审计更好，会让人在评审时
// 高估整体覆盖。故此处验的是「无论被哪一环拒，都有且只有一条记录」。
func TestChainRecordsEveryOutcome(t *testing.T) {
	cases := []struct {
		name    string
		setup   func() *Catalog
		op      policy.Op
		allowed bool
	}{
		{"两环放行", func() *Catalog {
			c := catalogOf("read_logs")
			c.Load("read_logs")
			return c
		}, policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}, true},
		{"披露门拒", func() *Catalog { return catalogOf("read_logs") },
			policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}, false},
		{"审计门拒", func() *Catalog {
			c := catalogOf("restart_service")
			c.Load("restart_service")
			return c
		}, policy.Op{Type: policy.OpToolCall, Mode: policy.ModeFull, Mutating: true}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var recs []invoke.Record
			hits := 0
			c, err := invoke.NewChain(
				func(context.Context, invoke.Call) (invoke.Result, error) {
					hits++
					return invoke.Result{Text: "ok"}, nil
				},
				[]invoke.Stage{Stage(tc.setup()), policy.Stage(policy.NewGate(nil, nil), tc.op, nil)},
				invoke.WithRecorder(func(r invoke.Record) { recs = append(recs, r) }),
			)
			if err != nil {
				t.Fatal(err)
			}

			_, _ = c.Do(context.Background(), invoke.Call{Name: "read_logs"})

			if len(recs) != 1 {
				t.Fatalf("每次调用应恰好一条留痕，实得 %d 条", len(recs))
			}
			if recs[0].Allowed != tc.allowed {
				t.Fatalf("Allowed 应为 %v，实得 %v", tc.allowed, recs[0].Allowed)
			}
			if !tc.allowed && recs[0].Err == nil {
				t.Fatal("失败留痕必须带 Err")
			}
		})
	}
}

// TestChainStagesVisibleForAudit 两环都必须能在链上被点名。
//
// 留痕要能回答「是哪一环拒的」；链上看不到环节名，这个问题就没有答案。
func TestChainStagesVisibleForAudit(t *testing.T) {
	cat := catalogOf("read_logs")
	c, _ := chain(t, cat, policy.NewGate(nil, nil), policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto})

	got := c.Stages()
	if len(got) != 2 {
		t.Fatalf("链上应有两环，实得 %d", len(got))
	}
	if got[0] != StageName {
		t.Fatalf("首环应为 %q，实得 %q", StageName, got[0])
	}
	if got[1] == "" || got[1] == StageName {
		t.Fatalf("次环应有独立名字，实得 %q", got[1])
	}
}
