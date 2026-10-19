// Package archcheck 是 architecture-policy.yaml 的**可执行面**。
//
// # 为什么它是一个可导入的包，而不是一个放在 cmd/ 下的脚本
//
// 存在意义：policy 文件若只当文档读，半年后就与代码事实脱节；把它做成门禁，
// 规则违反即非零退出。但门禁执行体若锁在 package main 里，别的仓就**用不上**——
// 于是每个宿主仓要么各写一遍判据（各判一套，漂移到没人发现为止），要么干脆不写
// （policy 的依赖拓扑表变成死条文：躺在文件里、看起来有人维护过，实际一条都不判）。
//
// 所以这一层被下沉成包：宿主仓 import 它、喂自己的 policy、拿自己的判定结果，
// 不复制一行实现。
//
// # 判据与实现分离
//
// 判据是**声明**（policy 文件）+ **执行体**（本包），两者分开：
//
//   - 声明面：modules 段的依赖拓扑、rules 段的正则、facades 段的门面包清单
//   - 执行面：本包的 AST 判定（go/ast 判 import、判函数体、判依赖方向）
//
// 结构性判定一律走 go/ast，不走逐行正则——import 声明有六种以上书写形态
// （单行 / 块 / 别名 / 空白导入 / 点导入 / 括号同行），逐行正则必然漏，
// 且漏得毫无声响。
//
// # 门禁的失败形态是「变绿」，不是「变红」
//
// 判据写错时最危险的不是报错，而是**什么都不判**却照样报「无阻断级违反」。
// 故本包把「给不出结论」与「没违规」严格分开：解析失败、policy 自相矛盾、
// 规则没有执行体——一律响亮失败（返回 error 或报成违反），绝不静默降级。
//
// # 复用方须知
//
//  1. policy 的 modules 段要求每个 root 指向**真实存在**的包目录：陈旧的 root
//     会被判成违反（「陈旧包根当天即红」），因为它让该模块对门禁隐形。
//  2. 跨仓依赖用 `ext:<import 路径前缀>` 表达，如
//     `ext:github.com/yi-nology/agent-harness/netguard`。
//  3. 报告用 WriteReport；是否阻断用 Result.HasBlocking。
package archcheck

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// RuleInfo 是报告用的一条规则元信息（ID、说明、级别）。
//
// 为什么单独一个类型：facades 段与 modules 段的规则**不在 rules 段里**，
// 它们没有 policy 里的 Rule 结构可复用，但报告形状必须一致。
type RuleInfo struct {
	ID       string
	Desc     string
	Severity string
}

// Result 是一次门禁运行的结果。
type Result struct {
	// Violations 是全部违反（含警告级与已命中例外的）。
	Violations []Violation
	// Blocking 是阻断级违反数，Warnings 是警告级数。
	Blocking int
	Warnings int
	// Excused 是命中已登记例外（且未到期）的违反数。
	Excused int
	// ExpiredExceptions 是已到期的例外——它们不再豁免任何东西。
	ExpiredExceptions []string
	// ThresholdFindings 是结构阈值（超长文件 / god object）的发现。
	ThresholdFindings []string
	// ThresholdEnforced 报告阈值是否阻断。
	ThresholdEnforced bool
	// Rules 是报告顺序下的规则清单。
	Rules []RuleInfo
	// Exceptions 是 policy 登记的例外清单（回显用，让债是显式的）。
	Exceptions []Exception

	// excused 记下被例外豁免的违反键，报告时不回显它们。
	excused map[string]bool
}

// HasBlocking 报告本次运行是否应当以非零退出码阻断。
//
// 阈值门禁只在 enforce 为真时阻断——它是「报告口径」还是「真阈值」由 policy 决定。
func (r *Result) HasBlocking() bool {
	if r == nil {
		return false
	}
	if r.Blocking > 0 {
		return true
	}
	return r.ThresholdEnforced && len(r.ThresholdFindings) > 0
}

// ReportOptions 控制报告输出形状。
type ReportOptions struct {
	// Full 为真时输出全部违反，否则每条规则截断到 10 条。
	Full bool
}

// Run 对 root 执行全套门禁，返回结果。
//
// 返回 error 表示门禁**给不出结论**（policy 解析失败、源码解析失败、声明自相矛盾）——
// 调用方必须据此非零退出，绝不能当成「无违反」继续。
func Run(root string, pol *Policy) (*Result, error) {
	if pol == nil {
		return nil, fmt.Errorf("policy 为空")
	}
	res := &Result{excused: map[string]bool{}}

	// 规则段：正则式规则，逐条判定。
	for _, r := range pol.Rules {
		vs, err := checkRule(root, r)
		if err != nil {
			return nil, err
		}
		res.Violations = append(res.Violations, vs...)
	}

	// 门面包不可变：AST 判定（facade.go），不走正则规则表——这条纪律是结构性的。
	for _, fr := range pol.Facades {
		vs, err := facadeViolations(root, fr)
		if err != nil {
			return nil, err
		}
		res.Violations = append(res.Violations, vs...)
	}

	// 包间依赖方向：modules[].requires 的执行体（deps.go），同样是 AST 判定。
	// 声明面早就在了，缺的是执行面——没有它，依赖拓扑就是散文。
	if pol.DepDir.on() {
		vs, err := dependencyViolations(root, pol.Modules, pol.DepDir)
		if err != nil {
			return nil, err
		}
		res.Violations = append(res.Violations, vs...)
	}

	// policy 自校验：门禁自己也得能被检查（规则没有执行体、id 重复、
	// 阈值判定面漏包、例外指向不存在的规则……）。详见 selfcheck.go。
	res.Violations = append(res.Violations, selfCheckViolations(root, pol)...)

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
	if pol.DepDir.on() {
		sevOf[depRuleID] = sevOrError(pol.DepDir.Severity)
	}
	// 自校验恒为 error：policy 结构有问题时，门禁必须响亮失败，
	// 而不是「其他规则都绿就当通过」——那正是它要防的那种静默失效。
	sevOf[selfCheckRuleID] = "error"

	// exceptions 精确豁免：rule + path + line 三元组命中即放行（不做前缀模糊豁免）。
	//
	// **已到期的例外不再豁免**：policy 要求的 `until` 字段到此才真正生效。
	// 旧实现解析了 until 就再没人看过它，于是「写个日期」这个动作没有任何
	// 约束力——一条该在 2026 年清偿的债可以无限期豁免下去（详见 selfcheck.go）。
	now := time.Now()
	exKey := map[string]Exception{}
	res.Exceptions = pol.Exceptions
	for _, ex := range pol.Exceptions {
		exKey[exceptionKey(ex.Rule, ex.Path, ex.Line)] = ex
		if expired, why := exceptionExpiry(ex, now); expired {
			res.ExpiredExceptions = append(res.ExpiredExceptions,
				fmt.Sprintf("%s:%d [%s] %s —— %s", ex.Path, ex.Line, ex.Rule, ex.Violation, why))
		}
	}
	for _, v := range res.Violations {
		key := exceptionKey(v.Rule, v.Path, v.Line)
		if ex, ok := exKey[key]; ok {
			if expired, _ := exceptionExpiry(ex, now); !expired {
				res.Excused++
				res.excused[key] = true
				continue
			}
			// 已过期：不再豁免，该违规照常计入阻断集（债到期即重新欠账）。
		}
		if sevOf[v.Rule] != "warn" {
			res.Blocking++
		} else {
			res.Warnings++
		}
	}

	// 规则清单（报告顺序：rules 段 → 门面包 → 依赖方向 → policy 自校验）。
	for _, r := range pol.Rules {
		res.Rules = append(res.Rules, RuleInfo{ID: r.ID, Desc: r.Desc, Severity: sevOf[r.ID]})
	}
	if len(pol.Facades) > 0 {
		res.Rules = append(res.Rules, RuleInfo{
			ID:       facadeRuleID,
			Desc:     "门面包只允许类型别名/常量别名/单条上游转发，实现归上游仓",
			Severity: sevOf[facadeRuleID],
		})
	}
	if pol.DepDir.on() {
		res.Rules = append(res.Rules, RuleInfo{
			ID:       depRuleID,
			Desc:     depDirDesc(pol.DepDir),
			Severity: sevOf[depRuleID],
		})
	}
	res.Rules = append(res.Rules, RuleInfo{
		ID:       selfCheckRuleID,
		Desc:     "policy 自身一致性：规则须有执行体、id 唯一、阈值判定面须覆盖全部登记包、例外须写 until 与拆除条件",
		Severity: sevOf[selfCheckRuleID],
	})

	// ── 阈值门禁（结构债）──
	th := pol.Thresholds
	res.ThresholdEnforced = th.Enforce
	if th.MaxFileLines > 0 {
		if long := longFilesReport(root, th.ScanRoots, th.MaxFileLines); len(long) > 0 {
			res.ThresholdFindings = append(res.ThresholdFindings,
				fmt.Sprintf("超长文件（阈值 %d 行）%d 个:\n    %s", th.MaxFileLines, len(long), strings.Join(long, "\n    ")))
		}
	}
	if th.MaxMethodsPerType > 0 {
		if gods := godObjects(root, th.ScanRoots, th.MaxMethodsPerType); len(gods) > 0 {
			res.ThresholdFindings = append(res.ThresholdFindings,
				fmt.Sprintf("god object（阈值 %d 方法/类型）%d 个:\n    %s", th.MaxMethodsPerType, len(gods), strings.Join(gods, "\n    ")))
		}
	}

	return res, nil
}

// CheckDependencyDirection 只跑包间依赖方向这一条门禁。
//
// 单独暴露它，是因为它是**跨包契约**的门禁：别的仓接入本机制时通常先接这一条
// （policy 里已经有 modules 段，只是没有执行体）。
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
