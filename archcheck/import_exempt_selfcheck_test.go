// import_exempt_selfcheck_test.go —— import_exempt 项的**形状**自校验。
//
// # 为什么这条自校验必须存在
//
// import_exempt 的语义是「放行这一条 import 路径」。若某一项**形状上就不可能
// 匹配**（空串、带尾斜杠、含通配/正则字符），它的效果是「**一条都没放行**」。
//
// 不判的后果是**误诊**：宿主会看到 import 被拒，报错指向 import，
// 真正的问题（policy 里那条豁免登记写错了）没人提示。
//
// 而「policy 写了例外但它不生效」正是本仓纪律里最贵的那一类静默失效——
// 门禁绿着（豁免项只是没起作用），代价却落在宿主侧一次 puzzling 的拒绝上。
//
// # 为什么判「形状」而不是判「当前有没有命中」
//
// 「当前没命中」有两种原因：豁免写错了（真问题），或这条豁免登记的是将来
// 才会用到的包（不是问题）。机器分不清 ⇒ 硬判就是误报 ⇒ 误报会让人学会
// 忽略这条告警，等于把判据关掉。
//
// 形状是**确定性**的：import 路径不会以 `/` 结尾，也不含 `*?()[]|^${}\`。
// 判形状不误报，且恰好覆盖最常见的两类笔误。

package archcheck

import "testing"

// TestSelfCheckCatchesTrailingSlashExempt 豁免项带尾斜杠必须被报出来。
//
// 「`a/b/` 看着像『放行 a/b 这棵树』」是常见直觉写法，而那个语义恰恰是
// 本条能力要消灭的（子树放行 = 静默放宽整棵依赖树）。
// 故本仓**刻意不做**前缀归一——带尾斜杠的项不匹配任何具体 import 路径。
//
// 但「不生效」必须**响亮**：宿主看到的应是「你登记的这条豁免无效」，
// 而不是一条指向 import 的、让人摸不着头脑的拒绝。
func TestSelfCheckCatchesTrailingSlashExempt(t *testing.T) {
	pol := Policy{Rules: []Rule{{
		ID:           "x",
		ForbidImport: `example\.com/demo`,
		ImportExempt: []string{"example.com/demo/internal/engine/"},
	}}}
	vs := selfCheckViolations(t.TempDir(), &pol)
	if !containsText(vs, "尾斜杠") {
		t.Fatalf("带尾斜杠的豁免项必须被自校验报出，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckCatchesPatternExempt 豁免项含通配/正则字符必须被报出来。
//
// 例外的匹配口径是「整条路径相等」，不是正则也不是通配。
// 宿主若照着 forbid_import_regex 的习惯写 `example\.com/demo/.*`，
// 它会**一条都放行不了**——而报错若只指向 import，等于误诊。
func TestSelfCheckCatchesPatternExempt(t *testing.T) {
	pol := Policy{Rules: []Rule{{
		ID:           "x",
		ForbidImport: `example\.com/demo`,
		ImportExempt: []string{`example\.com/demo/.*`},
	}}}
	vs := selfCheckViolations(t.TempDir(), &pol)
	if !containsText(vs, "模式字符") {
		t.Fatalf("含模式字符的豁免项必须被自校验报出，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckCatchesEmptyExempt 豁免项为空串必须被报出来。
//
// 空串是「登记了一条例外」这个动作的**空形态**：它让人以为豁免机制已配好，
// 实际一条都放行不了。
func TestSelfCheckCatchesEmptyExempt(t *testing.T) {
	pol := Policy{Rules: []Rule{{
		ID:           "x",
		ForbidImport: `example\.com/demo`,
		ImportExempt: []string{"  "},
	}}}
	vs := selfCheckViolations(t.TempDir(), &pol)
	if !containsText(vs, "空串") {
		t.Fatalf("空豁免项必须被自校验报出，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckAcceptsExactExempt 合法的精确豁免**不得**被报出来。
//
// 这条是「逃逸口的反向验证」：判据加了「形状无效就报」这个出口后，
// 必须成对地钉住「形状有效时不报」——否则一个把正常豁免也判成无效的坏实现
// 照样全绿（只是绿得吵闹）。
func TestSelfCheckAcceptsExactExempt(t *testing.T) {
	pol := Policy{Rules: []Rule{{
		ID:           "x",
		ForbidImport: `example\.com/demo`,
		ImportExempt: []string{
			"example.com/demo/internal/engine",
			"github.com/yi-nology/agent-memory/v2",
			"gopkg.in/yaml.v3",
			"golang.org/x/net/http2",
		},
	}}}
	vs := selfCheckViolations(t.TempDir(), &pol)
	for _, v := range vs {
		if v.Kind == "policy_self_check" && containsText([]Violation{v}, "import_exempt") {
			t.Fatalf("合法豁免项不应被自校验报出，实得 %v", textsOf(vs))
		}
	}
}
