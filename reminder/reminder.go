// Package reminder 系统注入提示的**防伪包裹通道**（门面）。
//
// 核心已下沉 `github.com/oagentkit/guardx/injectband`：带外注入的身份防伪是
// 通用安全原语，与编排无关，故归 guardx（tier 0 安全层）。本包只做一件事——
// 保住调用路径稳定，宿主的 `reminder.Wrap(...)` 调用点一行不动。
//
// 与 guardx 内其他包的关系：injectband 与 fence 互补而非替代——fence 封数据侧
// （不可信内容不得越出数据区取指令待遇），injectband 封系统侧（系统带外注入的
// 身份标记不得被伪造）。只做前者时，攻击者不必逃出数据区，只需在内容里写一个
// 像系统注入的标签即可抬升身份。
//
// 纪律：包裹体的 body 来自用户输入 / 工具产出 / 历史结论等**不可信内容**时，
// 必须经本包转义后再进 prompt——外部 webhook 文本、命令输出、抓取的网页正文
// 都是提示注入的现实入口。
//
// 本包与任何宿主领域无关：它是 LLM 应用的通用提示注入面。
package reminder

import "github.com/oagentkit/guardx/injectband"

// SanitizeBody 转义 body 内伪造的系统注入标签（`<` → `&lt;`，保留其余原文）。
// 包裹前调用；包裹函数自身不会再产出可被二次解析的嵌套结构。
func SanitizeBody(body string) string {
	return injectband.SanitizeBody(body)
}

// Wrap 以统一通道包裹系统注入：`<system-reminder source="…">…</system-reminder>`。
func Wrap(source, body string) string {
	return injectband.Wrap(source, body)
}
