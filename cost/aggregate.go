// aggregate.go —— 聚合契约（纯函数判据）。
//
// 聚合是**判据**不是工具函数：宿主的账单、配额判定（tenant.Quota.Judge）、
// 跨宿主对账全部落在这份产出上，所以「分组顺序确定」「口径外事件不静默
// 吸收」必须是契约的一部分，而不是实现的巧合。
package cost

import (
	"iter"
	"slices"
	"strings"

	"github.com/oekit/pkg/errors"
)

// Total 一组事件的聚合结果。
type Total struct {
	// Dims 该组的分组维度取值（恰好是 Aggregate 收到的分组键集合；
	// 事件未带该维度时取值为空串——「这一组在租户维度上是空的」是诚实
	// 的陈述，不隐去）。
	Dims map[string]string
	// Amounts 各计量的合计（只含出现过的事件种类；未出现的 Kind 查
	// Amount 返回 0，不必判 map 键存在）。
	Amounts map[Kind]int64
	// Events 组内事件数。
	Events int
}

// Amount 某类计量的合计；未出现过返回 0。
func (t Total) Amount(k Kind) int64 { return t.Amounts[k] }

// Aggregate 按 dims 分组聚合（纯函数判据）。
//
// events 走 iter.Seq 流式喂入：Ledger.Events()、宿主自己的存储游标、
// 测试夹具（slices.Values）都能作事件源，聚合不持有全量。
//
//   - dims 为空 = 不分组，全部事件合成一组（Dims 为空 map）；
//   - 事件源带进**口径外**的事件（空 Kind / 负量）时返回包装哨兵的错误
//     ——账面聚合不静默吸收坏账：跳过它等于让记账 bug 变成「账少了，
//     不知道为什么」。
//   - 返回值按分组键值的字典序排序（确定性：同一份事件、同一组维度，
//     任何两次聚合给出同一份顺序）。
func Aggregate(events iter.Seq[Event], dims ...string) ([]Total, error) {
	type group struct {
		dims    map[string]string
		amounts map[Kind]int64
		count   int
	}
	groups := make(map[string]*group)
	key := func(e Event) string {
		if len(dims) == 0 {
			return "" // 不分组：全部事件落进同一个组
		}
		parts := make([]string, len(dims))
		for i, d := range dims {
			parts[i] = e.Dims[d]
		}
		return strings.Join(parts, "\x00")
	}

	for e := range events {
		if err := e.Validate(); err != nil {
			return nil, errors.Wrap(err, "cost: 事件源带进口径外事件，聚合拒绝")
		}
		k := key(e)
		g, ok := groups[k]
		if !ok {
			g = &group{dims: make(map[string]string, len(dims)), amounts: make(map[Kind]int64)}
			for _, d := range dims {
				g.dims[d] = e.Dims[d]
			}
			groups[k] = g
		}
		g.amounts[e.Kind] += e.Amount
		g.count++
	}

	out := make([]Total, 0, len(groups))
	for _, g := range groups {
		out = append(out, Total{Dims: g.dims, Amounts: g.amounts, Events: g.count})
	}
	// 按「分组维度的取值序列」排序：先把 Total.Dims 投影回与 key 同构的
	// 字符串，再排——顺序由**分组键**决定，与 map 迭代序无关。
	slices.SortFunc(out, func(a, b Total) int {
		return strings.Compare(totalKey(a, dims), totalKey(b, dims))
	})
	return out, nil
}

// totalKey 把聚合结果投影回分组键（与 Aggregate 内部的 key 同构）。
func totalKey(t Total, dims []string) string {
	if len(dims) == 0 {
		return ""
	}
	parts := make([]string, len(dims))
	for i, d := range dims {
		parts[i] = t.Dims[d]
	}
	return strings.Join(parts, "\x00")
}

// ByTenant 按租户聚合（规范维度快捷面，口径与 Aggregate 完全一致）。
func ByTenant(events iter.Seq[Event]) ([]Total, error) { return Aggregate(events, DimTenant) }

// ByRun 按 run 聚合（规范维度快捷面）。
func ByRun(events iter.Seq[Event]) ([]Total, error) { return Aggregate(events, DimRun) }

// ByTask 按任务聚合（规范维度快捷面）。
func ByTask(events iter.Seq[Event]) ([]Total, error) { return Aggregate(events, DimTask) }
