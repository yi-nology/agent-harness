// docsblock.go —— 文档机器口径块判定（同 docsync 对 README/USAGE 的口径）。
//
// 文档里的每个数字，要么被机器判，要么在旁边写明不受机器管。本门禁新增
// 两处数字口径：
//
//   - docs/api-compat.md：「api-lifecycle.yaml 登记 **N** 个包」——判等；
//   - docs/USAGE.md：口径表「生命周期登记」行——判等（USAGE 在才判，
//     USAGE 与 policy 的成对存在性由 docsync 管，这里不重复判）。

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

const (
	apiCompatPath = "docs/api-compat.md"
	usagePath     = "docs/USAGE.md"
)

var (
	// apiCompatCountRe 抓 api-compat.md 里的清单登记数口径块。
	apiCompatCountRe = regexp.MustCompile(`api-lifecycle\.yaml 登记\s*\*\*(\d+)\*\*\s*个包`)
	// usageLifecycleCountRe 抓 USAGE 口径表的生命周期登记行。
	usageLifecycleCountRe = regexp.MustCompile(`生命周期登记[^\n]*\*\*(\d+)\*\*`)
)

// fileRegular 判断路径是否存在且是普通文件。
func fileRegular(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// checkDocsBlocks 两处文档口径块判等。
func checkDocsBlocks(root string, m *Manifest) []string {
	var probs []string
	want := len(m.Packages)

	if !fileRegular(filepath.Join(root, apiCompatPath)) {
		probs = append(probs, apiCompatPath+
			" 不存在——它是分级判定规则与破坏性变更流程的声明面，清单没有配套文档等于只有判据没有口径")
	} else {
		b, err := os.ReadFile(filepath.Join(root, apiCompatPath))
		if err != nil {
			probs = append(probs, apiCompatPath+" 无法读取: "+err.Error())
		} else if got, ok := matchCount(apiCompatCountRe, string(b)); !ok {
			probs = append(probs, apiCompatPath+": 找不到清单登记数口径块——请写「api-lifecycle.yaml 登记 **N** 个包」")
		} else if got != want {
			probs = append(probs, fmt.Sprintf("%s: 清单登记数写 %d，实际 %d（%s 的 packages 段）",
				apiCompatPath, got, want, manifestName))
		}
	}

	if fileRegular(filepath.Join(root, usagePath)) {
		b, err := os.ReadFile(filepath.Join(root, usagePath))
		if err != nil {
			probs = append(probs, usagePath+" 无法读取: "+err.Error())
		} else if got, ok := matchCount(usageLifecycleCountRe, string(b)); !ok {
			probs = append(probs, usagePath+": 找不到「生命周期登记」口径行——请在口径表加一行（机器校验，make apilifecheck 判）")
		} else if got != want {
			probs = append(probs, fmt.Sprintf("%s: 生命周期登记写 %d，实际 %d（%s 的 packages 段）",
				usagePath, got, want, manifestName))
		}
	}
	return probs
}

// matchCount 取正则第一个捕获组并转成整数。
func matchCount(re *regexp.Regexp, text string) (int, bool) {
	m := re.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}
