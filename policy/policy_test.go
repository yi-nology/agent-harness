package policy

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// 本包是到 oagentkit/toolgate/policy 的**类型别名转发层**：它的职责不是实现审计语义
// （那是 kit 的事），而是保证「转发不漂移」——kit 升级后别名、常量、转发函数
// 仍指向同一套东西。故本测试钉的是**接线契约**，不是 kit 自身行为。

// ── 1. 线值钉死 ──
// Mode / Verdict / OpType / Decider 的字面量会跨进程边界落进
// _shared/operation_policy.yaml 与宿主存储列。kit 侧一旦改字面量，本包必须
// 跟着断——否则宿主存量数据静默失效，且没有任何编译错误提醒。
func TestWireValues(t *testing.T) {
	// 显式 string() 转换：顺带钉住「这些常量是字符串底层的命名类型」——
	// 若 kit 哪天把它们改成结构体，这行直接编译失败，而不是静默换掉线值。
	cases := []struct{ name, got, want string }{
		{"ModeConfirm", string(ModeConfirm), "confirm"},
		{"ModeAuto", string(ModeAuto), "auto"},
		{"ModePlan", string(ModePlan), "plan"},
		{"ModeFull", string(ModeFull), "full"},
		{"VerdictAuto", string(VerdictAuto), "auto_proceed"},
		{"VerdictNeedHuman", string(VerdictNeedHuman), "need_human"},
		{"VerdictDeny", string(VerdictDeny), "deny"},
		{"VerdictPlanOnly", string(VerdictPlanOnly), "plan_only"},
		{"VerdictUnknown", string(VerdictUnknown), "unknown"},
		{"OpToolCall", string(OpToolCall), "tool_call"},
		{"OpExecuteStep", string(OpExecuteStep), "execute_step"},
		{"OpPlanSubmit", string(OpPlanSubmit), "plan_submit"},
		{"DeciderRule", string(DeciderRule), "rule"},
		{"DeciderLLM", string(DeciderLLM), "llm"},
		{"DeciderHuman", string(DeciderHuman), "human"},
		{"DeciderSystem", string(DeciderSystem), "system_policy"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s 线值漂移: got %q want %q（kit 改字面量会让宿主存量策略静默失效）", c.name, c.got, c.want)
		}
	}
}

// ── 2. 转发函数确实落到 kit ──
// 注意 Normalize 是**精确匹配**（非大小写不敏感）：脏值一律回落 confirm。
// 这不是缺陷而是纵深兜底——审计门不允许因模式脏值放大权限，故此处钉住
// 「大小写脏值不得意外升档」这条安全语义。
func TestNormalizeAndValidMode(t *testing.T) {
	if got := Normalize("confirm"); got != ModeConfirm {
		t.Errorf("Normalize(confirm) = %v, want ModeConfirm", got)
	}
	if got := Normalize("full"); got != ModeFull {
		t.Errorf("Normalize(full) = %v, want ModeFull", got)
	}
	// 脏值（大小写不符/拼错/空串）必须回落最严档，不得升权。
	for _, dirty := range []string{"", "AUTO", "Full", "nonsense"} {
		if got := Normalize(dirty); got != ModeConfirm {
			t.Errorf("脏值 %q 应回落 confirm, got %v（不得因脏值升权）", dirty, got)
		}
		if ValidMode(dirty) {
			t.Errorf("ValidMode(%q) 应为 false（API 入口应 400 拦截）", dirty)
		}
	}
	if !ValidMode("full") || !ValidMode("auto") || !ValidMode("plan") || !ValidMode("confirm") {
		t.Error("四个合法档位应全部通过 ValidMode")
	}
}

// ── 3. 外置策略缺文件必须回落内置基座，不得报错 ──
// 解析失败即拒是审计门的安全语义，但**文件不存在**是部署常态（首次拉起），
// 两者不能混为一谈：缺文件回内置基座，坏内容才报错。
func TestLoadOverridesMissingFile(t *testing.T) {
	p, err := LoadOverrides(fstest.MapFS{})
	if err != nil {
		t.Fatalf("缺文件应回落内置基座而非报错: %v", err)
	}
	if p == nil {
		t.Fatal("缺文件也应返回非 nil 基座策略")
	}
}

// fakeTool 最小可调用工具替身。
type fakeTool struct{ ran bool }

func (f *fakeTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "probe"}, nil
}

func (f *fakeTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	f.ran = true
	return "ok", nil
}

// ── 4. nil gate 原样返回（未装配审计门 = 存量行为）──
func TestWithAuditGateNilPassthrough(t *testing.T) {
	inner := &fakeTool{}
	got := WithAuditGate(nil, Op{Type: OpToolCall, Mode: ModeConfirm}, nil, inner)
	if got != tool.InvokableTool(inner) {
		t.Error("gate 为 nil 时应原样返回同一工具（不得套壳改变行为）")
	}
}

// ── 5. onAudit 回调必须被透传触发 ──
// 这是转发层最容易断的一根线：审计回调漏传会让宿主侧**审计留痕静默消失**，
// 而工具调用照常成功——不报错、不告警，只少了记录。
func TestWithAuditGateFiresAudit(t *testing.T) {
	inner := &fakeTool{}
	var seen []Decision
	g := NewGate(nil, nil)
	gated := WithAuditGate(g, Op{Type: OpToolCall, Mode: ModeConfirm, Tool: "probe"},
		func(_ Op, d Decision) { seen = append(seen, d) }, inner)

	if _, err := gated.InvokableRun(context.Background(), "{}"); err != nil {
		t.Fatalf("放行路径不应报错: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("审计回调应恰好触发一次, got %d", len(seen))
	}
	if seen[0].Decider != DeciderRule {
		t.Errorf("审计决策者应为 rule, got %q", seen[0].Decider)
	}
}
