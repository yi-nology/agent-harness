// backpressure.go —— 背压的裁决判据：满了就明说，不悄悄排队到失控。
//
// 背压判据的本体：**裁决必须带机器可判的原因**。「拒绝」如果只有一个
// 布尔，调用方分不清「队列满」（稍后重试有意义）与「在途满」（加容量
// 才有意义）——两种拒绝的处置完全不同，压平它们就是把信号埋掉。
package scheduling

import (
	"strconv"

	"github.com/oekit/pkg/errors"
)

// Reason 背压拒绝的原因（词表封闭：宿主按常量比对，不按字符串猜）。
type Reason string

const (
	// ReasonQueueFull 等待队列已满：暂不接受新请求（稍后重试有意义）。
	ReasonQueueFull Reason = "queue_full"
	// ReasonInFlightFull 在途执行已达上限：暂不接受新请求
	// （重试同样会被拒——要消化积压得先让在途收敛或扩容量）。
	ReasonInFlightFull Reason = "in_flight_full"
)

// Decision 背压裁决：准入与否 + 拒绝原因（准入时 Reason 为空）。
type Decision struct {
	Admit  bool
	Reason Reason
}

// Policy 背压阈值。零值语义刻意取「不设限」——背压是宿主**显式选择**
// 的保护机制，缺省关着；要保护就写明数字，与 netguard.Allow 的
// 「零值即严」方向相反但同一立场：缺省侧取「不改变既有行为」的那一侧，
// 写错的数字在装配期被 Validate 拦下，不会被静默钳位。
type Policy struct {
	// MaxQueue 等待队列深度上限（<=0 = 不设限）。
	MaxQueue int
	// MaxInFlight 在途执行数上限（<=0 = 不设限）。
	MaxInFlight int
}

// Validate 装配期自检：负值即红（阈值语义只有「不设限(<=0)」与「上限(>0)」
// 两档，负数是笔误不是语义）。
func (p Policy) Validate() error {
	switch {
	case p.MaxQueue < 0:
		return errors.Wrap(ErrInvalidConfig, "scheduling: MaxQueue 为负（实得 "+strconv.Itoa(p.MaxQueue)+"）；不设限请写 0")
	case p.MaxInFlight < 0:
		return errors.Wrap(ErrInvalidConfig, "scheduling: MaxInFlight 为负（实得 "+strconv.Itoa(p.MaxInFlight)+"）；不设限请写 0")
	}
	return nil
}

// Decide 背压裁决（纯函数）：queueDepth 是**新请求入队前**的等待数，
// inFlight 是当前在途执行数。
//
// 判定次序：**先判在途、再判队列**。在途满是容量问题（重试无用），
// 队列满是拥塞问题（退避重试有用）——先报容量这一条，宿主的处置
// 才指向正确的修法（与 gatecheck「需人审优先于档位不足」同一思路：
// 先报哪条决定宿主去找谁）。
//
// 两个都满时报 in_flight_full：容量约束是最硬的那条，队列只是它的结果。
func (p Policy) Decide(queueDepth, inFlight int) Decision {
	if p.MaxInFlight > 0 && inFlight >= p.MaxInFlight {
		return Decision{Admit: false, Reason: ReasonInFlightFull}
	}
	if p.MaxQueue > 0 && queueDepth >= p.MaxQueue {
		return Decision{Admit: false, Reason: ReasonQueueFull}
	}
	return Decision{Admit: true}
}
