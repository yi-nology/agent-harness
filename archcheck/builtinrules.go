// builtinrules.go —— 内置规则 id 清单（policy 的 rules 段之外的那些），以及
// 「这条规则当前是否真的在判」的判定。
//
// # 为什么需要这张清单
//
// 例外登记按「rule + path + line」精确豁免某条规则的某处违反。要登记一条例外，
// 先得知道那条规则的 id 存在——而**恒开规则的 id 不在 policy 的 rules 段里**：
// 它们要么由本执行体自己实现（未受管逃逸门禁、门包门禁、自校验），要么由
// policy 的**段**驱动（execution_paths / sandbox_providers / egress_paths /
// dependency_direction）。
//
// 旧实现只把 rules 段的 id 收进「已见规则」集合，于是宿主照着本执行体给的指引
// （未受管逃逸门禁的违规文案原文：「确需逃逸时在 policy 的 exceptions 里显式
// 登记豁免与拆除条件」）去登记时，自校验反而报它「指向不存在的规则」
// ——**照着指引做就红**。
//
// 而恒开门禁恰恰是最需要豁免的那几条：PTC 的未受管逃逸门禁管的是「宿主显式声明
// 本层工具另有一套管控」这件事，宿主要**合法**表达这件事就只能登记例外。
// 指引说可以、机制不让——这是门禁自己的缺陷，不是宿主的写法问题。
//
// 实测（v0.32.0）：宿主按指引登记 PTC 逃逸口的例外 ⇒
//   - 逃逸违规本身没被豁免；
//   - 自校验报「例外登记指向不存在的规则 "harness-ptc-unmanaged-escape"」⇒ 门禁红。
//
// # 为什么「存在」与「在判」要分开
//
// 存在 ≠ 在判：门包门禁只在 policy 写了 facades 段时才有判定面；四段驱动的门禁
// 都可以被显式关掉（enabled: false）。
//
// 例外指向一条当前不判的规则 ⇒ 这条豁免**今天什么都没豁免**，却会在将来那条
// 规则被启用时无声地开始生效。与 ignore_dirs「豁免必须指向真实存在的目录」
// 是同一条纪律（那一条的理由：豁免指向不存在的目录时，它今天什么都没豁免，
// 却会在将来出现同名目录时整棵跳过），故同样判掉。
package archcheck

// builtinRuleIDs 内置规则 id 清单（policy 的 rules 段之外的那些）。
//
// 顺序固定：自校验的错误文案按这个顺序回显，稳定的顺序让同一份 policy 的
// 两次运行给出一致的输出——门禁的输出也是契约。
var builtinRuleIDs = []string{
	unmanagedEscapeRuleID,
	facadeRuleID,
	depRuleID,
	sandboxProviderRuleID,
	executionPathRuleID,
	egressPathRuleID,
}

// isBuiltinRule 报告某条规则 id 是否是内置的（不在 policy 的 rules 段里）。
func isBuiltinRule(id string) bool {
	for _, bid := range builtinRuleIDs {
		if bid == id {
			return true
		}
	}
	return false
}

// builtinRuleEnforced 报告某条内置规则当前是否真的在判。
//
// 只对内置规则有意义：policy 自己声明的规则永远在判——它就写在 rules 段里。
//
// 口径与执行体一致（见 archcheck.go 里各段的取值方式）：段缺省即开启，
// 显式 enabled: false 才关。facade 门禁是唯一的例外——它要有 facades 段
// 才有判定面。
func builtinRuleEnforced(pol *Policy, id string) bool {
	switch id {
	case unmanagedEscapeRuleID:
		return true // 恒开：它是 PTC 安全默认（缺判定环即拒）的配套护栏
	case facadeRuleID:
		return len(pol.Facades) > 0
	case depRuleID:
		return pol.DepDir.on()
	case sandboxProviderRuleID:
		return pol.SandboxProviders == nil || pol.SandboxProviders.on()
	case executionPathRuleID:
		return pol.ExecutionPaths == nil || pol.ExecutionPaths.on()
	case egressPathRuleID:
		return pol.EgressPaths == nil || pol.EgressPaths.on()
	}
	return false
}
