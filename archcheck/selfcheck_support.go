// selfcheck_support.go —— policy 自校验的支撑函数（覆盖关系 / 豁免段 / 例外到期）。
//
// 从 selfcheck.go 抽出：那边的主体是「按 policy 段顺序逐条判」，这三件是它调用的
// 工具。抽出的直接原因是 selfcheck.go 已顶到 max_file_lines（400 行），而**它还要
// 继续长出判据**——阈值一旦逼着人停止加判据，那条阈值就在保护错误的那个量。
//
// 划界按**用途**而不是按行数：这里全是「给一个声明式输入、回答一个是非问题」
// 的纯函数，不认识 policy 的段顺序，也不产生 Violation。

package archcheck

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// coveredByAnyRoot 报告某个包根是否被 thresholds.scan_roots 里的某一项**覆盖**。
//
// 覆盖是**祖先关系**，不是逐字相等：scan_roots 里的 "cmd" 覆盖 "cmd/docsync"。
//
// 为什么必须按祖先判（issue #35 顺带修）：旧实现逐字比相等，于是「登记了
// cmd/ptcchild 这个模块、而 scan_roots 写的是 cmd」会**假红**——阈值其实
// 覆盖着它（scan 是按目录递归走的），报错却逼人把每个 cmd 子目录逐个列一遍。
// 那是「假红」最坏的形态：判据把正确的工作判成错的，而唯一的解是给清单灌水
// 直到它和目录树同构。量纲要与前提对齐：这里问的是「有没有人扫它」，
// 不是「有没有逐字写它」。
func coveredByAnyRoot(scanRoots []string, root string) bool {
	root = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(root)), "/")
	for _, sr := range scanRoots {
		sr = strings.TrimSuffix(filepath.ToSlash(strings.TrimSpace(sr)), "/")
		if sr == "" {
			continue
		}
		if sr == "." || root == sr || strings.HasPrefix(root, sr+"/") {
			return true
		}
	}
	return false
}

// ignoreSection 是一段带目录级豁免的 policy 段（报错要点名是哪一段）。
type ignoreSection struct {
	name string
	dirs []string
}

// ignoreDirSections 取全部「有目录级豁免」的段。
//
// 集中在这里而不是在判定处逐个写：新增一段豁免时，忘掉下面那条自校验的
// 代价是「那一段的豁免永远不受管辖」——而豁免不受管辖正是这条自校验要防的事。
func ignoreDirSections(pol *Policy) []ignoreSection {
	out := []ignoreSection{{"dependency_direction", pol.DepDir.IgnoreDirs}}
	if pol.SandboxProviders != nil {
		out = append(out, ignoreSection{"sandbox_providers", pol.SandboxProviders.IgnoreDirs})
	}
	if pol.ExecutionPaths != nil {
		out = append(out, ignoreSection{"execution_paths", pol.ExecutionPaths.IgnoreDirs})
	}
	return out
}

// exceptionExpiry 判一条例外是否已到期。
//
// until 支持两种形态：
//   - 可比较的日期：2026-01-31 / 2026-01 → 与 now 比，过期即不再豁免
//   - 不可比较的字符串（版本号如 v1.2.0）→ 机器判不了，只保证它非空
//
// 返回 expired 与人类可读的说明。
func exceptionExpiry(ex Exception, now time.Time) (bool, string) {
	until := strings.TrimSpace(ex.Until)
	if until == "" {
		return false, ""
	}
	for _, layout := range []string{"2006-01-02", "2006-01", "2006/01/02"} {
		if t, err := time.Parse(layout, until); err == nil {
			if now.After(t) {
				return true, fmt.Sprintf("已于 %s 到期，不再豁免", until)
			}
			return false, ""
		}
	}
	return false, ""
}
