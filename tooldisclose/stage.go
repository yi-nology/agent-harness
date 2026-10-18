// 工具披露门接成 invoke 链上的一环。
//
// # 为什么要这个适配器
//
// 本包的 Gate 是**独立的判定件**：宿主拿到它，自己决定在什么位置判、判完怎么办。
// 这带来一个真实的失效形态——装配点忘了接，这次工具调用就没有披露门，
// 而模型拿到的是完整 schema 直接执行，事后从留痕里也看不出「这次没走门」。
//
// 接进 invoke.Chain 之后这条路径消失：链由构造保证，裸执行体不外泄，
// 宿主拿到的不是「一个可以选择调用的 Gate」，而是「一条必须走完的链」。
//
// # 保持本包既有的严格二分，不做任何软化
//
// Gate.Check 的两类结局在链上原样保留：
//
//	目录内但 schema 未取回 → 拒绝但**可重试**（且 Check 已把 schema 激活，
//	                        重试即可成，不必为取 schema 再绕一圈）
//	不在目录内             → 拒绝且**不可重试**（不存在的东西不会因为重试出现，
//	                        退避只会把一次策略否定变成无穷重试）
//
// 这条二分是本包的核心，适配器不得为了「让链路更顺」把它抹平。

package tooldisclose

import (
	"context"

	"github.com/oagentkit/toolgate/invoke"
)

// StageName 本环在链上的名字。固定值：留痕要能回答「是哪一环拒的」。
const StageName = "tool_disclosure"

// Stage 把工具披露门接成 invoke 链上的一环。
//
// cat 为 nil 时按「不在目录内」处理——即 fail-closed：不装配目录就是没有工具，
// 门禁不因缺配置而乐观放行（与 Gate.Check 在空目录下的语义一致）。
//
// # 为什么 Reason 回的是指引而不是 err.Error()
//
// 链上的 Verdict.Reason 是宿主把这环结论交给上层的唯一出口，绝大多数宿主会
// 把它原样送进模型可见的失败消息。而两类结局要求模型做的动作恰好相反：
// 未加载要**重试**、不在目录要**换工具**。回一句「工具不在目录内」的话，
// 模型只知道自己被拒了，接下来做什么全靠猜——而在第二类上继续重试，
// 就是把一次策略否定拖成一段空转。
//
// 指引文本本身含工具名，审计要的「哪次调用被拒」不会因此丢失，
// 故同一字段同时满足留痕与模型两头的需要，不需要再开一个字段。
func Stage(cat *Catalog) invoke.Stage {
	g := NewGate(cat)
	return invoke.StageFunc{
		StageName: StageName,
		Fn: func(_ context.Context, c invoke.Call) (invoke.Verdict, error) {
			err := g.Check(c.Name)
			if err == nil {
				return invoke.Verdict{Allow: true}, nil
			}
			reason := Guidance(err)
			if reason == "" {
				// 兜底：Check 只产出本包两类结局，理论上到不了这里；
				// 真到了也不能交白卷——空 Reason 会让审计与模型同时失去依据。
				reason = err.Error()
			}
			return invoke.Verdict{
				Allow:     false,
				Reason:    reason,
				Retryable: Retryable(err),
			}, nil
		},
	}
}
