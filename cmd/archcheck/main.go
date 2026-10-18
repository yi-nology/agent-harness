// Command archcheck 执行 architecture-policy.yaml 的可执行面。
//
// 存在意义：policy 文件若只当文档读，半年后就与代码事实脱节；把它做成
// `make architecture-check` 门禁，规则违反即非零退出。本仓的头号红线是
// harness-no-domain-vocabulary——内核不得长出领域知识。
//
// 正则一律 RE2（Go 原生不支持 (?!...)）；负向前置如需表达，用结构化
// negative_follow 字段，不写进正则。
//
// main() 只做装配；扫描判定在 scan.go，policy 反序列化在 policy.go。

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	report := false
	for _, a := range os.Args[1:] {
		if a == "--report" {
			report = true
		}
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	data, err := os.ReadFile(filepath.Join(root, "architecture-policy.yaml"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		os.Exit(2)
	}
	var pol policyFile
	if err := yaml.Unmarshal(data, &pol); err != nil {
		fmt.Fprintln(os.Stderr, "archcheck: 解析 policy 失败:", err)
		os.Exit(2)
	}

	var all []violation
	for _, r := range pol.Rules {
		vs, err := checkRule(root, r)
		if err != nil {
			fmt.Fprintln(os.Stderr, "archcheck:", err)
			os.Exit(2)
		}
		all = append(all, vs...)
	}

	// 门面包不可变：AST 判定（facade.go），不走正则规则表——这条纪律是结构性的。
	for _, fr := range pol.Facades {
		vs, err := facadeViolations(root, fr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "archcheck:", err)
			os.Exit(2)
		}
		all = append(all, vs...)
	}

	// severity 分流：error 违规阻断，warn 仅报告。
	sevOf := map[string]string{}
	for _, r := range pol.Rules {
		s := r.Severity
		if s == "" {
			s = "error"
		}
		sevOf[r.ID] = s
	}
	for _, fr := range pol.Facades {
		s := fr.Severity
		if s == "" {
			s = "error"
		}
		sevOf[facadeRuleID] = s
	}
	isBlocking := func(v violation) bool { return sevOf[v.rule] != "warn" }

	var blocking, warnings, excused int
	// exceptions 精确豁免：rule + path + line 三元组命中即放行（不做前缀模糊豁免）。
	exKey := map[string]policyException{}
	for _, ex := range pol.Exceptions {
		exKey[fmt.Sprintf("%s|%s|%d", ex.Rule, ex.Path, ex.Line)] = ex
	}
	var stillBlocked []violation
	for _, v := range all {
		if _, ok := exKey[fmt.Sprintf("%s|%s|%d", v.rule, v.path, v.line)]; ok {
			excused++
			continue
		}
		if isBlocking(v) {
			stillBlocked = append(stillBlocked, v)
			blocking++
		} else {
			warnings++
		}
	}

	if len(all) > 0 {
		if blocking > 0 {
			fmt.Fprintf(os.Stderr, "架构门禁失败：%d 处阻断级违反", blocking)
		} else {
			fmt.Fprintf(os.Stderr, "架构门禁：%d 处告警级（不阻断）", warnings)
		}
		if warnings > 0 && blocking > 0 {
			fmt.Fprintf(os.Stderr, "，另有 %d 处告警级", warnings)
		}
		if excused > 0 {
			fmt.Fprintf(os.Stderr, "，%d 处命中已登记例外（policy exceptions）", excused)
		}
		fmt.Fprintln(os.Stderr)
		byRule := map[string][]violation{}
		for _, v := range all {
			if _, ok := exKey[fmt.Sprintf("%s|%s|%d", v.rule, v.path, v.line)]; ok {
				continue
			}
			byRule[v.rule] = append(byRule[v.rule], v)
		}
		for _, r := range pol.Rules {
			vs := byRule[r.ID]
			if len(vs) == 0 {
				continue
			}
			s := sevOf[r.ID]
			fmt.Fprintf(os.Stderr, "\n规则 %s [%s]：%s（%d 处）\n", r.ID, s, r.Desc, len(vs))
			limit := len(vs)
			if !report && limit > 10 {
				limit = 10
			}
			for _, v := range vs[:limit] {
				fmt.Fprintf(os.Stderr, "  %s:%d  [%s] %s\n", v.path, v.line, v.kind, v.text)
			}
			if !report && len(vs) > limit {
				fmt.Fprintf(os.Stderr, "  … 另有 %d 处（--report 看全量）\n", len(vs)-limit)
			}
		}
		// 门面规则不在 rules 段里声明（它是 facades 段逐包配置的），单独回显。
		if vs := byRule[facadeRuleID]; len(vs) > 0 {
			fmt.Fprintf(os.Stderr, "\n规则 %s [%s]：%s（%d 处）\n",
				facadeRuleID, sevOf[facadeRuleID],
				"门面包只允许类型别名/常量别名/单条上游转发，实现归上游仓", len(vs))
			limit := len(vs)
			if !report && limit > 10 {
				limit = 10
			}
			for _, v := range vs[:limit] {
				fmt.Fprintf(os.Stderr, "  %s:%d  [%s] %s\n", v.path, v.line, v.kind, v.text)
			}
			if !report && len(vs) > limit {
				fmt.Fprintf(os.Stderr, "  … 另有 %d 处（--report 看全量）\n", len(vs)-limit)
			}
		}
		// 已登记例外回显：债是显式的，不静默。
		var shown int
		for _, ex := range pol.Exceptions {
			if shown >= 3 && !report {
				fmt.Fprintf(os.Stderr, "\n已登记例外 %d 条（--report 看全量），例：", len(pol.Exceptions))
			}
			if shown < 3 || report {
				fmt.Fprintf(os.Stderr, "\n  %s:%d [%s] %s —— %s（拆除条件：%s）",
					ex.Path, ex.Line, ex.Rule, ex.Violation, firstLine(ex.Reason), firstLine(ex.Removal))
			}
			shown++
		}
		if shown > 0 {
			fmt.Fprintln(os.Stderr)
		}
		if blocking > 0 {
			os.Exit(1)
		}
	}

	fmt.Printf("架构门禁通过（%d 条规则 + %d 个门面包，policy version %d；无阻断级违反）\n",
		len(pol.Rules), len(pol.Facades), pol.Version)

	// ── 阈值门禁（结构债）──
	th := pol.Thresholds
	var thViolations []string
	if th.MaxFileLines > 0 {
		if long := longFilesReport(root, th.ScanRoots, th.MaxFileLines); len(long) > 0 {
			thViolations = append(thViolations,
				fmt.Sprintf("超长文件（阈值 %d 行）%d 个:\n    %s", th.MaxFileLines, len(long), strings.Join(long, "\n    ")))
		}
	}
	if th.MaxMethodsPerType > 0 {
		if gods := godObjects(root, th.ScanRoots, th.MaxMethodsPerType); len(gods) > 0 {
			thViolations = append(thViolations,
				fmt.Sprintf("god object（阈值 %d 方法/类型）%d 个:\n    %s", th.MaxMethodsPerType, len(gods), strings.Join(gods, "\n    ")))
		}
	}
	if len(thViolations) > 0 {
		verb := "报告口径，不阻断"
		if th.Enforce {
			verb = "阻断"
		}
		fmt.Printf("\n结构阈值%s:\n", verb)
		for _, v := range thViolations {
			fmt.Printf("  - %s\n", v)
		}
		if th.Enforce {
			fmt.Println("\n阈值超限：结构债已超阈值，先清偿或显式上调阈值再提交。")
			os.Exit(1)
		}
	}
}
