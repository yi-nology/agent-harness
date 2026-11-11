package archcheck

import (
	"fmt"
	"path/filepath"
	"strings"
)

// support.go —— 门禁装配的支撑件（判据之外的取值与路径解析）。
//
// 单独成文件是为了让 archcheck.go 保持在结构阈值之内：
// 「文件超长」这个阈值判的是**文件是否还读得动**，而 Run() 已经足够长——
// 继续往里塞辅助函数只会让读者在主线逻辑里迷路。

func CheckDependencyDirection(root string, pol *Policy) ([]Violation, error) {
	if pol == nil {
		return nil, fmt.Errorf("policy 为空")
	}
	if !pol.DepDir.on() {
		return nil, nil
	}
	return dependencyViolations(root, pol.Modules, pol.DepDir)
}

// exceptionKey 是例外匹配键：rule + path + line 三元组。
func exceptionKey(rule, path string, line int) string {
	return fmt.Sprintf("%s|%s|%d", rule, path, line)
}

// sevOrError 规则级别缺省即 error：悬空的 warn 等于没有规则。
func sevOrError(s string) string {
	if s == "" {
		return "error"
	}
	return s
}

// depDirDesc 取依赖方向规则的说明，policy 未写时用兜底文案。
func depDirDesc(d DependencyDirection) string {
	if d.Desc != "" {
		return d.Desc
	}
	return "包间依赖必须先在 policy modules 段登记 requires"
}

// PolicyPath 归一 policy 文件路径（相对仓库根），供报告与自校验定位。
func PolicyPath(root string, pol *Policy) string {
	if pol == nil || pol.Path == "" {
		return "architecture-policy.yaml"
	}
	if rel, err := filepath.Rel(root, pol.Path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return pol.Path
}
