package tooldisclose

import (
	"fmt"

	"github.com/oekit/pkg/errors"
)

// 门禁哨兵。两者重试性**相反**，混淆的后果是把一次策略否定变成无穷重试。
var (
	// ErrNotLoaded 工具在目录内，但 schema 尚未取回。
	// 可恢复：门禁已把它作为副作用激活，重试即成。
	ErrNotLoaded = errors.New("工具 schema 尚未取回")

	// ErrNotInCatalog 工具不在目录内，从未可用。
	// 不可恢复：不存在的东西不会因为重试而出现，任何退避策略都应就此停手。
	ErrNotInCatalog = errors.New("工具不在目录内")
)

// GateError 门禁拒绝的结构化形态。
//
// 用共享类型 + 哨兵而不是两个独立类型：调用方多半只想问「能不能重试」，
// 而审计/留痕另需工具名与「门禁是否动了手」。拆成两个类型会逼每个分支各抄一遍这几个字段。
// 两者靠 Unwrap 指向的哨兵区分，errors.Is 分支，调用方不必认这个结构体。
//
// Retryable() 方法同时是对外的**自报接口**：通用退避层（toolfail）问的就是「能不能重试」，
// 不该被迫认识本包、也不该靠 errors.Is 认哨兵。让本类型直接答，比让每个调用方各抄一遍
// 「哪种结局可重试」可靠——那正是两边各判一套、慢慢漂移的起点。
type GateError struct {
	// Name 被拒的工具名。
	Name string
	// Activated 门禁是否已把该工具激活为已加载。仅 not-loaded 一路为 true：
	// 激活是让重试能成功的**副作用**，不改变「这次调用被拒」这个事实。
	Activated bool

	kind      error
	retryable bool
}

func newGateError(name string, kind error, retryable, activated bool) *GateError {
	return &GateError{Name: name, kind: kind, retryable: retryable, Activated: activated}
}

// Error 文本。nil 接收者给缺省串而不是解引用 e.kind：typed-nil 的 *GateError
// 会被无防御地塞进日志链路（%v / err.Error()），panic 会杀掉整个编排进程。
func (e *GateError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s（工具 %q）", e.kind, e.Name)
}

// Unwrap 指向哨兵，使 errors.Is(err, ErrNotLoaded) / errors.Is(err, ErrNotInCatalog) 成立。
//
// nil 接收者返回 nil：本包的用法约定就是「调用方按 errors.Is 分支」，而 errors.Is
// 会逐环调 Unwrap——链上挂着一个 typed-nil 时，这一环就是解引用 nil 指针。
// 返回 nil 等于「没有更深的根因」，正是没有门禁上下文时唯一诚实的答案。
func (e *GateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.kind
}

// Retryable 该次拒绝是否值得重试。
//
// nil 接收者返回 false（而不是解引用 e.retryable）：错误链上可能挂着 typed-nil 的
// *GateError——那是个**非 nil 的 error 接口包着 nil 指针**，errors.As 命中它时
// 返回 true，而本包在别处（见 toolfail 的自报判据）会把链上每个节点当方法接收者直接调用。
// 返回 false 与本包 fail-closed 的口径一致：没有门禁上下文，就不主张「值得重试」。
func (e *GateError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.retryable
}

// Retryable 该错误是否值得重试。
//
// 认不出的错误一律 false（fail-closed）。理由同上：门禁两类结局的差别只在「是否可恢复」上，
// 猜错的代价不是少重试一次，而是不停重复一个永远不会成功的调用——把策略否定伪装成抖动。
func Retryable(err error) bool {
	var ge *GateError
	// ge 必须判空再读字段：errors.As 命中 typed-nil（包装过的 nil *GateError 指针）时
	// 返回 true 却把 ge 置为 nil，下一行读字段就是 panic——与上面「认不出就 false」
	// 的承诺正好相反。toolfail.Classify 的 `f != nil` 是同一条纪律的另一处落点。
	if errors.As(err, &ge) && ge != nil {
		return ge.retryable
	}
	return false
}

// Gate 工具执行门禁。
type Gate struct{ cat *Catalog }

// NewGate 基于目录构造门禁。目录是唯一事实来源：不在目录里的工具**不存在**，
// 门禁不会因为「看着像有」而乐观放行。
func NewGate(c *Catalog) *Gate { return &Gate{cat: c} }

// Check 判定一次调用是否放行。三种结局，处置互不代偿：
//
//   - 已加载 → nil，可执行；
//   - 在目录内但未加载 → 返回可重试的 *GateError，并**顺手激活该工具**
//     （Activated=true）。激活是副作用而非放行：这次调用照样被拒，但重试必成，
//     免得再让模型为「取 schema」多绕一圈；
//   - 不在目录内 → 返回不可重试的 *GateError，调用方应就地终止，不得退避重试。
//
// 空工具名按「不在目录内」处置：门禁不做乐观放行。
func (g *Gate) Check(name string) error {
	if g == nil || g.cat == nil {
		return newGateError(name, ErrNotInCatalog, false, false)
	}
	if !g.cat.Has(name) {
		return newGateError(name, ErrNotInCatalog, false, false)
	}
	if g.cat.IsLoaded(name) {
		return nil
	}
	// 目录成员构造后不变，故 Has 为真即 Load 必成，无需再判一次失败。
	g.cat.Load(name)
	return newGateError(name, ErrNotLoaded, true, true)
}
