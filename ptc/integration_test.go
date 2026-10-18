// 端到端验证：从工具 schema 派生的调用形态，ptc 真的认。
//
// 单独验证两边是不够的：派生端与解析端各有一套「什么算合法标识符」的判据，
// 两处若不一致，就会出现「这里派生得出、那边解析不了」的夹缝——宿主照着 schema
// 派生，装配期却被拒，而两边都看不出原因。
//
// 用**外部测试包**（package ptc_test）正是为了这条：它可以同时 import 两个包，
// 而生产代码里 ptc 与 tooldisclose 互不依赖（policy 两者 requires 均为空），
// 依赖方向不受影响；deps 门禁也只判生产代码。

package ptc_test

import (
	"context"
	"strings"
	"testing"

	"github.com/oagentkit/toolgate/invoke"
	"github.com/oagentkit/toolgate/policy"
	"github.com/yi-nology/agent-harness/ptc"
	"github.com/yi-nology/agent-harness/tooldisclose"
)

// TestDerivedSignatureRunsInPTC 派生 → 装配 → 执行，一条龙走通。
func TestDerivedSignatureRunsInPTC(t *testing.T) {
	tl := tooldisclose.Tool{
		Name: "get_logs",
		Schema: tooldisclose.Schema{
			Type:       "object",
			Required:   []string{"host"},
			Properties: map[string]tooldisclose.Param{"host": {}, "count": {}, "since": {}},
		},
	}
	sig, err := tooldisclose.SignatureOf(tl)
	if err != nil {
		t.Fatal(err)
	}

	var got string
	bind := ptc.Binding{
		Name:      tl.Name,
		Signature: sig,
		Call: func(_ context.Context, argsJSON string) (string, error) {
			got = argsJSON
			return "日志片段", nil
		},
	}

	// 只给必填：可选槽位不应出现。
	r := ptc.Run(context.Background(), `return host.get_logs("h1")`, []ptc.Binding{bind}, ptc.Options{})
	if !r.OK {
		t.Fatalf("派生形态应可用，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(got, `"host":"h1"`) || strings.Contains(got, "count") {
		t.Fatalf("只传必填时 JSON 应只有 host，实得 %s", got)
	}

	// 给到中间那个可选：应按签名序落到正确键上。
	r = ptc.Run(context.Background(), `return host.get_logs("h1", 30)`, []ptc.Binding{bind}, ptc.Options{})
	if !r.OK {
		t.Fatalf("实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(got, `"count":30`) {
		t.Fatalf("第二个实参应对应签名里的第二个形参 count，实得 %s", got)
	}

	// 少传必填：必须被拦下，且不得触达绑定。
	hits := 0
	bind.Call = func(context.Context, string) (string, error) { hits++; return "x", nil }
	r = ptc.Run(context.Background(), `return host.get_logs()`, []ptc.Binding{bind}, ptc.Options{})
	if r.OK {
		t.Fatal("必填缺失应被拒")
	}
	if hits != 0 {
		t.Fatalf("被拒的调用不得触达绑定，实得 %d 次", hits)
	}
}

// TestDerivedSignatureMatchesBoundName 派生出的函数名必须与绑定名一致。
//
// 两处任一不一致都会在装配期被拒——但那条错误指向「调用形态」，而真正的病因是
// 工具名里的非法字符在净化后变了形状。钉这条能让人一眼看出是哪边的问题。
func TestDerivedSignatureMatchesBoundName(t *testing.T) {
	tl := tooldisclose.Tool{
		Name:   "get-logs",
		Schema: tooldisclose.Schema{Type: "object", Required: []string{"h"}, Properties: map[string]tooldisclose.Param{"h": {}}},
	}
	sig, err := tooldisclose.SignatureOf(tl)
	if err != nil {
		t.Fatal(err)
	}
	r := ptc.Run(context.Background(), `return 1`, []ptc.Binding{{
		Name: tl.Name, Signature: sig,
		Call: func(context.Context, string) (string, error) { return "x", nil },
	}}, ptc.Options{})
	if r.OK || r.ErrorKind != "binding_signature" {
		t.Fatalf("派生态函数名与净化后的绑定名不一致时应被拒，实得 OK=%v kind=%q", r.OK, r.ErrorKind)
	}
}

// ── 真实门禁在程序内生效 ──
//
// 上一条修复（Options.Stages）只证明了**机制**存在。这一组用**真的**门禁证明
// 「此前被绕过的那些门，现在确实在程序里生效」——用桩环证明不了这件事：桩环只能
// 说明环被调用了，说明不了它拦的是哪一种拒绝、拒绝理由能不能传回模型眼前。

// TestRealGatesApplyInsideProgram 披露门 + 审计门在程序内真的拦得住。
func TestRealGatesApplyInsideProgram(t *testing.T) {
	cat := tooldisclose.NewCatalog([]tooldisclose.Tool{
		{Name: "read_logs", Schema: tooldisclose.Schema{
			Type: "object", Required: []string{"host"},
			Properties: map[string]tooldisclose.Param{"host": {}},
		}},
		{Name: "restart_service"},
	})
	// read_logs 已取回 schema；restart_service 保持未取回。
	if _, ok := cat.Load("read_logs"); !ok {
		t.Fatal("read_logs 应已加载")
	}
	audit := policy.NewGate(nil, nil)
	stageForToolCall := policy.Op{Type: policy.OpToolCall, Mode: policy.ModeAuto}

	bind := ptc.Binding{
		Name:      "read_logs",
		Signature: mustSig(t, cat, "read_logs"),
		Call:      func(context.Context, string) (string, error) { return "日志片段", nil },
	}
	stages := []invoke.Stage{
		tooldisclose.Stage(cat),
		policy.Stage(audit, stageForToolCall, nil),
	}

	// 正常路径：已加载 + 只读 → 放行，程序拿到工具输出。
	r := ptc.Run(context.Background(), `return host.read_logs("h1")`, []ptc.Binding{bind}, ptc.Options{Stages: stages})
	if !r.OK {
		t.Fatalf("已加载 + 只读应放行，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if r.Value != `"日志片段"` {
		t.Fatalf("程序应拿到工具输出，实得 %s", r.Value)
	}

	// 披露门：restart_service 未取回 schema → 拒绝，且**可重试**（模型可直接重试）。
	const prog = `try { return host.restart_service("h1") } catch (e) { return "拒:" + e.message }`
	r = ptc.Run(context.Background(), prog, []ptc.Binding{{
		Name: "restart_service",
		Call: func(context.Context, string) (string, error) { return "已重启", nil },
	}}, ptc.Options{Stages: stages})
	if !r.OK {
		t.Fatalf("try/catch 后应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(r.Value, "拒:") || !strings.Contains(r.Value, "restart_service") {
		t.Fatalf("未取回 schema 的工具应被披露门拦下并把理由传给程序，实得 %s", r.Value)
	}
	if !strings.Contains(r.Value, tooldisclose.StageName) {
		t.Fatalf("拒绝理由应点名是哪一环拒的（%s），实得 %s", tooldisclose.StageName, r.Value)
	}
}

// TestRealAuditGateBlocksInsideProgram 审计门在程序内拦得住变异工具。
//
// 这条是「门禁不是摆设」的核心：披露门只管「工具取没取到 schema」，管不了
// 「这个工具这次调用该不该放行」。后者只有审计门判，而它此前在程序内根本不跑。
func TestRealAuditGateBlocksInsideProgram(t *testing.T) {
	hits := 0
	bind := ptc.Binding{Name: "restart_service", Call: func(context.Context, string) (string, error) {
		hits++
		return "已重启", nil
	}}
	audit := policy.NewGate(nil, nil)
	stages := []invoke.Stage{
		policy.Stage(audit, policy.Op{Type: policy.OpToolCall, Mode: policy.ModeFull, Mutating: true}, nil),
	}

	const prog = `try { return host.restart_service("h1") } catch (e) { return "拒:" + e.message }`
	r := ptc.Run(context.Background(), prog, []ptc.Binding{bind}, ptc.Options{Stages: stages})

	if !r.OK {
		t.Fatalf("try/catch 后应成功，实得 %q / %q", r.ErrorKind, r.Error)
	}
	if !strings.Contains(r.Value, "拒:") {
		t.Fatalf("变异工具应被审计门拦下，实得 %s", r.Value)
	}
	if hits != 0 {
		t.Fatalf("被拒的调用不得触达执行体，实得 %d 次", hits)
	}
	// 审计门不能被 try/catch 绕过成「换个分支继续执行同一个工具」——上面的 hits==0 钉住了。
	if len(r.CallLog) != 1 || r.CallLog[0].Status != ptc.StatusDenied {
		t.Fatalf("留痕应为 denied 且恰好一条，实得 %+v", r.CallLog)
	}
}

func mustSig(t *testing.T, cat *tooldisclose.Catalog, name string) string {
	t.Helper()
	e, ok := cat.Expose(name)
	if !ok {
		t.Fatalf("%s 应在目录内", name)
	}
	sig, err := tooldisclose.SignatureOf(tooldisclose.Tool{Name: e.Name, Description: e.Description, Schema: e.Schema})
	if err != nil {
		t.Fatal(err)
	}
	return sig
}
