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
