// policy_rules_test.go —— 真 policy 里各条 forbid_regex 的反向验证。
//
// # 为什么要拿真 policy 来测
//
// 规则的正则与门禁实现的匹配口径是**一对契约**：正则写了什么形状，实现就
// 必须认什么形状。各自测各自的（测试里另写一份正则）会漏掉「正则改了但
// 实现没跟上」这类漂移——而漂移的表现是门禁变绿。
//
// 所以这里直接读仓库真实的 architecture-policy.yaml，把真规则灌进真实现，
// 然后注入违规看它响不响。
//
// # 本文件覆盖的两条规则
//
//	- harness-no-bare-goroutine      ：旧写法只拦 `go func(`，漏 `go someFunc()`
//	- harness-no-printf-log-callback ：旧写法只认 `func(string`，漏带参数名的形态
//
// 两条的共同形状：**漏掉的形态是本仓自己在用的主流写法**，不是罕见边角。

package main

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadRealPolicy 读仓库真实的 architecture-policy.yaml。
func loadRealPolicy(t *testing.T) policyFile {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var pol policyFile
	if err := yaml.Unmarshal(data, &pol); err != nil {
		t.Fatal(err)
	}
	return pol
}

// ruleByID 取真 policy 里的某条规则。
func ruleByID(t *testing.T, pol policyFile, id string) rule {
	t.Helper()
	for _, r := range pol.Rules {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("真 policy 里找不到规则 %s", id)
	return rule{}
}

// TestRealPolicyBareGoroutineRule 裸 goroutine 规则必须拦住两种形态。
//
// 旧写法只拦 `go func(`，漏 `go someFunc()`——而后者的 panic 同样**无法被
// 外层 recover 捕获**（panic 发生在被调函数内部，裸 goroutine 没有任何
// recover 边界），风险与前者完全相同。
func TestRealPolicyBareGoroutineRule(t *testing.T) {
	pol := loadRealPolicy(t)
	r := ruleByID(t, pol, "harness-no-bare-goroutine")
	forms := map[string]string{
		"go func() 形式": "package p\n\nfunc f() {\n\tgo func() {\n\t\t_ = 1\n\t}()\n}\n",
		"go 具名函数形式":    "package p\n\nfunc f() {\n\tgo work()\n}\n\nfunc work() {}\n",
	}
	for name, src := range forms {
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"p/a.go": src})
		vs, err := checkRule(root, r)
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) == 0 {
			t.Fatalf("%s：必须被拦下，实得零违反", name)
		}
	}
}

// TestRealPolicyBareGoroutineRuleAllowsSafeForms 合法形态不得被误伤。
//
// 误报是门禁最致命的失败形态：维护者学会忽略它，规则随即名存实亡。
func TestRealPolicyBareGoroutineRuleAllowsSafeForms(t *testing.T) {
	pol := loadRealPolicy(t)
	r := ruleByID(t, pol, "harness-no-bare-goroutine")
	safe := map[string]string{
		"async.GoSafe": "package p\n\nimport \"github.com/oekit/concurrency/async\"\n\nfunc f() {\n\tasync.GoSafe(func() {})\n}\n",
		"普通调用":         "package p\n\nfunc f() {\n\twork()\n}\n\nfunc work() {}\n",
		"含 go 的标识符":    "package p\n\nfunc gopher() {}\n\nfunc f() {\n\tgopher()\n}\n",
		"字符串字面量":       "package p\n\nconst s = \"go func(\"\n",
	}
	for name, src := range safe {
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"p/a.go": src})
		vs, err := checkRule(root, r)
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) != 0 {
			t.Fatalf("%s：不得被误报，实得 %v", name, vs[0].text)
		}
	}
}

// TestRealPolicyPrintfCallbackRule printf 回调规则必须认带参数名的形态。
//
// 旧写法 `func\(string` 只认「首参无名的裸 string」，于是最常见的那种形态
// （`logf func(format string, args ...any)`）反而漏检——而那正是 drain 包
// 注释里讨论的形态。
func TestRealPolicyPrintfCallbackRule(t *testing.T) {
	pol := loadRealPolicy(t)
	r := ruleByID(t, pol, "harness-no-printf-log-callback")
	forms := map[string]string{
		"首参无名": "package p\n\nfunc f(logf func(string, ...any)) {}\n",
		"首参有名": "package p\n\nfunc f(logf func(format string, args ...any)) {}\n",
		"别名":   "package p\n\nfunc f(logger func(msg string)) {}\n",
	}
	for name, src := range forms {
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"p/a.go": src})
		vs, err := checkRule(root, r)
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) == 0 {
			t.Fatalf("%s：必须被拦下，实得零违反", name)
		}
	}
}

// TestRealPolicyPrintfCallbackRuleAllowsSafeForms 非 printf 回调不得被误伤。
func TestRealPolicyPrintfCallbackRuleAllowsSafeForms(t *testing.T) {
	pol := loadRealPolicy(t)
	r := ruleByID(t, pol, "harness-no-printf-log-callback")
	safe := map[string]string{
		"普通回调":    "package p\n\nfunc f(cb func(int) error) {}\n",
		"无参回调":    "package p\n\nfunc f(cb func()) {}\n",
		"非日志语义回调": "package p\n\nfunc f(apply func(string) (string, bool)) {}\n",
		"普通接口":    "package p\n\ntype Logger interface{ Info(string) }\n",
	}
	for name, src := range safe {
		root := t.TempDir()
		writeFixture(t, root, map[string]string{"p/a.go": src})
		vs, err := checkRule(root, r)
		if err != nil {
			t.Fatal(err)
		}
		if len(vs) != 0 {
			t.Fatalf("%s：不得被误报，实得 %v", name, vs[0].text)
		}
	}
}
