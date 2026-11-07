package archcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIncludePatternDefect 表驱动：结构上**不可能命中**的 include 必须被点名。
//
// 这一组的判据前提是「比对面是 filepath.Base(path)」，故反面用例（必须放行）与
// 正面用例同样重要：把 "**" 或 "*.md" 判死，就是**假红**——而假红会教人改判据，
// 比漏红的代价更高。
func TestIncludePatternDefect(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		dead    bool
	}{
		// ── 必须被判死 ──
		{"语法非法：字符类未闭合", "*.[go", true},
		{"语法非法：范围未闭合", "[a-", true},
		{"语法非法：尾部单个反斜杠（Unix 上是转义符，转义没有对象）", "a\\", true},
		{"含分隔符：** 跨目录", "**/*.go", true},
		{"含分隔符：相对路径", "src/*.go", true},
		{"含分隔符：纯目录", "pkg/", true},
		{"空模式", "", true},
		{"只有空白", "   ", true},
		// ── 必须放行 ──
		{"正常", "*.go", false},
		{"精确名", "policy.go", false},
		{"单字符通配", "?_?.go", false},
		{"字符类", "[abc]*.go", false},
		{"双星只匹配基名", "**", false},
		{"当前零命中也放行（将来该生效）", "*.md", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			why := includePatternDefect(c.pattern)
			if c.dead && why == "" {
				t.Fatalf("include 模式 %q 结构上不可能命中任何文件，判据却放行了", c.pattern)
			}
			if !c.dead && why != "" {
				t.Fatalf("include 模式 %q 是合法声明，判据却判死：%s", c.pattern, why)
			}
		})
	}
}

// TestSelfCheckCatchesDeadInclude 集成：死模式必须经由 policy 自校验报出。
//
// 判据落在自校验而不是扫描路径，就是为了让这条**声明缺陷**在加载期就红——
// 扫描路径那里只能静默跳过（它没法因为一条模式中止整个判定）。
func TestSelfCheckCatchesDeadInclude(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o750); err != nil {
		t.Fatal(err)
	}
	pol := Policy{Rules: []Rule{{
		ID:          "r1",
		ForbidRegex: "x",
		Scope:       []string{"pkg"},
		Include:     []string{"*.go", "**/*.go"},
	}}}
	vs := selfCheckViolations(root, &pol)
	if !containsText(vs, "include 模式") {
		t.Fatalf("死掉的 include 模式必须由自校验报出，实得 %v", textsOf(vs))
	}
	if !containsText(vs, "**/*.go") {
		t.Fatalf("报错必须点名是哪一条模式，实得 %v", textsOf(vs))
	}
	if containsText(vs, "\"*.go\" ") {
		t.Fatalf("同一个规则里的合法模式不得被连带判死，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckAcceptsLiveInclude 反向：合法 include 不得被误报。
func TestSelfCheckAcceptsLiveInclude(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o750); err != nil {
		t.Fatal(err)
	}
	pol := Policy{Rules: []Rule{{
		ID:          "r1",
		ForbidRegex: "x",
		Scope:       []string{"pkg"},
		Include:     []string{"*.go"},
	}}}
	if vs := selfCheckViolations(root, &pol); containsText(vs, "include 模式") {
		t.Fatalf("合法 include 被误报：%v", textsOf(vs))
	}
}

// TestSelfCheckSkipsIncludeForBodylessRule 没有执行体的规则不重复报 include。
//
// 那种规则一条都判不了，已由「没有执行体」那条报出。同一件事说两遍，维护者就会
// 学会从报告里挑着看——那比少报一条更危险。
func TestSelfCheckSkipsIncludeForBodylessRule(t *testing.T) {
	pol := Policy{Rules: []Rule{{ID: "doc", Include: []string{"**/*.go"}}}}
	vs := selfCheckViolations(t.TempDir(), &pol)
	if !containsText(vs, "没有执行体") {
		t.Fatalf("没有执行体的规则必须被报出，实得 %v", textsOf(vs))
	}
	if containsText(vs, "include 模式") {
		t.Fatalf("没有执行体的规则不该重复报 include，实得 %v", textsOf(vs))
	}
}
