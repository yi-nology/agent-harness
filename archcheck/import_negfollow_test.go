// import_negfollow_test.go —— import 规则上的 negative_follow 豁免。
//
// # 为什么这条用例必须存在
//
// 同一个 `negative_follow` 字段在两个分支语义不同是一个**静默失效**：
// 文本分支认它、import 分支不认。policy 作者按字段名的自然用法（import 规则配
// import 豁免）写负向前置，实际一条都豁免不掉——**假红**：该放行的 import 照样
// 报违规，而报错文案里没有任何「你的豁免没生效」的线索。
//
// 失败形态是「门禁变红」，但红得没有道理：维护者的第一反应是「规则太严了」，
// 于是把豁免删掉或改用更宽的表达式——真正的缺陷（豁免没生效）留在原地。
//
// 判据面：豁免锚点写在**被匹配的那段文本**上。文本分支是源码行，import 分支是
// import 路径。

package archcheck

import "testing"

// TestImportRuleHonorsNegativeFollow import 规则必须认 negative_follow 豁免。
//
// 用例逐字复刻 issue #11 的复现步骤：一条 import 规则 + 一条路径片段豁免 +
// 两个 import（一个该豁免、一个不该）。
func TestImportRuleHonorsNegativeFollow(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		// 该豁免：路径以 engine/ 开头、紧接着是 statemachine
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/statemachine\"\n",
		// 不该豁免：同样命中规则，但路径片段不同
		"internal/demo/b.go": "package demo\n\nimport _ \"example.com/demo/internal/service/orders\"\n",
	})
	r := Rule{
		ID:           "demo",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/(engine|service)`,
		NegFollow: []NegFollow{
			{Anchor: "example.com/demo/internal/engine/", ForbidFollow: "statemachine"},
		},
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("应恰好剩一处违反（b.go 的 import 未被豁免），实得 %d 处: %+v", len(vs), vs)
	}
	if vs[0].Path != "internal/demo/b.go" {
		t.Fatalf("未被豁免的应是 b.go，实得 %s", vs[0].Path)
	}
}

// TestImportRuleNegativeFollowDoesNotOverExempt 豁免不得扩大成「整条规则都放行」。
//
// 豁免的判据是「anchor 紧跟 forbid_follow」——是一处**具体**的豁免。
// 若实现把它做成「路径里含 anchor 就放行」，那么同一条规则下的其他 import
// 会被一并放掉，规则形同虚设。
func TestImportRuleNegativeFollowDoesNotOverExempt(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		// 含 anchor，但 anchor 之后不是 forbid_follow → 不得豁免
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/orders\"\n",
		// 连 anchor 都不含 → 不得豁免
		"internal/demo/b.go": "package demo\n\nimport _ \"example.com/demo/internal/service/orders\"\n",
	})
	r := Rule{
		ID:           "demo",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/(engine|service)`,
		NegFollow: []NegFollow{
			{Anchor: "example.com/demo/internal/engine/", ForbidFollow: "statemachine"},
		},
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("豁免只覆盖 anchor+forbid_follow 的那一处，其余两处必须都报，实得 %d 处: %+v", len(vs), vs)
	}
}
