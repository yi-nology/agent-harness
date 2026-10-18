package tooldisclose

// 门禁指引（模型侧）的用例。夹具用合成工具名，不带领域味。
//
// 与 gate_test.go 分文件：那里钉的是机器侧契约（哨兵二分 / 重试性 / 激活副作用），
// 这里钉的是模型侧契约（拒绝时模型拿到的话能不能指导它做对下一步）。
// 两组用例都必须存在——机器侧全对而模型侧缺位，是这个包最容易出现的一种半成品。

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/toolgate/invoke"
	"github.com/oekit/pkg/errors"
)

// TestGuidanceOrdersModelToRetryOnlyWhenRetryWillSucceed 门禁的两类结局要求模型
// 做的动作恰好相反，因此指引必须把这件事说死，不能只说「被拒了」。
//
// 这条是本组用例的核心：指引若缺位，模型在「不在目录」这一类上继续重试，
// 就是把一次策略否定拖成一段空转——而退避层对此无能为力（它只看 Retryable）。
func TestGuidanceOrdersModelToRetryOnlyWhenRetryWillSucceed(t *testing.T) {
	c := catalogOf("alpha_lookup")
	g := NewGate(c)

	err := g.Check("alpha_lookup")
	if err == nil {
		t.Fatal("未加载的调用必须被拒")
	}
	loaded := Guidance(err)
	if !strings.Contains(loaded, "重新调用同一工具") {
		t.Fatalf("未加载已激活，指引必须让模型原样重试，实得 %q", loaded)
	}
	if strings.Contains(loaded, "不要重试") {
		t.Fatalf("可恢复的结局不得出现「不要重试」，实得 %q", loaded)
	}

	err = g.Check("ghost_tool")
	if err == nil {
		t.Fatal("不在目录的调用必须被拒")
	}
	ghost := Guidance(err)
	if !strings.Contains(ghost, "不要重试") {
		t.Fatalf("不在目录不可恢复，指引必须明说不要重试，实得 %q", ghost)
	}

	// 反证：两句话给出的指令必须真的相反，而不是同一句话换个说法——
	// 若将来有人把指引写成模板套话，这条会立刻变红。
	if loaded == ghost {
		t.Fatal("两类结局的指引必须不同，否则指引没有承载二分")
	}
}

// TestGuidanceCarriesToolNameForAudit 指引要同时服务模型与留痕两头的需要，
// 所以工具名不能因为「写给模型看」而丢掉。
func TestGuidanceCarriesToolNameForAudit(t *testing.T) {
	c := catalogOf("alpha_lookup", "beta_export")
	g := NewGate(c)

	for _, name := range []string{"alpha_lookup", "beta_export"} {
		err := g.Check(name)
		if err == nil {
			t.Fatalf("%s 未加载时应被拒", name)
		}
		if !strings.Contains(Guidance(err), name) {
			t.Fatalf("指引应带出工具名 %q（审计要回答「哪次调用被拒」），实得 %q", name, Guidance(err))
		}
	}
}

// TestGuidanceDoesNotFabricate 非门禁错误返回空串，本包不替别人的失败编措辞。
func TestGuidanceDoesNotFabricate(t *testing.T) {
	if got := Guidance(errors.New("srv-beta 连接被重置")); got != "" {
		t.Fatalf("非门禁错误应返回空串，实得 %q", got)
	}
	if got := Guidance(nil); got != "" {
		t.Fatalf("nil 应返回空串，实得 %q", got)
	}
	// 认不出的门禁结局也不能交白卷：空指引会让模型看到一条没有内容的拒绝。
	ge := newGateError("alpha_lookup", errors.New("某种没登记过的结局"), false, false)
	if got := ge.Guidance(); strings.TrimSpace(got) == "" {
		t.Fatal("未登记的结局也应给出中性指引，不能是空串")
	}
	var nilGE *GateError
	if got := nilGE.Guidance(); got != "" {
		t.Fatalf("nil 接收者应返回空串，实得 %q", got)
	}
}

// TestStageReasonCarriesGuidance 指引必须有**确定消费方**。
//
// 链上 Verdict.Reason 是宿主把这环结论交出去的唯一出口，绝大多数宿主会把它
// 原样送进模型可见的失败消息。若这里回的是裸哨兵文案，Guidance 就退化成
// 「建了没人读」的死代码——正是我们批评过的失败形态。
func TestStageReasonCarriesGuidance(t *testing.T) {
	cat := catalogOf("alpha_lookup")
	st := Stage(cat)

	v, err := st.Check(context.Background(), invoke.Call{Name: "alpha_lookup"})
	if err != nil {
		t.Fatalf("门禁环不应返回 error（约定是 (Verdict, nil)），实得 %v", err)
	}
	if v.Allow {
		t.Fatal("未加载的调用不得放行")
	}
	if !v.Retryable {
		t.Fatal("未加载结局应标为可重试")
	}
	if !strings.Contains(v.Reason, "重新调用同一工具") {
		t.Fatalf("链上 Reason 应是面向模型的指引，实得 %q", v.Reason)
	}
	if !strings.Contains(v.Reason, "alpha_lookup") {
		t.Fatalf("链上 Reason 应保留工具名供留痕，实得 %q", v.Reason)
	}

	// 第二类结局：不可重试，且指引与第一类相反。
	v2, err := st.Check(context.Background(), invoke.Call{Name: "ghost_tool"})
	if err != nil {
		t.Fatalf("门禁环不应返回 error，实得 %v", err)
	}
	if v2.Retryable {
		t.Fatal("不在目录的结局不得标为可重试")
	}
	if !strings.Contains(v2.Reason, "不要重试") {
		t.Fatalf("不可恢复结局的指引必须明说不要重试，实得 %q", v2.Reason)
	}

	// 已激活后同一工具必须放行（自愈回归：指引改造不得碰这条既有行为）。
	v3, err := st.Check(context.Background(), invoke.Call{Name: "alpha_lookup"})
	if err != nil || !v3.Allow {
		t.Fatalf("首次拒绝已激活该工具，重试必须放行；实得 allow=%v err=%v", v3.Allow, err)
	}
}

// TestStageNilCatalogStillRejectsWithGuidance 未装配目录时 fail-closed，
// 且拒绝也要给出指引——否则宿主漏装配目录时，模型拿到的是一句没有出处的拒绝。
func TestStageNilCatalogStillRejectsWithGuidance(t *testing.T) {
	st := Stage(nil)
	v, err := st.Check(context.Background(), invoke.Call{Name: "alpha_lookup"})
	if err != nil {
		t.Fatalf("门禁环不应返回 error，实得 %v", err)
	}
	if v.Allow {
		t.Fatal("未装配目录时不得放行（fail-closed）")
	}
	if v.Retryable {
		t.Fatal("未装配目录属不可恢复，不应标为可重试")
	}
	if strings.TrimSpace(v.Reason) == "" {
		t.Fatal("fail-closed 路径同样要有可读的 Reason")
	}
}
