// sandbox_supports.go —— 执行沙箱后端的**预算支持面**核对（policy 的
// sandbox_providers 段的 supports 登记项）。
//
// # 为什么单独一个文件
//
// 「档位」（能力面）与「支持面」（资源兜底兑现哪几项）是**两个正交维度**：
// 进程内解释器的能力面与 LevelProcess 同级，却兜不住内存。合成一维会让
// 档位表说谎（标成 LevelOS 就是谎——它没有内核级裁剪）。
//
// 拆出来也让关注点分开：sandbox.go 判「是不是后端实现、档位对不对、登记齐不齐」，
// 本文件判「声明的预算支持面与代码事实是否一致」。
//
// # 判据
//
// 后端**没有** Supports 方法 ⇒ 只能兑现基本三闸（上游 Capable 的缺省语义：
// 漏声明得到的是「兜不住」，不是「兜得住」）。
//
// 方向是保守的那一侧：
//   - 登记了 max_memory 而代码没有 Supports ⇒ **报红**（声明面说了兜得住、
//     代码兜不住，要求与实际之间出现静默落差）；
//   - 代码有 Supports 而登记只写三闸 ⇒ **不报红**（后端自报得比登记更保守，
//     不是撒谎；真要兑现时改登记即可）。

package archcheck

import (
	"fmt"
)

// 预算项名（与上游 guardx/sandbox 的 Budget 字段一一对应）。
//
// 门禁**不能 import 被判的包**（它必须是最底层，否则「用它判别人」就变成
// 「它自己也在被判定」），所以它拿不到上游的常量表，只能比对**字符串名**——
// policy 侧填同一份名字，两侧共用一份真相（同档位名的做法）。
var sandboxBudgetItems = map[string]bool{
	"wall_clock": true,
	"max_calls":  true,
	"max_bytes":  true,
	"max_memory": true,
}

// 支持面判据：后端**没有** Supports 方法 ⇒ 只能兑现基本三闸（上游 Capable
// 的缺省语义：漏声明得到的是「兜不住」，不是「兜得住」）。
//
// 方向是保守的那一侧：登记了 max_memory 而代码没有 Supports ⇒ 报红（声明面
// 说了兜得住，代码兜不住）；代码有 Supports 而登记只写三闸 ⇒ **不报红**
// （后端自报得比登记更保守，不是撒谎；真要兑现时改登记即可）。
func supportsViolations(p SandboxProvider, d *sandboxDecl) []Violation {
	var out []Violation
	claimed := map[string]bool{}
	for _, item := range p.Supports {
		if !sandboxBudgetItems[item] {
			out = append(out, Violation{
				Rule: sandboxProviderRuleID, Path: p.Package, Line: 1, Kind: "sandbox_provider_unknown_budget",
				Text: fmt.Sprintf("后端 %q 的 supports 登记了未知预算项 %q——可登记的项：%s",
					p.ID, item, knownBudgetItems()),
			})
			continue
		}
		claimed[item] = true
	}
	if claimed["max_memory"] && !d.hasSupports {
		out = append(out, Violation{
			Rule: sandboxProviderRuleID, Path: d.file, Line: d.line, Kind: "sandbox_provider_supports_drift",
			Text: fmt.Sprintf("后端 %q 登记了 max_memory，但代码没有 Supports 方法——"+
				"上游 Capable 的缺省语义是「不实现即只兑现基本三闸」，"+
				"登记声明了兜得住、代码兜不住，要求与实际之间出现静默落差", p.ID),
		})
	}
	return out
}

// knownBudgetItems 可登记的预算项名（排序后拼接，供错误文案用）。
func knownBudgetItems() string {
	return "wall_clock / max_calls / max_bytes / max_memory"
}
