// includecheck.go —— 规则的 include 模式必须**可能**命中一个文件名。
//
// # 为什么这条要判
//
// 文件发现是：遍历 scope 目录 → 对每个文件取 filepath.Base(path) → 用
// filepath.Match(pat, base) 与 include 里的每条模式比。这个判定面有一个窄得多
// 的定义域：**比对面是基名**，不是路径。
//
// 于是两类写法会让规则一个文件都不扫，而它照样计入「N 条规则」、severity 照样
// 是 error、报告里照样是绿的：
//
//   - **语法非法**（"*.[go"、"[a-"）→ filepath.Match 返回 ErrBadPattern，
//     而调用点把错误吞成「不匹配」（它没法因为一条模式让整个扫描失败）；
//   - **含路径分隔符**（"**/*.go"、"src/*.go"）→ filepath.Match 里 '*' 不跨
//     分隔符，而比对面是基名、永远不含分隔符，故它**恒不命中**。
//
// 第二种尤其容易踩：Go 生态里 "**/*.go" 几乎人人写过，看着语义正确，在这里却是
// 一条死模式。这不是「glob 写得不够强」，是**量纲用错了**——本仓的 scope 已经承担
// 目录维度，include 只需要文件名。
//
// # 为什么判在 policy 自校验而不是扫描路径
//
// 这是**声明缺陷**：修法是改 policy 的那一行（目录部分写进 scope），而扫描路径给不出
// 这个结论——它只能说「这棵子树没被判定」。何况让 iterFiles 先返回 error 会中止整个
// Run，自校验那一条永远走不到，判据就成了死的。与 ruleFiles 对不存在 scope 的既有
// 处置同一条纪律：扫描路径静默跳过 + 注释指向自校验，判决在 selfcheck。
//
// # 不判什么
//
// 不判「今天命中 0 个文件」：include: ["*.md"] 在还没有文档时就是 0 命中，那不是
// 缺陷，将来加了文档它就该生效。只判**结构上不可能命中**的形态，不确定一律放行——
// 判据宁可漏掉一种坏形态，也不能把合法声明判成错的（假红比漏红更贵：它教人调阈值）。

package archcheck

import (
	"fmt"
	"path/filepath"
	"strings"
)

// includeProbe 语法校验用的探针文件名。
//
// filepath.Match 是「先编译模式、再匹配」的：语法错误与第二个参数无关。用一个**可能
// 被任何合法模式命中**的普通文件名当探针，就能把「语法非法」单独判出来，而不把
// 「这个模式确实不匹配 probe.go」误当成缺陷。
const includeProbe = "probe.go"

// includePatternProblems 收集一条规则 include 模式的声明缺陷（带规则 id，可直接报）。
//
// 只在规则**有执行体**时调用：没有执行体的规则一条都判不了，已由「没有执行体」那条
// 判据报出，同一件事不说两遍。
func includePatternProblems(ruleID string, patterns []string) []string {
	var out []string
	for _, p := range patterns {
		why := includePatternDefect(p)
		if why == "" {
			continue
		}
		out = append(out, fmt.Sprintf(
			"规则 %q 的 include 模式 %q 结构上不可能命中任何文件：%s"+
				"（文件发现只拿 filepath.Base(path) 与模式比；目录维度请写进 scope）",
			ruleID, p, why))
	}
	return out
}

// includePatternDefect 返回空串表示「可能命中」——不确定一律不判。
//
// 三条判定都是**可证明**的「永不命中」，没有一条依赖「当前仓库里恰好没有这种文件」：
// 空模式、模式里带分隔符、模式语法非法。
func includePatternDefect(pattern string) string {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return "空模式不匹配任何文件名（要匹配全部就整条 include 留空，它的语义就是「不过滤」）"
	}
	if _, err := filepath.Match(p, includeProbe); err != nil {
		// 扫描路径把该错误静默当成「不匹配」，故这里必须点名错误本身：
		// 它是唯一能让人看懂「为什么这条规则一条都没报」的线索。
		return fmt.Sprintf("模式语法非法（%v），扫描时该错误被静默当成「不匹配」", err)
	}
	if strings.ContainsRune(p, '/') {
		return "模式含路径分隔符 '/'，而比对面是文件名基名（filepath.Base），基名永远不含分隔符"
	}
	if filepath.Separator == '\\' && strings.ContainsRune(p, '\\') {
		// 只在分隔符就是反斜杠的平台上报：Unix 上 '\\' 是合法转义符（"a\\*" 匹配字面 a*），
		// 在那里判它就是**假红**——判据的适用范围必须与它引用的语义同平台。
		return "模式含路径分隔符 '\\\\'，而比对面是文件名基名（filepath.Base），基名永远不含分隔符"
	}
	return ""
}
