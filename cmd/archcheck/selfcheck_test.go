// selfcheck_test.go —— policy 自校验与例外到期机制的反向验证用例。
//
// # 为什么「门禁检查门禁」也要有反向验证
//
// 自校验要抓的是「规则存在但判不了东西」这类失效。它们的共同点是：
// **门禁变绿**。规则照常在报告里被数成「7 条规则」，severity 照常是 error，
// 唯独一件事都没判。
//
// 而自校验本身也会失效——它也是代码。所以每一条判据都要有「错误实现会红」
// 的用例，否则自校验只是一段看起来很严谨的代码。
//
// # 本文件覆盖的真实事故
//
//	- guardset 登记在 modules 段却漏在 thresholds.scan_roots 之外：
//	  「超长文件」「god object」两条阈值对它从来不生效。
//	- exceptions 的 until 字段被解析后就再没人看过：到期不豁免，
//	  债可以无限期挂着，而每一条都还躺在 policy 里看着像有人管过。

package main

import (
	"strings"
	"testing"
	"time"
)

// containsText 判断回显里是否含某段文字。
func containsText(vs []violation, sub string) bool {
	for _, v := range vs {
		if strings.Contains(v.text, sub) {
			return true
		}
	}
	return false
}

// TestSelfCheckCatchesRuleWithoutBody 没有执行体的规则必须被报出来。
//
// 这是最隐蔽的一类门禁失效：policy 里写了一条 severity: error 的规则，但
// 它的 forbid_regex 字段名拼错了 → 反序列化后两个正则都是空串 → 旧实现
// `return nil, nil` 把它静默跳过。规则还在、severity 还是 error、报告里还
// 写着「N 条规则」，但它一条都判不了。
func TestSelfCheckCatchesRuleWithoutBody(t *testing.T) {
	pol := policyFile{Rules: []rule{{ID: "x", Severity: "error"}}}
	vs := selfCheckViolations(t.TempDir(), pol)
	if !containsText(vs, "没有执行体") {
		t.Fatalf("无执行体的规则必须被自校验报出，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckCatchesDuplicateRuleID 规则 id 重复必须被报出来。
//
// id 重复的后果：豁免按 rule+path+line 三元组匹配，两条同名规则会互相顶替，
// 且告警无法定位到具体是哪一条判的。
func TestSelfCheckCatchesDuplicateRuleID(t *testing.T) {
	pol := policyFile{Rules: []rule{
		{ID: "dup", ForbidRegex: "a"},
		{ID: "dup", ForbidRegex: "b"},
	}}
	vs := selfCheckViolations(t.TempDir(), pol)
	if !containsText(vs, "重复") {
		t.Fatalf("重复的规则 id 必须被自校验报出，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckCatchesModuleMissingFromScanRoots 阈值判定面漏包必须被报出来。
//
// 本仓真实发生过：guardset 登记在 modules 段（依赖方向门禁管着它），却漏在
// thresholds.scan_roots 之外，于是「超长文件」「god object」两条阈值对它
// 从来不生效——两条都只差一个字面量，谁也不会发现。
func TestSelfCheckCatchesModuleMissingFromScanRoots(t *testing.T) {
	pol := policyFile{
		Modules: []moduleDecl{{ID: "ghost", Roots: []string{"ghost"}, Requires: []string{}}},
		Thresholds: threshold{
			Enforce:           true,
			MaxFileLines:      400,
			MaxMethodsPerType: 25,
			ScanRoots:         []string{"other"},
		},
	}
	vs := selfCheckViolations(t.TempDir(), pol)
	if !containsText(vs, "scan_roots") {
		t.Fatalf("漏在 scan_roots 之外的包必须被自校验报出，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckAcceptsCoveredModule 反向：判定面覆盖到的包不得被误报。
//
// 自校验最容易的失败不是漏判，而是把没问题的 policy 判成有问题——维护者
// 于是学会忽略它，规则随即名存实亡。
func TestSelfCheckAcceptsCoveredModule(t *testing.T) {
	pol := policyFile{
		Modules: []moduleDecl{{ID: "ok", Roots: []string{"ok"}, Requires: []string{}}},
		Thresholds: threshold{
			Enforce:           true,
			MaxFileLines:      400,
			MaxMethodsPerType: 25,
			ScanRoots:         []string{"ok", "cmd"},
		},
	}
	vs := selfCheckViolations(t.TempDir(), pol)
	if containsText(vs, "scan_roots") {
		t.Fatalf("判定面已覆盖的包不得被误报，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckCatchesIncompleteException 例外登记缺 until / removal 必须被报出来。
//
// 「写个日期」的例外若不检查 until，日期就是装饰；「写个拆除条件」若不检查，
// 例外就是赖账。两者都是 policy 声明过却没执行的纪律。
func TestSelfCheckCatchesIncompleteException(t *testing.T) {
	pol := policyFile{
		Rules: []rule{{ID: "r1", ForbidRegex: "x"}},
		Exceptions: []policyException{
			{Rule: "r1", Path: "a.go", Line: 1, Violation: "x", Reason: "r"},
		},
	}
	vs := selfCheckViolations(t.TempDir(), pol)
	if !containsText(vs, "until") {
		t.Fatalf("缺 until 的例外必须被报出，实得 %v", textsOf(vs))
	}
	if !containsText(vs, "removal") {
		t.Fatalf("缺 removal 的例外必须被报出，实得 %v", textsOf(vs))
	}
}

// TestSelfCheckCatchesExceptionOnUnknownRule 例外指向不存在的规则必须被报出来。
func TestSelfCheckCatchesExceptionOnUnknownRule(t *testing.T) {
	pol := policyFile{
		Rules: []rule{{ID: "r1", ForbidRegex: "x"}},
		Exceptions: []policyException{
			{Rule: "ghost", Path: "a.go", Line: 1, Violation: "x", Reason: "r", Until: "2099-01-01", Removal: "rm"},
		},
	}
	vs := selfCheckViolations(t.TempDir(), pol)
	if !containsText(vs, "不存在的规则") {
		t.Fatalf("指向不存在规则的例外必须被报出，实得 %v", textsOf(vs))
	}
}

// TestExceptionExpiryUntilTakesEffect until 必须真的生效。
//
// 旧实现把 until 解析成字段后就再没人看过它——到期不豁免。于是「该在
// 2026 年清偿的债」可以无限期豁免下去，且每一条都还躺在 policy 里看着像
// 有人管过。这是 policy 声明过却没执行的纪律。
func TestExceptionExpiryUntilTakesEffect(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

	expired := policyException{Until: "2026-01-31"}
	ok, why := exceptionExpiry(expired, now)
	if !ok {
		t.Fatal("已过期（until=2026-01-31，now=2026-10-19）的例外必须判为过期")
	}
	if why == "" {
		t.Fatal("过期必须带可读说明（回显要告诉人它不再豁免）")
	}

	fresh := policyException{Until: "2027-01-31"}
	if ok, _ := exceptionExpiry(fresh, now); ok {
		t.Fatal("未到期（until=2027-01-31）的例外不得判为过期")
	}

	// 不可比较的形态（版本号）：机器判不了，但不得误判为过期。
	ver := policyException{Until: "v1.2.0"}
	if ok, _ := exceptionExpiry(ver, now); ok {
		t.Fatal("版本号形态的 until 不得被判为过期")
	}
}
