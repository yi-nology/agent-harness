// Package cost 运行成本账：任务级成本事件的**口径与聚合面**。
//
// 生命周期: Experimental（v0.35.0 定级）——本包仍会改、删、拆，不承诺任何
// 兼容，升级风险自负；定级理由见 api-lifecycle.yaml，分级规则见
// docs/api-compat.md。
//
// # 它补的是哪一段（issue #112）
//
// 本仓的 budget 管的是**输入侧上下文预算**（分段估算、窗口上限、压实计划），
// 它估的是「还没发的下一轮提示」。运行成本是另一件事：**已经发生**的消费
// ——模型调用实际消耗的 token、实际发生的工具调用次数、实际走过的墙钟、
// 并发持有实际占用的时长。此前这一面在仓内没有任何口径：宿主各拼各的，
// 「省了多少上下文」与「花了多少成本」混为一谈，压实收益被当成成本优化。
//
// # 与 budget 的边界（不重复实现、不口径漂移）
//
//   - budget：**估算**输入侧会话构成，永不回填 provider 报数（见其包注释）；
//   - cost：**记录**已发生的消费报数，只做聚合；不做估算、不做预算约束、
//     不参与任何裁剪决策。
//
// 一句话：budget 回答「下一轮要发多少」，cost 回答「这一单已经花了多少」。
// 两边都出现 token 时按各自口径各自算——那是两个不同的问题，共用一个
// 数字才是口径漂移。
//
// # 事件口径
//
// 一条成本事件 = 一种计量（Kind）× 一个量（Amount）× 一组维度归属（Dims）：
//
//   - Kind 是**开放枚举**：规范五种（输入 / 输出 token、工具调用、墙钟、
//     并发占用），宿主自定义种类直接用自己的键，内核不解释（与
//     budget.Segment 同一条开放键纪律）；
//   - Dims 是**开放键**：规范维度给名字（租户 / run / 任务 / 环节 / agent /
//     模型）便于跨宿主对齐，宿主自定义维度直接用自己的键；
//   - 量必须**非负**：报数侧给负数是记账 bug，拒收留痕（ErrNegativeAmount），
//     既不静默归零也不许它反向抹掉别段的账。budget 的估算侧把负值钳位为 0
//     （估错可以少算），两侧语义刻意不同——账错必须被看见。
//
// # 聚合契约
//
// Aggregate 按任意维度子集分组聚合（纯函数，事件源走 iter.Seq 流式喂入）；
// 规范维度给具名快捷面：按租户（ByTenant）/ 按 run（ByRun）/ 按任务
// （ByTask）。租户配额是聚合的一个维度：判定口径在 tenant 包
// （Quota.Judge 对一份聚合结果判超限），本包不做任何配额判定——先定
// 事件口径，配额是它的下游。
//
// # 聚合结果
//
// Total 按分组键排序返回（确定性）：同一份事件、同一组维度，任何两次聚合
// 给出同一份顺序——跨宿主对账与测试断言都不必再排序。
//
// # 内核给口径，不给存储
//
// Ledger 是进程内参考实现（演示口径怎么落），不是内核的持久化承诺：
// 跨进程聚合、持久化、按租户出账单是宿主资产。与 taskboard 同一条分工——
// 契约与判据归内核，落库归宿主。
package cost

import (
	"maps"

	"github.com/oekit/pkg/errors"
)

// Kind 计量种类（开放枚举：规范五种 + 宿主自定义键）。
type Kind string

const (
	// KindInputTokens 输入 token 消耗（单位：个）。
	KindInputTokens Kind = "input_tokens"
	// KindOutputTokens 输出 token 消耗（单位：个）。
	KindOutputTokens Kind = "output_tokens"
	// KindToolCalls 工具调用次数（单位：次）。
	KindToolCalls Kind = "tool_calls"
	// KindWallTime 墙钟时长（单位：纳秒，与 time.Duration 同刻度——
	// 聚合后可直接 time.Duration(t.Amount(KindWallTime)) 转回）。
	KindWallTime Kind = "wall_time"
	// KindConcurrency 并发占用（单位：并发数 × 纳秒）——持有租约的时长
	// 按并发数加权累计，回答「这一单同时占着多少份资源、占多久」。
	// 这是成本四口径里唯一不是简单计数的一项，单列而不是折进墙钟：
	// 折算会丢掉「几路并发」这一维，出账单时无法拆分。
	KindConcurrency Kind = "concurrency"
)

// 规范维度键。宿主自定义维度直接用自己的键，内核不解释（开放键纪律）。
const (
	// DimTenant 租户（配额判定的主维度，见 tenant.Quota）。
	DimTenant = "tenant"
	// DimRun 一轮编排运行（contract.RunContext.ID）。
	DimRun = "run"
	// DimTask 子任务（taskboard.TaskID）。
	DimTask = "task"
	// DimStep 环节。
	DimStep = "step"
	// DimAgent 执行者（taskboard.AgentID）。
	DimAgent = "agent"
	// DimModel 模型（provider 侧的模型标识，宿主原样记录）。
	DimModel = "model"
)

// Event 一条成本事件：一种计量 × 一个量 × 一组维度归属。
type Event struct {
	// Kind 计量种类。空 Kind 拒收——没有种类的账无从聚合。
	Kind Kind
	// Amount 计量值，必须非负。负数是记账 bug：拒收（ErrNegativeAmount），
	// 不静默归零、不许反向抹账（与 budget.Add 的钳位语义刻意不同，见包注释）。
	Amount int64
	// Dims 维度归属。可为 nil；Record 入账时**整份拷贝**——调用方复用
	// map 改写不能牵动已入账的事件（账面不可被调用方改写）。
	Dims map[string]string
}

// 错误哨兵（宿主判定走 errors.Is，不要各造一套）。
var (
	// ErrEmptyKind 事件缺计量种类。
	ErrEmptyKind = errors.New("cost: 成本事件缺计量种类")
	// ErrNegativeAmount 事件量为负（记账 bug，拒收留痕）。
	ErrNegativeAmount = errors.New("cost: 成本事件量为负")
)

// Validate 事件口径判定：Kind 非空且 Amount 非负。
// Ledger.Record 与 Aggregate 都走这一份判据——口径只有一个真相。
func (e Event) Validate() error {
	if e.Kind == "" {
		return ErrEmptyKind
	}
	if e.Amount < 0 {
		return ErrNegativeAmount
	}
	return nil
}

// Clone 深拷贝：Dims 整份复制（nil 维度保持 nil）。
// 账面事件都以 Clone 落账与出账——事件是值语义的账目，不是共享可变状态。
func (e Event) Clone() Event {
	if e.Dims == nil {
		return Event{Kind: e.Kind, Amount: e.Amount}
	}
	dims := make(map[string]string, len(e.Dims))
	maps.Copy(dims, e.Dims)
	return Event{Kind: e.Kind, Amount: e.Amount, Dims: dims}
}
