// audit.go —— 介入留痕契约与进程内参考实现。
//
// 留痕是介入的硬前提，不是事后增强：没有留痕的介入无法回答「谁在什么时候
// 依据什么改了运行状态」，出问题时与「存储被绕过直接改」无法区分。
// 故契约层面 Auditor 缺位 ⇒ 介入一律拒绝（ErrNoAuditor），实现层面
// MemAuditor 对不完整的留痕拒收——两层各挡一个方向。
package controlplane

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/oekit/pkg/errors"
)

// Action 介入/查询动作种类（留痕的 Action 字段；httpapi 鉴权钩子的判定对象）。
type Action string

const (
	// ActionQuery 查询（运行状态视图 / 不变量视图）。
	ActionQuery Action = "query"
	// ActionForceRelease 强制释放。
	ActionForceRelease Action = "force_release"
	// ActionAdjudicate 裁决未知结果。
	ActionAdjudicate Action = "adjudicate"
	// ActionTerminate 终止运行。
	ActionTerminate Action = "terminate"
)

// AuditEntry 一条人工介入留痕：谁、什么时候、做了什么、对谁、依据什么、结果如何。
type AuditEntry struct {
	// At 留痕时刻。由**留痕实现侧**补（实现收到零值时取自己的当前时刻）——
	// 介入方的时钟不可信，留痕时刻以实现侧为准。
	At time.Time
	// Operator 谁在介入（必填）。
	Operator string
	// Action 动作种类（必填，词表见 Action* 常量）。
	Action Action
	// Target 目标（任务 id / 效果键 / run id，必填）。
	Target string
	// Basis 依据什么结论（必填）。
	Basis string
	// Detail 结果摘要（可空：成功 / 被拒原因）。
	Detail string
}

// ErrInvalidAudit 留痕不完整（谁 / 动作 / 目标 / 依据缺一不可）。
// MemAuditor 对不完整的留痕拒收；宿主实现应复用同一判据。
var ErrInvalidAudit = errors.New("controlplane: 留痕不完整——谁/动作/目标/依据缺一不可")

// Auditor 留痕契约：**只增不改**（append-only）。
//
// 实现方必须保证：
//
//   - Append 成功后条目永不修改、永不删除（留痕可被追加，不可被改写——
//     可改写的留痕不是留痕）；
//   - 收到零值 At 时补实现侧当前时刻；
//   - 对缺 Operator / Action / Target / Basis 的条目拒绝（ErrInvalidAudit），
//     不静默补空。
//
// 持久化形态归宿主（表 / 文件 / 只读副本皆可）；MemAuditor 是进程内参考实现。
type Auditor interface {
	// Append 追加一条介入留痕。
	Append(ctx context.Context, e AuditEntry) error
	// Entries 按目标取留痕（查询面）。target 为空取全部；按追加序返回。
	Entries(ctx context.Context, target string) ([]AuditEntry, error)
}

// MemAuditor 进程内参考留痕面。零值可用，并发安全。
type MemAuditor struct {
	mu      sync.Mutex
	entries []AuditEntry
}

// Append 追加留痕（先判完整性与动作词表，再补时刻，再落账）。
func (a *MemAuditor) Append(_ context.Context, e AuditEntry) error {
	if e.Operator == "" || e.Target == "" || e.Basis == "" || !knownAction(e.Action) {
		return ErrInvalidAudit
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

// Entries 按目标取留痕（target 空 = 全部；按追加序）。
// 返回的是拷贝：调用方改写条目不影响留痕面（只增不改的可观测面同样只读）。
func (a *MemAuditor) Entries(_ context.Context, target string) ([]AuditEntry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuditEntry, 0, len(a.entries))
	for _, e := range a.entries {
		if target == "" || e.Target == target {
			out = append(out, e)
		}
	}
	return out, nil
}

// actions 介入动作的词表（MemAuditor 校验用；查询动作不经留痕）。
var actions = []Action{ActionForceRelease, ActionAdjudicate, ActionTerminate}

// knownAction 报告动作是否在介入词表内。
func knownAction(a Action) bool { return slices.Contains(actions, a) }
