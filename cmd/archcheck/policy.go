// policy 文件的 schema：规则、阈值、例外登记的反序列化目标。

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// methodRe 匹配方法定义（排除普通函数与接口声明）：`func (recv T) Name(...)`
var methodRe = regexp.MustCompile(`^func\s+\(([^)]*)\)\s+[A-Za-z_]\w*`)

type negFollow struct {
	Anchor       string `yaml:"anchor"`
	ForbidFollow string `yaml:"forbid_follow"`
}

type rule struct {
	ID           string      `yaml:"id"`
	Desc         string      `yaml:"desc"`
	Severity     string      `yaml:"severity"` // error（默认，违反即非零退出）| warn（报告口径，不阻断）
	Scope        []string    `yaml:"scope"`
	ScopeExclude []string    `yaml:"scope_exclude"`
	Include      []string    `yaml:"include"`
	IgnoreTest   bool        `yaml:"ignore_test"`
	OnlyTest     bool        `yaml:"only_test"` // 只扫测试文件（配合 severity: warn 做夹具体检）
	ForbidImport string      `yaml:"forbid_import_regex"`
	ForbidRegex  string      `yaml:"forbid_regex"`
	NegFollow    []negFollow `yaml:"negative_follow"`
}

type threshold struct {
	MaxFileLines      int      `yaml:"max_file_lines"`
	MaxMethodsPerType int      `yaml:"max_methods_per_type"`
	ScanRoots         []string `yaml:"scan_roots"`
	Enforce           bool     `yaml:"enforce"` // true=超限即非零退出；false=仅报告
}

type policyException struct {
	Rule      string `yaml:"rule"`
	Path      string `yaml:"path"`
	Line      int    `yaml:"line"`
	Violation string `yaml:"violation"`
	Reason    string `yaml:"reason"`
	Until     string `yaml:"until"`
	Removal   string `yaml:"removal"`
}

// facadeRuleID 门面包不可变规则的 ID。所有门面包共用一条规则 ID：它们守的是同一条
// 纪律（转发到上游、不得在本仓长实现），逐包差异体现在 violation 的 path 与 text 里。
const facadeRuleID = "harness-facade-stays-facade"

type policyFile struct {
	Version    int               `yaml:"version"`
	Modules    []struct{}        `yaml:"modules"`
	Rules      []rule            `yaml:"rules"`
	Facades    []facadeRule      `yaml:"facades"`
	Thresholds threshold         `yaml:"thresholds"`
	Exceptions []policyException `yaml:"exceptions"`
}

type violation struct {
	rule string
	path string
	line int
	text string
	kind string
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "architecture-policy.yaml")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("未找到 architecture-policy.yaml（从 %s 向上查找失败）", dir)
		}
		dir = parent
	}
}

// iterFiles 遍历 root 下匹配 include 的文件；ignoreTest 时跳过 _test.go。
