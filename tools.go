//go:build tools

// 本文件**不参与任何构建**（build tag tools 默认不启用），只为在 go.mod 里钉住
// 四个「模块图里有、但本仓没有任何包 import」的第一方依赖版本。
//
// ## 为什么需要它
//
// Go 的规则是 go.mod 只记录构建本模块所需的模块。llm / obs / textx / core 这几个
// 都不在 `go list -deps ./...` 的包级闭包里，所以哪怕手写 `require` 钉到最新版，
// 下一次 `go mod tidy` 也会把它们当多余条目删掉——实测过：`go get` 加进去，
// `tidy` 立刻剥回原状。于是「钉住版本」这件事在纯 require 的写法下根本不成立。
//
// 钉子的实现方式是 blank import：它给每个包造出一个真实的 import 边，
// 让 `tidy` 有理由把对应的 require 留下。
//
// ## 钉的是哪个版本
//
// ⚠️ **下面的版本号是散文，不受机器管**——`go.mod` 才是权威。改依赖地板后
// **要手工回来改这一段**，否则读者会照着过期版本号做判断（本轮就发生过：
// 2026-10-21 抬 llm/obs 地板后忘了回改，注释里还写着 v0.14.1 / v0.9.2）。
//
// 核对方法（一条命令，机械）：
//
//	go list -m github.com/oagentkit/llm github.com/oagentkit/obs \
//	        github.com/oagentkit/textx github.com/oekit/core
//
//	llm   v0.15.2 ← 领先 agent 声明的地板
//	obs   v0.9.5  ← 领先 agent 声明的地板
//	textx v0.6.2  ← 领先 agent / llm 声明的地板
//	core  v0.11.0 ← 领先 logx 声明的地板
//
// **钉子重新领先于上游地板了**，原因与 2026-10-19 那次「不再领先」相反：
//
//  1. llm / textx：本轮上游把能力搬了家（obsx 从 obs 迁进 llm；textx 补了
//     ratio / tokenize 两个原语），本仓要用就必须跟到新版——**这不是抬地板，
//     是功能前提**。旧版 llm 里根本没有 obsx 这个包，钉在 v0.11.0 会直接编译不过。
//  2. obs：钉子改指 `obs/audit`（obsx 已迁往 llm，obs 里现存的是从 guardx 迁入的
//     audit）。指向哪个包不重要，重要的是**这条 require 还在 go.mod 里可见**——
//     agent 声明的 obs 地板是 v0.7.0，而 audit 只在 v0.8.0 之后才有；将来本仓
//     真要接审计留痕时，钉子已经把它放在能用的版本上，不会撞上「包不存在」的
//     编译错误。
//  3. core：v0.11.0 把配置加载入口按来源拆开（`LoadFile` 任意路径 / `Unmarshal`
//     字节源），严格模式的诊断面（未知键拒载 + 合法键清单 + 拼写建议）对**不遵循
//     etc/ 约定路径**的宿主也可达——钉在能用这些能力的版本上，将来本仓接配置时
//     落到的就是验证过的版本。
//
// 钉子领先地板时的作用回到它最初的形态：「将来本仓一旦 import 它，落到的是我们
// 验证过的版本，而不是上游声明的旧地板」。
//
// ## 代价（明写，不藏）
//
// 这些包**一个字节都没进本仓的二进制**——它们是纯版本契约，不是运行时依赖。
// 代价是 go.sum 里会多出这几条校验记录，且它们各自的传递依赖会被记进模块图。
//
// ## 什么时候删掉这个文件
//
// 若哪天 harness 真的开始 import 其中某个包，这条 require 就由真实依赖接管，
// 本文件里对应的那行 blank import 可以删。
//
// 若上游把地板抬到了这里钉的版本以上，MVS 取更高者，钉的版本号自动失效，
// 同样可以删。
//
// 删除判据要问的是「这条 require 还有没有真实依赖支撑」，不是「版本号变没变」。
package tools

import (
	_ "github.com/oagentkit/llm"
	_ "github.com/oagentkit/llm/obsx"
	_ "github.com/oagentkit/obs/audit"
	_ "github.com/oagentkit/textx/textutil"
	_ "github.com/oekit/core/version"
)
