// quota.go —— 资源配额的账本判据。
//
// 配额判据的本体是**账平**：占用与释放必须守恒，任何一侧超界都要显式拒绝。
// 账不平的形态是静默的——重复释放让余额虚增，后续占用全部「看起来有余量」，
// 而真实资源早就被占满了。
package scheduling

import (
	"strconv"

	"github.com/oekit/pkg/errors"
)

// Quota 配额账本：同一资源的并发占用上限（判据面不做内部锁，
// 并发使用方自担同步——与 FairPicker 同一条边界）。
//
// 上限在构造期定死：运行期调额走「换整份」（重建一份并把 Used 带过去），
// 不提供「改上限」入口——运行中悄悄调大上限等于绕过已经做出的占用决策。
type Quota struct {
	limit int64
	used  int64
}

// NewQuota 构造配额账本。limit 为负拒绝（「负上限」无从解释；
// limit = 0 合法：完全不许占用——显式写 0 与漏配是两回事）。
func NewQuota(limit int64) (*Quota, error) {
	if limit < 0 {
		return nil, errors.Wrap(ErrInvalidConfig, "scheduling: 配额上限必须 ≥ 0（实得 "+strconv.FormatInt(limit, 10)+"）")
	}
	return &Quota{limit: limit}, nil
}

// Acquire 占用 n 个额度：余额不足整单拒绝（**不做部分占用**）。
//
// 部分占用会让调用方拿到「占了一半还失败了」的半截状态，回滚路径
// 立刻多一种写法；整单原子拒绝把失败收缩到「什么都没占」。
// n <= 0 视为调用方错误（占用零个或负数个额度无从解释），按
// ErrInvalidConfig 拒绝。
func (q *Quota) Acquire(n int64) error {
	if n <= 0 {
		return errors.Wrap(ErrInvalidConfig, "scheduling: 占用量必须为正（实得 "+strconv.FormatInt(n, 10)+"）")
	}
	if q.used+n > q.limit {
		return errors.Wrap(ErrQuotaExhausted,
			"scheduling: 需要 "+strconv.FormatInt(n, 10)+"，剩余 "+strconv.FormatInt(q.Remaining(), 10)+
				"，上限 "+strconv.FormatInt(q.limit, 10))
	}
	q.used += n
	return nil
}

// Release 归还 n 个额度。释放超过已占用 ⇒ ErrOverRelease（账不平，
// 修调用点而不是钳位——钳位把重复释放这个 bug 静默吞掉）。
func (q *Quota) Release(n int64) error {
	if n <= 0 {
		return errors.Wrap(ErrInvalidConfig, "scheduling: 释放量必须为正（实得 "+strconv.FormatInt(n, 10)+"）")
	}
	if n > q.used {
		return errors.Wrap(ErrOverRelease,
			"scheduling: 释放 "+strconv.FormatInt(n, 10)+" 超过已占用 "+strconv.FormatInt(q.used, 10))
	}
	q.used -= n
	return nil
}

// Used 当前已占用。
func (q *Quota) Used() int64 { return q.used }

// Remaining 当前余额（limit − used；limit=0 时恒为 0）。
func (q *Quota) Remaining() int64 { return q.limit - q.used }

// Limit 上限（换整份重建时核对用）。
func (q *Quota) Limit() int64 { return q.limit }
