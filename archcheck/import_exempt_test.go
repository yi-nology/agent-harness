// import_exempt_test.go —— import 规则上的**路径级精确例外**（issue #15）。
//
// # 为什么这条能力必须存在
//
// 宿主有一条不变式：「记忆内核的依赖只经 `internal/memstore` 一个包进入」
// （形式是 `forbid_import_regex` + `scope_exclude` 只放行 memstore 目录）。
//
// 业务层要用到内核的**某个具体子包**（本次是 `agent-memory/evaluation`）时，
// 宿主只有三条路：
//
//  1. 放宽 `scope_exclude` ⇒ 该目录即可引**整棵内核树**（含 types/store/ports）
//     ⇒ 「内核概念面不得渗进业务层」这条不变式被**静默放宽**；
//  2. 把 `forbid_import_regex` 改成逐包白名单 ⇒ 上游将来新增一个包就**静默放行**
//     （一条被静默放宽的声明等于没有声明）；
//  3. 绕开 —— 让判分也经既有单一入口（`internal/memstore` 暴露宿主形状的转译）
//     ⇒ 同一个上游能力要在宿主侧**再写一层转译**（类型不许外泄、
//     判分语义要自己核等价性）。
//
// 第 3 条可行，但代价本可以由上游的一个 import 例外免掉。
//
// # 关键语义：例外是**精确路径**，不是前缀
//
// 若豁免 `a/b` 时连 `a/b/c` 一起放行，那它等价于「放宽整棵子树」——
// 上游将来在 `a/b` 下新增一个包就会被静默放行。故本文件把这条钉死。

package archcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportExemptReleasesExactPath 钉住：登记的**那条** import 被放行。
func TestImportExemptReleasesExactPath(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/evaluation\"\n",
	})
	r := Rule{
		ID:           "demo",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/engine`,
		ImportExempt: []string{"example.com/demo/internal/engine/evaluation"},
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("登记的 import 应被放行，实得 %d 处违反: %+v", len(vs), vs)
	}
}

// TestImportExemptDoesNotCoverSubtree 钉住：**子包不被连带放行**。
//
// 这是本条能力的**核心约束**：若豁免 `a/b` 时 `a/b/c` 也放行，
// 那它就等于「放宽整棵子树」，而上游将来在 `a/b` 下新增的包会被静默放行。
func TestImportExemptDoesNotCoverSubtree(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		// 登记的这条：放行
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/evaluation\"\n",
		// 同一子树下但**未登记**的：仍被拒
		"internal/demo/b.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/store\"\n",
		// 另一个子包：仍被拒
		"internal/demo/c.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/ports\"\n",
	})
	r := Rule{
		ID:           "demo",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/engine`,
		ImportExempt: []string{"example.com/demo/internal/engine/evaluation"},
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("未登记的两条应仍被拒（豁免不得扩大成整棵子树），实得 %d 处: %+v", len(vs), vs)
	}
	for _, want := range []string{"internal/demo/b.go", "internal/demo/c.go"} {
		found := false
		for _, v := range vs {
			if v.Path == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("应报出 %s，实得 %+v", want, vs)
		}
	}
}

// TestImportExemptWithoutExactMatchDoesNotExempt 钉住：豁免项写的是
// **前缀**（带尾斜杠）时**不生效**。
//
// 为什么要刻意不归一：「`a/b/` 看着像『放行 a/b 这棵树』」是常见直觉，
// 但那个语义正是本条能力要消灭的（子树放行 = 静默放宽）。
// 故写成前缀时它**不匹配任何具体 import** ⇒ 宿主会看到「豁免没生效」，
// 而不是误以为整棵子树放行了。
func TestImportExemptWithoutExactMatchDoesNotExempt(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/evaluation\"\n",
	})
	r := Rule{
		ID:           "demo",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/engine`,
		// 写成前缀（带尾斜杠）⇒ 不匹配具体路径 ⇒ 豁免无效。
		ImportExempt: []string{"example.com/demo/internal/engine/"},
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("前缀写法不该产生豁免（否则等于放宽整棵子树），实得 %d 处: %+v", len(vs), vs)
	}
}

// TestImportExemptIsExactNotSubstring 钉住：豁免是**整条路径相等**，
// 不是子串包含。
//
// 误用形态：实现成 `strings.Contains(exempt, path)` 或反过来 ⇒
// 豁免 `a/b/c` 时把 `x/a/b/c` 也放行；或豁免 `a/b` 时把 `a/bcd` 放行。
func TestImportExemptIsExactNotSubstring(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		// 路径更长，豁免项是它的前缀 ⇒ 不该被放行
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/evaluation/extra\"\n",
		// 路径更短，豁免项包含它 ⇒ 也不该被放行
		"internal/demo/b.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/eval\"\n",
	})
	r := Rule{
		ID:           "demo",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/engine`,
		ImportExempt: []string{"example.com/demo/internal/engine/evaluation"},
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("子串相近的两条都该被拒（豁免是整条路径相等），实得 %d 处: %+v", len(vs), vs)
	}
}

// TestImportExemptOnlyAppliesToThatRule 钉住：豁免**只对本规则**生效。
//
// 误用形态：豁免表被提到规则之外（全局生效）⇒ 一条规则的豁免会顺带
// 放过另一条规则的 import ⇒ 声明面与判定面脱钩。
func TestImportExemptOnlyAppliesToThatRule(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/evaluation\"\n",
	})
	// 规则 A 有豁免，规则 B 没有 ⇒ B 仍应报出。
	rA := Rule{
		ID:           "a",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/engine`,
		ImportExempt: []string{"example.com/demo/internal/engine/evaluation"},
	}
	rB := Rule{
		ID:           "b",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/engine`,
	}
	vsA, err := importViolations(root, rA)
	if err != nil {
		t.Fatal(err)
	}
	if len(vsA) != 0 {
		t.Fatalf("规则 A 的豁免应放行，实得 %+v", vsA)
	}
	vsB, err := importViolations(root, rB)
	if err != nil {
		t.Fatal(err)
	}
	if len(vsB) != 1 {
		t.Fatalf("规则 B 无豁免，仍应报出，实得 %+v", vsB)
	}
}

// TestImportExemptParsedFromPolicy 钉住：**声明面与执行体成对**。
//
// policy 里写了 `import_exempt` 必须真的生效——否则就是「policy 写了例外
// 但执行体不认」的**静默假通过**（与 issue #11 同类：声明与执行分离）。
// 而 YAML 会**静默丢弃**未声明的字段 ⇒ 若 Rule 结构体里没这个字段，
// policy 写什么都不报错、也不生效。
func TestImportExemptParsedFromPolicy(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/evaluation\"\n",
	})
	// 走「policy 解析 → 执行」整条链（不是直接构造 Rule）：
	// YAML 会**静默丢弃**未声明的字段 ⇒ 若 Rule 结构体里没这个字段，
	// policy 写什么都不报错、也不生效（静默假通过）。
	polPath := filepath.Join(root, "architecture-policy.yaml")
	yamlBody := `rules:
  - id: demo
    scope: [internal/demo]
    include: ["*.go"]
    forbid_import_regex: 'example\.com/demo/internal/engine'
    import_exempt:
      - example.com/demo/internal/engine/evaluation
`
	if err := os.WriteFile(polPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("写 policy 失败: %v", err)
	}
	loaded, err := Load(polPath)
	if err != nil {
		t.Fatalf("解析 policy 失败: %v", err)
	}
	if len(loaded.Rules) != 1 {
		t.Fatalf("应解析出 1 条规则，实得 %d", len(loaded.Rules))
	}
	if len(loaded.Rules[0].ImportExempt) != 1 {
		t.Fatalf("import_exempt 应被 YAML 解析进 Rule（否则是静默丢弃），实得 %v", loaded.Rules[0].ImportExempt)
	}
	// 且真能生效。
	vs, err := importViolations(root, loaded.Rules[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("policy 里登记的 import_exempt 应生效，实得 %+v", vs)
	}
}

// TestImportExemptViolationTextKeepsPath 钉住：违规文案仍点名 import 路径
// （豁免不改变违规的呈现形态，宿主据它定位）。
func TestImportExemptViolationTextKeepsPath(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"internal/demo/a.go": "package demo\n\nimport _ \"example.com/demo/internal/engine/store\"\n",
	})
	r := Rule{
		ID:           "demo",
		Scope:        []string{"internal/demo"},
		Include:      []string{"*.go"},
		ForbidImport: `example\.com/demo/internal/engine`,
		ImportExempt: []string{"example.com/demo/internal/engine/evaluation"},
	}
	vs, err := importViolations(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("应报出 1 处，实得 %+v", vs)
	}
	if !strings.Contains(vs[0].Text, "example.com/demo/internal/engine/store") {
		t.Errorf("违规文案应点名 import 路径，实得 %q", vs[0].Text)
	}
}
