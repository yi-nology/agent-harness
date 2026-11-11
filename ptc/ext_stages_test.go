package ptc_test

import (
	"context"

	"github.com/oagentkit/guardx/invoke"
)

// allowStages 一条恒放行的判定环（外部测试包用）。
//
// 与 ptc 包内测试的同名助手同义：v0.32.0 起 PTC 缺省即拒绝
// （装配了工具绑定却没装判定环 ⇒ ErrorKindUnmanaged），
// 与被验对象无关的用例必须显式装一条放行环。
func allowStages() []invoke.Stage {
	return []invoke.Stage{invoke.StageFunc{
		StageName: "测试放行",
		Fn:        func(context.Context, invoke.Call) (invoke.Verdict, error) { return invoke.Verdict{Allow: true}, nil },
	}}
}
