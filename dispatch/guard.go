// Package dispatch 实现「权限即拓扑」：agent 间可达性由 allow 矩阵物理决定，
// 矩阵在构建期物化、运行期只读，被拒绝的派发返回结构化 *DenyError。
//
// 守卫核心已下沉 oagentkit/agent/dispatch；本包只做两件事：
//  1. 把 EdgeSource 抽象原样透传给宿主（宿主用它把自己的注册表投影成拓扑）；
//  2. 提供 NewGuard 单点构造，避免宿主直接依赖 agentkit 的包路径。
//
// 设计纪律：本包**不认识任何具体 agent**。拓扑由宿主的 EdgeSource 实现提供，
// 因此 harness 侧不得出现任何领域词表（见 architecture-policy.yaml 的
// harness-no-domain-vocabulary 红线）——领域拓扑是宿主的资产，不是 harness 的。
package dispatch

import adispatch "github.com/oagentkit/agent/dispatch"

// EdgeSource 拓扑来源：宿主注册表的最小投影。
// Slugs 返回全拓扑 slug；DispatchAllow 返回某 slug 允许派发的目标（未登记返回 nil）。
type EdgeSource = adispatch.EdgeSource

// DenyError 结构化拒绝。调用方不得转述、不得降级，按失败兜底如实上报。
type DenyError = adispatch.DenyError

// 拒绝原因词表（DenyError.Reason 的取值全集）。审计消费方按常量比对而非裸字符串，
// 故在此原样透出——宿主与内核共用同一组常量，跨仓比对不会因字面量漂移而失效。
const (
	ReasonNotAllowed    = adispatch.ReasonNotAllowed    // caller→callee 不在 allow 矩阵
	ReasonDepthExceeded = adispatch.ReasonDepthExceeded // 派发深度超限
	ReasonSelfDispatch  = adispatch.ReasonSelfDispatch  // 自派发
)

// Guard 派发守卫：allow 矩阵来自宿主提供的拓扑，构建期物化后运行期只读。
type Guard = adispatch.Guard

// NewGuard 从宿主拓扑构建守卫。maxDepth 为静态最深派发链上限（超限拒绝）。
func NewGuard(src EdgeSource, maxDepth int) *Guard {
	return adispatch.NewGuard(src, maxDepth)
}
