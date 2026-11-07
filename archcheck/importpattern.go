// importpattern.go —— forbid_import_regex 的「结构上命中不了」判据。
//
// # 为什么需要它
//
// forbid_import_regex 的判据面是 **import 路径**（imports.go 的 importViolationsIn），
// 而 Go 的 import 路径永远是 clean 形式：go 命令拒绝 a//b，也不存在以 / 结尾的
// 包路径。于是某些正则形状**一条都匹配不到**——它仍然是一条规则：计入「N 条规则」、
// 挂着 severity: error、selfcheck 只问「两个正则是否都为空」，于是它永不触发而
// 门禁照常绿（issue #41）。
//
// # 判据为什么走 regexp/syntax 而不是 strings.Contains
//
// 最省事的修法是 strings.Contains(pattern, "//")，但它两头都错：
//
//   - **假阳性**：[//] 是**字符类**（匹配单个 '/'），文本里含 '//' 却完全合法；
//   - **假阴性（更要命）**：issue #41 自己举的例子 …/internal/(/|$) 里
//     **没有相邻的两个斜杠**——它是「前缀以 / 收尾」+「边界组 (/|$)」拼出来的
//     **语义上的** '//'。按文本判，这条规则会被判成健康，issue 就算「修好了」，
//     而缺陷原封不动地躺在仓里。
//
// 故判定面是语法树：判的是**结构**（字面量里是否强制相邻两个 '/'、结尾分支
// 是否要求以 '/' 收尾），不是文本长相。这也与仓内既有的纪律同源：结构性判断
// 走 AST，不走逐行正则。
//
// # 判据的边界：只判「可证明永不命中」
//
// 本文件只报**能证明**永不命中的形态：返回空串**不等于**这条规则健康。
// 「这条规则还活着吗」的语义证明仍归 policy 的 examples 管
// （require_rule_examples），两者分工明确：这里防「斜杠写重了」，
// 样例防「词表被改名 / 包被迁走」。反过来若在这里追求「一定命中」，
// 判据就变成不可判定的问题，只能靠误报凑数。
package archcheck

import "regexp/syntax"

// importPatternDefect 报告 forbid_import_regex 里「结构上命中不了任何 import
// 路径」的形态；返回空串表示本判据没发现问题（**不等于**这条规则一定能命中）。
func importPatternDefect(pattern string) string {
	if pattern == "" {
		return ""
	}
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		// 非法正则由加载处与判定处报（那里能带上规则 id 与编译错误），
		// 本判据不重复报，也不猜。
		return ""
	}
	why, dead := impossibleImportPattern(re)
	if !dead {
		return ""
	}
	return why
}

// impossibleImportPattern 判一个语法树节点的**所有**匹配是否都不可能出现在
// 任何干净 import 路径里。
//
// 契约：返回 (理由, true) 必须是**可证明**的——任何一处不确定都返回 false。
// 宁可漏判（由 examples 兜底）也不能误判：一条把健康规则判红的门禁会被关掉，
// 然后连带把真问题一起关掉。
func impossibleImportPattern(re *syntax.Regexp) (string, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if doubledSlash(re.Rune) {
			return "字面量里出现相邻的两个 '/': import 路径不会含 '//'", true
		}
	case syntax.OpCapture:
		return impossibleImportPattern(re.Sub[0])

	case syntax.OpAlternate:
		// 只有**每个**分支都不可满足时才不可满足。任一分支能命中就不判——
		// 这正是「其实只有一半写错」不该被拦的地方。
		var reason string
		for _, sub := range re.Sub {
			why, dead := impossibleImportPattern(sub)
			if !dead {
				return "", false
			}
			if reason == "" {
				reason = why
			}
		}
		return reason, true

	case syntax.OpRepeat:
		// (/){2,}：至少两次 ⇒ 匹配串里必然含 '//'。
		if re.Min >= 2 && singleSlashLiteral(re.Sub[0]) {
			return "/ 至少重复两次：匹配串里必然含 '//'", true
		}
		if re.Min >= 1 {
			return impossibleImportPattern(re.Sub[0])
		}
		// Min == 0 ⇒ 可以一次都不匹配，没有可证明的约束。

	case syntax.OpPlus:
		// (/)+ **不**能判：它可以只匹配一次，得到单个 '/'。
		return impossibleImportPattern(re.Sub[0])

	case syntax.OpConcat:
		return impossibleImportConcat(re)
	}
	return "", false
}

// impossibleImportConcat 判一个连接式。四条路径，全部是「可证明」的形态。
func impossibleImportConcat(re *syntax.Regexp) (string, bool) {
	// ① 任一**不可省**子式自身不可满足 ⇒ 整个连接不可满足
	//    （连接的匹配把子式的匹配作为连续子串包含在内）。
	//    可省子式（Min == 0 / Quest / Star）跳过：它可能一次都不出现。
	for _, sub := range re.Sub {
		if emptyCapable(sub) {
			continue
		}
		if why, dead := impossibleImportPattern(sub); dead {
			return why, true
		}
	}

	// ② 相邻两个子式都**强制** '/' ⇒ 匹配串里必然含 '//'。
	//    零宽节点（^ $ 与词边界）不打断这种相邻关系，故先滤掉再判。
	view := zeroWidthFiltered(re.Sub)
	for i := 1; i < len(view); i++ {
		if endsWithSlashLiteral(view[i-1]) && startsWithSlashLiteral(view[i]) {
			return "相邻的两个子式都强制 '/': 匹配串里必然含 '//'", true
		}
	}

	// ③ 结尾锚点紧跟在「以 / 收尾的字面量」之后 ⇒ 要求整条路径以 '/' 结束。
	for i, sub := range re.Sub {
		if i == 0 || !isEndAnchor(sub) {
			continue
		}
		if endsWithSlashLiteral(re.Sub[i-1]) {
			return "结尾锚点紧跟以 '/' 收尾的字面量：它要求路径以 '/' 结束，而 import 路径不会", true
		}
	}

	// ④ 边界组 (/|$) 若紧跟在「以 / 收尾的字面量」之后：走 '/' 分支得到 '//'，
	//    走结尾分支则要求路径以 '/' 结束 —— 两条路都不成立。
	//
	//    这就是 issue #41 里那个真实写法 …/internal/(/|$)，也是文本判据
	//    （strings.Contains 判 '//'）抓不到的那一半。正确形态是
	//    …/internal(/|$)：斜杠属于**边界组**，不属于前缀。
	for i, sub := range re.Sub {
		if i == 0 || !slashOrEndGroup(sub) {
			continue
		}
		if endsWithSlashLiteral(re.Sub[i-1]) {
			return "边界组 (/|$) 紧跟以 '/' 收尾的字面量：'/' 分支给出 '//'、" +
				"结尾分支要求路径以 '/' 结束，两条都不成立", true
		}
	}
	return "", false
}

// doubledSlash 报一段字面量 rune 里是否有相邻的两个 '/'。
func doubledSlash(runes []rune) bool {
	for i := 1; i < len(runes); i++ {
		if runes[i-1] == '/' && runes[i] == '/' {
			return true
		}
	}
	return false
}

// unwrapCapture 剥掉分组包装（捕获组与非捕获组），只留实质节点。
func unwrapCapture(re *syntax.Regexp) *syntax.Regexp {
	for re.Op == syntax.OpCapture && len(re.Sub) == 1 {
		re = re.Sub[0]
	}
	return re
}

// singleSlashLiteral 报该节点是否恰好是字面量 '/'。
func singleSlashLiteral(re *syntax.Regexp) bool {
	re = unwrapCapture(re)
	return re.Op == syntax.OpLiteral && len(re.Rune) == 1 && re.Rune[0] == '/'
}

// endsWithSlashLiteral / startsWithSlashLiteral 报该节点是否是「以 / 收尾 /
// 开头」的非空字面量。
//
// 只认**字面量**：字符类 [/] 虽然也恒为 '/'，但认它只会多出容易出错的路径，
// 而收益极小。
func endsWithSlashLiteral(re *syntax.Regexp) bool {
	re = unwrapCapture(re)
	return re.Op == syntax.OpLiteral && len(re.Rune) > 0 && re.Rune[len(re.Rune)-1] == '/'
}

func startsWithSlashLiteral(re *syntax.Regexp) bool {
	re = unwrapCapture(re)
	return re.Op == syntax.OpLiteral && len(re.Rune) > 0 && re.Rune[0] == '/'
}

// isEndAnchor 报该节点是否是结尾锚点（$ 与 \z 在 Go 里都是 OpEndText）。
func isEndAnchor(re *syntax.Regexp) bool {
	re = unwrapCapture(re)
	return re.Op == syntax.OpEndText
}

// slashOrEndGroup 报该节点是否是 (/|$) 这一形状（两个分支、顺序不限）。
//
// 只认恰好两个分支：若还有第三个分支（(/|$|x)），'x' 那一支完全可能命中，
// 这时判它不可满足就是误报。
func slashOrEndGroup(re *syntax.Regexp) bool {
	re = unwrapCapture(re)
	if re.Op != syntax.OpAlternate || len(re.Sub) != 2 {
		return false
	}
	return (isEndAnchor(re.Sub[0]) && singleSlashLiteral(re.Sub[1])) ||
		(isEndAnchor(re.Sub[1]) && singleSlashLiteral(re.Sub[0]))
}

// zeroWidthFiltered 滤掉零宽节点（不消耗输入，因而不打断相邻关系）。
func zeroWidthFiltered(subs []*syntax.Regexp) []*syntax.Regexp {
	out := make([]*syntax.Regexp, 0, len(subs))
	for _, sub := range subs {
		switch unwrapCapture(sub).Op {
		case syntax.OpBeginText, syntax.OpEndText, syntax.OpBeginLine, syntax.OpEndLine,
			syntax.OpWordBoundary, syntax.OpNoWordBoundary, syntax.OpEmptyMatch:
			continue
		}
		out = append(out, sub)
	}
	return out
}

// emptyCapable 报该节点是否可能匹配空串（即「可以一次都不消耗输入」）。
func emptyCapable(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginText, syntax.OpEndText,
		syntax.OpBeginLine, syntax.OpEndLine, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true
	case syntax.OpLiteral, syntax.OpCharClass, syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		return false
	case syntax.OpCapture:
		return emptyCapable(re.Sub[0])
	case syntax.OpQuest, syntax.OpStar:
		return true
	case syntax.OpRepeat:
		return re.Min == 0 || emptyCapable(re.Sub[0])
	case syntax.OpPlus:
		return emptyCapable(re.Sub[0])
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if emptyCapable(sub) {
				return true
			}
		}
		return false
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			if !emptyCapable(sub) {
				return false
			}
		}
		return true
	}
	return false
}
