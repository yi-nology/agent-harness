// fair.go —— 加权公平份额的确定性选择器。
//
// 公平判据的本体：**按「每权重份已得服务」最欠的那个先被服务**——
// 每个参与者按自己的权重应得 served_i ∝ weight_i 的份额；
// 比值 served_i/weight_i 最小者就是当前最欠份额的人。平局按名字字典序，
// 全程不依赖 map 遍历顺序与时钟——两次重建同一状态得到同一选择。
package scheduling

import (
	"slices"
	"strconv"

	"github.com/oekit/pkg/errors"
)

// FairPicker 加权公平选择器：Pick 返回当前最「欠份额」的参与者。
//
// 零值不可用（构造期拒绝空集与非正权重）；构造后**只读权重、只增计数**，
// 并发使用方自担同步（判据面不做内部锁——它通常被宿主的调度循环单线程消费；
// 要并发就把它包进自己的锁里，判据不隐藏同步成本）。
//
// 权重变更不走「改其中一条」：用新权重表重建一份 Picker（换整份纪律），
// 旧的 served 计数想保留就经 Served() 带进 NewFairPickerWithServed。
type FairPicker struct {
	keys    []string // 排序后的参与者名单（确定性的遍历序）
	weights map[string]int
	served  map[string]uint64
}

// NewFairPicker 按权重表构造选择器。权重必须全为正；空集拒绝。
//
// 权重为 0 或负的参与者会让「应得份额」失去意义（0 权重者永远不该被选，
// 负权重无从解释）——装配期就拒，不静默钳位。
func NewFairPicker(weights map[string]int) (*FairPicker, error) {
	return NewFairPickerWithServed(weights, nil)
}

// NewFairPickerWithServed 在 NewFairPicker 的基础上带入既有服务计数
// （权重变更换整份时保留历史公平性）。
//
// served 里的未知键忽略（参与者已撤出），已登记但缺计数的按 0 起。
func NewFairPickerWithServed(weights map[string]int, served map[string]uint64) (*FairPicker, error) {
	if len(weights) == 0 {
		return nil, errors.Wrap(ErrInvalidConfig, "scheduling: 公平选择器需要至少一个参与者")
	}
	keys := make([]string, 0, len(weights))
	for k, w := range weights {
		if w <= 0 {
			return nil, errors.Wrap(ErrInvalidConfig,
				"scheduling: 权重必须为正（键 "+k+" 实得 "+strconv.Itoa(w)+"）")
		}
		keys = append(keys, k)
	}
	slices.Sort(keys) // 确定性遍历序：平局裁决不依赖 map 顺序
	sp := &FairPicker{
		keys:    keys,
		weights: weights,
		served:  make(map[string]uint64, len(weights)),
	}
	for _, k := range keys {
		if served != nil {
			sp.served[k] = served[k]
		}
	}
	return sp, nil
}

// Pick 返回当前最欠份额的参与者（不推进计数——是否算一次服务由调用方
// Record 决定，选中后不消费是合法的：调用方可能对它探活后再放弃）。
//
// 口径：served_i / weight_i 最小者（每权重份欠得最多）；平局按名字字典序。
func (p *FairPicker) Pick() string {
	best := ""
	var bestRatio float64 // 当前最小的 served/weight
	for i, k := range p.keys {
		ratio := float64(p.served[k]) / float64(p.weights[k])
		if i == 0 || ratio < bestRatio {
			best, bestRatio = k, ratio
		}
	}
	return best
}

// Record 记一次对 key 的服务（推进它的已服务计数）。
//
// 未登记的 key 忽略——选择器的参与者集在构造期定死，运行期不收新成员
// （要扩员就重建一份，与「换整份」同一条纪律）。
func (p *FairPicker) Record(key string) {
	if _, ok := p.weights[key]; ok {
		p.served[key]++
	}
}

// Served 返回一份服务计数的快照（权重变更换整份时带走历史）。
func (p *FairPicker) Served() map[string]uint64 {
	out := make(map[string]uint64, len(p.served))
	for k, v := range p.served {
		out[k] = v
	}
	return out
}

// MaxDebt 公平性的观察量（诊断面，不参与判定）：应得比例的最大落差
// max_i(served_i/w_i) − min_i(served_i/w_i)。0 = 完全成比例；
// 越大说明有人按权重份算落后得越多。命名直说它是债的幅度：
// 宿主拿它做告警阈值时，刻度是「每权重份的服务次数差」。
func (p *FairPicker) MaxDebt() float64 {
	if len(p.keys) == 0 {
		return 0
	}
	ratios := make([]float64, 0, len(p.keys))
	for _, k := range p.keys {
		ratios = append(ratios, float64(p.served[k])/float64(p.weights[k]))
	}
	hi, lo := slices.Max(ratios), slices.Min(ratios)
	return max(hi-lo, 0) // 浮点误差下 hi ≥ lo 恒成立，钳位只为表达「非负」的口径
}
