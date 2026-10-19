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
// 当前钉的版本与**上游各自声明的地板完全相等**（2026-10-19 核对）：
//
//	llm   v0.11.0  ← agent v0.10.0 声明
//	obs   v0.7.0   ← agent v0.10.0 声明（agent 侧标 indirect，本仓侧是 direct）
//	textx v0.5.0   ← agent v0.10.0 声明
//	core  v0.9.2   ← logx v0.7.1 声明
//
// 这一点要如实记下来：**钉子不再领先于上游地板**。早期版本这里钉的是比上游地板
// 更高的版本，作用是「将来本仓一旦 import 它，落到的是我们验证过的版本，
// 而不是上游声明的旧地板」。现在两者相等，MVS 无论有没有这行 blank import
// 都会解析到同一个版本——所以钉子当前的作用退化成两件更朴素的事：
//
//  1. 让这四条 require 在 go.mod 里**显式可见**，而不是仅以传递依赖的身份
//     藏在 agent / logx 的 go.mod 里（那样一旦上游改声明，本仓无从察觉）；
//  2. 记录「本仓实际验证过这些版本」这个事实。
//
// 换句话说：现在改版本号仍是同一套动作，但理由从「抬高地板」变成了「声明并锁定」。
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
	_ "github.com/oagentkit/obs/obsx"
	_ "github.com/oagentkit/textx/textutil"
	_ "github.com/oekit/core/version"
)
