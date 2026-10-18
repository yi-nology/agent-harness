// Package reminder 系统注入提示的**防伪包裹通道**。
//
// 所有「系统写给模型的带外提示」经同一包裹函数进 prompt，三件套：
//  1. source 元数据声明来源，模型可区分系统注入与用户输入；
//  2. body 内伪造的 <system-reminder> 标签（含大小写/空白变体）统一转义，
//     杜绝嵌套包裹越权；
//  3. 只用于 LLM 面（prompt 组装），用户可见面（时间线、报告渲染）保持原文不带标签。
//
// 纪律：包裹体的 body 来自用户输入 / 工具产出 / 历史结论等**不可信内容**时，
// 必须经本包转义后再进 prompt——告警 webhook 文本、目标机命令输出、
// 抓取网页正文都是提示注入的现实入口。
//
// 本包与任何宿主领域无关：它是 LLM 应用的通用提示注入面。
package reminder

import (
	"regexp"
	"strings"
)

// tagRe 匹配待转义的伪造标签：容许大小写与 `<` 后的空白变体。
var tagRe = regexp.MustCompile(`(?i)<\s*/?\s*system-reminder`)

// SanitizeBody 转义 body 内伪造的 system-reminder 标签（`<` → `&lt;`，保留其余原文）。
// 包裹前调用；包裹函数自身不会再产出可被二次解析的嵌套结构。
func SanitizeBody(body string) string {
	return tagRe.ReplaceAllStringFunc(body, func(m string) string {
		return strings.Replace(m, "<", "&lt;", 1)
	})
}

// Wrap 以统一通道包裹系统注入：`<system-reminder source="…">…</system-reminder>`。
func Wrap(source, body string) string {
	var b strings.Builder
	b.WriteString(`<system-reminder source="`)
	b.WriteString(SanitizeBody(source))
	b.WriteString(`">` + "\n")
	b.WriteString(SanitizeBody(strings.TrimSpace(body)))
	b.WriteString("\n</system-reminder>")
	return b.String()
}
