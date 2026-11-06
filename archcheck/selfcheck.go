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
//   - 规则 id 重复 → 两条规则共用一个 id，豁免与警告都无法定位
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

package archcheck

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
func selfCheckViolations(root string, pol *Policy) []Violation {
	var out []Violation

	add := func(path string, line int, format string, args ...any) {
		out = append(out, Violation{
			Rule: selfCheckRuleID,
			Path: path,
			Line: line,
			Text: fmt.Sprintf(format, args...),
			Kind: "policy_self_check",
		})
	}

	// policy 自身的位置：宿主仓的 policy 文件不一定叫 architecture-policy.yaml，
	// 写死文件名会让违规指向一个不存在的文件（复用方看到的位置是错的）。
	policyPath := PolicyPath(root, pol)

	// ── 规则 id 唯一 + 每条规则都有执行体 + severity 合法 ──
	seen := map[string]bool{}
	for _, r := range pol.Rules {
		if r.ID == "" {
			add(policyPath, 0, "存在没有 id 的规则（无法定位、无法豁免）")
			continue
		}
		if seen[r.ID] {
			add(policyPath, 0, "规则 id %q 重复：两条规则共用一个 id，豁免与警告都无法定位到具体哪条", r.ID)
		}
		seen[r.ID] = true

		// 没有执行体的规则 = 永远判不了的规则。旧实现把它当「声明面字段」
		// 静默跳过，于是「写错字段名」与「故意声明一条说明」不可区分。
		//
		// 报错要给出**可执行的下一步**：policy 里没有 forbid_call_regex /
		// allow_paths 这类字段（判据面只有 import 路径与源码文本两种），
		// 写了会被 YAML 解析静默丢弃——于是规则就成了「声明还在、一条都判不了」。
		if r.ForbidRegex == "" && r.ForbidImport == "" {
			add(policyPath, 0,
				"规则 %q 没有执行体（forbid_regex 与 forbid_import_regex 皆空）：它一条都判不了，"+
					"却仍计入「N 条规则」的报告——这是最隐蔽的门禁失效形态。"+
					"调用面禁令用 forbid_regex 表达、路径白名单用 scope_exclude 表达；"+
					"policy 里没有 forbid_call_regex / allow_paths 这两个字段，写了会被 YAML 静默丢弃",
				r.ID)
		}
		// 有执行体却 scope 为空 ⇒ 一行都不判。
		//
		// ruleFiles 只遍历 Scope 里的目录，空清单返回空文件集；于是这条规则有正则、
		// severity 是 error、报告里也计入「N 条规则」，但它永远不触发。
		// 这与「字段名拼错」是同一类，只是拼错已由 Load 的严格解析兜住；
		// 而 `scope: []` 是合法 YAML、拼写也对，只能靠这条判据。
		if (r.ForbidRegex != "" || r.ForbidImport != "") && len(r.Scope) == 0 {
			add(policyPath, 0,
				"规则 %q 有执行体但 scope 为空：判定面只遍历 scope 里的目录，空清单等于它**一行都不判**——"+
					"规则在、severity 是 error、报告里也计入条数，只是永不触发", r.ID)
		}
		switch r.Severity {
		case "", "error", "warn":
		default:
			add(policyPath, 0, "规则 %q 的 severity %q 非法（只允许 error / warn / 缺省）", r.ID, r.Severity)
		}

		// ── 规则自带的样例必须与判据一致（加载期验活）──
		//
		// 这是 P1-5 的落点：把「这条规则还活着吗」从**事故后回答的问题**变成
		// **加载期的断言**。判据复用真实判定路径（见 examples.go 头注）。
		for _, problem := range exampleViolations(r) {
			add(policyPath, 0, "%s", problem)
		}
		if pol.RequireRuleExamples {
			if (r.ForbidRegex != "" || r.ForbidImport != "") && len(r.Examples) == 0 {
				add(policyPath, 0,
					"规则 %q 没有自带 examples（require_rule_examples: true 已开）："+
						"有执行体却没有样例的规则，无法在加载期证明它还判得动——词表被改名、"+
						"module 路径被迁走之后，它会静静地不再命中，而门禁继续绿着", r.ID)
			}
			// 「有样例」还不等于「样例够」：只有反例的规则只能证明判据不乱报，
			// 证明不了判据还判得动（见 exampleCoverageProblems 头注）。
			for _, problem := range exampleCoverageProblems(r) {
				add(policyPath, 0, "%s", problem)
			}
		}

		// ── import_exempt 项的形状必须可能生效 ──
		//
		// 例外的语义是「放行这一条 import 路径」。若某一项**形状上就不可能
		// 匹配**（空串、带尾斜杠、含通配/正则字符），它的效果是「一条都没放行」。
		//
		// 不判的后果：宿主会看到 import 被拒，却不知道是自己登记的那条豁免
		// 没生效——**报错指向 import，真正的问题在 policy 里**（误诊）。
		// 而「policy 写了例外但它不生效」正是本仓纪律里最贵的那一类静默失效。
		//
		// 判的是**形状**（确定性、不误报），不是「当前有没有命中」
		// （命中与否分不清「写错」与「将来才用」，硬判就是误报）。
		for _, e := range r.ImportExempt {
			if defect := importExemptDefect(e); defect != "" {
				add(policyPath, 0,
					"规则 %q 的 import_exempt 项 %q 无效：%s", r.ID, e, defect)
			}
		}
	}

	// ── 阈值判定面必须覆盖所有登记在 modules 段的包 ──
	//
	// 漏一个包，该包对「超长文件」「god object」两条阈值门禁**完全隐形**：
	// 它可以长到 1000 行、可以挂 60 个方法，阈值门禁一声不吭。
	// 本仓真实发生过（guardset），所以这条自校验是必须项而非锦上添花。
	if pol.Thresholds.Enforce && (pol.Thresholds.MaxFileLines > 0 || pol.Thresholds.MaxMethodsPerType > 0) {
		for _, m := range pol.Modules {
			for _, r := range m.Roots {
				if r == "" {
					continue
				}
				if !coveredByAnyRoot(pol.Thresholds.ScanRoots, r) {
					add(policyPath, 0,
						"包 %q 登记在 modules 段却不在 thresholds.scan_roots 段："+
							"它对「超长文件」「god object」两条阈值门禁隐形（可长到任意行数而不报）", r)
				}
			}
		}
	}

	// ── 转发壳阈值不得为负 ──
	//
	// 负值没有意义：0 已经表达「关闭该区分」（所有方法都算实质方法）。
	// 负值会被判据当成「没有方法是壳」——但它长得像别的意思（有人可能以为
	// 负数 = 无限宽松）。声明面上的歧义要在这里掐掉，而不是留给判据去猜。
	if v := pol.Thresholds.ForwardingShellMaxChars; v != nil && *v < 0 {
		add(policyPath, 0,
			"thresholds.forwarding_shell_max_chars 不得为负（负值没有意义；"+
				"关闭「转发壳不计入阈值」这一区分请显式写 0）")
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

	// ── modules 段登记的包根必须存在 ──
	//
	// 与上面那条同源：声明指向一个不存在的目录时，门禁判的是空气。
	//
	// 这条判定的代价在实测里很具体：机制包迁到外仓那天，policy 里的包根就陈旧了，
	// 而**没有任何东西会读它**——于是声明与事实的偏离一直累积，直到有人把执行体
	// 接上才暴露。接上执行体的第一件事就是让它红：迁出的包根当天即红。
	//
	// 判在 policy 自校验这一层（而不是依赖方向门禁）的原因：自校验恒开，
	// 而依赖方向门禁可以被 policy 关掉；一个指向空目录的包根永远是假的声明。
	for _, m := range pol.Modules {
		for _, r := range m.Roots {
			if r == "" {
				add(policyPath, 0, "模块 %q 的 roots 里有空条目", m.ID)
				continue
			}
			if _, err := os.Stat(filepath.Join(root, r)); err != nil {
				add(policyPath, 0,
					"modules 段模块 %q 登记的包根 %q 不存在：它对依赖方向与阈值门禁都隐形"+
						"（requires 声明随之失效），声明与事实已经偏离", m.ID, r)
			}
		}
	}

	// ── 目录级豁免必须指向真实存在的目录 ──
	//
	// ignore_dirs 是比 exceptions **更宽**的豁免口：按目录整棵跳过、不要求逐项
	// 签字、也不用写拆除条件。它唯一还能自证的东西是「这个目录真的存在」——
	// 指向不存在目录的豁免，与指向空目录的包根是同一种偏离：**声明还在，事实没了**。
	//
	// 而它的失效形态比陈旧包根更隐蔽：陈旧包根至少还有人看报告；一条没人命中过的
	// 豁免会一直躺在 policy 里，等着将来某个恰好同名的新包出现时**无声地把它也
	// 豁免掉**——而那时没有人会重新判断「这个新包该不该被豁免」。
	// 这正是 issue #35 记的那件事：cmd/ 从 2 个包长到 3 个，前缀豁免顺带盖住了
	// 第三個，理由是「archcheck / docsync 是工具」，实际面已经不同。
	for _, sec := range ignoreDirSections(pol) {
		for _, d := range sec.dirs {
			if d == "" {
				add(policyPath, 0, "%s 的 ignore_dirs 有空条目（空字符串不豁免任何目录，只让清单看起来更长）", sec.name)
				continue
			}
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(d))); err != nil {
				add(policyPath, 0,
					"%s 的 ignore_dirs 项 %q 指向的目录不存在：这条豁免今天什么都没豁免，"+
						"却会在将来出现同名目录时**无声地**把它整棵跳过（豁免必须指向真实存在的目录）",
					sec.name, d)
			}
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
