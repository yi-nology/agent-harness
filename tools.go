//go:build tools

// 本文件**不参与任何构建**（build tag tools 默认不启用），只为在 go.mod 里钉住
// 三个「模块图里有、但本仓没有任何包 import」的第一方依赖版本。
//
// ## 为什么需要它
//
// Go 的规则是 go.mod 只记录构建本模块所需的模块。llm / obs / textx / core 这几个
// 都不在 `go list -deps ./...` 的包级闭包里，所以哪怕手写 `require` 钉到最新版，
// 下一次 `go mod tidy` 也会把它们当多余条目删掉——实测过：`go get` 加进去，
// `tidy` 立刻剥回原状。于是「钉住版本」这件事在纯 require 的写法下根本不成立。
//
// ## 为什么还是要钉
//
// 它们的版本由上游各自声明的地板经 MVS 决定（当前分别是 agent v0.7.0 声明的
// llm v0.7.2 / obs v0.4.1 / textx v0.3.0，logx v0.7.0 声明的 core v0.6.0）。
// 本仓一旦在将来某个版本开始 import 其中某个，届时直接落到的是**上游声明的地板**，
// 而不是本仓已验证过的最新版本。钉在这里等于把「我们验证过的版本」写进契约。
//
// ## 代价（明写，不藏）
//
// 这些包**一个字节都没进本仓的二进制**——它们是纯版本契约，不是运行时依赖。
// 代价是 go.sum 里会多出这几条校验记录，且它们各自的传递依赖会被记进模块图。
//
// ## 什么时候删掉这个文件
//
// 若哪天 harness 真的开始 import 其中某个包，这条 require 就由真实依赖接管，
// 本文件里对应的那行 blank import 可以删。若上游把地板抬到了这里钉的版本以上
// （MVS 取更高者），钉子自动失效，同样可以删。
//
// 删除判据要问的是「这条 require 还有没有真实依赖支撑」，不是「版本号变没变」。
package tools

import (
	_ "github.com/oagentkit/llm/llm"
	_ "github.com/oagentkit/obs/obsx"
	_ "github.com/oagentkit/textx/textutil"
	_ "github.com/oekit/core/version"
)
