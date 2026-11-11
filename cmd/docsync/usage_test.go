package main

// 本文件是 **USAGE.md 机器口径块**那条判据的用例。
//
// # 为什么单独一个文件
//
// 这条判据是本轮新加的（issue #76）：USAGE 里的包数 / policy 登记数 / 安装行版本
// 此前长期无人判，漂了几个月（27/24 → 实际 41/43）。新判据必须自带
// **负对照**——否则「规则存在但从不触发」与「规则正常工作」在日志里长得一样。
//
// # 用例矩阵
//
//	正向：三类数字都对 ⇒ 绿
//	负向：三类数字各错一种 ⇒ 红（错在哪句要钉住）
//	存在性：policy 在而 USAGE 不在 ⇒ 红（判据面被删不能静默失效）
//	存在性：两者都不在 ⇒ 跳过（合成仓不是本仓），且可观测

import (
	"strconv"
	"strings"
	"testing"
)

// bt 是反引号字符。
//
// 为什么要写成 \x60 而不是直接写反引号：Go 的原始字符串（反引号包裹）里
// 不能出现反引号本身，而本文件的夹具需要用反引号包住包名（README 包表格式）
// 与 modules（USAGE 机器口径块的格式）。用字符转义，源码里一个反引号都不出现。
const bt = "\x60"

// usagePolicyFixture 摆一份带 modules 段的 policy（登记 2 个包）。
const usagePolicyFixture = "version: 1\n" +
	"modules:\n" +
	"  - id: alpha\n" +
	"    roots: [alpha]\n" +
	"  - id: beta\n" +
	"    roots: [beta]\n" +
	"rules:\n" +
	"  - id: harness-demo\n" +
	"    desc: 演示用规则\n"

// usageDocFixture 拼一份 USAGE.md。三个参数分别是包数、policy 登记数、安装行版本。
func usageDocFixture(pkgCount, modulesCount int, version string) string {
	lines := []string{
		"# 用法",
		"",
		"    go get github.com/yi-nology/goagent-kernel@" + version,
		"",
		"本仓共 **" + strconv.Itoa(pkgCount) + "** 个包（README 包表口径）。",
		// 反引号包住 modules 是真实 USAGE 的写法（判据正则按这个形态匹配）。
		"policy 的 " + bt + "modules" + bt + " 段登记 **" +
			strconv.Itoa(modulesCount) + "** 个包目录。",
		"",
	}
	return strings.Join(lines, "\n")
}

// usageRow 拼一行 README 包表（包名要用反引号包住）。
func usageRow(pkg string) string {
	return "| " + bt + pkg + bt + " | 3 | 无 | 示例包 |"
}

// usageFixtureRepo 摆一份「像本仓」的最小仓：README 包表 + policy + USAGE。
func usageFixtureRepo(t *testing.T, pkgRows []string, policy string, usage string) {
	t.Helper()
	files := map[string]string{}
	if policy != "" {
		files["architecture-policy.yaml"] = policy
	}
	if usage != "" {
		files["docs/USAGE.md"] = usage
	}
	for i := range pkgRows {
		files[string(rune('a'+i))+"/a.go"] = "package p\n\nvar X = 1\n"
	}
	fixtureRepo(t, pkgTable(pkgRows...), files)
}

// TestUsageAcceptsConsistentDoc 三类数字都对 ⇒ 绿。
//
// 这条是正向锚：没有它，其余「该红」的用例可以靠「永远红」全部通过。
func TestUsageAcceptsConsistentDoc(t *testing.T) {
	usageFixtureRepo(t,
		[]string{usageRow("alpha")},
		usagePolicyFixture,
		usageDocFixture(1, 2, "latest"))
	problems, checked, err := checkUsage(1)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if !checked {
		t.Fatal("有 policy 与 USAGE 时必须进入判定（checked=false 说明判据被静默跳过）")
	}
	if len(problems) != 0 {
		t.Fatalf("一致的 USAGE 不该有问题: %v", problems)
	}
}

// TestUsageCatchesPkgCountDrift 包数写错 ⇒ 红，且报的是包数那一句。
func TestUsageCatchesPkgCountDrift(t *testing.T) {
	usageFixtureRepo(t,
		[]string{usageRow("alpha")},
		usagePolicyFixture,
		usageDocFixture(7, 2, "latest")) // 实际 1，写 7
	problems, checked, err := checkUsage(1)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if !checked || len(problems) == 0 {
		t.Fatalf("包数写错必须报出: %v", problems)
	}
	if !strings.Contains(problems[0], "包数写") {
		t.Fatalf("应报「包数写」，实得 %q", problems[0])
	}
}

// TestUsageCatchesModulesCountDrift policy 登记数写错 ⇒ 红。
func TestUsageCatchesModulesCountDrift(t *testing.T) {
	usageFixtureRepo(t,
		[]string{usageRow("alpha")},
		usagePolicyFixture,
		usageDocFixture(1, 9, "latest")) // 实际 2，写 9
	problems, checked, err := checkUsage(1)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if !checked || len(problems) == 0 {
		t.Fatalf("policy 登记数写错必须报出: %v", problems)
	}
	if !strings.Contains(problems[0], "policy 登记数写") {
		t.Fatalf("应报「policy 登记数写」，实得 %q", problems[0])
	}
}

// TestUsageRejectsPinnedInstallVersion 安装行钉死版本 ⇒ 红。
//
// 这条的判据价值：钉死的版本号不在任何其他门禁的判据面里，
// 文档看起来是新的，读者装的却是旧版。
func TestUsageRejectsPinnedInstallVersion(t *testing.T) {
	usageFixtureRepo(t,
		[]string{usageRow("alpha")},
		usagePolicyFixture,
		usageDocFixture(1, 2, "v0.31.0"))
	problems, checked, err := checkUsage(1)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if !checked || len(problems) == 0 {
		t.Fatalf("安装行钉死版本必须报出: %v", problems)
	}
	if !strings.Contains(problems[0], "安装行钉死了版本") {
		t.Fatalf("应报「安装行钉死了版本」，实得 %q", problems[0])
	}
}

// TestUsageFailsWhenDocMissingButPolicyPresent 判据面被删 ⇒ 红，不是静默绿。
//
// # 为什么这条最要紧
//
// 若实现写成「USAGE.md 不在就跳过」，那么删掉 USAGE.md 这道门禁就自动失效，
// 且失败形态是变绿——正是本仓纪律反复警告的那件事。
// 故：policy 在（说明这是本仓）而 USAGE 不在 ⇒ 必须显式报红。
func TestUsageFailsWhenDocMissingButPolicyPresent(t *testing.T) {
	usageFixtureRepo(t,
		[]string{usageRow("alpha")},
		usagePolicyFixture,
		"") // USAGE.md 不存在
	problems, checked, err := checkUsage(1)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if !checked || len(problems) == 0 {
		t.Fatal("USAGE.md 缺失必须报红（判据面被删不能静默失效）")
	}
	if !strings.Contains(problems[0], "不存在") {
		t.Fatalf("应报「不存在」，实得 %q", problems[0])
	}
}

// TestUsageSkipsWhenNeitherPresent 两者都不在 ⇒ 跳过且不报错。
//
// docsync 也被用来校验最小合成仓，那里既没有 policy 也没有 USAGE——
// 那不是「判据失灵」，是「没有可判的声明面」。但跳过必须可观测
// （checked=false），否则与「判过了」在日志里长得一样。
func TestUsageSkipsWhenNeitherPresent(t *testing.T) {
	usageFixtureRepo(t,
		[]string{usageRow("alpha")},
		"", "")
	problems, checked, err := checkUsage(1)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if checked {
		t.Fatal("没有 policy 与 USAGE 时应标记为未判定")
	}
	if len(problems) != 0 {
		t.Fatalf("跳过时不该有问题: %v", problems)
	}
}

// TestRunCatchesUsageDriftEndToEnd 端到端：run() 必须把 USAGE 的漂移带出来。
//
// 前面的用例直接调 checkUsage——接线本身（run 到底有没有调它）没被证过。
// 这条补上那一段：漂移必须出现在 run() 的错误里。
func TestRunCatchesUsageDriftEndToEnd(t *testing.T) {
	usageFixtureRepo(t,
		[]string{usageRow("alpha")},
		usagePolicyFixture,
		usageDocFixture(7, 2, "latest")) // 包数写错
	err := run()
	if err == nil {
		t.Fatal("run() 必须把 USAGE 的漂移报出来")
	}
	if !strings.Contains(err.Error(), "包数写") {
		t.Fatalf("应报「包数写」，实得 %v", err)
	}
}
