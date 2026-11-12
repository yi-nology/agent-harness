// quota.go —— 配额判定口径（纯函数面）。
//
// 配额是成本事件口径的一个下游维度（issue #112/#114 共用聚合契约）：
// 宿主用 cost.Aggregate 聚出某个维度（通常租户）的用量，本包判「这份用量
// 是否越过声明」。判定与声明来源分离：Quota.Judge 是纯函数判据，
// QuotaSource 是宿主的声明接口——换存储不动判据。
package tenant

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/oekit/pkg/errors"

	"github.com/yi-nology/goagent-kernel/cost"
)

// Quota 配额声明（宿主编排侧给出；内核只判不存）。
type Quota struct {
	// Max 按 Kind 的用量上限。
	//
	// 键缺失 = 该类**不设限**；值为 0 = 该类**归零**（用量 > 0 即超限）。
	// 「不设限」与「归零」是两种不同的声明，不能共用一个零值：前者是
	// 没表态，后者是明确收口——map 的「键存在性」就是两种声明的唯一区别。
	Max map[cost.Kind]int64
}

// Exceeded 一条超限判定（数据面，不是错误：宿主按自己的形态呈现）。
type Exceeded struct {
	// Kind 超限的计量种类。
	Kind cost.Kind
	// Used 实际用量。
	Used int64
	// Max 声明的上限（可为 0——归零声明被超出）。
	Max int64
}

// Judge 配额判据（纯函数）：同 Kind 用量 > 上限即超限；未声明上限的 Kind
// 不设限；used == limit 不超限（上限是允许值，不是禁止值）。
//
// used 是**已经聚合过**的用量（cost.Aggregate 的产出）——窗口语义在聚合侧
// 由宿主裁剪，判定不做任何时间判断（见包注释）。返回按 Kind 字典序排序
// （确定性：同一份用量任何两次判定给出同一份顺序）。
func (q Quota) Judge(used cost.Total) []Exceeded {
	out := make([]Exceeded, 0, len(used.Amounts))
	for kind, amount := range used.Amounts {
		limit, ok := q.Max[kind]
		if !ok {
			continue // 未声明 = 不设限
		}
		if amount > limit {
			out = append(out, Exceeded{Kind: kind, Used: amount, Max: limit})
		}
	}
	slices.SortFunc(out, func(a, b Exceeded) int { return cmp.Compare(a.Kind, b.Kind) })
	return out
}

// Remaining 各 Kind 的剩余额度（只含**声明了上限**的 Kind；未声明的
// Kind 不设限，没有「剩余」可言）。剩余额度不为负：超限即 0——
// 「还剩 -200」不是额度，是超限事实，超限明细走 Judge。
func (q Quota) Remaining(used cost.Total) map[cost.Kind]int64 {
	out := make(map[cost.Kind]int64, len(q.Max))
	for kind, limit := range q.Max {
		out[kind] = max(0, limit-used.Amount(kind))
	}
	return out
}

// QuotaSource 配额声明来源（宿主资产接口）：限额怎么存、怎么变由宿主实现。
type QuotaSource interface {
	// QuotaFor 返回该租户的配额声明；未配置的租户返回零值 Quota（不设限）。
	QuotaFor(tenant string) (Quota, error)
}

// ErrQuotaExceeded 配额超限（Enforce 的判定出口；明细经包装链的消息携带，
// 需要结构化明细时宿主直接用 Judge 判——Enforce 只是便捷出口）。
var ErrQuotaExceeded = errors.New("tenant: 配额超限")

// Enforce 组合面：取声明（QuotaSource）→ 判定（Judge）→ 出错误。
//
//   - 上下文未注入 → ErrNoContext（配额判定同样是写路径判据，
//     没有「判给哪个租户」就无法取声明，fail-closed）；
//   - 超限 → 包装 ErrQuotaExceeded 的错误，消息附每条超限的
//     Kind/Used/Max（宿主展示层直接可读）；
//   - 未超限 → nil。
func Enforce(src QuotaSource, c Context, used cost.Total) error {
	if !c.Present() {
		return ErrNoContext
	}
	q, err := src.QuotaFor(c.Tenant)
	if err != nil {
		return err
	}
	exceeded := q.Judge(used)
	if len(exceeded) == 0 {
		return nil
	}
	var b strings.Builder
	for i, e := range exceeded {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(string(e.Kind))
		b.WriteString(" 用量 ")
		b.WriteString(strconv.FormatInt(e.Used, 10))
		b.WriteString(" / 上限 ")
		b.WriteString(strconv.FormatInt(e.Max, 10))
	}
	return errors.Wrap(ErrQuotaExceeded, "超限明细: "+b.String())
}
