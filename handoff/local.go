// local.go —— 进程内参考实现：契约**可满足性的证明件**。
//
// 边界与 taskboard「不给存储实现」的口径不冲突：taskboard 拒的是**跨进程
// 存储**（给了参考实现，宿主就会照着改落库形态，两份真相并存）；本件的
// 存在价值是把「同一进程内的编排」也接进同一套契约（判据跑在它身上），
// 它对跨进程**不做任何承诺**——原子性由进程内互斥给出，崩溃语义没有
// 落库背书。跨进程落库形态参考 examples/pgstore 对 taskboard.Board 的
// 分工（宿主抄它，不 import 它）。
//
// 时钟可注入（Clock）：过期语义要能被确定性测，不靠 sleep。
package handoff

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"
)

// LocalDesk 进程内交接台（Store 的进程内参考实现）。
type LocalDesk struct {
	mu    sync.Mutex
	clock func() time.Time

	tickets map[Key]Ticket // 当前代（每个 Key 一条）
	tokens  map[Key]string // 领取令牌（accepted 单的回执凭据）
	seq     uint64         // 令牌序号（单进程内单调）
}

// DeskOption 交接台的装配项。
type DeskOption func(*LocalDesk)

// WithClock 注入时钟（缺省 time.Now）。
func WithClock(clock func() time.Time) DeskOption {
	return func(d *LocalDesk) { d.clock = clock }
}

// NewLocalDesk 构造进程内交接台。
func NewLocalDesk(opts ...DeskOption) *LocalDesk {
	d := &LocalDesk{
		clock:   time.Now,
		tickets: map[Key]Ticket{},
		tokens:  map[Key]string{},
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Offer 挂出交接单（原子 + 幂等）。
//
// 同一 Key 未终态 ⇒ 原样返回（重放）；终态 ⇒ 新一代；否则首代。
// Deadline 按本台自己的时钟推算。
func (d *LocalDesk) Offer(_ context.Context, t Ticket) (Ticket, error) {
	if err := t.Validate(); err != nil {
		return Ticket{}, errors.New("handoff: " + err.Error())
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if have, ok := d.tickets[t.Key]; ok && !have.State.Terminal() {
		return have, nil // 重放：同一 Key 的重复挂出不开新单
	}
	gen := t.Generation + 1
	if have, ok := d.tickets[t.Key]; ok {
		gen = have.Generation + 1 // 重新交接：在同 Key 上一代之上递增
	}
	now := d.clock()
	fresh := t
	fresh.Generation = gen
	fresh.Deadline = now.Add(t.TTL)
	fresh.State = StateOffered
	fresh.Holder = ""
	fresh.ReceiptAt = time.Time{}
	delete(d.tokens, t.Key)
	d.tickets[t.Key] = fresh
	return fresh, nil
}

// Take 领取交接单：负责人在这一次互斥段里完成转移。
func (d *LocalDesk) Take(_ context.Context, key Key, to Agent) (Handle, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.tickets[key]
	if !ok {
		return Handle{}, ErrUnknownKey
	}
	if t.State == StateAccepted {
		return Handle{}, ErrTaken
	}
	if t.State.Terminal() {
		return Handle{}, ErrAlreadySettled
	}
	if to != t.To {
		return Handle{}, ErrNotAddressee
	}
	if d.clock().After(t.Deadline) {
		t.State = StateExpired // 懒式过期：读到即落账
		d.tickets[key] = t
		return Handle{}, ErrExpired
	}
	d.seq++
	handle := Handle{
		Token: "take-" + strconv.FormatUint(t.Generation, 10) + "-" + strconv.FormatUint(d.seq, 10),
		Fence: t.Generation,
		Until: t.Deadline,
	}
	t.State = StateAccepted
	t.Holder = to
	d.tickets[key] = t
	d.tokens[key] = handle.Token
	return handle, nil
}

// Reply 交回执（凭领取令牌；迟到与令牌不匹配都拒收）。
func (d *LocalDesk) Reply(_ context.Context, key Key, token string, r Receipt) error {
	if err := r.Validate(); err != nil {
		return errors.New("handoff: " + err.Error())
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.tickets[key]
	if !ok {
		return ErrUnknownKey
	}
	if t.State.Terminal() {
		return ErrAlreadySettled
	}
	if t.State != StateAccepted || d.tokens[key] != token {
		return ErrLeaseLost
	}
	if d.clock().After(t.Deadline) {
		t.State = StateExpired // 责任期已过：回执迟到，单落 expired
		d.tickets[key] = t
		return ErrExpired
	}
	t.State = StateClosed
	t.ReceiptAt = d.clock()
	d.tickets[key] = t
	return nil
}

// Cancel 源方收回（仅 StateOffered；凭身份不凭内容）。
func (d *LocalDesk) Cancel(_ context.Context, key Key, by Agent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.tickets[key]
	if !ok {
		return ErrUnknownKey
	}
	if t.State.Terminal() {
		return ErrAlreadySettled
	}
	if t.State == StateAccepted {
		return ErrTaken
	}
	if by != t.From {
		return ErrNotSource
	}
	t.State = StateWithdrawn
	t.Holder = ""
	d.tickets[key] = t
	return nil
}

// Snapshot 当前代的全部交接单（按 Key 升序，确定性）。
func (d *LocalDesk) Snapshot(_ context.Context) ([]Ticket, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// slices.SortedFunc 直接吃 maps.Values 迭代器（range-over-func 面）：
	// 值集合收齐即排序，不经过手工 append。
	return slices.SortedFunc(maps.Values(d.tickets), func(a, b Ticket) int {
		return cmp.Compare(a.Key, b.Key)
	}), nil
}
