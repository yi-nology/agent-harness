// deps_test.go —— 依赖方向门禁本身的用例。
//
// 为什么门禁的判定逻辑也要有测试：这条规则查的是「别人写不写得对」，
// 而它自己的三个判据（最长前缀、声明自校验、未登记包）里有两处容易静默失配——
// 前缀匹配写成首个命中会把内层包的边判成外层的，声明引用错模块 id 会让该边
// 永远放行。没触发过的规则等于没有规则，这里就是它的反向验证固化处。

package archcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFixture 造一个最小仓：go.mod + 给定的包目录与文件。
func writeFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	mod := "module example.test/h\n\ngo 1.27\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestModuleImportPathReadsGoMod module 路径必须从 go.mod 读，不能写死——
// 迁移过一次，迁移那天忘了改门禁就会静默失配。
func TestModuleImportPathReadsGoMod(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, nil)
	got, err := moduleImportPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.test/h" {
		t.Fatalf("module 路径实得 %q，期望 example.test/h", got)
	}

	// go.mod 缺 module 声明时必须报错，不能返回空串继续跑。
	bad := t.TempDir()
	if err := os.WriteFile(filepath.Join(bad, "go.mod"), []byte("go 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := moduleImportPath(bad); err == nil {
		t.Fatal("go.mod 缺 module 声明时必须报错")
	}
}

// TestBuildModuleIndexRejectsBrokenDeclaration 声明自身出错必须响亮失败。
// 静默降级会让「这条边是否被允许」变成永远为真的判定——门禁看似在跑，
// 实则对该边完全放行，而这正是这条规则唯一要防的事。
func TestBuildModuleIndexRejectsBrokenDeclaration(t *testing.T) {
	cases := []struct {
		why      string
		mods     []Module
		universe []string
		wantIs   bool
	}{
		{"缺 id", []Module{{Roots: []string{"a"}}}, nil, false},
		{"id 重复", []Module{{ID: "a", Roots: []string{"x"}}, {ID: "a", Roots: []string{"y"}}}, nil, false},
		{"缺 roots", []Module{{ID: "a"}}, nil, false},
		{"roots 重复", []Module{{ID: "a", Roots: []string{"x"}}, {ID: "b", Roots: []string{"x"}}}, nil, false},
		{"requires 引用未登记模块", []Module{{ID: "a", Roots: []string{"x"}, Requires: []string{"ghost"}}}, nil, false},
		{"ext 条目未登记受管包面", []Module{{ID: "a", Roots: []string{"x"}, Requires: []string{"ext:example.test/ext"}}}, nil, false},
		{"ext 条目落在受管包面内", []Module{{ID: "a", Roots: []string{"x"}, Requires: []string{"ext:example.test/ext"}}}, []string{"example.test/ext"}, true},
		{"ext 条目是受管包面的子路径", []Module{{ID: "a", Roots: []string{"x"}, Requires: []string{"ext:example.test/ext/sub"}}}, []string{"example.test/ext"}, true},
		{"ext 条目比受管包面更宽（越权领取）", []Module{{ID: "a", Roots: []string{"x"}, Requires: []string{"ext:example.test"}}}, []string{"example.test/ext"}, false},
		{"ext 前缀为空", []Module{{ID: "a", Roots: []string{"x"}, Requires: []string{"ext:"}}}, []string{"example.test/ext"}, false},
		{"受管包面里有空条目", []Module{{ID: "a", Roots: []string{"x"}}}, []string{""}, false},
		{"合法声明", []Module{{ID: "a", Roots: []string{"x"}, Requires: []string{"b"}}, {ID: "b", Roots: []string{"y"}}}, nil, true},
	}
	for _, c := range cases {
		_, err := buildModuleIndex(c.mods, "example.test/h", c.universe)
		if c.wantIs && err != nil {
			t.Fatalf("%s：合法声明不应报错，实得 %v", c.why, err)
		}
		if !c.wantIs && err == nil {
			t.Fatalf("%s：声明出错时必须报错，不能静默放行", c.why)
		}
	}
}

// TestLongestPrefixWins 覆盖**匹配语义**：整段相等、斜杠子路径、以及前缀
// 不得跨斜杠误命中（hsub 不应命中 h）。
//
// 注意：本用例手工喂一个已排好序的表，只验证匹配本身，**验证不了排序**——
// 排序写反时它照样全绿。排序由 TestNestedRootsAttributeEdgeToInnermost 走真实
// 构造路径覆盖（初版就是缺了那条，注入后测试仍绿）。
func TestLongestPrefixWins(t *testing.T) {
	mi := moduleIndex{
		byPrefix: map[string]string{
			"example.test/h":     "outer",
			"example.test/h/sub": "inner",
		},
		prefixes: []string{"example.test/h/sub", "example.test/h"},
	}
	cases := []struct {
		imp  string
		want string
	}{
		{"example.test/h/sub", "inner"},
		{"example.test/h/sub/deep", "inner"},
		{"example.test/h", "outer"},
		{"example.test/h/other", "outer"},
		{"example.test/elsewhere", ""},
		{"example.test/hsub", ""}, // 前缀不得跨斜杠误命中
	}
	for _, c := range cases {
		got, ok := mi.longestPrefix(c.imp)
		if c.want == "" {
			if ok {
				t.Fatalf("%s 不应命中任何模块，实得 %q", c.imp, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Fatalf("%s 应命中 %q，实得 %q(ok=%v)", c.imp, c.want, got, ok)
		}
	}
}

// TestUnregisteredDirDetected 未登记的包目录必须被拦——
// 它对本门禁隐形，任何模块都能 import 它而不被判违规。
func TestUnregisteredDirDetected(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"known/a.go": "package known\n",
		"ghost/b.go": "package ghost\n",
		// 只有测试文件的目录不算「有生产代码」，不必登记。
		"testsonly/c_test.go": "package testsonly\n",
		// ignore_dirs 列出的目录不归这条规则管。
		"cmd/tool/d.go": "package tool\n",
	})
	mi := moduleIndex{byRoot: map[string]string{"known": "known"}, requires: map[string]map[string]bool{}}
	cfg := DependencyDirection{IgnoreDirs: []string{"cmd"}}

	vs := unregisteredPackageViolations(root, mi, cfg)
	if len(vs) != 1 {
		t.Fatalf("应恰好拦下一个未登记目录，实得 %d 个: %+v", len(vs), vs)
	}
	if vs[0].Path != "ghost" || vs[0].Kind != "module_unregistered" {
		t.Fatalf("实得 %+v，应指向 ghost 目录", vs[0])
	}
	if vs[0].Rule != depRuleID {
		t.Fatalf("违规应挂在依赖方向规则下，实得 %q", vs[0].Rule)
	}
}

// TestDependencyDirectionEndToEnd 端到端：越界依赖被拦、已登记依赖放行、
// 外部依赖不归这条规则管。
func TestDependencyDirectionEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		// leaf 未登记 base 却 import 了它 → 越界。
		"leaf/a.go": "package leaf\n\nimport _ \"example.test/h/base\"\n",
		"base/b.go": "package base\n",
		// ok 已登记 base，并 import 外部依赖（两者都不该被管）。
		"ok/c.go": "package ok\n\nimport (\n\t_ \"example.test/h/base\"\n\t_ \"github.com/oekit/pkg/errors\"\n)\n",
	})
	mods := []Module{
		{ID: "leaf", Roots: []string{"leaf"}, Requires: []string{}},
		{ID: "base", Roots: []string{"base"}, Requires: []string{}},
		{ID: "ok", Roots: []string{"ok"}, Requires: []string{"base"}},
	}
	cfg := DependencyDirection{IgnoreDirs: []string{"cmd"}}

	vs, err := dependencyViolations(root, mods, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var depViolations []Violation
	for _, v := range vs {
		if v.Kind == "module_dep" {
			depViolations = append(depViolations, v)
		}
	}
	if len(depViolations) != 1 {
		t.Fatalf("应恰好拦下一处越界依赖，实得 %d 处: %+v", len(depViolations), depViolations)
	}
	if depViolations[0].Path != filepath.Join("leaf", "a.go") {
		t.Fatalf("越界方应为 leaf/a.go，实得 %s", depViolations[0].Path)
	}
	// ok 包同时 import 了已登记的内部包与外部包，两者都不该被报。
	for _, v := range depViolations {
		if v.Path == filepath.Join("ok", "c.go") {
			t.Fatalf("已登记依赖与外部依赖都不该被拦，却报了 %s", v.Text)
		}
	}
}

// TestTestFilesOutOfScope 测试文件不在判定面内：依赖方向决定交付物里出现什么，
// 而测试依赖是临时的，纳入只会逼出「只为共享夹具而做的注册」噪声。
func TestTestFilesOutOfScope(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"leaf/a.go":      "package leaf\n",
		"leaf/a_test.go": "package leaf\n\nimport _ \"example.test/h/base\"\n",
		"base/b.go":      "package base\n",
	})
	mods := []Module{
		{ID: "leaf", Roots: []string{"leaf"}, Requires: []string{}},
		{ID: "base", Roots: []string{"base"}, Requires: []string{}},
	}
	vs, err := dependencyViolations(root, mods, DependencyDirection{IgnoreDirs: []string{"cmd"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.Kind == "module_dep" {
			t.Fatalf("测试文件的越界依赖不应被判违规，实得 %+v", v)
		}
	}
}

// TestNestedRootsAttributeEdgeToInnermost 嵌套 roots 的端到端判定：
// 归属必须落到**最内层**模块，否则一条已登记的依赖会被判成越界。
//
// 为什么必须走真实路径而不是手工造 moduleIndex：前缀表的排序发生在构造阶段，
// 手工喂一个排好序的表只会测到 longestPrefix 函数本身，排序写反了它照样绿
// ——本用例初版就栽在这里（注入「升序排序」后测试仍全绿）。
//
// 这里 outer/inner 是两个已登记模块，只有 inner 声明了 base。归属判对时无违规；
// 排序写反导致 outer 抢先命中时，outer 不声明 base → 立刻变红。
func TestNestedRootsAttributeEdgeToInnermost(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"base/b.go":        "package base\n",
		"outer/inner/x.go": "package inner\n\nimport _ \"example.test/h/base\"\n",
	})
	mods := []Module{
		{ID: "outer", Roots: []string{"outer"}, Requires: []string{}},
		{ID: "inner", Roots: []string{"outer/inner"}, Requires: []string{"base"}},
		{ID: "base", Roots: []string{"base"}, Requires: []string{}},
	}
	vs, err := dependencyViolations(root, mods, DependencyDirection{IgnoreDirs: []string{"cmd"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.Kind == "module_dep" {
			t.Fatalf("归属判到最内层时应无违规，实得 %s: %s", v.Path, v.Text)
		}
	}
}

// TestDepDirOn 缺省即启用：只有显式 false 才关——否则「注释掉一段配置」
// 就等于把一条 error 级规则悄悄关掉，而没有任何东西会说这件事。
func TestDepDirOn(t *testing.T) {
	off := false
	on := true
	if !(DependencyDirection{}).on() {
		t.Fatal("缺省应启用")
	}
	if (DependencyDirection{Enabled: &off}).on() {
		t.Fatal("显式 false 才关闭")
	}
	if !(DependencyDirection{Enabled: &on}).on() {
		t.Fatal("显式 true 应启用")
	}
}
