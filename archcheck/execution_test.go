// execution_test.go —— 执行路径门禁（execution.go）的行为钉住。
//
// # 这条门禁钉的是什么
//
// 「执行必须经 sandbox 登记簿」。绕开登记簿直接调裸执行入口，档位下限、
// 预算可兑现性、审批门三层会**同时**失效——而这三条正是宿主据以做风险判断的依据。
//
// # 夹具
//
// 用合成包名（ptc / app / tools）与合成符号（Run / Helper），不带领域味。
package archcheck

import (
	"os"
	"path/filepath"
	"testing"
)

// execFixture 造一个最小仓：一个 ptc 包（裸入口所在）+ 一个调用方包。
func execFixture(t *testing.T, root string, files map[string]string) {
	t.Helper()
	writeFixture(t, root, files)
}

// ptcPkg 合成一个「有裸入口 Run」的包。
const ptcPkg = "package ptc\n\nfunc Run(program string) string { return program }\n"

// TestExecutionPathCatchesRawEntryCall 钉住：调用裸执行入口必须被报出来。
//
// 这是本门禁存在的理由：调用方绕过登记簿 ⇒ 声明的强度与实际执行的强度之间
// 没有任何强制，只有**门禁全绿**。
func TestExecutionPathCatchesRawEntryCall(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"ptc/ptc.go": ptcPkg,
		"app/app.go": "package app\n\nimport \"github.com/yi-nology/agent-harness/ptc\"\n\n" +
			"func Run() string { return ptc.Run(\"prog\") }\n",
	})
	vs, err := executionPathViolations(root, ExecutionPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "裸执行入口") {
		t.Fatalf("调用裸执行入口必须被报出，实得 %v", textsOf(vs))
	}
}

// TestExecutionPathIgnoresAlias 钉住：**改 import 别名绕不过去**。
//
// 判据按 import 路径的**最后一段**（包名）匹配，不按限定符——
// 限定符是调用方随手起的，按它匹配等于给了一条绕过路径。
func TestExecutionPathIgnoresAlias(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"ptc/ptc.go": ptcPkg,
		"app/app.go": "package app\n\nimport alias \"github.com/yi-nology/agent-harness/ptc\"\n\n" +
			"func Run() string { return alias.Run(\"prog\") }\n",
	})
	vs, err := executionPathViolations(root, ExecutionPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "裸执行入口") {
		t.Fatalf("别名调用同样必须被报出（别名不是绕过路径），实得 %v", textsOf(vs))
	}
}

// TestExecutionPathIgnoresSamePackageCall 钉住：**同包内的调用不报**。
//
// 同包调用没有限定符（`Run(...)` 而不是 `ptc.Run(...)`）⇒ 实现内部的调用
// （`ptc.Provider.Exec` 调 `Run`）天然不构成绕过。
func TestExecutionPathIgnoresSamePackageCall(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"ptc/ptc.go": ptcPkg + "\nfunc Run2() string { return Run(\"x\") }\n",
	})
	vs, err := executionPathViolations(root, ExecutionPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("同包内的调用不该被报出，实得 %v", textsOf(vs))
	}
}

// TestExecutionPathIgnoresOtherCalls 钉住：**别的调用不报**（无误报）。
//
// 判据只认「命中登记的裸入口」这一件事。误报会让门禁变吵，而吵的门禁
// 会被忽略——等于把判据关掉。
func TestExecutionPathIgnoresOtherCalls(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"ptc/ptc.go": ptcPkg,
		"app/app.go": "package app\n\nimport \"github.com/yi-nology/agent-harness/ptc\"\n\n" +
			"func Other() string { return ptc.Helper() }\n",
		"tools/tools.go": "package tools\n\nfunc Local() string { return \"x\" }\n",
	})
	vs, err := executionPathViolations(root, ExecutionPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("未命中裸入口的调用不该被报出，实得 %v", textsOf(vs))
	}
}

// TestExecutionPathSkipsTestFiles 钉住：**测试文件不判**。
//
// 判定面只含生产代码——测试里直接调裸入口是合法的（测试就是要构造那种场景）。
//
// 夹具的关键：同目录必须**同时有**生产文件与测试文件。若目录里只有测试文件，
// 它压根不会被扫（包目录按「含生产 Go 文件」筛选），本用例就成了空的——
// 判据「不排除测试文件」与「整个目录没被扫」都同样让它变绿 ⇒ 没牙齿。
func TestExecutionPathSkipsTestFiles(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"ptc/ptc.go": ptcPkg,
		// 生产文件（合规调用）：让这个目录**会**被扫到。
		"app/app.go": "package app\n\nfunc Local() string { return \"x\" }\n",
		// 测试文件（违规调用）：不该被扫。
		"app/app_test.go": "package app\n\nimport \"github.com/yi-nology/agent-harness/ptc\"\n\n" +
			"func TestRun() { _ = ptc.Run(\"prog\") }\n",
	})
	vs, err := executionPathViolations(root, ExecutionPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("测试文件不在判定面内，实得 %v", textsOf(vs))
	}
}

// TestExecutionPathDetectsStaleEntry 钉住：登记的裸入口**在仓内不存在** ⇒ 报「陈旧」。
//
// 登记指向已删除或已改名的符号时，它会让「这条执行路径没人管」这件事
// **看起来有人管过**——与「陈旧包根当天即红」同一条纪律。
func TestExecutionPathDetectsStaleEntry(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		// ptc 包在，但它没有 Run ⇒ 内置表指向的符号不存在 ⇒ 陈旧。
		"ptc/other.go": "package ptc\n\nfunc Other() string { return \"x\" }\n",
	})
	vs, err := executionPathViolations(root, ExecutionPaths{})
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "不存在") {
		t.Fatalf("登记的符号不存在必须被报出（陈旧当天即红），实得 %v", textsOf(vs))
	}
}

// TestExecutionPathCatchesPolicyDeclaredEntry 钉住：policy 段登记的**宿主自己的**
// 裸入口也能判。
//
// 内置表只管本仓的 API 事实；宿主自己的裸入口写在 policy 里，
// 同一套执行体判——**判据只有一份**。
func TestExecutionPathCatchesPolicyDeclaredEntry(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"internal/x/x.go": "package x\n\nfunc RunRaw(program string) string { return program }\n",
		"app/app.go": "package app\n\nimport \"example.test/h/internal/x\"\n\n" +
			"func Go() string { return x.RunRaw(\"prog\") }\n",
	})
	cfg := ExecutionPaths{RawEntries: []ExecutionEntry{{PackageName: "x", Symbol: "RunRaw"}}}
	vs, err := executionPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "裸执行入口") {
		t.Fatalf("policy 段登记的裸入口必须被报出，实得 %v", textsOf(vs))
	}
}

// TestExecutionPathImportPathNarrowsMatch 钉住：填了 `import_path` 就**收紧**到
// 那个具体模块。
//
// 按包名匹配是宽的（同名的另一个模块也会命中）；填 import_path 后，
// 别的模块的同名包不再命中——这让宿主能精确登记自己那一条。
func TestExecutionPathImportPathNarrowsMatch(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"internal/x/x.go": "package x\n\nfunc RunRaw(program string) string { return program }\n",
		"app/app.go": "package app\n\nimport \"example.test/h/internal/x\"\n\n" +
			"func Go() string { return x.RunRaw(\"prog\") }\n",
	})
	// import_path 指向别的模块 ⇒ 不命中。
	cfg := ExecutionPaths{RawEntries: []ExecutionEntry{{
		PackageName: "x", Symbol: "RunRaw", ImportPath: "example.test/other/internal/x",
	}}}
	vs, err := executionPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Fatalf("import_path 不符时不该命中，实得 %v", textsOf(vs))
	}
	// import_path 对上 ⇒ 命中。
	cfg.RawEntries[0].ImportPath = "example.test/h/internal/x"
	vs, err = executionPathViolations(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !containsText(vs, "裸执行入口") {
		t.Fatalf("import_path 对上时必须命中，实得 %v", textsOf(vs))
	}
}

// TestExecutionPathRuleIsRegisteredForReport 钉住：这条规则必须登记进
// `res.Rules`，否则它的违规**判了但不回显**。
//
// 这是实测踩到的：违规被计入阻断数、门禁确实红了，但报告里只印出别的规则，
// 使用者看到「2 处违反」却找不到是哪条规则判的——**判了但看不见**，
// 与「没判」在排查时几乎等价。
func TestExecutionPathRuleIsRegisteredForReport(t *testing.T) {
	root := t.TempDir()
	execFixture(t, root, map[string]string{
		"ptc/ptc.go": ptcPkg,
		"app/app.go": "package app\n\nimport \"github.com/yi-nology/agent-harness/ptc\"\n\n" +
			"func Run() string { return ptc.Run(\"prog\") }\n",
	})
	pol := &Policy{ExecutionPaths: &ExecutionPaths{}}
	pol.Path = filepath.Join(root, "architecture-policy.yaml")
	if err := os.WriteFile(pol.Path, []byte("execution_paths:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Run(root, pol)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res.Rules {
		if r.ID == executionPathRuleID {
			found = true
		}
	}
	if !found {
		t.Fatalf("执行路径规则必须登记进 res.Rules（否则违规判了但不回显），实得 %+v", res.Rules)
	}
}
