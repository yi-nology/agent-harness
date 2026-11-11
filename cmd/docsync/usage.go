package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// usage.go —— docs/USAGE.md 的机器口径块校验。
//
// # 这条门禁补的是哪一类失效
//
// README 的包表由 docsync 判（包名 / 行数 / 依赖），但 **USAGE.md 里的
// 数字没人判**：它长期写着「27 个能力包」「modules 段登记 24 个」，
// 而实际是 41 / 43——漂了几个月没人发现，因为它不参与任何门禁。
//
// 本仓的纪律是「文档里的每个数字，要么被机器判，要么在旁边写明不受机器管」。
// 故给 USAGE 的三处关键数字装上判据：
//
//  1. 本仓包数（README 包表口径）——判相等；
//  2. policy modules 段登记的包目录数——判相等；
//  3. 安装行的版本——必须写 @latest（钉死的版本号会随发版过期，而它不在
//     任何门禁的判据面里）。
//
// # 为什么不给「本文覆盖 N 个能力包」装判据
//
// 那个数是**文档取舍**（本文讲哪几个包），不是仓库事实——它由作者决定
// 讲不讲某个包，机器判不了「该不该讲」。故本门禁只判**仓库事实**那类数字：
// 拿不准主意的数字不硬判，能判的一个都不放过。

const (
	// usagePath USAGE.md 的路径。
	usagePath = "docs/USAGE.md"
	// policyPath 门禁声明面的路径（modules 段在这里）。
	policyPath = "architecture-policy.yaml"
)

var (
	// usagePkgCountRe 抓 USAGE 里的包数声明。
	usagePkgCountRe = regexp.MustCompile(`本仓共\s*\*\*(\d+)\*\*\s*个包`)
	// usageModulesCountRe 抓 USAGE 里的 policy 登记数声明。
	usageModulesCountRe = regexp.MustCompile(`modules\x60 段登记\s*\*\*(\d+)\*\*\s*个包目录`)
	// usageInstallRe 抓安装行及其版本。
	usageInstallRe = regexp.MustCompile(`go get github\.com/yi-nology/goagent-kernel@(\S+)`)
)

// checkUsage 校验 USAGE.md 的机器口径块。
//
// pkgCount 是 README 包表行数（本包已解析出 rows，直接传入，避免二次读盘）。
//
// # 存在性口径：USAGE.md 与 architecture-policy.yaml **成对**
//
// 这条门禁不能只按「USAGE.md 在不在」来决定跑不跑——那样会出现两种坏形态：
//
//   - 只看 USAGE.md：它被删掉 ⇒ 整条判据静默失效，门禁变**绿**（而不是红）。
//     这正是本仓反复说的「门禁的失败形态必须是变红，不能是变绿」。
//   - 无条件要求它在：docsync 也被用来校验**最小合成仓**（测试夹具），
//     那里根本没有 USAGE.md ⇒ 一条与「本仓事实」无关的门禁把别的仓也判红。
//
// 故用 policy 文件当「这是不是本仓」的标记：
//
//	两个都没有 ⇒ 不是本仓，跳过（不报错，也不假装判过）；
//	有 policy 没 USAGE ⇒ 红灯（判据面被删了，必须显式补回而不是静默失守）；
//	有 USAGE 没 policy ⇒ 红灯（USAGE 里的 policy 登记数无从核对）。
//
// 「跳过」必须**可观测**：run() 会在输出里说明跳过了哪一段（见 usageSkipped）。
func checkUsage(pkgCount int) (problems []string, checked bool, err error) {
	usageExists := fileExists(usagePath)
	policyExists := fileExists(policyPath)
	switch {
	case !usageExists && !policyExists:
		return nil, false, nil // 不是本仓：没有可判的声明面。
	case !usageExists:
		return []string{fmt.Sprintf(
			"%s 不存在——本仓的 USAGE 口径块是这道门禁的判据面，删掉它等于让判据静默失效"+
				"（%s 在，说明这是本仓）", usagePath, policyPath)}, true, nil
	case !policyExists:
		return []string{fmt.Sprintf(
			"%s 不存在——无法核对 %s 里声明的 policy 登记数", policyPath, usagePath)}, true, nil
	}

	b, err := os.ReadFile(usagePath)
	if err != nil {
		return nil, false, err
	}
	text := string(b)

	// ① 包数声明：必须存在，且等于 README 包表行数。
	m := usagePkgCountRe.FindStringSubmatch(text)
	if m == nil {
		problems = append(problems, fmt.Sprintf(
			"%s: 找不到包数声明——请写成「本共 **N** 个包（README 包表口径）」的机器口径块",
			usagePath))
	} else {
		got, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			return nil, true, fmt.Errorf("%s: 包数声明不是整数: %q", usagePath, m[1])
		}
		if got != pkgCount {
			problems = append(problems, fmt.Sprintf(
				"%s: 包数写 %d，实际 %d（README 包表口径）", usagePath, got, pkgCount))
		}
	}

	// ② policy 登记数声明：必须存在，且等于 modules 段的条目数。
	modules, err := countPolicyModules(policyPath)
	if err != nil {
		return nil, true, err
	}
	m = usageModulesCountRe.FindStringSubmatch(text)
	if m == nil {
		problems = append(problems, fmt.Sprintf(
			"%s: 找不到 policy 登记数声明——请写成「modules 段登记 **N** 个包目录」",
			usagePath))
	} else {
		got, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			return nil, true, fmt.Errorf("%s: policy 登记数声明不是整数: %q", usagePath, m[1])
		}
		if got != modules {
			problems = append(problems, fmt.Sprintf(
				"%s: policy 登记数写 %d，实际 %d（%s 的 modules 段）",
				usagePath, got, modules, policyPath))
		}
	}

	// ③ 安装行版本：必须写 @latest。
	//
	// 钉死版本号的安装行会随每次发版过期，而它不在任何门禁的判据面里——
	// 于是读者按文档装到旧版本，而文档看起来是新的。
	m = usageInstallRe.FindStringSubmatch(text)
	if m == nil {
		problems = append(problems, fmt.Sprintf(
			"%s: 找不到安装行（go get github.com/yi-nology/goagent-kernel@...）",
			usagePath))
	} else if m[1] != "latest" {
		problems = append(problems, fmt.Sprintf(
			"%s: 安装行钉死了版本 %q——请写 @latest（钉死的版本会随发版过期且无人判）",
			usagePath, m[1]))
	}

	return problems, true, nil
}

// fileExists 判断路径是否存在且是普通文件。
//
// 用它而不是 os.Stat 之后判 Mode，是因为判据要的是「有没有这个文件」，
// 而目录（同名目录）不该被当成一份可读文档。
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// countPolicyModules 数 policy 的 modules 段登记了多少个包目录。
//
// 判据是**段内**的 `  - id:` 条目：modules 段到下一个顶层键为止。
// 为什么自己数而不引 YAML 依赖：本仓的门禁解析器（archcheck）已经有一套，
// 但 docsync 是独立 CLI，引依赖就多一份依赖面；而这里要判的只是
// 「一个顶层键下面的二级列表有多长」——逐行扫足够，且不引依赖。
func countPolicyModules(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(b), "\n")
	inModules := false
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "modules:") {
			inModules = true
			continue
		}
		if !inModules {
			continue
		}
		// 下一个顶层键 ⇒ modules 段结束。
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "#") {
			break
		}
		if strings.HasPrefix(strings.TrimSpace(line), "- id:") {
			count++
		}
	}
	return count, nil
}
