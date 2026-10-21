package ptc

import (
	"context"

	"github.com/oagentkit/guardx/invoke"
	"github.com/yi-nology/agent-harness/sandbox"
)

// ProviderID 本包执行面在 sandbox 登记簿里的标识。
const ProviderID = "ptc-goja"

// Provider 把本包的执行面适配成 sandbox.Provider。
//
// # 为什么适配器在本包而不是 sandbox 包
//
// 适配器要调 Run，而 Run 在本包。放进 sandbox 就成了 sandbox → ptc → sandbox 的
// 环（Go 的 import 环直接编译不过）。方向只能是 ptc → sandbox：
// **沙箱机制不认识任何后端，后端认识沙箱**。
//
// # 档位为什么是 LevelProcess 而不是 LevelOS
//
// goja 是**进程内解释器**：能力面靠 JS 子集 + 绑定白名单裁剪，资源面靠三重预算，
// 但这层约束由解释器自己执行，不是内核强制的。解释器自身的缺陷可以越过它。
//
// 故诚实的档位是 LevelProcess。把它标成 LevelOS 就是让档位表说谎——
// 而档位表是宿主做风险判断的依据，说谎的成本落在宿主身上。
type Provider struct {
	// Bindings 挂给程序的宿主绑定。
	Bindings []Binding
	// Discover 程序内工具发现入口（nil = 不暴露该全局）。
	Discover Discoverer
	// Stages 每次绑定调用前必过的判定环（nil = 不接判定）。
	Stages []invoke.Stage
}

// ID 后端标识。
func (p Provider) ID() string { return ProviderID }

// Level 本包提供的隔离档位。
func (p Provider) Level() sandbox.Level { return sandbox.LevelProcess }

// Exec 执行一次程序。
//
// 失败是结果字段不是异常：sandbox.Failure 带回本包的失败分类（ErrorKind），
// 调用方据此区分「程序写错了」与「门禁拒了」——这两者给出的处置完全不同，
// 合成一条 runtime 的话，调用方会去改那段本来没问题的程序。
func (p Provider) Exec(ctx context.Context, req sandbox.Request) (sandbox.Outcome, error) {
	res := Run(ctx, req.Program, p.Bindings, Options{
		Timeout:  req.Budget.WallClock,
		MaxCalls: req.Budget.MaxCalls,
		MaxBytes: req.Budget.MaxBytes,
		Discover: p.Discover,
		Stages:   p.Stages,
	})
	out := sandbox.Outcome{OK: res.OK, Value: res.Value, Detail: res}
	if !res.OK {
		out.Err = &sandbox.Failure{Kind: res.ErrorKind, Message: res.Error}
	}
	return out, nil
}
