package main

import (
	"strings"
	"testing"
)

// TestLooksLikeModulePathExcludesNonModules 钉住「什么不算 module 路径」。
//
// 这道门禁会扫描**整个文件内容**（不是只扫 import 块），所以仓里到处是
// 必须被排除的 token：测试夹具里的 http/https URL 字面量、协议相对地址、
// 注释里的上级相对路径。判据一旦把它们当依赖，门禁会永远红；反之若把真依赖
// 当非依赖，README 漏写就永远绿——两个方向都不能错。
func TestLooksLikeModulePathExcludesNonModules(t *testing.T) {
	notModule := []string{
		// 带 scheme 的 URL：第一段按 / 切开会得到 "http:"，含冒号 → 排除。
		"http://127.0.0.1:8900/api",
		"https://example.com/hook",
		// 协议相对地址：以 / 开头 → 排除。
		"/host/meta-data/v1",
		// 上级相对路径：以 . 开头 → 排除。
		"../escape",
		"../../etc/passwd",
		// 纯 stdlib 标识：无斜杠、无点 → 排除。
		"context",
		"runtime/debug",
		// 只有一个点且不在第一段：不是 module host 形态。
		"pkg/errors",
	}
	for _, p := range notModule {
		if looksLikeModulePath(p) {
			t.Errorf("%q 不该被当成 module 路径", p)
		}
	}
}

// TestLooksLikeModulePathAcceptsModules 与上一个用例互为反面：一漏就出静默漏判。
func TestLooksLikeModulePathAcceptsModules(t *testing.T) {
	isModule := []string{
		"github.com/oekit/pkg",
		"github.com/oekit/pkg/errors",
		"github.com/yi-nology/agent-harness-fork/x", // 前缀近似自身，但不能当自身
		"github.com/Azure/azure-sdk/v2",             // 含大写：Go 允许，漏判方向反了
		"gopkg.in/yaml.v3",
		"mvdan.cc/sh/v3",
	}
	for _, p := range isModule {
		if !looksLikeModulePath(p) {
			t.Errorf("%q 应被识别为 module 路径（漏判会让 README 漏写依赖仍能过）", p)
		}
	}
}

// TestSelfModuleExclusionNeedsSlashBoundary 钉住「排除自身」必须有路径边界。
//
// 纯 strings.HasPrefix(path, selfModule) 会把 agent-harness-fork 这类**真实
// 外部依赖**当成自身静默排除。这是漏判方向——与门禁职责正好相反，且无声。
func TestSelfModuleExclusionNeedsSlashBoundary(t *testing.T) {
	// 自身与其子包：排除
	for _, p := range []string{
		"github.com/yi-nology/agent-harness",
		"github.com/yi-nology/agent-harness/contract",
		"github.com/yi-nology/agent-harness/checkpoint",
	} {
		if !isSelfPath(p) {
			t.Errorf("%q 应判为自身并排除", p)
		}
	}

	// 前缀相近但**不是**自身：必须当外部依赖
	for _, p := range []string{
		"github.com/yi-nology/agent-harness-fork/x",
		"github.com/yi-nology/agent-harnessx",
		"github.com/yi-nology/agent-harness2/tools",
	} {
		if isSelfPath(p) {
			t.Errorf("%q 不是自身却被排除了——纯 HasPrefix 正是这个 bug", p)
		}
	}
}

// TestNormalizeDepCollapsesSubpaths 钉据归并口径：同一仓的不同子路径应归一，
// 否则 goja 与 goja/xxx 会被算成两个依赖，门禁逼着 README 写重复项。
func TestNormalizeDepCollapsesSubpaths(t *testing.T) {
	cases := map[string]string{
		"github.com/oekit/pkg":               "oekit/pkg",
		"github.com/oekit/pkg/errors":        "oekit/pkg",
		"github.com/oekit/pkg/jsonrepair":    "oekit/pkg",
		"gopkg.in/yaml.v3":                   "gopkg.in/yaml.v3",
		"santhosh-tekuri/jsonschema/v6":      "santhosh-tekuri/jsonschema",
		"mvdan.cc/sh/v3":                     "sh/v3",
		"github.com/oagentkit/guardx/egress": "oagentkit/guardx",
	}
	for in, want := range cases {
		if got := normalizeDep(in); got != want {
			t.Errorf("normalizeDep(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestSuffixMatchTolerance 钉住「简写可接受」的边界：README 写 jsonschema 能匹配
// santhosh-tekuri/jsonschema，但方向不能反（实测短名不得匹配无关依赖）。
func TestSuffixMatchTolerance(t *testing.T) {
	if !suffixMatch("jsonschema", "santhosh-tekuri/jsonschema") {
		t.Error("README 写简写 jsonschema 应匹配实际路径")
	}
	if !suffixMatch("goja", "dop251/goja") {
		t.Error("README 写简写 goja 应匹配实际路径")
	}
	if !suffixMatch("oekit/pkg", "oekit/pkg") {
		t.Error("完全相同应匹配")
	}
	if suffixMatch("jsonrepair", "santhosh-tekuri/jsonschema") {
		t.Error("不同名的简写不应匹配——否则漏写依赖能蒙混过关")
	}
	if suffixMatch("santhosh-tekuri/jsonschema", "jsonschema") {
		t.Error("方向不能反：README 不得比实际更详细却仍算匹配")
	}
}

// TestCountLinesHandlesMissingTrailingNewline 钉住行数口径不被指标自指拖偏。
//
// 单纯数换行符时，丢了结尾换行的文件会被少算一行，于是「改了文件并把 README
// 同步成少算的值」就能蒙混过关——指标自指，让门禁在自己改动的量纲上失准。
func TestCountLinesHandlesMissingTrailingNewline(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"a\nb\nc\n", 3},
		{"a\nb\nc", 3},
		{"a\n", 1},
		{"a", 1},
		{"", 0},
		{"\n", 1},
		{"a\n\nb", 3},
	}
	for _, c := range cases {
		if got := countLines(c.in); got != c.want {
			t.Errorf("countLines(%q) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// TestCountLinesMatchesWcOnNewlineTerminatedFiles 保证口径变更对现存文件零影响：
// 当前全部生产文件都以换行结尾，新旧口径必须完全一致，否则 README 要整表重算。
func TestCountLinesMatchesWcOnNewlineTerminatedFiles(t *testing.T) {
	for _, s := range []string{"a\nb\nc\n", "x\n", "one\ntwo\nthree\nfour\n"} {
		if got, want := countLines(s), strings.Count(s, "\n"); got != want {
			t.Errorf("以换行结尾时 countLines(%q)=%d 应等于 wc -l 的 %d", s, got, want)
		}
	}
}
