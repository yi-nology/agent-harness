package archcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureRoot 造一个最小仓：policy + 一个包目录，用于跑真门禁。
func fixtureRoot(t *testing.T, polYAML, code string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "x"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "architecture-policy.yaml"), []byte(polYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x", "a.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// escapeCode 是含 PTC 未受管逃逸口的最小生产代码。
//
// 行号是**故意排好的**：复合字面量 ptc.Options{ 落在第 6 行，
// 例外登记按 rule+path+line 三元组精确匹配，测试要能算准这个行号。
const escapeCode = "package x\n" +
	"\n" +
	"import \"github.com/yi-nology/goagent-kernel/ptc\"\n" +
	"\n" +
	"func F() ptc.Options {\n" +
	"\treturn ptc.Options{\n" +
	"\t\tAllowUnmanaged: true,\n" +
	"\t}\n" +
	"}\n"

// policyHead 是各用例共用的 policy 骨架。
//
// dependency_direction 关掉：本用例只关心例外登记的判定面，而依赖方向门禁
// 需要仓里有 go.mod（模块路径解析），与本用例无关。
const policyHead = "version: 1\n" +
	"modules:\n" +
	"  - id: x\n" +
	"    roots: [x]\n" +
	"    requires: []\n" +
	"    owner: test\n" +
	"    managed: true\n" +
	"dependency_direction:\n" +
	"  enabled: false\n" +
	"rules: []\n" +
	"thresholds:\n" +
	"  scan_roots: [x]\n" +
	"  max_file_lines: 400\n" +
	"  enforce: false\n"

// TestExceptionForBuiltinRuleIsAcceptedAndExcuses 正例：宿主按内核指引登记
// PTC 未受管逃逸口的例外 ⇒ 豁免**真的生效**（违反被计入 excused，不再阻断）。
//
// 这条用例钉的是 v0.32.0 的实测缺陷：照指引登记 ⇒ 自校验报「指向不存在的规则」
// ⇒ 门禁红。修法是让「规则存在」的口径并上内置规则 id。
//
// 为什么必须断言 excused 而不只是「没有自校验违规」：只测「没报错」的用例，
// 一个把豁免**静默忽略**的实现照样全过——而那正是「逃逸口白开了」的形态。
func TestExceptionForBuiltinRuleIsAcceptedAndExcuses(t *testing.T) {
	root := fixtureRoot(t, policyHead+
		"exceptions:\n"+
		"  - rule: harness-ptc-unmanaged-escape\n"+
		"    path: x/a.go\n"+
		"    line: 6\n"+
		"    violation: \"生产代码显式选择了未受管执行\"\n"+
		"    reason: \"宿主的工具另有一套管控\"\n"+
		"    until: \"2099-01-01\"\n"+
		"    removal: \"接上判定环即删\"\n", escapeCode)
	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatalf("policy 解析失败: %v", err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatalf("门禁给不出结论: %v", err)
	}
	if containsText(res.Violations, "不存在的规则") {
		t.Fatalf("内置规则的例外登记不得被判为「指向不存在的规则」，实得 %v", textsOf(res.Violations))
	}
	if res.Excused != 1 {
		t.Fatalf("豁免必须真的生效（违反计入 excused），实得 excused=%d，违规 %v", res.Excused, textsOf(res.Violations))
	}
	if res.Blocking != 0 {
		t.Fatalf("已登记且未到期的豁免不得计入阻断集，实得 blocking=%d，违规 %v", res.Blocking, textsOf(res.Violations))
	}
}

// TestExceptionForUnknownRuleStillReported 负对照：指向**真不存在**的规则仍须报出。
//
// 只测「好实现能过」的用例钉不住判据：一个把规则存在性判据写成「一律接受」的
// 实现，上面那条正例照样全过。
func TestExceptionForUnknownRuleStillReported(t *testing.T) {
	root := fixtureRoot(t, policyHead+
		"exceptions:\n"+
		"  - rule: harness-ghost-rule\n"+
		"    path: x/a.go\n"+
		"    line: 6\n"+
		"    reason: \"r\"\n"+
		"    until: \"2099-01-01\"\n"+
		"    removal: \"rm\"\n", escapeCode)
	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatalf("policy 解析失败: %v", err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatalf("门禁给不出结论: %v", err)
	}
	if !containsText(res.Violations, "不存在的规则") {
		t.Fatalf("指向不存在规则的例外必须被报出，实得 %v", textsOf(res.Violations))
	}
}

// TestExceptionCannotExcuseSelfCheck 负对照：自校验**不可豁免**。
//
// 自校验是豁免机制的前置条件；豁免它等于让任何声明都能静音（含「这条豁免
// 本身不合法」这件事）。这条负对照守的是「例外机制自身的不可绕过性」。
func TestExceptionCannotExcuseSelfCheck(t *testing.T) {
	root := fixtureRoot(t, policyHead+
		"exceptions:\n"+
		"  - rule: harness-policy-self-check\n"+
		"    path: architecture-policy.yaml\n"+
		"    line: 0\n"+
		"    reason: \"r\"\n"+
		"    until: \"2099-01-01\"\n"+
		"    removal: \"rm\"\n", escapeCode)
	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatalf("policy 解析失败: %v", err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatalf("门禁给不出结论: %v", err)
	}
	if !containsText(res.Violations, "不得豁免自校验") {
		t.Fatalf("豁免自校验本身必须被报出，实得 %v", textsOf(res.Violations))
	}
}

// TestExceptionForDisabledBuiltinRuleReported 负对照：例外指向**当前不在判**的
// 内置规则 ⇒ 报出。
//
// 这条豁免今天什么都没豁免，却会在将来那条规则被启用时无声地开始生效——
// 与 ignore_dirs「豁免必须指向真实存在的目录」同一条纪律。
func TestExceptionForDisabledBuiltinRuleReported(t *testing.T) {
	root := fixtureRoot(t, policyHead+
		"execution_paths:\n"+
		"  enabled: false\n"+
		"exceptions:\n"+
		"  - rule: harness-sandbox-execution-path\n"+
		"    path: x/a.go\n"+
		"    line: 6\n"+
		"    reason: \"r\"\n"+
		"    until: \"2099-01-01\"\n"+
		"    removal: \"rm\"\n", escapeCode)
	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatalf("policy 解析失败: %v", err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatalf("门禁给不出结论: %v", err)
	}
	if !containsText(res.Violations, "当前不在判") {
		t.Fatalf("指向当前不在判的规则的例外必须被报出，实得 %v", textsOf(res.Violations))
	}
}

// TestPolicyRuleIdCollidingWithBuiltinReported 负对照：policy 里声明一条与内置
// 规则同名的规则 ⇒ 报出。
//
// 例外按 rule+path+line 三元组豁免；两条判定共用一个 id ⇒ 一条例外同时落到
// 两条判定上，豁免不再精确。
func TestPolicyRuleIdCollidingWithBuiltinReported(t *testing.T) {
	root := fixtureRoot(t, "version: 1\n"+
		"modules:\n"+
		"  - id: x\n"+
		"    roots: [x]\n"+
		"    requires: []\n"+
		"    owner: test\n"+
		"    managed: true\n"+
		"dependency_direction:\n"+
		"  enabled: false\n"+
		"rules:\n"+
		"  - id: harness-ptc-unmanaged-escape\n"+
		"    forbid_regex: \"zzz-never-matches\"\n"+
		"thresholds:\n"+
		"  scan_roots: [x]\n"+
		"  max_file_lines: 400\n"+
		"  enforce: false\n", escapeCode)
	pol, err := Load(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatalf("policy 解析失败: %v", err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatalf("门禁给不出结论: %v", err)
	}
	if !containsText(res.Violations, "与内置规则重名") {
		t.Fatalf("与内置规则重名的规则 id 必须被报出，实得 %v", textsOf(res.Violations))
	}
}
