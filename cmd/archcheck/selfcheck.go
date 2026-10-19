// selfcheck.go —— policy 自身的自校验 + 例外到期机制。
//
// # 为什么门禁需要检查门禁
//
// 本仓的纪律是「从没见过它触发」的规则等于没有规则。这句话有半边是显然的
// （没人验证过的规则可能根本不触发），另半边容易被忽略：**规则也可能以
// 「声明了但没有执行体」的形式存在**——policy 里写了一条 severity: error 的
// 规则，但它的 forbid_regex 字段名拼错了，于是反序列化后两个正则都是空串，
// 旧实现 `return nil, nil` 把它当成「声明面字段」静默跳过。规则还在、severity
// 还是 error、报告里还写着「7 条规则」，但它一条都判不了。
//
// 这与「门禁失效」的失败形态完全一致：门禁变绿。区别只在于这次绿得特别安详。
//
// 同一类问题还有几处：
//
//   - 规则 id 重复 → 两条规则共用一个 id，豁免与告警都无法定位
//   - thresholds.scan_roots 漏了 modules 里的某个包 → 该包对阈值门禁隐形
//     （本仓真实发生过：guardset 登记在 modules 段却不在 scan_roots 段，
//     于是「超长文件」「god object」两条阈值对它从来不生效）
//   - exceptions 里的规则 id 不存在 → 例外指向一条不存在的规则
//
// 全部由本文件一次性判掉，且**只判机制、不判内容**——它不评价某条规则该不该存在，
// 只保证「存在着的规则确实有执行体、确实在跑、确实被阈值覆盖」。
//
// # 例外到期
//
// policy 的例外登记格式要求写 `until` 与 `removal`（拆除条件）。旧实现把
// `until` 解析成字段后就再没人看过它——**到期不失效**。于是「写个日期」的
// 动作没有任何约束力：一条例外在 2020 年就该清偿的债，可以无限期豁免下去，
// 且每一条都还躺在 policy 里看着像有人管过。
//
// 本文件让 `until` 真正生效：到期后该例外**不再豁免**它原先豁免的违规，
// 违规重新计入阻断集，同时明确回显「此例外已过期」。这是「债到期」的纪律
// 在机器上的落点：到期不是提醒，是重新欠账。

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// selfCheckRuleID policy 自校验这条规则自己的 ID。
const selfCheckRuleID = "harness-policy-self-check"

// selfCheckViolations 校验 policy 自身的结构一致性。
//
// 判定面是 policy 文件 + 仓库实际目录，不涉及任何业务代码。
func selfCheckViolations(root string, pol policyFile) []violation {
	var out []violation

	add := func(path string, line int, format string, args ...any) {
		out = append(out, violation{
			rule: selfCheckRuleID,
			path: path,
			line: line,
			text: fmt.Sprintf(format, args...),
			kind: "policy_self_check",
		})
	}

	const policyPath = "architecture-policy.yaml"

	// ── 规则 id 唯一 + 每条规则都有执行体 + severity 合法 ──
	seen := map[string]bool{}
	for _, r := range pol.Rules {
		if r.ID == "" {
			add(policyPath, 0, "存在没有 id 的规则（无法定位、无法豁免）")
			continue
		}
		if seen[r.ID] {
			add(policyPath, 0, "规则 id %q 重复：两条规则共用一个 id，豁免与告警都无法定位到具体哪条", r.ID)
		}
		seen[r.ID] = true

		// 没有执行体的规则 = 永远判不了的规则。旧实现把它当「声明面字段」
		// 静默跳过，于是「写错字段名」与「故意声明一条说明」不可区分。
		if r.ForbidRegex == "" && r.ForbidImport == "" {
			add(policyPath, 0,
				"规则 %q 没有执行体（forbid_regex 与 forbid_import_regex 皆空）：它一条都判不了，"+
					"却仍计入「N 条规则」的报告——这是最隐蔽的门禁失效形态", r.ID)
		}
		switch r.Severity {
		case "", "error", "warn":
		default:
			add(policyPath, 0, "规则 %q 的 severity %q 非法（只允许 error / warn / 缺省）", r.ID, r.Severity)
		}
	}

	// ── 阈值判定面必须覆盖所有登记在 modules 段的包 ──
	//
	// 漏一个包，该包对「超长文件」「god object」两条阈值门禁**完全隐形**：
	// 它可以长到 1000 行、可以挂 60 个方法，阈值门禁一声不吭。
	// 本仓真实发生过（guardset），所以这条自校验是必须项而非锦上添花。
	if pol.Thresholds.Enforce && (pol.Thresholds.MaxFileLines > 0 || pol.Thresholds.MaxMethodsPerType > 0) {
		scanned := map[string]bool{}
		for _, sr := range pol.Thresholds.ScanRoots {
			scanned[sr] = true
		}
		for _, m := range pol.Modules {
			for _, r := range m.Roots {
				if r == "" {
					continue
				}
				if !scanned[r] {
					add(policyPath, 0,
						"包 %q 登记在 modules 段却不在 thresholds.scan_roots 段："+
							"它对「超长文件」「god object」两条阈值门禁隐形（可长到任意行数而不报）", r)
				}
			}
		}
	}

	// ── 门面包指向的目录必须存在 ──
	for _, fr := range pol.Facades {
		if fr.Package == "" {
			add(policyPath, 0, "门面包缺少 package 字段")
			continue
		}
		if _, err := os.Stat(filepath.Join(root, fr.Package)); err != nil {
			add(policyPath, 0, "门面包 %q 指向的目录不存在：门禁将判一个不存在的包，永远为零违规", fr.Package)
		}
	}

	// ── 例外登记的完整性 ──
	for _, ex := range pol.Exceptions {
		if ex.Rule == "" {
			add(policyPath, 0, "例外登记缺少 rule 字段：无法定位它豁免的是哪条规则")
		} else if !seen[ex.Rule] {
			add(policyPath, 0, "例外登记指向不存在的规则 %q：它豁免的东西无从查证", ex.Rule)
		}
		if ex.Until == "" {
			add(policyPath, 0, "例外登记（%s %s:%d）缺少 until 字段：写不出到期日的例外等于永久豁免", ex.Rule, ex.Path, ex.Line)
		}
		if ex.Removal == "" {
			add(policyPath, 0, "例外登记（%s %s:%d）缺少 removal（拆除条件）字段：没有拆除条件的例外是赖账", ex.Rule, ex.Path, ex.Line)
		}
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
func exceptionExpiry(ex policyException, now time.Time) (bool, string) {
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
